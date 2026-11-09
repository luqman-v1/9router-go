package executor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	json "encoding/json/v2"

	"9router/proxy/internal/proxy"
	"9router/proxy/internal/translator"
)

// ForwardGeminiWeb routes a chat completion to gemini.google.com.
//
// This is the direct path: not a browser, not a sidecar. Gemini Web has no
// documented API, but it does have an HTTP one — `GET /app` hands a plain client
// the session tokens, and the same `StreamGenerate` endpoint the page's own
// JavaScript posts to answers a turn. Verified live, that works anonymously: no
// cookies, no credential, no TLS impersonation, nothing the single-binary
// CGO-free constraint rules out.
//
// What the anonymous session does not get is a choice of model. It is served
// Flash-Lite, and a request for anything else is answered by Flash-Lite under the
// requested name, which is why a turn verifies the identity that comes back
// instead of trusting the request. Configuring a `__Secure-1PSID` cookie from a
// signed-in tab raises the set the session can be served from.
//
// Everything the router owns — model resolution, account rotation, usage
// accounting, cooling down — stays with the router; this returns an
// *UpstreamError carrying the status it has to act on.
func ForwardGeminiWeb(w http.ResponseWriter, req *Request) error {
	envelope, err := geminiWebParseRequest(req)
	if err != nil {
		return err
	}
	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	prompt := geminiWebMessagesToPrompt(envelope.Messages)
	if prompt == "" {
		return geminiWebFailure(http.StatusBadRequest, "gemini-web received a request with no text content")
	}
	// A client that declared tools with tool_choice "none" wants no tools
	// called, so the declarations are not rendered and no call is recovered
	// from the answer.
	toolsEnabled := len(envelope.Tools) > 0 && geminiWebToolChoice(envelope.ToolChoice) != geminiWebToolChoiceNone
	if toolsEnabled {
		prompt = geminiWebRenderTools(envelope.Tools, envelope.ToolChoice) + "\n\n" + prompt
	}

	session := geminiWebSessionFor(req)
	requested := geminiWebRequestedModel(envelope)
	model, err := geminiWebSelectModel(ctx, session, requested)
	if err != nil {
		return err
	}

	// An image is uploaded before the turn is sent: the upload is a separate
	// request with its own failure, and a turn that names an image the site
	// cannot resolve answers without it — silently, and as if the client had
	// never sent one.
	attachments, err := geminiWebUploadAttachments(ctx, session, envelope.Messages)
	if err != nil {
		return err
	}

	thinking := geminiWebClampThinkingLevel(model, geminiWebThinkingLevel(envelope, model))
	turn := newGeminiWebTurn(w, requested, model, req, envelope.Tools, toolsEnabled, prompt)
	if err := session.streamTurn(ctx, prompt, thinking, model, attachments, turn.apply); err != nil {
		return turn.failure(err)
	}
	if !turn.answered() {
		return geminiWebFailure(http.StatusBadGateway,
			"gemini-web: gemini.google.com ended the stream without an answer")
	}
	if err := turn.verifyIdentity(); err != nil {
		return err
	}

	// A tool-calling turn buffers whatever the client asked for: the calls only
	// exist once the answer is whole, and this site has no native tool channel to
	// stream them from. An ordinary turn has already been written as it streamed
	// and only needs closing.
	calls := turn.toolCalls()
	if len(calls) > 0 {
		return geminiWebWriteToolCalls(w, req, requested, calls, turn.preamble(calls), turn.usage())
	}
	if turn.streamed {
		return turn.emitter.finish(turn.usage())
	}
	return geminiWebWriteCompletion(w, req, requested, turn)
}

// geminiWebParseRequest reads the inbound body.
func geminiWebParseRequest(req *Request) (geminiWebRequestEnvelope, error) {
	var envelope geminiWebRequestEnvelope
	if err := json.Unmarshal(req.Body, &envelope); err != nil {
		return envelope, fmt.Errorf("gemini-web: parse request: %w", err)
	}
	if len(envelope.Messages) == 0 {
		return envelope, geminiWebFailure(http.StatusBadRequest, "gemini-web received an empty messages array")
	}
	return envelope, nil
}

// geminiWebRequestedModel is the model id this turn is addressed to. The chat
// path leaves Request.ModelName unset, so the body carries it.
func geminiWebRequestedModel(envelope geminiWebRequestEnvelope) string {
	model := strings.TrimSpace(envelope.Model)
	if index := strings.LastIndex(model, "/"); index >= 0 {
		model = model[index+1:]
	}
	return model
}

