package executor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	json "encoding/json/v2"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/log"
	"9router/proxy/internal/proxy"
)

// grok.com's web chat, ported from upstream open-sse/executors/grok-web.js
// (38a0c449, part of #624). One POST to conversations/new with the full
// grok.com Cookie header answers an NDJSON stream: token deltas, a thinking
// channel for the thinking modes, native tool cards, and — for media turns —
// card attachments. Everything routes to grok-4-1-thinking-1129, the only
// modelName the endpoint still accepts; the model the client names decides
// the modelMode.

const (
	grokWebChatEndpoint = "/rest/app-chat/conversations/new"
	grokWebOrigin       = "https://grok.com"
)

// grokWebChatURL is the endpoint every chat and media turn posts to; var so
// the test suite can aim it at a fake site.
var grokWebChatURL = grokWebOrigin + grokWebChatEndpoint

// grokAssetsBase is where finished Imagine assets are downloaded from.
var grokAssetsBase = "https://assets.grok.com"

// grokModelRoute is the wire translation for one client-facing model name.
type grokModelRoute struct {
	grokModel  string
	modelMode  string
	isThinking bool
}

// grokModelMap routes every catalog name onto the one modelName the endpoint
// accepts plus the account-gated mode. FAST is the reliably-available mode;
// THINKING/EXPERT/420 are usage-gated per account.
var grokModelMap = map[string]grokModelRoute{
	"grok-3":            {grokModel: "grok-4-1-thinking-1129", modelMode: "MODEL_MODE_FAST", isThinking: false},
	"grok-3-mini":       {grokModel: "grok-4-1-thinking-1129", modelMode: "MODEL_MODE_GROK_4_1_MINI_THINKING", isThinking: true},
	"grok-3-thinking":   {grokModel: "grok-4-1-thinking-1129", modelMode: "MODEL_MODE_GROK_4_1_THINKING", isThinking: true},
	"grok-4":            {grokModel: "grok-4-1-thinking-1129", modelMode: "MODEL_MODE_FAST", isThinking: false},
	"grok-4-mini":       {grokModel: "grok-4-1-thinking-1129", modelMode: "MODEL_MODE_GROK_4_1_MINI_THINKING", isThinking: true},
	"grok-4-thinking":   {grokModel: "grok-4-1-thinking-1129", modelMode: "MODEL_MODE_GROK_4_1_THINKING", isThinking: true},
	"grok-4-heavy":      {grokModel: "grok-4-1-thinking-1129", modelMode: "MODEL_MODE_EXPERT", isThinking: true},
	"grok-4.1-mini":     {grokModel: "grok-4-1-thinking-1129", modelMode: "MODEL_MODE_GROK_4_1_MINI_THINKING", isThinking: true},
	"grok-4.1-fast":     {grokModel: "grok-4-1-thinking-1129", modelMode: "MODEL_MODE_FAST", isThinking: false},
	"grok-4.1-expert":   {grokModel: "grok-4-1-thinking-1129", modelMode: "MODEL_MODE_EXPERT", isThinking: true},
	"grok-4.1-thinking": {grokModel: "grok-4-1-thinking-1129", modelMode: "MODEL_MODE_GROK_4_1_THINKING", isThinking: true},
	"grok-4.2":          {grokModel: "grok-4-1-thinking-1129", modelMode: "MODEL_MODE_GROK_420", isThinking: false},
	"grok-4.20":         {grokModel: "grok-4-1-thinking-1129", modelMode: "MODEL_MODE_GROK_420", isThinking: false},
	"grok-4.20-beta":    {grokModel: "grok-4-1-thinking-1129", modelMode: "MODEL_MODE_GROK_420", isThinking: false},
}

func grokResolveModel(model string) grokModelRoute {
	if route, ok := grokModelMap[model]; ok {
		return route
	}
	log.Info("grok-web", "unmapped model, defaulting to grok-4.1-fast", "model", model)
	return grokModelMap["grok-4.1-fast"]
}

// ——— Cookie normalization (upstream utils/webCookieAuth.js) ———

