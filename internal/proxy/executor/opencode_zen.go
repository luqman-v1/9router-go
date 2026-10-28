package executor

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
	"9router/proxy/internal/translator"
)

// OpenCode Zen serves three endpoints off one key
// (open-sse/providers/registry/opencode-zen.js transports[]):
//
//	/zen/v1/chat/completions  Chat Completions
//	/zen/v1/messages          Claude Messages
//	/zen/v1/responses         OpenAI Responses
//
// Upstream picks the endpoint from the model's declared formats rather than the
// client's: a model that only speaks one lane is translated into that lane from
// whichever format the client used. This file is the Go form of that decision
// (open-sse/handlers/chatCore.js resolveTransport + modelTargetFormat, then
// open-sse/executors/opencode-zen.js).

const (
	zenMessagesPath  = "/messages"
	zenResponsesPath = "/responses"
)

// zenBaseURL resolves the API prefix of the request's base URL: the registry
// default, a connection override, or a relay host with the path carried in
// x-relay-path. Free-tier models answer behind the keyless v2 inference gate
// (keyed /zen/v1 requests for them 403), so the prefix follows the model and
// the lane: the v2 gate only serves the OpenAI-compatible surface, so the
// messages lane keeps the legacy prefix (see zenFreePrefix).
func zenBaseURL(cfg *providers.ProviderConfig, cleanModel, lanePath string) string {
	if cfg == nil {
		return ""
	}
	if _, isRelay := cfg.StaticHeaders["x-relay-target"]; isRelay {
		return strings.TrimRight(cfg.BaseURL, "/")
	}
	if isOpencodeFreeModel(cleanModel) {
		if lanePath == zenMessagesPath {
			return opencodeMessagesInferenceBase(cfg.BaseURL)
		}
		return opencodeInferenceBase(cfg.BaseURL)
	}
	base := strings.TrimRight(cfg.BaseURL, "/")
	base = strings.TrimSuffix(base, "/chat/completions")
	return strings.TrimSuffix(base, "/messages")
}

func isRelayConfig(cfg *providers.ProviderConfig) bool {
	if cfg == nil {
		return false
	}
	_, ok := cfg.StaticHeaders["x-relay-target"]
	return ok
}

// ServesMessagesEndpoint reports whether a provider answers a model on a
// Claude Messages endpoint, so a /v1/messages client can be forwarded in its own
// format instead of being translated to OpenAI first.
func ServesMessagesEndpoint(provider, model string) bool {
	formats, declared := providers.GetModelFormats(provider, model)
	return declared && formats.TargetFormat == providers.FormatClaude
}

// zenTargetFormat reports which of the three lanes a request must travel on.
func zenTargetFormat(ctx context.Context, model string) string {
	formats, declared := providers.GetModelFormats("opencode-zen", model)
	if !declared {
		return providers.FormatOpenAI
	}
	// A client that already speaks a lane the model serves is forwarded as-is;
	// upstream prefers that match over the model-level target format.
	if formats.SupportsFormat(clientFormatFor(ctx)) {
		return clientFormatFor(ctx)
	}
	return formats.TargetFormat
}

// clientFormatFor reports the wire format the caller is speaking. A /v1/messages
// caller is already Claude-shaped by the time it reaches an executor — the
// handler converts it to OpenAI for providers that do not speak Claude upstream
// — so the conversation endpoint only needs the Chat/Responses distinction.
func clientFormatFor(ctx context.Context) string {
	if translator.IsResponsesClient(ctx) {
		return providers.FormatOpenAIResponses
	}
	return providers.FormatOpenAI
}

// zenRequestMode reports whether a Claude-native request may stay in Messages
// form instead of being converted to OpenAI, and which wire format the body is
// currently in. A /v1/messages body is converted away from Claude format only
// for providers without a Messages endpoint, so a model routed to that
// endpoint must keep the client's own format end to end.
func zenRequestMode(req *Request, model string) (claudeNative bool, sourceFormat string) {
	ctx := reqCtx(req)
	clientFormat := clientFormatFor(ctx)
	if !req.ClaudeClient {
		return false, clientFormat
	}
	// A Responses caller is never Claude-native, even when the model is
	// answered from /messages: its body still has to be converted.
	if clientFormat == providers.FormatOpenAIResponses {
		return false, clientFormat
	}
	if zenTargetFormat(ctx, model) == providers.FormatClaude {
		return true, providers.FormatClaude
	}
	return false, clientFormat
}

