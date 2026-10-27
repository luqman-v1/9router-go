package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
)

// deepSeekFirstMessageFingerprint is the stable identity of a conversation
// that sent no client session id: the account plus the conversation's first
// user message. The system prompt never stands in for it — most clients lead
// every conversation with the same system message, and keying on that would
// put all of a client's conversations on one fallback key where each
// divergent turn evicts the other's chain, leaving every turn a fresh session
// carrying the full transcript. Identical first user messages still collide,
// which is why the session store pairs this with an idle TTL and a tail check
// that opens a fresh session the moment the prompts diverge.
func deepSeekFirstMessageFingerprint(messages []deepSeekMessage) string {
	for _, message := range messages {
		if message.Role != "user" {
			continue
		}
		h := sha256.New()
		h.Write([]byte(message.Role))
		h.Write([]byte{0})
		h.Write([]byte(message.Content))
		return hex.EncodeToString(h.Sum(nil))[:16]
	}
	return ""
}

// ForwardDeepSeekWeb routes a chat completion through chat.deepseek.com's web
// API using a browser-login credential — the port of OmniRoute's
// open-sse/executors/deepseek-web.ts.
//
// The turn is four upstream calls: exchange the pasted userToken for a
// short-lived access token, open a chat session, fetch and answer a proof of
// work, then stream the completion. Each can fail independently, and each
// failure has to surface as the status the router can act on — a 401 so the
// account is cooled down and rotated, a 429 so the client backs off — rather
// than as one opaque 502.
//
// Stealth mode (see deepseek_web_stealth.go) decides how closely the turn
// imitates the site's own client: whether the request carries the full browser
// fingerprint, whether the conversation is pinned to one upstream session with
// only its new tail sent, and how many turns one account may run at once.
func ForwardDeepSeekWeb(w http.ResponseWriter, req *Request) error {
	var envelope deepSeekRequestEnvelope
	if err := json.Unmarshal(req.Body, &envelope); err != nil {
		return fmt.Errorf("deepseek-web: parse request: %w", err)
	}
	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	userToken := deepSeekUserToken(req.APIKey)
	if userToken == "" {
		return deepSeekFailure(http.StatusBadRequest,
			"deepseek-web needs a userToken — paste the value from chat.deepseek.com DevTools → Application → Local Storage → userToken")
	}
	if len(envelope.Messages) == 0 {
		return deepSeekFailure(http.StatusBadRequest, "deepseek-web received an empty messages array")
	}

	stealth := deepSeekStealthFor(req.ConnData)
	prompt := deepSeekTurnPrompt(envelope, req, stealth)
	if prompt == "" {
		return deepSeekFailure(http.StatusBadRequest, "deepseek-web received a request with no text content")
	}

	options := resolveDeepSeekModelOptions(envelope.Model, envelope)
	turn := newDeepSeekTurn(req.Client, deepSeekBaseURL(req.Config), stealth).
		withBrowserFingerprint(stealth.cookie, stealth.deviceID)

	// A turn that leaves in lockstep with its neighbours is not what a person
	// typing produces, and an agent client dispatches several turns at once.
	deepSeekPause(ctx, stealth.jitter)

	// One account, at most maxConcurrency turns at a time. A burst is what turns
	// a busy account into a limited one, so the over-cap turns wait here instead
	// of being sent. The slot is freed after a randomized quiet window rather
	// than the moment the answer is written: two generations overlapping on one
	// account is the measured trigger of the account-level restriction, and the
	// window is what keeps the next turn out of this one's breath.
	release, err := deepSeekGates.acquire(ctx, userToken, stealth.maxConcurrency, deepSeekStealthQueueWait)
	if err != nil {
		return deepSeekFailure(http.StatusTooManyRequests, "deepseek-web: "+err.Error())
	}
	defer deepSeekReleaseAfterTurn(release, stealth.callGap)

	accessToken, err := turn.acquireAccessToken(ctx, userToken)
	if err != nil {
		return deepSeekAuthFailure(err, userToken)
	}

	session, err := deepSeekAcquireSession(ctx, turn, accessToken, stealth, userToken, deepSeekFirstMessageFingerprint(envelope.Messages), req, prompt)
	if err != nil {
		return deepSeekAuthFailure(err, userToken)
	}
	deepSeekStealthLog(stealth, session.claim.reuse)

	response, err := turn.completion(ctx, accessToken, deepSeekCompletionRequest{
		ChatSessionID:   session.id,
		ParentMessageID: session.claim.parentMessageID,
		ModelType:       options.modelType,
		Prompt:          session.claim.tail,
		RefFileIDs:      []string{},
		ThinkingEnabled: options.thinking,
		SearchEnabled:   options.search,
		// The site's own client has no non-streaming mode: every completion it
		// issues is an event stream, and a `stream: false` on this endpoint is
		// a value no browser ever wrote. A non-streaming client is served by
		// draining the stream, not by asking the upstream not to send one.
		Stream: true,
	})
	if err != nil && session.claim.reuse && ctx.Err() == nil && !isDeepSeekAuthError(err) {
		// The pinned session failed at the HTTP level: the chat was deleted in
		// the DeepSeek UI, the upstream session expired, or the connection
		// dropped. None of that is the account being refused — the sibling
		// implementation heals the same situation in-request by dropping the
		// stale session and retrying once on a fresh one, and so does this:
		// the fresh session carries the whole prompt, because nothing of the
		// old thread can be assumed to exist. One retry only — a second
		// failure is an upstream condition no third attempt of the same shape
		// fixes, and every attempt is another request on this account. An
		// auth failure never heals: the token was freshly exchanged and the
		// upstream rejected it, so a new session would just add two more
		// doomed calls to the account the site is refusing.
		deepSeekAbandonSession(session, ctx, turn, userToken, accessToken)
		deepSeekCloseSession(ctx, turn, userToken, accessToken, session.id)
		if freshID, createErr := turn.createSession(ctx, accessToken); createErr == nil {
			retry, retryErr := turn.completion(ctx, accessToken, deepSeekCompletionRequest{
				ChatSessionID:   freshID,
				ParentMessageID: nil,
				ModelType:       options.modelType,
				Prompt:          prompt,
				RefFileIDs:      []string{},
				ThinkingEnabled: options.thinking,
				SearchEnabled:   options.search,
				Stream:          true,
			})
			if retryErr == nil {
				response, err = retry, nil
				session.id = freshID
				session.createdHere = true
				session.claim = deepSeekSessionClaim{tail: prompt, pin: true}
			}
		}
	}
	if err != nil {
		deepSeekAbandonSession(session, ctx, turn, userToken, accessToken)
		return deepSeekAuthFailure(err, userToken)
	}
	defer response.Body.Close()

	// An enforcement body is not a turn. The account mute and the application
	// errors arrive as HTTP 200 with a JSON payload, and a reader that treated
	// one as an empty stream would report the account as merely cut off — which
	// is how a briefly muted account earns a longer mute.
	if enforcement := deepSeekEnforcementFromResponse(response); enforcement != nil {
		deepSeekAbandonSession(session, ctx, turn, userToken, accessToken)
		return enforcement.error()
	}

	// One accumulator serves the whole turn: what the upstream said about its
	// own answer — its message id, whether it signed off, whether it refused —
	// decides whether this conversation can be continued next turn.
	accumulator := newDeepSeekStreamAccumulator(envelope.Model)

	// Tool calling is a separate path, taken only when the client declared
	// tools. Everything above is identical either way: same token exchange, same
	// session, same completion. The branch exists below the shared preamble so
	// that a request without tools cannot be affected by any of it.
	var runErr error
	switch {
	case len(envelope.Tools) > 0:
		runErr = deepSeekToolTurnToClient(w, req, response.Body, envelope, accumulator)
	default:
		forwardUpstreamResponseHeaders(w, response.Header)
		if req.IsStream {
			runErr = deepSeekStreamToClient(w, req, response.Body, envelope.Model, accumulator)
		} else {
			runErr = deepSeekCollectToClient(w, req, response.Body, envelope.Model, accumulator)
		}
	}

	if !deepSeekSettleSession(session, prompt, accumulator, runErr, ctx, turn, accessToken) {
		// The conversation is not pinned to this session, so nothing will ever
		// thread on it: the upstream copy is retired rather than left behind.
		// With stealth on the retirement is queued and batched — an instant
		// create-then-delete is a cadence no browser produces (see
		// deepseek_web_delete_queue.go).
		deepSeekCloseSession(ctx, turn, userToken, accessToken, session.id)
	}
	return runErr
}