// grokStripCookieInputPrefix removes the decorations a paste sometimes
// carries: a bearer prefix or a "cookie:" label.
func grokStripCookieInputPrefix(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	lower := strings.ToLower(trimmed)
	if after, ok := strings.CutPrefix(lower, "bearer "); ok {
		_ = after
		trimmed = trimmed[len("bearer "):]
	}
	lower = strings.ToLower(trimmed)
	if after, ok := strings.CutPrefix(lower, "cookie:"); ok {
		_ = after
		trimmed = trimmed[len("cookie:"):]
	}
	return strings.TrimSpace(trimmed)
}

// grokExtractCookieValue pulls one cookie's value out of whatever the user
// pasted: a bare value, a single pair, or a full DevTools blob. "" when the
// blob does not carry the named cookie.
func grokExtractCookieValue(raw, cookieName string) string {
	trimmed := grokStripCookieInputPrefix(raw)
	if trimmed == "" {
		return ""
	}
	if strings.Contains(trimmed, ";") {
		for _, part := range strings.Split(trimmed, ";") {
			part = strings.TrimSpace(part)
			if value, ok := strings.CutPrefix(part, cookieName+"="); ok {
				return strings.TrimSpace(value)
			}
		}
		return ""
	}
	if value, ok := strings.CutPrefix(trimmed, cookieName+"="); ok {
		return value
	}
	return trimmed
}

// grokBuildCookieHeader builds the Cookie header for grok.com from whatever
// the user pasted. Always emits sso=<value>; the paired sso-rw write cookie
// and the Cloudflare cookies are forwarded when present, because Grok's
// anti-bot rejects a sso without sso-rw and Cloudflare reads cf_clearance.
func grokBuildCookieHeader(raw string) string {
	sso := grokExtractCookieValue(raw, "sso")
	if sso == "" {
		return ""
	}
	parts := []string{"sso=" + sso}
	for _, name := range []string{"sso-rw", "cf_clearance", "__cf_bm"} {
		for _, part := range strings.Split(raw, ";") {
			part = strings.TrimSpace(part)
			if strings.HasPrefix(part, name+"=") {
				if value := grokExtractCookieValue(raw, name); value != "" {
					parts = append(parts, name+"="+value)
				}
				break
			}
		}
	}
	return strings.Join(parts, "; ")
}

// ——— Request identity headers ———

