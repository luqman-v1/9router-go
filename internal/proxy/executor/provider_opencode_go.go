package executor

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/log"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
	"9router/proxy/internal/translator"
)

func isOpencodeResponsesModel(model string) bool {
	base := model
	if parenIdx := strings.IndexByte(base, '('); parenIdx != -1 {
		base = base[:parenIdx]
	}
	if base == "muse-spark-1.2-contributor-free" || base == "muse-spark-1.3-contributor-free" {
		return true
	}
	lower := strings.ToLower(base)
	for _, prefix := range []string{"muse-spark", "musespark", "muse_spark"} {
		rest, ok := strings.CutPrefix(lower, prefix)
		if !ok {
			continue
		}
		if rest == "" || strings.ContainsRune("-_:.\u00a0 \t", rune(rest[0])) {
			return true
		}
	}
	return false
}

// goTargetFormat reports which opencode-go endpoint a request must travel on,
// the paid sibling of zenTargetFormat and museTargetFormat: a client that
// already speaks a lane the model serves is forwarded on it, anything else is
// translated into the model's target lane (open-sse/handlers/chatCore.js
// resolveTransport). deepseek-v4* serves /responses next to chat, so a
// Responses client reaches it with nested reasoning while a Chat client keeps
// the deepseek thinking shape on /chat/completions.
func goTargetFormat(ctx context.Context, model string) string {
	formats, declared := providers.GetModelFormats("opencode-go", model)
	if !declared {
		return providers.FormatOpenAI
	}
	if formats.SupportsFormat(clientFormatFor(ctx)) {
		return clientFormatFor(ctx)
	}
	return formats.TargetFormat
}

func deriveOpencodeSession(rawSession, clientTool, connID string) string {
	raw := strings.TrimSpace(rawSession)
	if raw == "" {
		raw = strings.TrimSpace(connID)
	}
	if raw == "" {
		raw = "default"
	}
	return proxy.TranslateOpenCodeSessionID(raw, clientTool)
}

// museSparkOutputBudgetFields are the three spellings an OpenAI-shaped request
// may use for its output cap: Chat Completions' max_tokens, Responses'
// max_output_tokens, and the OpenAI-compatible max_completion_tokens. A request
// may arrive on either lane, so all three are clamped.
var museSparkOutputBudgetFields = []string{"max_tokens", "max_output_tokens", "max_completion_tokens"}

// clampMuseSparkOutputBudget raises a caller-set output budget to the floor for
// the muse-spark responses lane. The floor and the measurement behind it live
// in output_budget.go; this wrapper only supplies the lane's scope.
//
// Muse Spark spends 570-1320 hidden reasoning tokens before emitting any
// completion text, so a budget below the floor is consumed entirely by
// reasoning: the upstream answers 200 with an empty message, or stalls the
// stream until the gateway's watchdog reports a 502.
func clampMuseSparkOutputBudget(m map[string]any, cleanModel string) []string {
	if m == nil || !isOpencodeResponsesModel(cleanModel) {
		return nil
	}
	return clampOutputBudget(m, true, providers.GetCapabilitiesForModel("opencode", cleanModel).MaxOutput)
}

// outputBudgetOf reads the numeric shape an output cap decodes to. A JSON
// integer may arrive as int64, int or float64 depending on how the body was
// decoded, so all three are accepted; a string ("1024") is not a budget and a
// non-finite float is not one either.
func outputBudgetOf(v any) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case int:
		return int64(n), true
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return 0, false
		}
		return int64(n), true
	default:
		return 0, false
	}
}

// museSparkEffortSuffix returns the reasoning effort a caller encoded in the
// model id, e.g. "muse-spark-1.3(high)". The parenthesised level is 9router
// request notation and never part of the wire id — which is why the callers
// strip it before dispatch — but the intent it carried is still the caller's.
func museSparkEffortSuffix(rawModel string) string {
	open := strings.LastIndex(rawModel, "(")
	if open == -1 || !strings.HasSuffix(rawModel, ")") {
		return ""
	}
	level := strings.ToLower(strings.TrimSpace(rawModel[open+1 : len(rawModel)-1]))
	switch level {
	case "high", "medium", "low":
		return level
	default:
		return ""
	}
}

// museSparkCleanModel reduces the model as the client asked for it to the
// catalog id: no client prefix and no parenthesised request override.
func museSparkCleanModel(model string) string {
	id := strings.TrimPrefix(model, "oc/")
	id = strings.TrimPrefix(id, "opencode-go/")
	id = strings.TrimPrefix(id, "opencode/")
	if open := strings.IndexByte(id, '('); open != -1 {
		id = id[:open]
	}
	return id
}