// deepSeekTurnPrompt renders the conversation as the single prompt string the
// web API accepts, with the tool declarations folded in.
//
// The tool declarations never reach the upstream as a parameter — the endpoint
// has no field for them — so they lead the prompt. Only a request that actually
// declared tools reaches that branch, so an ordinary turn's prompt is
// byte-for-byte what it was before.
func deepSeekTurnPrompt(envelope deepSeekRequestEnvelope, req *Request, stealth deepSeekStealth) string {
	// A pinned conversation renders as a stable transcript, because only the
	// tail of each prompt is sent: the same conversation has to render the same
	// way from turn to turn, or no turn extends the previous one.
	prompt := deepSeekMessagesToPrompt(envelope.Messages, deepSeekHistoryWindow(req.ConnData), stealth.persistSession)
	prompt = deepSeekClampPrompt(prompt, deepSeekMaxPromptChars(req.ConnData))
	if prompt == "" || len(envelope.Tools) == 0 {
		return prompt
	}
	return deepSeekRenderTools(envelope.Tools) + "\n\n" + prompt
}

// deepSeekSession is the session one turn runs against, and the claim that
// produced it.
type deepSeekSession struct {
	// id is the upstream chat session the completion call names.
	id string

	// claim carries what the turn must send: the prompt tail, the parent to
	// thread on, and whether the store may be written when the turn ends.
	claim deepSeekSessionClaim

	// key is the conversation's store key, empty when this turn cannot pin —
	// no client conversation id, or session persistence off.
	key string

	// createdHere is true when this turn opened the upstream session, which is
	// what decides whether abandoning it closes it upstream.
	createdHere bool
}