// ForwardOpencodeZen handles requests for opencode-zen, the pay-as-you-go lane
// that shares its catalog with the free tier.
func ForwardOpencodeZen(w http.ResponseWriter, req *Request) error {
	var reqObj struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(req.Body, &reqObj)
	cleanModel := stripOpenCodePrefix(reqObj.Model, "opencode-zen", "ocz")

	claudeNative, sourceFormat := zenRequestMode(req, cleanModel)
	switch zenTargetFormat(reqCtx(req), cleanModel) {
	case providers.FormatClaude:
		return forwardZenMessages(w, req, cleanModel, claudeNative, sourceFormat)
	case providers.FormatOpenAIResponses:
		return forwardZenResponses(w, req, cleanModel, sourceFormat)
	default:
		return forwardZenChat(w, req, cleanModel, sourceFormat)
	}
}

// zenChatLaneBody converts a Responses payload for a model the /responses lane
// does not serve. Without it the Messages and Chat endpoints reject the
// unknown "input" key.
func zenChatLaneBody(body []byte) ([]byte, error) {
	converted, err := translator.ResponsesToChatRequest(body)
	if err != nil {
		return nil, err
	}
	return converted.Body, nil
}

// zenShapedBody applies the tool-name shaping the request's destination gate
// expects, and returns the shaped body with the rename map the response side
// needs to restore the caller's spelling.
//
// The shaper must follow the gate the request lands on, not the lane it arrived
// from: a free id travels to the keyless /inference gate whichever lane carried
// it, and that gate wants the genuine CLI 2.x read/shell definitions. Shaping a
// free request with the legacy quartet — the /zen/v1 fingerprint — sends a body
// the inference gate refuses, which is what the messages lane used to do.
func zenShapedBody(body []byte, cleanModel string, free bool) ([]byte, map[string]string) {
	if free {
		return translator.ConcealOpenCodeV2FingerprintTools(body, false, isOpenCodeAutoToolChoiceModel(cleanModel))
	}
	return translator.ConcealFingerprintTools(body)
}

func reqCtx(req *Request) context.Context {
	if req.Ctx != nil {
		return req.Ctx
	}
	return context.Background()
}

// stripOpenCodePrefix removes the provider prefix a caller may have typed
// ("ocz/muse-spark-1.3") plus the thinking suffix, leaving the bare model id
// upstream receives.
func stripOpenCodePrefix(model string, prefixes ...string) string {
	clean := model
	for _, p := range prefixes {
		clean = strings.TrimPrefix(clean, p+"/")
	}
	for _, p := range []string{"opencode-go/", "opencode/", "oc/"} {
		clean = strings.TrimPrefix(clean, p)
	}
	if parenIdx := strings.IndexByte(clean, '('); parenIdx != -1 {
		clean = clean[:parenIdx]
	}
	return strings.TrimSpace(clean)
}

// zenHeaders builds the fingerprint headers the free-tier gate expects, keeping
// the caller's key rather than the public placeholder
// (open-sse/executors/opencode-zen.js buildHeaders).
//
// A relay header set (x-relay-target / x-relay-path) is carried over verbatim:
// BuildEdgeRelayHeaders stamps those when an edge pool fronts a provider, and
// the relay answers 400 "Missing x-relay-target header" without them.
func zenHeaders(cfg *providers.ProviderConfig, session string, stream bool, free bool) map[string]string {
	headers := proxy.BuildOpenCodeHeaders(nil, session, stream)
	if !free {
		// The keyed lane keeps the v1 identity: upstream leaves opencode-zen
		// on opencode/1.19.4 + desktop while only the keyless free executor
		// moves to v2. Free-tier models keep the v2 cli identity above, which
		// is what the inference gate expects alongside Bearer public.
		headers["x-opencode-client"] = "desktop"
		headers["User-Agent"] = proxy.DefaultOpenCodeV1UA
	}
	for k, v := range cfg.StaticHeaders {
		switch strings.ToLower(k) {
		case "x-relay-target", "x-relay-path", "x-opencode-project":
			headers[k] = v
		case "x-opencode-client":
			// Paid lane only: a connection may override the desktop default.
			// Free-tier models always travel as the cli identity the gate
			// expects, never as the connection's desktop default.
			if !free && v != "" {
				headers[k] = v
			}
		}
	}
	return headers
}