// normalizeMuseSparkResponsesBody reshapes an OpenAI Responses body for the
// opencode /responses lanes: the Muse Spark ids it is named for, plus every
// other model the go lane routes there (deepseek-v4*, grok-4.x, gpt-*-luna).
// model is the id exactly as the client sent it (prefix and
// "(level)" override included) so the request modifier survives the cleaning.
func normalizeMuseSparkResponsesBody(body []byte, model string) ([]byte, error) {
	cleanModel := museSparkCleanModel(model)

	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return body, nil
	}
	// Reasoning folds into the nested Responses shape the way
	// applyZenResponsesReasoning does for zen: an explicit "none" has no nested
	// equivalent and is expressed upstream by removing reasoning entirely —
	// sending effort "none" instead asks a model to think at the lowest level
	// rather than to not think, which is a different (and billable) request.
	if rEffort, ok := m["reasoning_effort"].(string); ok {
		delete(m, "reasoning_effort")
		if strings.EqualFold(strings.TrimSpace(rEffort), "none") {
			delete(m, "reasoning")
		} else if rEffort != "" {
			if rEffort == "max" {
				rEffort = "xhigh"
			}
			m["reasoning"] = map[string]any{
				"effort":  rEffort,
				"summary": "auto",
			}
		}
	} else if rMap, ok := m["reasoning"].(map[string]any); ok {
		if eff, ok := rMap["effort"].(string); ok {
			if strings.EqualFold(strings.TrimSpace(eff), "none") {
				delete(m, "reasoning")
			} else if eff == "max" {
				rMap["effort"] = "xhigh"
			}
		}
		if _, still := m["reasoning"]; still {
			if rMap["summary"] == nil {
				rMap["summary"] = "auto"
			}
		}
	} else if effort := museSparkEffortSuffix(model); effort != "" {
		// Fill-only: an explicit body effort already won above, so the model-id
		// override never silently replaces what the caller asked for in the body.
		m["reasoning"] = map[string]any{
			"effort":  effort,
			"summary": "auto",
		}
	}
	clampMuseSparkOutputBudget(m, cleanModel)
	// PR #4061: Strip prior reasoning items & continuity properties
	if inList, ok := m["input"].([]any); ok {
		cleanInput := make([]any, 0, len(inList))
		for _, item := range inList {
			if itemMap, ok := item.(map[string]any); ok {
				if itemMap["type"] == "reasoning" {
					continue
				}
				delete(itemMap, "encrypted_content")
				delete(itemMap, "reasoning_encrypted_content")
				cleanInput = append(cleanInput, itemMap)
			} else {
				cleanInput = append(cleanInput, item)
			}
		}
		m["input"] = cleanInput
	}
	// PR #4062: Normalize explicit non-auto tool_choice to "auto" on muse-spark-1.3
	if strings.Contains(cleanModel, "muse-spark-1.3") {
		if tc, ok := m["tool_choice"]; ok && tc != nil {
			if tcStr, ok := tc.(string); !ok || tcStr != "auto" {
				m["tool_choice"] = "auto"
			}
		}
	}
	// marshalStable, not json.Marshal: opencode-go's muse-spark lane is a
	// prefix-caching upstream and a body whose members reshuffle on every
	// call turns each request after the first into a full cache miss.
	return marshalStable(m)
}

// ForwardOpencodeGo handles requests for opencode-go (paid tier).
// opencodeGoInferenceBase rewrites a go-layout base URL to the v2 inference
// gate's /inference/v1 prefix, mirroring opencodeInferenceBase for the
// /zen/go/v1 layout. Custom endpoints pass through untouched.
func opencodeGoInferenceBase(base string) string {
	base = strings.TrimRight(base, "/")
	base = strings.TrimSuffix(base, "/chat/completions")
	base = strings.TrimSuffix(base, "/messages")
	base = strings.TrimSuffix(base, "/responses")
	switch {
	case strings.HasSuffix(base, "/zen/go/v1"):
		return strings.TrimSuffix(base, "/zen/go/v1") + "/inference/openai/v1"
	case strings.HasSuffix(base, "/inference/go/openai/v1"):
		return strings.TrimSuffix(base, "/inference/go/openai/v1") + "/inference/openai/v1"
	case strings.HasSuffix(base, "/inference/openai/v1"):
		return base
	case strings.HasSuffix(base, "/inference/v1"):
		return strings.TrimSuffix(base, "/inference/v1") + "/inference/openai/v1"
	default:
		return base
	}
}