// deepSeekAcquireSession resolves the session one turn runs against.
//
// With session persistence on, the conversation's pinned session is reused and
// only the new tail of the prompt is sent. Every case that cannot reuse —
// no conversation id, another turn holding the session, a prompt that does not
// extend what was sent — falls back to a fresh session carrying the whole
// prompt, which is the plain behaviour and always correct.
func deepSeekAcquireSession(
	ctx context.Context,
	turn deepSeekTurn,
	accessToken string,
	stealth deepSeekStealth,
	userToken string,
	firstMessageFingerprint string,
	req *Request,
	prompt string,
) (deepSeekSession, error) {
	session := deepSeekSession{claim: deepSeekSessionClaim{tail: prompt}}

	if stealth.persistSession {
		session.key = deepSeekSessionKey(userToken, req.SessionID, firstMessageFingerprint)
		session.claim = deepSeekSessions.claim(session.key, prompt)
	}

	session.id = session.claim.sessionID
	if session.claim.reuse {
		return session, nil
	}

	id, err := turn.createSession(ctx, accessToken)
	if err != nil {
		return deepSeekSession{}, err
	}
	session.id = id
	session.createdHere = true
	return session, nil
}

// deepSeekSettleSession keeps the conversation's session when the turn produced
// something the next turn can continue from, and drops it otherwise. It reports
// whether the store now owns the session: false means the caller must close it
// upstream, because nothing will thread on it.
//
// A session is only worth pinning when the turn finished, the upstream named the
// answer it produced, and nothing in the stream said the account was refused.
// Any other outcome leaves the conversation on a fresh session next turn, which
// is the plain behaviour and always correct.
func deepSeekSettleSession(
	session deepSeekSession,
	prompt string,
	accumulator *deepSeekStreamAccumulator,
	runErr error,
	ctx context.Context,
	turn deepSeekTurn,
	accessToken string,
) bool {
	if session.key == "" || !session.claim.pin {
		return false
	}
	switch {
	case runErr != nil, accumulator.enforcement != nil, !accumulator.sawFinished:
		deepSeekSessions.forget(session.key, session.id)
		return false
	}
	deepSeekSessions.commit(session.claim, session.key, session.id, prompt, accumulator.responseMessageID)
	return deepSeekSessions.holds(session.key, session.id)
}