// zenFreePrefix reports the inference-gate prefix a free model's lane travels
// on. Chat and Responses live under the v2 gate's OpenAI-compatible surface
// (/inference/openai/v1); the Messages endpoint was never migrated — the v2
// prefix 404s it — so a free id on the messages lane keeps the legacy
// /inference/v1 prefix, which still serves POST /messages.
func zenFreePrefix(lanePath string) string {
	if lanePath == zenMessagesPath {
		return "/inference/v1"
	}
	return "/inference/openai/v1"
}

// zenRelayPath reports the path an edge relay should forward to for a Zen lane.
// The relay header set is stamped from the connection's base URL
// (BuildEdgeRelayHeaders), so the path it carries is whatever lane that base
// URL named — /chat/completions for a default connection. Rewriting it keeps
// the relay pointed at the endpoint the model actually lives on.
func zenRelayPath(cfg *providers.ProviderConfig, cleanModel, lanePath string) string {
	prefix := "/zen/v1"
	if isOpencodeFreeModel(cleanModel) {
		// Free-tier models live behind the v2 inference gate, so their lane
		// carries the inference prefix instead of /zen/v1 — except messages,
		// which the v2 gate does not serve (zenFreePrefix).
		prefix = zenFreePrefix(lanePath)
	}
	if cfg != nil {
		if target := cfg.StaticHeaders["x-relay-target"]; target != "" {
			// An absolute origin is only a host; the lane decides the path. A
			// relative one already carries the origin's path.
			if strings.Contains(target, "://") {
				return prefix + lanePath
			}
			if i := strings.IndexByte(target, '/'); i >= 0 {
				return target[i:]
			}
			return "/"
		}
	}
	return prefix + lanePath
}

func zenAuthHeaders(apiKey string) map[string]string {
	return map[string]string{"x-api-key": apiKey}
}

// forwardZenChat serves the /chat/completions lane (Gemini, DeepSeek, GLM,
// MiniMax, Kimi, Big Pickle, free tier).
func forwardZenChat(w http.ResponseWriter, req *Request, cleanModel, sourceFormat string) error {
	body := req.Body
	if sourceFormat == providers.FormatOpenAIResponses {
		// A /v1/responses client asking for a chat-lane model: the Chat
		// endpoints reject the Responses "input" key.
		converted, err := zenChatLaneBody(req.Body)
		if err != nil {
			return fmt.Errorf("ForwardOpencodeZen chat lane conversion: %w", err)
		}
		body = converted
	}
	body = InjectReasoningContent(body, "opencode-zen")
	// Keyed /zen/v1 requests for free-tier ids 403 ("free tier can only be
	// used from within OpenCode"), so those travel exactly like free-executor
	// requests: v2 fingerprint, inference endpoint, public credentials.
	free := isOpencodeFreeModel(cleanModel)
	var zenToolNameMap map[string]string
	if free {
		body, zenToolNameMap = translator.ConcealOpenCodeV2FingerprintTools(body, false, isOpenCodeAutoToolChoiceModel(cleanModel))
		body = translator.ApplyFingerprintSystemPrompt(body)
		if req.IsStream {
			w = NewOpenCodeExecuteBridgeWriter(w)
		}
	} else {
		body, zenToolNameMap = translator.ConcealFingerprintTools(body)
	}
	req.Ctx = translator.WithToolNameMap(req.Ctx, zenToolNameMap)
	w = NewToolNameRestoringWriter(w, zenToolNameMap)

	// Upstream rejects stream:false on the free-tier gate even for a keyed lane.
	body = withJSONFields(body, map[string]any{"stream": true, "model": cleanModel})

	cfg := *req.Config
	if isRelayConfig(&cfg) {
		// The relay stamps the destination in its headers, so the lane
		// decision belongs in x-relay-path, not in the URL we dial.
		cfg.StaticHeaders = copyHeaders(cfg.StaticHeaders)
		cfg.StaticHeaders["x-relay-path"] = zenRelayPath(req.Config, cleanModel, "/chat/completions")
		cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/") + "/chat/completions"
	} else {
		cfg.BaseURL = zenBaseURL(&cfg, cleanModel, "/chat/completions") + "/chat/completions"
	}
	zenHint := opencodeSessionHint(req, free)
	cfg.StaticHeaders = zenHeaders(&cfg, zenHint, true, free)

	// The retry wraps the transport choice: a repeat must re-dial through
	// whichever stack the first attempt used, because the free tier only
	// answers the stealth client and a plain-HTTP repeat would fail for a
	// different reason than the refusal it is retrying.
	resp, err := forwardOpenCodeRotating(reqCtx(req), func() (*http.Response, error) {
		if free {
			return forwardOpencodeRequest(reqCtx(req), req.Client, &cfg, req.APIKey, body, true)
		}
		return proxy.ForwardOpenAI(reqCtx(req), req.Client, &cfg, req.APIKey, body, true)
	}, func() (*http.Response, error) {
		return forwardOpencodeRequestDirect(reqCtx(req), &cfg, req.APIKey, body, true)
	}, rotateOpencodeStableSession(opencodeStableSessionKey(req, free), zenHint, func(session string) {
		cfg.StaticHeaders = zenHeaders(&cfg, session, true, free)
	}))
	if err != nil {
		return fmt.Errorf("ForwardOpencodeZen: %w", err)
	}
	defer resp.Body.Close()
	if err := upstreamStatusError(resp); err != nil {
		return err
	}

	if req.IsStream {
		return execSSEStream(w, resp.Body, req)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return fmt.Errorf("read opencode-zen response: %w", err)
	}
	// The gate forces streaming, so a sync caller gets the SSE body re-aggregated.
	if converted, ok := sseToOpenAIJSON(data); ok {
		data = converted
	}
	if free {
		data = bridgeOpenCodeExecuteToolCallsJSON(data)
	}
	return jsonResponse(req.Ctx, w, bytes.NewReader(data), req.TranslateResp, req.ResponseBuf)
}