// forwardOpencodeGoFree serves free-tier ids on the paid go lane (today only
// space-bunny-free). Keyed requests for them 403, so they travel exactly like
// free-executor requests: the inference chat lane (no free go model takes the
// Responses lane upstream), v2 fingerprint, public credentials.
func forwardOpencodeGoFree(w http.ResponseWriter, req *Request, cleanModel string) error {
	body := InjectReasoningContent(req.Body, "opencode-go")
	body, toolNameMap := shapeOpenCodeFreeBody(body, false, cleanModel)
	body = translator.ApplyFingerprintSystemPrompt(body)
	req.Ctx = translator.WithToolNameMap(req.Ctx, toolNameMap)
	w = NewToolNameRestoringWriter(w, toolNameMap)
	// Same execute/CALL_TOOL bridge as the free chat lane: streaming only —
	// the non-streaming lane rewrites the re-aggregated JSON below.
	if req.IsStream {
		w = NewOpenCodeExecuteBridgeWriter(w)
	}

	var reqMap map[string]any
	if err := json.Unmarshal(body, &reqMap); err == nil {
		reqMap["stream"] = true
		if cleanModel != "" {
			reqMap["model"] = cleanModel
		}
		// Same reasoning-budget floor as the free chat lane: this lane reaches
		// the same keyless gate, so the same empty-answer failure applies.
		clampOpenCodeOutputBudget(reqMap, "opencode-go", cleanModel)
		includeStreamUsage(reqMap)
		if b, err := marshalOpenCodeFreeChatBody(reqMap); err == nil {
			body = b
		}
	}

	// One lane URL for both egress paths: a relay forwards to whatever this names.
	cfg := *req.Config
	cfg.BaseURL = opencodeGoInferenceBase(cfg.BaseURL) + "/chat/completions"
	baseHeaders := cfg.StaticHeaders
	hint := opencodeSessionHint(req, true)
	cfg.StaticHeaders = proxy.BuildOpenCodeHeaders(cfg.StaticHeaders, hint, true)

	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	resp, err := forwardOpenCodeRotating(ctx, func() (*http.Response, error) {
		return forwardOpencodeRequest(ctx, req.Client, &cfg, "public", body, true)
	}, func() (*http.Response, error) {
		return forwardOpencodeRequestDirect(ctx, &cfg, "public", body, true)
	}, opencodeSessionRotation(req, true, hint, func(session string) {
		cfg.StaticHeaders = proxy.BuildOpenCodeHeaders(baseHeaders, session, true)
	}), forgetOpencodeStableSession(req, true, hint))
	if err != nil {
		return fmt.Errorf("ForwardOpencodeGo (free lane): %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
		return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: errBody, Header: resp.Header}
	}

	// Read the first SSE frame before committing 200 OK. The free-tier gate can
	// answer a refusal inside a 200 frame, or stall before its first one, and
	// once the response head is on the wire no failover is possible — so this
	// has to happen here rather than inside the stream writer.
	streamBody, ferr := awaitOpenCodeFirstEvent(ctx, resp, 0)
	if ferr != nil {
		return ferr
	}

	if req.IsStream {
		return execSSEStream(w, streamBody, req)
	}
	data, err := io.ReadAll(io.LimitReader(streamBody, 10*1024*1024))
	if err != nil {
		return fmt.Errorf("read opencode-go free response: %w", err)
	}
	if converted, ok := sseToOpenAIJSON(data); ok {
		data = converted
	}
	data = bridgeOpenCodeExecuteToolCallsJSON(data)
	return jsonResponse(req.Ctx, w, bytes.NewReader(data), req.TranslateResp, req.ResponseBuf)
}

func isOpencodeGoGeminiModel(cleanModel string) bool {
	if mf, ok := providers.GetModelFormats("opencode-go", cleanModel); ok {
		return mf.TargetFormat == providers.FormatGemini
	}
	return strings.HasPrefix(strings.ToLower(cleanModel), "gemini-")
}