// deepSeekAbandonSession drops a conversation's session after a turn that must
// not continue it.
//
// A session this turn created is retired upstream, because nothing will thread on
// it. A reused one is only forgotten: the upstream thread is still the one the
// conversation belongs to, but the next turn must not assume a refused turn was
// recorded, so the entry goes either way. The retirement itself goes through
// deepSeekCloseSession, so a stealth turn queues it rather than deleting in the
// same second the session was created.
func deepSeekAbandonSession(session deepSeekSession, ctx context.Context, turn deepSeekTurn, userToken, accessToken string) {
	if session.key != "" && session.claim.pin {
		deepSeekSessions.forget(session.key, session.id)
	}
	if session.createdHere {
		deepSeekCloseSession(ctx, turn, userToken, accessToken, session.id)
	}
}

// deepSeekBaseURL reads the endpoint this connection dials. A connection may
// override it, and the sibling endpoints are derived from whatever it names —
// a request that left for a configured host while the headers and sibling
// calls named the public site would be both detectable and incoherent.
func deepSeekBaseURL(cfg *providers.ProviderConfig) string {
	if cfg == nil || strings.TrimSpace(cfg.BaseURL) == "" {
		return deepSeekWebBase + deepSeekCompletionPath
	}
	return cfg.BaseURL
}

// deepSeekHistoryWindow reads the optional per-connection replay bound. Zero
// means the executor applies its own default.
func deepSeekHistoryWindow(connData map[string]any) int {
	if connData == nil {
		return 0
	}
	switch value := connData["historyWindow"].(type) {
	case float64:
		return int(value)
	case int:
		return value
	}
	return 0
}