// forwardZenResponses serves the /responses lane (GPT, Grok, Muse Spark). A
// Responses client is forwarded untouched; a Chat Completions caller is
// converted, because the Responses endpoint has no other entry point.
func forwardZenResponses(w http.ResponseWriter, req *Request, cleanModel, sourceFormat string) error {
	body := req.Body
	if sourceFormat != providers.FormatOpenAIResponses {
		transformed, _, err := buildResponsesBody(req.Body)
		if err != nil {
			return fmt.Errorf("ForwardOpencodeZen transform body: %w", err)
		}
		body = transformed
	}

	body, err := normalizeZenResponsesBody(body, cleanModel)
	if err != nil {
		return fmt.Errorf("normalize opencode-zen responses body: %w", err)
	}
	// Same free-tier rule as the chat lane above: v2 fingerprint and public
	// credentials behind the inference gate, quartet cloaking otherwise.
	free := isOpencodeFreeModel(cleanModel)
	var toolNameMap map[string]string
	if free {
		body, toolNameMap = translator.ConcealOpenCodeV2FingerprintTools(body, true, isOpenCodeAutoToolChoiceModel(cleanModel))
		body = translator.ApplyFingerprintSystemPrompt(body)
	} else {
		body, toolNameMap = translator.ConcealFingerprintTools(body)
	}
	req.Ctx = translator.WithToolNameMap(req.Ctx, toolNameMap)
	w = NewToolNameRestoringWriter(w, toolNameMap)
	if sourceFormat == providers.FormatOpenAIResponses {
		req.ToolNameMap = mergeToolNameMaps(req.ToolNameMap, toolNameMap)
	}

	cfg := *req.Config
	if isRelayConfig(&cfg) {
		// The relay stamps the destination in its headers, so the lane
		// decision belongs in x-relay-path, not in the URL we dial.
		cfg.StaticHeaders = copyHeaders(cfg.StaticHeaders)
		cfg.StaticHeaders["x-relay-path"] = zenRelayPath(req.Config, cleanModel, zenResponsesPath)
		cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/") + zenResponsesPath
	} else {
		cfg.BaseURL = zenBaseURL(&cfg, cleanModel, zenResponsesPath) + zenResponsesPath
	}
	// zenHeaders reads from the copy so the relay headers just set survive.
	zenHint := opencodeSessionHint(req, free)
	cfg.StaticHeaders = zenHeaders(&cfg, zenHint, req.IsStream, free)

	ctx := reqCtx(req)
	if free && req.IsStream && !translator.IsResponsesClient(ctx) {
		// A chat-completions client on the responses lane receives converted
		// chat chunks, so the execute/CALL_TOOL bridge applies. A native
		// Responses client gets the event stream passed through, where the
		// bridge's line protocol does not apply.
		w = NewOpenCodeExecuteBridgeWriter(w)
	}
	resp, err := forwardOpenCodeRotating(ctx, func() (*http.Response, error) {
		if free {
			return forwardOpencodeRequest(ctx, req.Client, &cfg, req.APIKey, body, req.IsStream)
		}
		return proxy.ForwardOpenAI(ctx, req.Client, &cfg, req.APIKey, body, req.IsStream)
	}, func() (*http.Response, error) {
		return forwardOpencodeRequestDirect(ctx, &cfg, req.APIKey, body, req.IsStream)
	}, rotateOpencodeStableSession(opencodeStableSessionKey(req, free), zenHint, func(session string) {
		cfg.StaticHeaders = zenHeaders(&cfg, session, req.IsStream, free)
	}))
	if err != nil {
		return fmt.Errorf("ForwardOpencodeZen (responses): %w", err)
	}
	defer resp.Body.Close()
	if err := upstreamStatusError(resp); err != nil {
		return err
	}

	if req.IsStream {
		stallReader := proxy.NewStallReaderWithContext(ctx, resp.Body, 0, "opencode-zen-responses")
		defer stallReader.Close()
		return handleCodexStream(w, req, stallReader)
	}
	return handleCodexStream(w, req, resp.Body)
}