func forwardOpencodeGoGemini(w http.ResponseWriter, req *Request, cleanModel, sessionHeader string) error {
	action := "generateContent"
	if req.IsStream {
		action = "streamGenerateContent?alt=sse"
	}

	geminiURL := fmt.Sprintf("https://opencode.ai/inference/go/v1/models/%s:%s", cleanModel, action)
	if req.Config != nil && req.Config.BaseURL != "" && !strings.Contains(req.Config.BaseURL, "opencode.ai") {
		base := strings.TrimSuffix(strings.TrimRight(req.Config.BaseURL, "/"), "/chat/completions")
		geminiURL = fmt.Sprintf("%s/%s:%s", base, cleanModel, action)
	}

	geminiBody, err := translator.TranslateOpenAIToGeminiWithSession(req.Body, req.SessionID, req.APIKeyID)
	if err != nil {
		return fmt.Errorf("translate to Gemini for opencode-go: %w", err)
	}

	headers := map[string]string{
		"Content-Type":       "application/json",
		"x-goog-api-key":     req.APIKey,
		"x-opencode-session": sessionHeader,
	}
	if req.IsStream {
		headers["Accept"] = "text/event-stream"
	}

	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	resp, err := proxy.DoRequest(ctx, req.Client, "POST", geminiURL, headers, geminiBody)
	if err != nil {
		return fmt.Errorf("ForwardOpencodeGo (gemini route): %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, constants.UpstreamErrLimit))
		return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: errBody, Header: resp.Header}
	}

	if req.IsStream {
		return geminiStream(w, resp.Body, ctx, req)
	}
	return geminiNonStream(w, resp.Body)
}