// deepSeekStreamToClient relays the upstream event stream as OpenAI chunks.
//
// The stream is only complete when the upstream says so. A body that closes
// without a FINISHED signal was cut off — an expired session, a dropped
// connection — and reporting the partial text as a finished answer is how a
// client comes to treat a truncated reply as the model's real conclusion.
//
// A refusal carried inside the stream (`event: hint` / `event: toast`, which
// the scanner hands over as ordinary `data:` payloads) is the other case: the
// response is already on the wire, so the refusal travels as an error frame —
// and the turn still returns it, because the router cools the account down on
// the status rather than on what the client saw.
func deepSeekStreamToClient(w http.ResponseWriter, req *Request, upstream io.Reader, model string, accumulator *deepSeekStreamAccumulator) error {
	hw := proxy.NewHeartbeatWriter(req.Ctx, w, 0)
	defer hw.Close()
	flusher := proxy.WriteSSEHeaders(hw)

	responseID := deepSeekResponseID()
	created := time.Now().Unix()

	startTime := req.StartTime
	if startTime.IsZero() {
		startTime = time.Now()
	}

	var emitErr error
	emit := func(delta map[string]any, finish any) error {
		if emitErr != nil {
			return emitErr
		}
		if req.TTFT != nil && *req.TTFT == 0 {
			*req.TTFT = time.Since(startTime).Milliseconds()
		}
		chunk := map[string]any{
			"id":      responseID,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   model,
			"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": finish}},
		}
		encoded, err := json.Marshal(chunk)
		if err != nil {
			emitErr = err
			return err
		}
		if _, err := hw.Write(fmt.Appendf(nil, "data: %s\n\n", encoded)); err != nil {
			emitErr = err
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}

	// Reasoning and visible text are separate channels on the wire. The role
	// delta rides whichever channel opens first, and each channel is emitted as
	// a delta from what the client has already been sent.
	//
	// DeepSeek's web models reach for their native DSML tool syntax even when
	// the request declared no tools, and that markup is not an answer. The
	// scrubber drops it as it arrives, so the client sees the prose around it
	// and never the tags.
	var scrubber deepSeekDSMLScrubber
	thinkingSent, contentSent, roleAnnounced := 0, 0, false
	scanErr := proxy.ScanStream(upstream, func(payload []byte) {
		if emitErr != nil {
			return
		}
		accumulator.consume(payload)
		if accumulator.enforcement != nil {
			// A refusal ends the turn: emitting the text that arrived beside it
			// would present a partial answer as the model's conclusion.
			return
		}

		if thinking, sent := deepSeekDeltaFrom(accumulator.thinkingText(), thinkingSent); thinking != "" {
			thinkingSent = sent
			delta := map[string]any{"reasoning_content": thinking}
			if !roleAnnounced {
				roleAnnounced = true
				delta["role"] = "assistant"
			}
			_ = emit(delta, nil)
		}
		contentTail := accumulator.contentText()[contentSent:]
		clean, consumed := scrubber.scrub(contentTail)
		contentSent += consumed
		if clean != "" {
			delta := map[string]any{"content": clean}
			if !roleAnnounced {
				roleAnnounced = true
				delta["role"] = "assistant"
			}
			_ = emit(delta, nil)
		}
	})
	if scanErr != nil {
		return deepSeekReportStreamAbort(hw, flusher, scanErr)
	}
	if emitErr != nil {
		return emitErr
	}

	if enforcement := accumulator.enforcement; enforcement != nil {
		_, _ = hw.Write(proxy.BuildStreamErrorBytes(enforcement.status(), enforcement.Message, proxy.SSEFormatOpenAI))
		if flusher != nil {
			flusher.Flush()
		}
		return enforcement.error()
	}
	if !accumulator.sawFinished {
		return deepSeekReportStreamAbort(hw, flusher,
			fmt.Errorf("deepseek-web stream ended before completion — the upstream session was cut off"))
	}

	if err := emit(map[string]any{}, "stop"); err != nil {
		return err
	}
	_, err := hw.Write([]byte("data: [DONE]\n\n"))
	if flusher != nil {
		flusher.Flush()
	}
	return err
}

// deepSeekReportStreamAbort reports an aborted upstream stream in-band. HTTP 200
// is already on the wire by this point, so the failure has to travel as a chunk
// carrying an error rather than as a status the client never sees.
//
// A nil writer means nothing has been written yet, which is the case for the
// tool path: it buffers, so it is still able to answer with a status. Writing
// through a nil writer panicked and took the server down with it, turning an
// upstream cut-off into a request that never got a response at all.
func deepSeekReportStreamAbort(hw *proxy.HeartbeatWriter, flusher http.Flusher, cause error) error {
	if hw == nil {
		return cause
	}
	code, message := proxy.ClassifyStreamAbort(cause)
	_, _ = hw.Write(proxy.BuildStreamErrorBytes(code, message, proxy.SSEFormatOpenAI))
	_, _ = hw.Write([]byte("data: [DONE]\n\n"))
	if flusher != nil {
		flusher.Flush()
	}
	return cause
}