// geminiWebOnlyModelAdvice explains an unavailable model in terms of the
// credential the session actually presented.
//
// Saying "an anonymous session reaches one model" to a caller whose cookie is
// configured but expired sends them looking for a cookie they already pasted,
// and the fix is different: that one has to be replaced, while an anonymous
// session has to be given one. Naming the wrong cause costs a support round
// trip, so the advice follows the credential rather than assuming its absence.
//
// The cause of a one-model session is a missing `__Secure-1PSIDTS`: that
// cookie is what carries the session into the model registry, and a header
// without it is answered as a visitor's however fresh the rest of it is.
func geminiWebOnlyModelAdvice(cookie string) string {
	switch {
	case cookie == "":
		return "An anonymous session reaches one model — configure the cookie header of a signed-in " +
			"gemini.google.com tab to reach the others."
	case !GeminiWebHasSessionTimestamp(cookie):
		return "The configured cookie carries no __Secure-1PSIDTS, so gemini.google.com answers it as an " +
			"anonymous visitor and the session reaches one model. Copy the whole cookie header, including " +
			"__Secure-1PSIDTS."
	default:
		return "The configured session reaches this one model only — a signed-in tab that can use the others " +
			"needs the cookie pasted again."
	}
}

// geminiWebSelectModel resolves the registry entry the turn is addressed to.
//
// The site answers a model it does not recognize with a different one instead of
// an error, so an unrecognized id has to be refused here. Naming what the session
// does offer is what makes the refusal actionable rather than a dead end.
func geminiWebSelectModel(ctx context.Context, session *geminiWebSession, requested string) (*geminiWebModel, error) {
	models, err := session.availableModels(ctx)
	if err != nil {
		return nil, geminiWebClassifyFailure(err)
	}
	model := geminiWebResolveModel(models, requested)
	if model == nil {
		return nil, geminiWebFailure(http.StatusBadRequest, fmt.Sprintf(
			"gemini-web cannot be served %q by this session; it offers %s. %s",
			requested, geminiWebAvailableModels(models), geminiWebOnlyModelAdvice(session.cookie)))
	}
	return model, nil
}

// geminiWebClassifyFailure maps an error raised before a turn started onto the
// status the router has to act on.
//
// The distinction that matters is between a failure the credential is at fault
// for and one the site is: the first has to be a 401 so the connection is cooled
// down and the next one tried, the second a 502. A configured cookie the site
// refuses is a staleness the user can fix; an anonymous session the site refuses
// is not a credential problem at all.
func geminiWebClassifyFailure(err error) error {
	if errors.Is(err, errGeminiWebSessionRefused) {
		return geminiWebFailure(http.StatusUnauthorized, err.Error())
	}
	var rpc *geminiWebRPCError
	if errors.As(err, &rpc) {
		return geminiWebFailure(rpc.Status, "gemini-web: "+rpc.Message)
	}
	var status *geminiWebUpstreamStatus
	if errors.As(err, &status) {
		switch status.Status {
		case http.StatusUnauthorized, http.StatusForbidden:
			return geminiWebFailure(http.StatusUnauthorized, "gemini-web: "+status.Error())
		case http.StatusTooManyRequests:
			return geminiWebFailure(http.StatusTooManyRequests, "gemini-web: "+status.Error())
		}
		return geminiWebFailure(http.StatusBadGateway, "gemini-web: "+status.Error())
	}
	return geminiWebFailure(http.StatusBadGateway, "gemini-web: "+err.Error())
}

// geminiWebTurn accumulates one answer and, when the client asked to stream,
// emits it as it arrives.
//
// Gemini Web re-sends the whole answer on every frame rather than deltas, so the
// accumulation here is a replacement and the deltas are derived from the
// difference — text the client has already been sent is never sent again.
type geminiWebTurn struct {
	requested string
	resolved  *geminiWebModel
	req       *Request
	tools     []deepSeekToolSpec
	// toolsEnabled reports whether the client wanted calls at all. A client
	// that declared tools with tool_choice "none" did not, so no call is
	// recovered from its answer.
	toolsEnabled bool
	// prompt is what was sent, kept for the token accounting: the site
	// reports no token counts, so the prompt's worth is estimated from it.
	prompt   string
	emitter  *geminiWebEmitter
	streamed bool
	text     string
	thinking string
	served   string
	label    string
	started  bool
	done     bool
}

func newGeminiWebTurn(w http.ResponseWriter, requested string, resolved *geminiWebModel, req *Request, tools []deepSeekToolSpec, toolsEnabled bool, prompt string) *geminiWebTurn {
	turn := &geminiWebTurn{requested: requested, resolved: resolved, req: req, tools: tools, toolsEnabled: toolsEnabled, prompt: prompt}
	// A tool turn buffers and is written at the end, so only a plain streaming
	// turn gets an emitter. A client that declared tools with tool_choice
	// "none" asked for no call, so its turn streams like an ordinary one.
	if req.IsStream && (!toolsEnabled || len(tools) == 0) {
		turn.streamed = true
		turn.emitter = newGeminiWebEmitter(w, req, requested)
	}
	return turn
}