// forwardZenMessages serves the /messages lane (Claude, Qwen). A /v1/messages
// client is forwarded in its own format; a Chat Completions caller is converted
// because the Messages endpoint rejects that shape.
func forwardZenMessages(w http.ResponseWriter, req *Request, cleanModel string, claudeNative bool, sourceFormat string) error {
	body := req.Body
	switch {
	case claudeNative:
		// The client already speaks the Messages wire format.
	case sourceFormat == providers.FormatOpenAIResponses:
		converted, err := zenChatLaneBody(req.Body)
		if err != nil {
			return fmt.Errorf("ForwardOpencodeZen messages lane conversion: %w", err)
		}
		body = EnsureClaudeMessagesFor(converted, "opencode-zen", cleanModel)
	default:
		body = EnsureClaudeMessagesFor(body, "opencode-zen", cleanModel)
	}
	free := isOpencodeFreeModel(cleanModel)
	body, toolNameMap := zenShapedBody(body, cleanModel, free)
	req.Ctx = translator.WithToolNameMap(req.Ctx, toolNameMap)
	w = NewToolNameRestoringWriter(w, toolNameMap)

	headers := zenAuthHeaders(req.APIKey)
	if free {
		// The inference gate expects public credentials, not a user key.
		delete(headers, "x-api-key")
		headers["Authorization"] = "Bearer public"
	}
	headers["anthropic-version"] = "2023-06-01"
	// The session merge is a closure so a spent-session rotation (429
	// FreeUsageLimitError, free lane) can re-install a freshly minted session
	// on the same header map the forward dials with.
	msgHint := opencodeSessionHint(req, free)
	mergeZenSession := func(session string) {
		for k, v := range zenHeaders(req.Config, session, req.IsStream, free) {
			switch strings.ToLower(k) {
			case "authorization", "x-api-key", "content-type", "accept":
				continue
			default:
				headers[k] = v
			}
		}
	}
	mergeZenSession(msgHint)

	// An edge relay carries the destination in headers, so the lane belongs in
	// x-relay-path rather than in the URL the gateway dials.
	messagesURL := zenBaseURL(req.Config, cleanModel, zenMessagesPath) + zenMessagesPath
	if isRelayConfig(req.Config) {
		headers["x-relay-path"] = zenRelayPath(req.Config, cleanModel, zenMessagesPath)
		messagesURL = strings.TrimRight(req.Config.BaseURL, "/") + zenMessagesPath
	}

	ctx := reqCtx(req)
	var (
		resp *http.Response
		err  error
	)
	if free {
		// The free tier answers only the stealth stack on a direct dial: a
		// plain Go request is refused with 403 FreeTierError while the same
		// bytes through an edge relay pass, so the free lane must not take
		// the plain path (forwardOpencodeGoFree had the same hole).
		msgCfg := *req.Config
		msgCfg.BaseURL = messagesURL
		msgCfg.StaticHeaders = headers
		resp, err = forwardOpenCodeRotating(ctx, func() (*http.Response, error) {
			return forwardOpencodeRequest(ctx, req.Client, &msgCfg, "public", body, req.IsStream)
		}, func() (*http.Response, error) {
			return forwardOpencodeRequestDirect(ctx, &msgCfg, "public", body, req.IsStream)
		}, rotateOpencodeStableSession(opencodeStableSessionKey(req, free), msgHint, mergeZenSession))
	} else {
		resp, err = proxy.DoRequest(ctx, req.Client, "POST", messagesURL, headers, body)
	}
	if err != nil {
		return fmt.Errorf("ForwardOpencodeZen (messages): %w", err)
	}
	defer resp.Body.Close()
	if err := upstreamStatusError(resp); err != nil {
		return err
	}

	if req.IsStream {
		return handleClaudeMessagesStream(w, req, resp.Body)
	}
	return handleClaudeMessagesNonStream(w, req, resp.Body)
}