// deepSeekCollectToClient drives the same stream to completion and returns one
// chat.completion document.
func deepSeekCollectToClient(w http.ResponseWriter, req *Request, upstream io.Reader, model string, accumulator *deepSeekStreamAccumulator) error {
	if err := deepSeekDrain(upstream, accumulator); err != nil {
		return deepSeekFailure(http.StatusBadGateway, "deepseek-web: "+err.Error())
	}
	if enforcement := accumulator.enforcement; enforcement != nil {
		return enforcement.error()
	}
	if !accumulator.sawFinished {
		return deepSeekFailure(http.StatusBadGateway,
			"deepseek-web: stream ended before completion — the upstream session was cut off")
	}

	text := accumulator.contentText()
	if deepSeekHasDSML(text) {
		text = deepSeekStripDSML(text)
	}
	message := map[string]any{"role": "assistant", "content": text}
	if thinking := accumulator.thinkingText(); thinking != "" {
		message["reasoning_content"] = thinking
	}
	body := map[string]any{
		"id":      deepSeekResponseID(),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{{"index": 0, "message": message, "finish_reason": "stop"}},
		"usage":   map[string]any{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0},
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("deepseek-web: marshal response: %w", err)
	}

	// The answer goes through the shared response path so a /v1/responses or
	// Claude client receives the shape it asked for, outbound guardrails still
	// apply, and the log buffer records the same text every other provider does.
	return jsonResponse(req.Ctx, w, bytes.NewReader(encoded), req.TranslateResp, req.ResponseBuf)
}

// deepSeekDrain folds the upstream body into the caller's accumulator.
//
// The accumulator belongs to the caller because what the stream said about the
// answer has to outlive the read: it decides whether the conversation can be
// continued and whether the account has to be cooled down.
func deepSeekDrain(upstream io.Reader, accumulator *deepSeekStreamAccumulator) error {
	return proxy.ScanStream(upstream, func(payload []byte) {
		accumulator.consume(payload)
	})
}

// deepSeekEnforcementFromResponse reads a refusal out of a completion answer
// that is a JSON document rather than an event stream.
//
// The mute enforcement arrives this way — a plain HTTP 200 carrying
// `{"data":{"biz_code":5,…}}` — and so does the site's general application
// error. Neither can be found by scanning an event stream that was never sent,
// and both have to become a status the router acts on. The body is put back for
// the caller, so a response that turns out to be ordinary content is still
// readable.
func deepSeekEnforcementFromResponse(response *http.Response) *deepSeekEnforcement {
	if response == nil {
		return nil
	}
	if strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		return nil
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, deepSeekMaxBodyBytes))
	if err != nil || len(body) == 0 {
		return nil
	}
	response.Body = io.NopCloser(bytes.NewReader(body))

	return deepSeekEnforcementFromBody(body)
}

// deepSeekResponseID mints a completion id in the shape OpenAI clients expect.
func deepSeekResponseID() string {
	return "chatcmpl-deepseek-" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

// deepSeekFailure builds the error the router sees for a failure that is not an
// upstream transport error.
func deepSeekFailure(status int, message string) error {
	return proxy.UpstreamFailure(status, message)
}

// deepSeekAuthFailure maps a failure onto the HTTP status it should surface as,
// and drops the cached access token when it is an auth failure so the next
// attempt re-exchanges the credential instead of replaying a refused one.
func deepSeekAuthFailure(err error, userToken string) error {
	var apiErr *deepSeekAPIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case deepSeekCodeAuthFailed, deepSeekCodeMissingToken:
			// The credential is refused or absent. Drop the cached access
			// token so the next attempt re-exchanges the credential instead
			// of replaying a refused one.
			deepSeekTokens.drop(userToken)
			return deepSeekFailure(http.StatusUnauthorized, apiErr.Error())
		case deepSeekCodeUserBanned:
			// The account itself is refused. Cooling it down would only send
			// the next turn to the same refusal, so this is reported as
			// forbidden instead.
			return deepSeekFailure(http.StatusForbidden, apiErr.Error())
		case deepSeekCodeIPRestricted:
			// The account is fine; this egress is not.
			return deepSeekFailure(http.StatusForbidden, apiErr.Error())
		case deepSeekCodeMuted:
			return deepSeekFailure(http.StatusTooManyRequests, apiErr.Error())
		default:
			return deepSeekFailure(http.StatusBadGateway, apiErr.Error())
		}
	}
	if isDeepSeekAuthError(err) {
		deepSeekTokens.drop(userToken)
		return deepSeekFailure(http.StatusUnauthorized, "deepseek-web: "+err.Error())
	}
	return deepSeekFailure(http.StatusBadGateway, "deepseek-web: "+err.Error())
}