// apply folds one parsed line into the turn, emitting deltas as it streams.
func (t *geminiWebTurn) apply(line *geminiWebLine) error {
	if line.CID != "" || line.Started {
		t.started = true
	}
	if line.ServedModelID != "" && line.ServedModelID != t.served {
		t.served = line.ServedModelID
		t.label = line.ServedModelLabel
		// The identity arrives before any answer text, so a substitution is
		// refused before the client has seen anything rather than after.
		if err := t.verifyIdentity(); err != nil {
			return err
		}
	}
	for _, frame := range line.Frames {
		t.started = true
		if frame.Done {
			t.done = true
		}
		if frame.Thoughts != "" {
			t.thinking = frame.Thoughts
		}
		if frame.Text != "" {
			t.text = frame.Text
		}
		if t.emitter == nil {
			continue
		}
		if err := t.emitter.emitThinkingDelta(t.thinking); err != nil {
			return err
		}
		if err := t.emitter.emitContentDelta(t.text); err != nil {
			return err
		}
	}
	return nil
}

// usage reports the token accounting for the turn.
//
// The site reports no token counts, so the prompt and the answer are
// estimated from their text. The reasoning channel is counted as part of the
// completion, which is where OpenAI's own accounting puts it.
//
// The counts are also published on the request context, because that is where
// the router's own usage recorder reads them from.
func (t *geminiWebTurn) usage() map[string]any {
	usage := geminiWebBuildUsage(t.prompt, t.text, t.thinking)
	if t.req != nil && t.req.Ctx != nil {
		translator.SetUsage(t.req.Ctx, &translator.OpenAIUsage{
			PromptTokens:     geminiWebUsageInt(usage, "prompt_tokens"),
			CompletionTokens: geminiWebUsageInt(usage, "completion_tokens"),
		})
	}
	return usage
}

func (t *geminiWebTurn) answered() bool {
	return t.started && (strings.TrimSpace(t.text) != "" || strings.TrimSpace(t.thinking) != "" || t.done)
}

// toolCalls recovers the calls a tool-calling turn asked for.
//
// A call whose arguments violate the schema the client declared is dropped:
// the client would fail on it, and the model's prose is the better answer.
func (t *geminiWebTurn) toolCalls() []deepSeekToolRequest {
	if !t.toolsEnabled || len(t.tools) == 0 || strings.TrimSpace(t.text) == "" {
		return nil
	}
	calls := deepSeekExtractToolCalls(t.text, t.tools)
	valid := make([]deepSeekToolRequest, 0, len(calls))
	for _, call := range calls {
		if !geminiWebValidToolArguments(t.tools, call.Name, call.Arguments) {
			continue
		}
		valid = append(valid, call)
	}
	return valid
}

// preamble is the prose the model wrote alongside its tool calls. A client that
// declared tools still expects narration it produced; dropping it makes the
// assistant turn look like it skipped a step.
func (t *geminiWebTurn) preamble(calls []deepSeekToolRequest) string {
	if len(calls) == 0 {
		return ""
	}
	raws := make([]string, 0, len(calls))
	for _, call := range calls {
		raws = append(raws, call.Raw)
	}
	return strings.TrimSpace(deepSeekSurroundingText(t.text, raws...))
}

// verifyIdentity refuses a substituted model.
//
// The site answers an unknown or unavailable model with a different one and no
// error, so the only place a substitution can be caught is on the identity it
// reports for its own answer. Letting it through would file a Flash-Lite answer
// under a Pro model's name — a failure the caller cannot see, and the reason the
// model registry is consulted before the turn is sent.
func (t *geminiWebTurn) verifyIdentity() error {
	if t.served == "" || t.resolved == nil || t.served == t.resolved.ID {
		return nil
	}
	advice := "An anonymous session reaches one model only."
	if geminiWebCookieHeader(t.req) != "" {
		advice = "The configured __Secure-1PSID cannot reach the requested model — the session it produced was served a different one."
	}
	return geminiWebFailure(http.StatusBadGateway, fmt.Sprintf(
		"gemini-web was asked for %q and the session answered with %q — the account cannot reach the requested model. %s",
		t.requested, t.servedLabelOrID(), advice))
}

// servedLabelOrID names the model the site actually used.
func (t *geminiWebTurn) servedLabelOrID() string {
	if t.label != "" {
		return t.label
	}
	return t.served
}

// failure classifies a failure that arrived mid-turn.
//
// A model the site will not serve is this provider's own refusal rather than an
// upstream outage, so it keeps its status whatever else the code implies.
func (t *geminiWebTurn) failure(err error) error {
	var rpc *geminiWebRPCError
	if errors.As(err, &rpc) && rpc.Type == "model_invalid" {
		return geminiWebFailure(http.StatusBadRequest, "gemini-web: "+rpc.Message)
	}
	if t.emitter != nil {
		// HTTP 200 is already on the wire, so the failure travels in-band.
		return t.emitter.abort(err)
	}
	return geminiWebClassifyFailure(err)
}

// geminiWebFailure builds the error the router sees for a failure this executor
// diagnosed itself.
func geminiWebFailure(status int, message string) error {
	return proxy.UpstreamFailure(status, message)
}