// normalizeZenResponsesBody applies the Responses-lane requirements: the output
// cap is max_output_tokens, reasoning is an object, the request is streamed and
// not stored, and prior-turn reasoning items are dropped because their
// encrypted_content cannot be validated across rotated accounts
// (open-sse/executors/opencode-zen.js transformRequest + sanitizeResponsesItems).
func normalizeZenResponsesBody(body []byte, cleanModel string) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return body, nil
	}
	if out, ok := m["max_output_tokens"]; !ok || out == nil {
		if v := firstNonNil(m["max_completion_tokens"], m["max_tokens"]); v != nil {
			m["max_output_tokens"] = v
		}
	}
	delete(m, "max_tokens")
	delete(m, "max_completion_tokens")

	applyZenResponsesReasoning(m)

	if inList, ok := m["input"].([]any); ok {
		clean := make([]any, 0, len(inList))
		for _, item := range inList {
			itemMap, ok := item.(map[string]any)
			if !ok {
				clean = append(clean, item)
				continue
			}
			if itemMap["type"] == "reasoning" {
				continue
			}
			delete(itemMap, "encrypted_content")
			delete(itemMap, "reasoning_encrypted_content")
			clean = append(clean, itemMap)
		}
		m["input"] = clean
	}

	// PR #4062: an explicit non-auto tool_choice is rejected on muse-spark-1.3.
	if strings.Contains(cleanModel, "muse-spark-1.3") {
		if tc, ok := m["tool_choice"]; ok && tc != nil {
			if s, isStr := tc.(string); !isStr || s != "auto" {
				m["tool_choice"] = "auto"
			}
		}
	}

	m["stream"] = true
	m["store"] = false
	// Deterministic marshaling keeps the serialized request prefix stable
	// across the turns of a session, which prefix-caching upstreams
	// (deepseek) require.
	return marshalStable(m)
}

// applyZenResponsesReasoning folds a Chat-shaped reasoning_effort into the
// Responses wire format, which nests effort under reasoning:{effort,summary}
// and rejects a top-level reasoning_effort outright (strict upstreams answer
// "unknown parameter `reasoning_effort`"). An explicit "none" has no nested
// equivalent and is expressed upstream by removing reasoning entirely — sending
// effort "none" instead asks a model to think at the lowest level rather than
// to not think, which is a different (and billable) request. Parity with the
// openai-responses branch of applyFormat in
// open-sse/translator/concerns/thinkingUnified.js.
func applyZenResponsesReasoning(m map[string]any) {
	effort, ok := m["reasoning_effort"].(string)
	if !ok {
		if existing, isMap := m["reasoning"].(map[string]any); isMap && existing["summary"] == nil {
			existing["summary"] = "auto"
		}
		return
	}
	delete(m, "reasoning_effort")
	if strings.EqualFold(strings.TrimSpace(effort), "none") {
		delete(m, "reasoning")
		return
	}
	if effort == "" {
		return
	}
	reasoning, isMap := m["reasoning"].(map[string]any)
	if !isMap {
		reasoning = map[string]any{}
	}
	reasoning["effort"] = effort
	if reasoning["summary"] == nil {
		reasoning["summary"] = "auto"
	}
	m["reasoning"] = reasoning
}

// withJSONFields merges top-level fields into a JSON object body, leaving it
// untouched when it does not parse.
func withJSONFields(body []byte, fields map[string]any) []byte {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return body
	}
	for k, v := range fields {
		if v == "" {
			continue
		}
		m[k] = v
	}
	out, err := marshalStable(m)
	if err != nil {
		return body
	}
	return out
}

func copyHeaders(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func mergeToolNameMaps(base, extra map[string]string) map[string]string {
	if len(extra) == 0 {
		return base
	}
	merged := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range extra {
		merged[k] = v
	}
	return merged
}

func firstNonNil(values ...any) any {
	for _, v := range values {
		if v != nil {
			return v
		}
	}
	return nil
}

// upstreamStatusError converts a non-200 upstream reply into a typed error so
// the caller sees the real status and body for failover decisions.
func upstreamStatusError(resp *http.Response) error {
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
	return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: body, Header: resp.Header}
}