func ForwardOpencodeGo(w http.ResponseWriter, req *Request) error {
	body := InjectReasoningContent(req.Body, "opencode-go")

	var reqObj struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &reqObj); err != nil {
		log.Warn("executor", "opencode unmarshal body", "error", err)
	}

	sessionHeader := deriveOpencodeSession(req.SessionID, "", req.ConnectionID)

	cleanModel := strings.TrimPrefix(reqObj.Model, "oc/")
	cleanModel = strings.TrimPrefix(cleanModel, "opencode-go/")
	cleanModel = strings.TrimPrefix(cleanModel, "opencode/")
	if parenIdx := strings.IndexByte(cleanModel, '('); parenIdx != -1 {
		cleanModel = cleanModel[:parenIdx]
	}

	// Free-tier ids 403 on the keyed go lane just like on zen, so they take
	// the keyless inference gate with public credentials (see zen).
	if isOpencodeFreeModel(cleanModel) {
		return forwardOpencodeGoFree(w, req, cleanModel)
	}

	if isOpencodeGoGeminiModel(cleanModel) {
		return forwardOpencodeGoGemini(w, req, cleanModel, sessionHeader)
	}

	if goTargetFormat(reqCtx(req), cleanModel) == providers.FormatOpenAIResponses {
		// Route through Responses API format: https://opencode.ai/zen/go/v1/responses (#3819, #3820, v0.5.75)
		transformedBody, _, err := buildResponsesBody(req.Body)
		if err != nil {
			return fmt.Errorf("transform body for opencode-go muse-spark: %w", err)
		}

		transformedBody, err = normalizeMuseSparkResponsesBody(transformedBody, reqObj.Model)
		if err != nil {
			return fmt.Errorf("normalize opencode-go muse-spark body: %w", err)
		}

		// The lane URL is what a relay forwards to, so it is built without a relay
		// branch.
		cfg := *req.Config
		cfg.BaseURL = strings.Replace(cfg.BaseURL, "opencode.ai/zen/go/v1", "opencode.ai/inference/go/openai/v1", 1)
		if !strings.HasSuffix(cfg.BaseURL, "/responses") {
			baseURL := strings.TrimRight(cfg.BaseURL, "/")
			if strings.HasSuffix(baseURL, "/chat/completions") {
				baseURL = strings.TrimSuffix(baseURL, "/chat/completions")
			}
			cfg.BaseURL = baseURL + "/responses"
		}
		headers := make(map[string]string)
		for k, v := range cfg.StaticHeaders {
			headers[k] = v
		}
		if headers["User-Agent"] == "" || !proxy.HasValidOpenCodeV1Version(headers["User-Agent"]) {
			headers["User-Agent"] = proxy.DefaultOpenCodeV1UA
		}
		if headers["x-opencode-client"] == "" {
			headers["x-opencode-client"] = "desktop"
		}
		if headers["x-opencode-request"] == "" {
			headers["x-opencode-request"] = proxy.GenerateOpenCodeRequestID()
		}
		headers["x-opencode-session"] = sessionHeader
		cfg.StaticHeaders = headers

		ctx := req.Ctx
		if ctx == nil {
			ctx = context.Background()
		}
		resp, err := forwardOpenCode(ctx, func() (*http.Response, error) {
			return proxy.ForwardOpenAI(ctx, req.Client, &cfg, req.APIKey, transformedBody, req.IsStream)
		}, func() (*http.Response, error) {
			return forwardOpencodeRequestDirect(ctx, &cfg, req.APIKey, transformedBody, req.IsStream)
		})
		if err != nil {
			return fmt.Errorf("ForwardOpencodeGo (muse-spark responses): %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
			return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: errBody, Header: resp.Header}
		}

		if req.IsStream {
			stallReader := proxy.NewStallReaderWithContext(ctx, resp.Body, cfg.ResolveStallTimeout(proxy.ResolveDefaultStallTimeout()), "opencode-go-responses")
			defer stallReader.Close()
			return handleCodexStream(w, req, stallReader)
		}
		return handleCodexStream(w, req, resp.Body)
	}

	if opencodeGoMessagesModels[reqObj.Model] || opencodeGoMessagesModels[cleanModel] {
		// Route to /inference/go/anthropic/v1/messages (Anthropic/Claude format)
		messagesURL := "https://opencode.ai/inference/go/anthropic/v1/messages"
		if req.Config != nil && req.Config.BaseURL != "" && !strings.Contains(req.Config.BaseURL, "opencode.ai") {
			base := strings.TrimRight(req.Config.BaseURL, "/")
			if strings.HasSuffix(base, "/chat/completions") {
				base = strings.TrimSuffix(base, "/chat/completions")
			}
			messagesURL = base + "/messages"
		}
		headers := map[string]string{
			"Content-Type":       "application/json",
			"x-api-key":          req.APIKey,
			"anthropic-version":  "2023-06-01",
			"x-opencode-session": sessionHeader,
		}
		if req.IsStream {
			headers["Accept"] = "text/event-stream"
		}
		ctx := req.Ctx
		if ctx == nil {
			ctx = context.Background()
		}
		messagesBody := ensureMessagesFor(body, "opencode-go", cleanModel)
		resp, err := proxy.DoRequest(ctx, req.Client, "POST", messagesURL, headers, messagesBody)
		if err != nil {
			return fmt.Errorf("ForwardOpencodeGo (messages route): %w", err)
		}
		defer resp.Body.Close()

		if req.IsStream {
			return handleClaudeMessagesStream(w, req, resp.Body)
		}
		return handleClaudeMessagesNonStream(w, req, resp.Body)
	}

	// Default OpenAI format endpoint: https://opencode.ai/zen/go/v1/chat/completions
	body, toolNameMap := translator.ConcealFingerprintTools(body)
	req.Ctx = translator.WithToolNameMap(req.Ctx, toolNameMap)
	w = NewToolNameRestoringWriter(w, toolNameMap)

	var reqGoMap map[string]any
	if err := json.Unmarshal(body, &reqGoMap); err == nil {
		if cleanModel != "" {
			reqGoMap["model"] = cleanModel
		}
		if strings.HasPrefix(strings.ToLower(cleanModel), "deepseek") {
			delete(reqGoMap, "response_format")
		}
		// Deterministic marshaling keeps the serialized request prefix
		// stable across turns, which DeepSeek's prefix prompt cache
		// requires; json/v2 randomizes map member order by default.
		if b, err := marshalStable(reqGoMap); err == nil {
			body = b
		}
	}

	cfg := *req.Config
	headers := make(map[string]string)
	for k, v := range cfg.StaticHeaders {
		headers[k] = v
	}
	// The paid go lane keeps the v1 identity (see DefaultOpenCodeV1UA).
	if headers["User-Agent"] == "" || !proxy.HasValidOpenCodeV1Version(headers["User-Agent"]) {
		headers["User-Agent"] = proxy.DefaultOpenCodeV1UA
	}
	if headers["x-opencode-client"] == "" {
		headers["x-opencode-client"] = "desktop"
	}
	if headers["x-opencode-request"] == "" {
		headers["x-opencode-request"] = proxy.GenerateOpenCodeRequestID()
	}
	headers["x-opencode-session"] = sessionHeader
	cfg.StaticHeaders = headers

	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	resp, err := forwardOpenCode(ctx, func() (*http.Response, error) {
		return proxy.ForwardOpenAI(ctx, req.Client, &cfg, req.APIKey, body, req.IsStream)
	}, func() (*http.Response, error) {
		return forwardOpencodeRequestDirect(ctx, &cfg, req.APIKey, body, req.IsStream)
	})
	if err != nil {
		return fmt.Errorf("ForwardOpencodeGo (default route): %w", err)
	}
	defer resp.Body.Close()

	if req.IsStream {
		return execSSEStream(w, resp.Body, req)
	}
	return jsonResponse(req.Ctx, w, resp.Body, req.TranslateResp, req.ResponseBuf)
}