func grokRandomHex(bytes int) string {
	buf := make([]byte, bytes)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

func grokRandomString(length int, alphanumeric bool) string {
	chars := "abcdefghijklmnopqrstuvwxyz"
	if alphanumeric {
		chars += "0123456789"
	}
	out := make([]byte, length)
	for i := range out {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		out[i] = chars[n.Int64()]
	}
	return string(out)
}

// grokStatsigID base64s the fake TypeError payload the site's client sends
// as x-statsig-id (upstream generateStatsigId).
func grokStatsigID() string {
	var msg string
	if i, _ := rand.Int(rand.Reader, big.NewInt(2)); i.Int64() == 0 {
		msg = fmt.Sprintf("x1:TypeError: Cannot read properties of null (reading 'children[\"%s\"]')", grokRandomString(5, true))
	} else {
		msg = fmt.Sprintf("x1:TypeError: Cannot read properties of undefined (reading '%s')", grokRandomString(10, false))
	}
	return base64.StdEncoding.EncodeToString([]byte(msg))
}

func grokUUID() string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	h := hex.EncodeToString(buf)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// grokTraceparent mints the W3C trace header the site's client attaches.
func grokTraceparent() string {
	return "00-" + grokRandomHex(16) + "-" + grokRandomHex(8) + "-00"
}

// grokChatHeaders builds the browser-identity header set for one chat turn.
// The order is Chrome's, and it is load-bearing: header order is part of the
// fingerprint the transport replays.
func grokChatHeaders(cookieHeader, browserID, requestID string) [][2]string {
	return [][2]string{
		{"Accept", "*/*"},
		{"Accept-Language", "en-US,en;q=0.9"},
		{"Baggage", "sentry-environment=production,sentry-release=d6add6fb0460641fd482d767a335ef72b9b6abb8,sentry-public_key=b311e0f2690c81f25e2c4cf6d4f7ce1c"},
		{"Cache-Control", "no-cache"},
		{"Content-Type", "application/json"},
		{"Origin", grokWebOrigin},
		{"Pragma", "no-cache"},
		{"Referer", grokWebOrigin + "/"},
		{"Sec-Ch-Ua", `"Chromium";v="146", "Not(A:Brand";v="24"`},
		{"Sec-Ch-Ua-Mobile", "?0"},
		{"Sec-Ch-Ua-Platform", `"macOS"`},
		{"Sec-Fetch-Dest", "empty"},
		{"Sec-Fetch-Mode", "cors"},
		{"Sec-Fetch-Site", "same-origin"},
		{"User-Agent", grokWebUserAgent},
		{"x-statsig-id", grokStatsigID()},
		{"Browser-Id", browserID},
		{"x-xai-request-id", requestID},
		{"traceparent", grokTraceparent()},
	}
}

// grokMessagePayload is the conversations/new request body. The field set is
// what the site's own client sends; several are opaque to this side but
// present on the wire.
type grokMessagePayload struct {
	Temporary                   bool           `json:"temporary"`
	ModelName                   string         `json:"modelName"`
	ModelMode                   string         `json:"modelMode,omitempty"`
	Message                     string         `json:"message"`
	FileAttachments             []any          `json:"fileAttachments"`
	ImageAttachments            []any          `json:"imageAttachments"`
	DisableSearch               bool           `json:"disableSearch"`
	EnableImageGeneration       bool           `json:"enableImageGeneration"`
	ReturnImageBytes            bool           `json:"returnImageBytes"`
	ReturnRawGrokInXaiRequest   bool           `json:"returnRawGrokInXaiRequest"`
	EnableImageStreaming        bool           `json:"enableImageStreaming"`
	ImageGenerationCount        int            `json:"imageGenerationCount"`
	ForceConcise                bool           `json:"forceConcise"`
	ToolOverrides               map[string]any `json:"toolOverrides"`
	EnableSideBySide            bool           `json:"enableSideBySide"`
	SendFinalMetadata           bool           `json:"sendFinalMetadata"`
	IsReasoning                 bool           `json:"isReasoning"`
	DisableTextFollowUps        bool           `json:"disableTextFollowUps"`
	DisableMemory               bool           `json:"disableMemory"`
	ForceSideBySide             bool           `json:"forceSideBySide"`
	IsAsyncChat                 bool           `json:"isAsyncChat"`
	DisableSelfHarmShortCircuit bool           `json:"disableSelfHarmShortCircuit"`
	DeviceEnvInfo               map[string]any `json:"deviceEnvInfo"`
	Kind                        string         `json:"kind,omitempty"`
	MediaGenInput               map[string]any `json:"mediaGenInput,omitempty"`
	ResponseMetadata            map[string]any `json:"responseMetadata,omitempty"`
}

func grokDeviceEnvInfo() map[string]any {
	return map[string]any{
		"darkModeEnabled":  false,
		"devicePixelRatio": 2,
		"screenWidth":      2056,
		"screenHeight":     1329,
		"viewportWidth":    2056,
		"viewportHeight":   1083,
	}
}

func grokChatPayload(route grokModelRoute, message string) grokMessagePayload {
	return grokMessagePayload{
		Temporary:                   true,
		ModelName:                   route.grokModel,
		ModelMode:                   route.modelMode,
		Message:                     message,
		FileAttachments:             []any{},
		ImageAttachments:            []any{},
		DisableSearch:               false,
		EnableImageGeneration:       false,
		ReturnImageBytes:            false,
		ReturnRawGrokInXaiRequest:   false,
		EnableImageStreaming:        false,
		ImageGenerationCount:        0,
		ForceConcise:                false,
		ToolOverrides:               map[string]any{},
		EnableSideBySide:            true,
		SendFinalMetadata:           true,
		IsReasoning:                 false,
		DisableTextFollowUps:        false,
		DisableMemory:               true,
		ForceSideBySide:             false,
		IsAsyncChat:                 false,
		DisableSelfHarmShortCircuit: false,
		DeviceEnvInfo:               grokDeviceEnvInfo(),
	}
}

// grokParseMessages flattens the OpenAI messages into Grok's single-message
// format for a turn without client tools: every turn is prefixed with its
// role except the last user turn, which rides bare.
func grokParseMessages(messages []any) string {
	type turn struct {
		role string
		text string
	}
	extracted := make([]turn, 0, len(messages))
	for _, raw := range messages {
		msg, _ := raw.(map[string]any)
		if msg == nil {
			continue
		}
		role, _ := msg["role"].(string)
		if role == "" {
			role = "user"
		}
		if role == "developer" {
			role = "system"
		}
		text := grokExtractTextContent(msg)
		if strings.TrimSpace(text) == "" {
			continue
		}
		extracted = append(extracted, turn{role: role, text: text})
	}
	lastUserIdx := -1
	for i := len(extracted) - 1; i >= 0; i-- {
		if extracted[i].role == "user" {
			lastUserIdx = i
			break
		}
	}
	parts := make([]string, 0, len(extracted))
	for i, t := range extracted {
		if i == lastUserIdx {
			parts = append(parts, t.text)
		} else {
			parts = append(parts, t.role+": "+t.text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// ——— NDJSON event stream ———

// grokEventResponse is one `result.response` object on the NDJSON stream.
type grokEventResponse struct {
	Token         any `json:"token"`
	ModelResponse *struct {
		Message  string `json:"message"`
		Metadata *struct {
			LLMInfo *struct {
				ModelHash string `json:"modelHash"`
			} `json:"llm_info"`
		} `json:"metadata"`
	} `json:"modelResponse"`
	ToolUsageCard   map[string]any `json:"toolUsageCard"`
	ToolUsageCardID string         `json:"toolUsageCardId"`
	LLMInfo         *struct {
		ModelHash string `json:"modelHash"`
	} `json:"llmInfo"`
	ResponseID     string `json:"responseId"`
	CardAttachment *struct {
		JSONData string `json:"jsonData"`
	} `json:"cardAttachment"`
}

// grokChunk is one decoded NDJSON line, pre-classified into the emits the
// bridge above needs.
//
// The thinking channel: upstream's extractor declares a thinkOpened flag for
// the thinking modes but never opens it — every modelResponse.message yields
// as a fullMessage, and the thinking emit is dead code. This port keeps the
// same wire behaviour: modelResponse.message is always the visible message.
type grokChunk struct {
	err        string
	delta      string
	fullMsg    string
	toolCard   map[string]any
	toolCardID string
}

// grokScanNDJSON walks the NDJSON body and hands each decoded chunk to emit.
// Partial or unparsable lines are skipped the way upstream's reader does.
func grokScanNDJSON(body io.Reader, emit func(grokChunk)) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 16<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var event struct {
			Error *struct {
				Message string `json:"message"`
				Code    any    `json:"code"`
			} `json:"error"`
			Result *struct {
				Response *grokEventResponse `json:"response"`
			} `json:"result"`
		}
		if json.Unmarshal([]byte(line), &event) != nil || event.Result == nil || event.Result.Response == nil {
			continue
		}
		resp := event.Result.Response
		if event.Error != nil {
			message := event.Error.Message
			if message == "" {
				message = fmt.Sprintf("Grok error: %v", event.Error.Code)
			}
			emit(grokChunk{err: message})
			return nil
		}
		chunk := grokChunk{}
		if resp.ModelResponse != nil {
			// A modelResponse while the thinking channel is open closes it as
			// thinking and then delivers the visible message — the thinking
			// text arrives as modelResponse before the answer tokens.
			if resp.ModelResponse.Message != "" {
				chunk.fullMsg = resp.ModelResponse.Message
			}
		}
		if token, ok := resp.Token.(string); ok {
			chunk.delta = token
		}
		if resp.ToolUsageCard != nil {
			chunk.toolCard = resp.ToolUsageCard
			chunk.toolCardID = resp.ToolUsageCardID
		}
		emit(chunk)
	}
	return scanner.Err()
}

// ForwardGrokWeb routes a chat completion through grok.com's web client.
func ForwardGrokWeb(w http.ResponseWriter, req *Request) error {
	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	var body map[string]any
	if err := json.Unmarshal(req.Body, &body); err != nil || body == nil {
		return proxy.UpstreamFailure(http.StatusBadRequest, "grok-web: request body is not a JSON object")
	}
	messages, _ := body["messages"].([]any)
	if len(messages) == 0 {
		return proxy.UpstreamFailure(http.StatusBadRequest, "grok-web: missing or empty messages array")
	}

	modelName, _ := body["model"].(string)
	route := grokResolveModel(modelName)

	// Client tools (OpenAI `tools`) → manifest injection + native-card/markup
	// bridge. A turn without tools takes the plain flattening path and none of
	// the bridge state exists.
	registry := grokBuildToolRegistry(body)
	var message string
	if registry.enabled {
		message = grokBuildMessage(messages, registry, body["tool_choice"])
	} else {
		message = grokParseMessages(messages)
	}
	if strings.TrimSpace(message) == "" {
		return proxy.UpstreamFailure(http.StatusBadRequest, "grok-web: empty query after processing")
	}

	cookieHeader := grokBuildCookieHeader(req.APIKey)
	if cookieHeader == "" {
		return proxy.UpstreamFailure(http.StatusBadRequest,
			"grok-web needs the grok.com Cookie header — paste the full Cookie value (sso=...) from a logged-in grok.com browser into the connection")
	}

	doer, err := grokTransportFactory(req.Client, grokWebOrigin)
	if err != nil {
		return proxy.UpstreamFailure(http.StatusServiceUnavailable, err.Error())
	}

	requestID := grokUUID()
	headers := grokChatHeaders(cookieHeader, requestID, requestID)
	headers = append(headers, [2]string{"Cookie", cookieHeader})

	encoded, err := json.Marshal(grokChatPayload(route, message))
	if err != nil {
		return proxy.UpstreamFailure(http.StatusInternalServerError, "grok-web: marshal payload: "+err.Error())
	}

	log.Info("grok-web", "query", "model", modelName, "mode", route.modelMode, "chars", len(message))

	callCtx, cancel := context.WithTimeout(ctx, grokRequestTimeout("chat"))
	defer cancel()
	response, err := doer.do(callCtx, http.MethodPost, grokWebChatURL, headers, encoded)
	if err != nil {
		return proxy.UpstreamFailure(http.StatusBadGateway, "grok-web: connection failed: "+err.Error())
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return grokStatusFailure(response.StatusCode, string(snippet))
	}

	responseID := "chatcmpl-grok-" + grokUUID()[:12]
	if req.IsStream {
		return grokStreamToClient(w, req, response.Body, modelName, route.isThinking, responseID, registry)
	}
	return grokCollectToClient(w, req, response.Body, modelName, route.isThinking, responseID, registry)
}

// grokStatusFailure maps a refused turn onto the failure the router acts on,
// with the message the operator can do something about.
func grokStatusFailure(status int, body string) error {
	switch {
	case grokIsCloudflareChallenge(body):
		return proxy.UpstreamFailure(status,
			"grok-web returned a Cloudflare challenge — the cf_clearance in the pasted Cookie header no longer matches this machine. Re-copy the full Cookie header from a logged-in grok.com browser on this machine.")
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return proxy.UpstreamFailure(status, "grok-web auth failed — the SSO cookie may be expired. Re-paste your sso cookie value from grok.com.")
	case status == http.StatusTooManyRequests:
		return proxy.UpstreamFailure(status, "grok-web rate limited. Wait a moment and retry, or rotate cookies.")
	}
	first := strings.TrimSpace(body)
	if len(first) > 200 {
		first = first[:200]
	}
	return proxy.UpstreamFailure(status, fmt.Sprintf("grok-web returned HTTP %d %s", status, first))
}

// grokStreamToClient relays the NDJSON reply as OpenAI chunks.
//
// With client tools declared, content is held back while a <tool_call> block
// may be opening across chunk boundaries, and the buffered tail is resolved
// into tool_calls at the end — markup first, native cards as the fallback.
func grokStreamToClient(w http.ResponseWriter, req *Request, upstream io.Reader, model string, isThinking bool, responseID string, registry grokToolRegistry) error {
	hw := proxy.NewHeartbeatWriter(req.Ctx, w, 0)
	defer func() { _ = hw.Close() }()
	flusher := proxy.WriteSSEHeaders(hw)

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

	_ = emit(map[string]any{"role": "assistant"}, nil)

	pending := ""
	holding := false
	var nativeCalls []grokOpenAIToolCall

	scanErr := grokScanNDJSON(upstream, func(chunk grokChunk) {
		if emitErr != nil {
			return
		}
		switch {
		case chunk.err != "":
			_ = emit(map[string]any{"content": "[Error: " + chunk.err + "]"}, nil)
		case chunk.toolCard != nil && registry.enabled:
			if call := grokMapNativeToolCard(chunk.toolCard, chunk.toolCardID, registry); call != nil {
				nativeCalls = append(nativeCalls, *call)
			}
		default:
			if chunk.fullMsg != "" && registry.enabled {
				// A snapshot replaces the buffered tail.
				pending = chunk.fullMsg
				return
			}
			if chunk.fullMsg != "" {
				_ = emit(map[string]any{"content": chunk.fullMsg}, nil)
				return
			}
			if chunk.delta == "" {
				return
			}
			if !registry.enabled {
				_ = emit(map[string]any{"content": chunk.delta}, nil)
				return
			}
			pending += chunk.delta
			if grokHasOpenToolCallMarkup(pending) {
				holding = true
				return
			}
			if holding {
				return
			}
			// Flush the safe prefix, keep a small tail in case a <tool_call>
			// opens across chunks.
			keep := min(12, len(pending))
			flush := pending[:len(pending)-keep]
			if flush != "" {
				_ = emit(map[string]any{"content": flush}, nil)
			}
			pending = pending[len(pending)-keep:]
		}
	})
	if scanErr != nil {
		_, _ = hw.Write(proxy.BuildStreamErrorBytes(http.StatusBadGateway, "grok-web: "+scanErr.Error(), proxy.SSEFormatOpenAI))
		_, _ = hw.Write([]byte("data: [DONE]\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
		return scanErr
	}
	if emitErr != nil {
		return emitErr
	}

	calls := grokParseClientToolCallMarkup(pending, registry)
	if calls == nil {
		calls = nativeCalls
	}
	if len(calls) > 0 {
		for i, call := range calls {
			_ = emit(map[string]any{"tool_calls": []map[string]any{{
				"index":    i,
				"id":       call.ID,
				"type":     call.Type,
				"function": map[string]any{"name": call.Function.Name, "arguments": call.Function.Arguments},
			}}}, nil)
		}
		_ = emit(map[string]any{}, "tool_calls")
	} else {
		if pending != "" {
			_ = emit(map[string]any{"content": pending}, nil)
		}
		_ = emit(map[string]any{}, "stop")
	}
	_, err := hw.Write([]byte("data: [DONE]\n\n"))
	if flusher != nil {
		flusher.Flush()
	}
	return err
}

// grokCollectToClient drains the NDJSON reply into one chat.completion.
func grokCollectToClient(w http.ResponseWriter, req *Request, upstream io.Reader, model string, isThinking bool, responseID string, registry grokToolRegistry) error {
	fullContent := ""
	var nativeCalls []grokOpenAIToolCall

	err := grokScanNDJSON(upstream, func(chunk grokChunk) {
		switch {
		case chunk.err != "":
			// surfaced as the in-band error emit on the stream path; the
			// collect path has no chunk-level error handling upstream either
		case chunk.toolCard != nil && registry.enabled:
			if call := grokMapNativeToolCard(chunk.toolCard, chunk.toolCardID, registry); call != nil {
				nativeCalls = append(nativeCalls, *call)
			}
		default:
			if chunk.fullMsg != "" {
				fullContent = chunk.fullMsg
			}
			if chunk.delta != "" {
				fullContent += chunk.delta
			}
		}
	})
	if err != nil {
		return proxy.UpstreamFailure(http.StatusBadGateway, "grok-web: "+err.Error())
	}

	message := map[string]any{"role": "assistant", "content": fullContent}

	calls := grokParseClientToolCallMarkup(fullContent, registry)
	if calls == nil {
		calls = nativeCalls
	}
	finishReason := "stop"
	if len(calls) > 0 {
		message["content"] = grokStripToolCallMarkup(fullContent)
		encodedCalls := make([]map[string]any, 0, len(calls))
		for _, call := range calls {
			encodedCalls = append(encodedCalls, map[string]any{
				"id":       call.ID,
				"type":     call.Type,
				"function": map[string]any{"name": call.Function.Name, "arguments": call.Function.Arguments},
			})
		}
		message["tool_calls"] = encodedCalls
		finishReason = "tool_calls"
	}

	promptTokens := (len(fullContent) + 3) / 4
	body := map[string]any{
		"id":      responseID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{{"index": 0, "message": message, "finish_reason": finishReason}},
		"usage": map[string]any{
			"prompt_tokens":     promptTokens,
			"completion_tokens": promptTokens,
			"total_tokens":      promptTokens * 2,
		},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return proxy.UpstreamFailure(http.StatusInternalServerError, "grok-web: marshal response: "+err.Error())
	}
	return jsonResponse(req.Ctx, w, bytes.NewReader(encoded), req.TranslateResp, req.ResponseBuf)
}
