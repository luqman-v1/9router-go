package executor

import (
	"9router/proxy/internal/log"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	json "encoding/json/v2"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
	"9router/proxy/internal/translator"
)

// jsonErrorBody renders an error payload as JSON.
//
// The message is encoded by json.Marshal, never interpolated into a template.
// An upstream failure routinely carries characters that are illegal raw inside a
// JSON string — a URL that failed to parse embeds itself in quotes, a Windows
// path carries backslashes — and a format string emits them verbatim. The
// result is a body no client can parse: the first quote in the error text
// closes the string early and the following backslash becomes a bare token,
// which a JavaScript client reports as `JSON Parse error: Unrecognized token '\'`.
func jsonErrorBody(message, errType string, code int) []byte {
	body, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    errType,
			"code":    code,
		},
	})
	if err != nil {
		return []byte(`{"error":{"message":"upstream error","type":"server_error","code":500}}`)
	}
	return body
}

// ---- Provider-specific executors ----

// ForwardGrokCLI forwards to grok-cli using Responses API format.
// Transforms Chat Completions body → Responses API body before forwarding.
func ForwardGrokCLI(w http.ResponseWriter, req *Request) error {
	transformedBody, _, err := buildResponsesBody(req.Body)
	if err != nil {
		return fmt.Errorf("transform body: %w", err)
	}
	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	resp, err := proxy.ForwardGrokCLI(ctx, req.Client, req.Config, req.APIKey, transformedBody, req.IsStream)
	if err != nil {
		return fmt.Errorf("ForwardGrokCLI: %w", err)
	}
	defer resp.Body.Close()
	return handleCodexStream(w, req, resp.Body)
}

// ForwardCodex forwards to codex using Responses API format.
// Transforms Chat Completions body → Responses API body before forwarding.
func ForwardCodex(w http.ResponseWriter, req *Request) error {
	transformedBody, _, err := buildResponsesBody(req.Body)
	if err != nil {
		return fmt.Errorf("transform body: %w", err)
	}

	// Resolve conversation-stable session_id for Codex prompt caching
	sessionID := req.SessionID
	if sessionID == "" {
		if req.ConnectionID != "" {
			sessionID = req.ConnectionID
		} else if req.ConnData != nil {
			if cid, ok := req.ConnData["connectionId"].(string); ok && cid != "" {
				sessionID = cid
			}
		}
	}
	if sessionID == "" {
		sessionID = "default"
	}

	// Inject prompt_cache_key for stable Codex prompt caching if not already set
	transformedBody = injectCodexPromptCacheKey(transformedBody, sessionID)

	responsesLite := codexResponsesLiteBody(transformedBody)

	// The ChatGPT Codex backend rejects unsupported parameters and requires the allowlist
	transformedBody = stripCodexUnsupportedTokenParams(transformedBody, responsesLite)
	// Only now that the "(level)" suffix is gone can the catalog id be mapped
	// to its wire id, so this has to follow the transform rather than precede it.
	transformedBody = rewriteCodexUpstreamModel(transformedBody)
	cfg, transformedBody := applyCodexCompact(req.Config, transformedBody)
	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	resp, err := proxy.ForwardCodex(ctx, req.Client, cfg, req.APIKey, transformedBody, req.IsStream, req.ConnData, responsesLite, sessionID)
	if err != nil {
		return fmt.Errorf("ForwardCodex: %w", err)
	}
	defer resp.Body.Close()
	return handleCodexStream(w, req, resp.Body)
}

func injectCodexPromptCacheKey(body []byte, sessionID string) []byte {
	if sessionID == "" || sessionID == "default" {
		return body
	}
	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		return body
	}
	if pck, ok := envelope["prompt_cache_key"].(string); ok && pck != "" {
		return body
	}
	envelope["prompt_cache_key"] = sessionID
	out, err := marshalStable(envelope)
	if err != nil {
		return body
	}
	return out
}

// applyCodexCompact routes a compaction request to the codex /compact endpoint
// and strips the marker from the body, porting open-sse/executors/codex.js
// transformRequest + buildUrl. Without it the flag would ride upstream as an
// unknown field and the request would take the normal /responses path, so the
// client would get an answer where it asked for a compacted transcript.
func applyCodexCompact(cfg *providers.ProviderConfig, body []byte) (*providers.ProviderConfig, []byte) {
	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		return cfg, body
	}
	compact, ok := envelope["_compact"].(bool)
	if !ok || !compact {
		return cfg, body
	}
	delete(envelope, "_compact")
	out, err := marshalStable(envelope)
	if err != nil {
		return cfg, body
	}
	cloned := *cfg
	cloned.BaseURL = strings.TrimRight(cloned.BaseURL, "/") + "/compact"
	return &cloned, out
}

// ForwardIflow forwards to iflow with HMAC-SHA256 signature.
// Injects stream_options and generates HMAC headers before forwarding.
func ForwardIflow(w http.ResponseWriter, req *Request) error {
	var reqMap map[string]any
	if err := json.Unmarshal(req.Body, &reqMap); err != nil {
		return fmt.Errorf("parse body: %w", err)
	}
	if req.IsStream {
		reqMap["stream"] = true
		if _, ok := reqMap["stream_options"]; !ok {
			reqMap["stream_options"] = map[string]any{"include_usage": true}
		}
	}
	reqBody, err := marshalStable(reqMap)
	if err != nil {
		return fmt.Errorf("marshal iflow body: %w", err)
	}

	// HMAC-SHA256 signature
	sessionID := "session-" + uuid.New().String()
	timestamp := time.Now().UnixMilli()
	userAgent := "iFlow-Cli"
	payload := userAgent + ":" + sessionID + ":" + strconv.FormatInt(timestamp, 10)

	mac := hmac.New(sha256.New, []byte(req.APIKey))
	mac.Write([]byte(payload))
	signature := hex.EncodeToString(mac.Sum(nil))

	extraHeaders := map[string]string{
		"User-Agent":        userAgent,
		"session-id":        sessionID,
		"x-iflow-timestamp": strconv.FormatInt(timestamp, 10),
		"x-iflow-signature": signature,
	}
	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	resp, err := proxy.ForwardIflow(ctx, req.Client, req.Config, req.APIKey, reqBody, req.IsStream, extraHeaders)
	if err != nil {
		return fmt.Errorf("ForwardIflow upstream: %w", err)
	}
	defer resp.Body.Close()

	if req.IsStream {
		return execSSEStream(w, resp.Body, req)
	}
	return jsonResponse(req.Ctx, w, resp.Body, req.TranslateResp, req.ResponseBuf)
}

// ForwardKimchi forwards to kimchi with Anthropic field stripping.
// Cleans Anthropic-specific fields from body before forwarding.
func ForwardKimchi(w http.ResponseWriter, req *Request) error {
	var reqBody map[string]any
	if err := json.Unmarshal(req.Body, &reqBody); err != nil {
		return fmt.Errorf("parse request body: %w", err)
	}

	CleanKimchiBody(reqBody)

	cleanedBody, err := marshalStable(reqBody)
	if err != nil {
		return fmt.Errorf("marshal cleaned body: %w", err)
	}

	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	resp, err := proxy.ForwardKimchi(ctx, req.Client, req.Config, req.APIKey, cleanedBody, req.IsStream)
	if err != nil {
		return fmt.Errorf("ForwardKimchi: %w", err)
	}
	defer resp.Body.Close()

	if req.IsStream {
		return execSSEStream(w, resp.Body, req)
	}
	return jsonResponse(req.Ctx, w, resp.Body, req.TranslateResp, req.ResponseBuf)
}

// ForwardKiro forwards to kiro with AWS EventStream response handling.
// Uses EventStream binary parsing instead of standard SSE.
//
// The kiro.dev gateway only accepts a conversationState envelope and answers
// 400 "Improperly formed request." for anything else, so the OpenAI body is
// translated first (internal/translator.OpenAIToKiro). Bodies that already
// carry a conversationState (MITM passthrough) are forwarded untouched.
func ForwardKiro(w http.ResponseWriter, req *Request) error {
	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	body, err := kiroUpstreamBody(req)
	if err != nil {
		return err
	}

	resp, err := proxy.ForwardKiro(ctx, req.Client, req.Config, req.APIKey, body, req.IsStream, req.ConnData)
	if err != nil {
		return fmt.Errorf("ForwardKiro: %w", err)
	}
	defer resp.Body.Close()
	if req.IsStream {
		return handleKiroStream(w, req, resp.Body)
	}
	return handleKiroNonStream(w, req, resp.Body)
}

// kiroUpstreamBody translates an OpenAI chat body into the Kiro envelope,
// passing through bodies that already use conversationState.
func kiroUpstreamBody(req *Request) ([]byte, error) {
	var probe map[string]any
	if err := json.Unmarshal(req.Body, &probe); err != nil {
		return nil, fmt.Errorf("ForwardKiro: parse request body: %w", err)
	}
	if _, ok := probe["conversationState"]; ok {
		// Already a Kiro envelope (MITM passthrough) — only clean it.
		return req.Body, nil
	}

	model := req.ModelName
	if model == "" {
		var oreq struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(req.Body, &oreq); err == nil {
			model = oreq.Model
		}
	}
	// Drop the provider prefix the client sent (kr/claude-sonnet-4.5).
	if _, after, ok := strings.Cut(model, "/"); ok {
		model = after
	}
	// -thinking / -agentic are 9router fictions, stripped before upstream.
	model = strings.TrimSuffix(model, "-thinking")
	model = strings.TrimSuffix(model, "-agentic")
	if idx := strings.IndexByte(model, '('); idx > 0 {
		model = model[:idx]
	}
	// Anthropic spellings (claude-sonnet-4-5-20250929) 400 on Kiro's data
	// plane; fold them onto the dotted upstream alias.
	model = normalizeKiroUpstreamModelId(model)

	profileArn := ""
	if req.ConnData != nil {
		if s, ok := req.ConnData["profileArn"].(string); ok {
			profileArn = strings.TrimSpace(s)
		}
	}

	out, err := translator.OpenAIToKiro(req.Body, translator.KiroTranslateOptions{
		Model:      model,
		ProfileArn: profileArn,
	})
	if err != nil {
		log.Error("executor", "kiro translation failed", "model", model, "error", err)
		return nil, &proxy.UpstreamError{
			StatusCode: http.StatusBadRequest,
			Body:       jsonErrorBody(fmt.Sprintf("kiro request could not be built: %v", err), "invalid_request_error", http.StatusBadRequest),
		}
	}
	return out, nil
}

// ForwardAzure forwards to Azure OpenAI with dynamic URL from env vars.
func ForwardAzure(w http.ResponseWriter, req *Request) error {
	var oreq struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(req.Body, &oreq); err != nil {
		log.Warn("executor", "azure unmarshal body", "error", err)
	}
	modelName := oreq.Model
	if modelName == "" {
		modelName = "gpt-4"
	}

	endpoint := os.Getenv("AZURE_ENDPOINT")
	apiVersion := os.Getenv("AZURE_API_VERSION")
	if apiVersion == "" {
		apiVersion = "2024-10-01-preview"
	}
	deployment := os.Getenv("AZURE_DEPLOYMENT")
	if deployment == "" {
		deployment = modelName
	}
	if endpoint == "" {
		endpoint = "https://api.openai.com"
	}

	baseURL := strings.TrimRight(endpoint, "/")
	url := fmt.Sprintf("%s/openai/deployments/%s/chat/completions?api-version=%s",
		baseURL, deployment, apiVersion)

	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	r, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(req.Body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("api-key", req.APIKey)
	if req.IsStream {
		r.Header.Set("Accept", "text/event-stream")
	}

	resp, err := req.Client.Do(r)
	if err != nil {
		return fmt.Errorf("upstream request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
		if readErr != nil {
			return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: []byte("failed to read error body")}
		}
		return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: errBody}
	}

	if req.IsStream {
		return execSSEStream(w, resp.Body, req)
	}
	return jsonResponse(req.Ctx, w, resp.Body, req.TranslateResp, req.ResponseBuf)
}
func parseDataURIMime(uri string) string {
	if strings.HasPrefix(uri, "data:") {
		if idx := strings.Index(uri, ";"); idx > 5 {
			return uri[5:idx]
		}
	}
	return "image/png"
}

func toCommandcodeImageBlock(part map[string]any) map[string]any {
	pType, _ := part["type"].(string)
	if pType == "image_url" {
		var urlStr string
		switch u := part["image_url"].(type) {
		case string:
			urlStr = u
		case map[string]any:
			urlStr, _ = u["url"].(string)
		}
		if urlStr != "" && strings.HasPrefix(urlStr, "data:") {
			mime := parseDataURIMime(urlStr)
			return map[string]any{
				"type":      "image",
				"image":     urlStr,
				"mimeType":  mime,
				"mediaType": mime,
			}
		}
	}
	if pType == "image" {
		if imgStr, ok := part["image"].(string); ok && strings.HasPrefix(imgStr, "data:") {
			mime, _ := part["mimeType"].(string)
			if mime == "" {
				mime = parseDataURIMime(imgStr)
			}
			return map[string]any{
				"type":      "image",
				"image":     imgStr,
				"mimeType":  mime,
				"mediaType": mime,
			}
		}
		if src, ok := part["source"].(map[string]any); ok {
			mediaType, _ := src["media_type"].(string)
			if mediaType == "" {
				mediaType = "image/png"
			}
			data, _ := src["data"].(string)
			if data != "" {
				dataURI := fmt.Sprintf("data:%s;base64,%s", mediaType, data)
				return map[string]any{
					"type":      "image",
					"image":     dataURI,
					"mimeType":  mediaType,
					"mediaType": mediaType,
				}
			}
		}
	}
	return nil
}

// buildCommandcodeBody transforms OpenAI request payload into CommandCode schema
// {threadId, memory, config, params} matching upstream openaiToCommandCodeRequest.
func buildCommandcodeBody(body []byte, model string) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return body, err
	}

	// If already wrapped in params, keep the native envelope but re-derive
	// the fields the gateway owns. A client-supplied params object is a
	// request to the provider, so it goes through the same whitelist the
	// flat path builds: without it a client could set the model or any
	// parameter the provider accepts.
	if rawParams, hasParams := m["params"]; hasParams {
		p, ok := rawParams.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("buildCommandcodeBody: params must be an object")
		}
		params := map[string]any{}
		for _, k := range []string{"messages", "stream", "temperature", "top_p", "tools", "reasoning_effort", "system"} {
			if v, ok := p[k]; ok && v != nil {
				params[k] = v
			}
		}
		if _, ok := params["stream"]; !ok {
			params["stream"] = true
		}
		if _, ok := params["messages"]; !ok {
			params["messages"] = []any{}
		}
		// The model is the gateway's to decide: the client asked for a
		// model, the gateway resolved one, and the provider is called with
		// the resolved id. The budget follows the flat path: the client's
		// own value when it set one, the 1024 default otherwise.
		params["model"] = model
		params["max_tokens"] = commandcodeMaxTokens(p)
		m["params"] = params
		if _, hasThread := m["threadId"]; !hasThread {
			m["threadId"] = uuid.New().String()
		}
		if _, hasMem := m["memory"]; !hasMem {
			m["memory"] = ""
		}
		if _, hasCfg := m["config"]; !hasCfg {
			m["config"] = map[string]any{
				"workingDir":    "/",
				"date":          time.Now().UTC().Format("2006-01-02"),
				"environment":   providers.NodePlatform(),
				"structure":     []any{},
				"isGitRepo":     false,
				"currentBranch": "",
				"mainBranch":    "",
				"gitStatus":     "",
				"recentCommits": []any{},
			}
		}
		// marshalStable, not json.Marshal: CommandCode is a prefix-caching
		// upstream, so the same logical request has to serialise identically
		// or every call after the first misses the prompt cache.
		return marshalStable(m)
	}

	// Build params from an explicit whitelist (upstream openai-to-commandcode.js:187-199).
	// Copying every client key leaks OpenAI-only fields (stream_options, store,
	// metadata, tool_choice, …) into the provider payload.
	params := map[string]any{
		"model":      model,
		"messages":   []any{},
		"stream":     true,
		"max_tokens": commandcodeMaxTokens(m),
	}
	if temp, ok := m["temperature"]; ok && temp != nil {
		params["temperature"] = temp
	} else {
		params["temperature"] = 0.3
	}
	if top, ok := m["top_p"]; ok && top != nil {
		params["top_p"] = top
	}
	if tools, ok := m["tools"].([]any); ok && len(tools) > 0 {
		params["tools"] = commandcodeTools(convertOpenAIToolsToClaude(tools))
	}
	if effort, ok := m["reasoning_effort"]; ok && effort != nil {
		params["reasoning_effort"] = effort
	}

	// CommandCode messages require content as array of blocks (never raw string)
	if rawMsgs, ok := m["messages"].([]any); ok {
		messages, system := commandcodeConvertMessages(rawMsgs)
		params["messages"] = messages
		if system != "" {
			params["system"] = system
		}
	}

	payload := map[string]any{
		"threadId": uuid.New().String(),
		"memory":   "",
		"config": map[string]any{
			"workingDir":    "/",
			"date":          time.Now().UTC().Format("2006-01-02"),
			"environment":   providers.NodePlatform(),
			"structure":     []any{},
			"isGitRepo":     false,
			"currentBranch": "",
			"mainBranch":    "",
			"gitStatus":     "",
			"recentCommits": []any{},
		},
		"params": params,
	}

	// json.Marshal is deliberate here: this path mints a fresh threadId on
	// every request, so the body differs call to call whatever the member
	// order is. The caller-shaped `params` path above keeps a supplied
	// threadId and is the one that must stay byte-stable.
	return json.Marshal(payload)
}

// ForwardCommandcode forwards to CommandCode with NDJSON→SSE translation.
func ForwardCommandcode(w http.ResponseWriter, req *Request) error {
	var oreq struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(req.Body, &oreq); err != nil {
		log.Warn("executor", "commandcode unmarshal body", "error", err)
	}

	reqBody, err := buildCommandcodeBody(req.Body, oreq.Model)
	if err != nil {
		return fmt.Errorf("marshal commandcode body: %w", err)
	}
	// Build request with custom headers (not using proxy.ForwardCommandcode)
	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	r, err := http.NewRequestWithContext(ctx, "POST", req.Config.BaseURL, bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+req.APIKey)
	r.Header.Set("x-session-id", uuid.New().String())
	r.Header.Set("x-command-code-version", "0.25.7")
	r.Header.Set("x-cli-environment", "cli")
	r.Header.Set("User-Agent", "commandcode/0.25.7 (cli)")
	r.Header.Set("Accept", "text/event-stream")
	if req.Config != nil && req.Config.StaticHeaders != nil {
		for k, v := range req.Config.StaticHeaders {
			r.Header.Set(k, v)
		}
	}
	resp, err := req.Client.Do(r)
	if err != nil {
		return fmt.Errorf("upstream request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
		if readErr != nil {
			return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: []byte("failed to read error body")}
		}
		return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: errBody}
	}

	return handleCommandcodeStream(w, req, resp.Body, oreq.Model)
}

// ForwardOpencode handles requests for opencode (free tier).
func ForwardOpencode(w http.ResponseWriter, req *Request) error {
	apiKey := req.APIKey
	if apiKey == "" {
		apiKey = "public"
	}

	var reqObj struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(req.Body, &reqObj)
	cleanModel := strings.TrimPrefix(reqObj.Model, "oc/")
	cleanModel = strings.TrimPrefix(cleanModel, "opencode/")
	if parenIdx := strings.IndexByte(cleanModel, '('); parenIdx != -1 {
		cleanModel = cleanModel[:parenIdx]
	}

	if isOpencodeResponsesModel(cleanModel) {
		// Route through Responses API format: https://opencode.ai/zen/v1/responses
		transformedBody, _, err := buildResponsesBody(req.Body)
		if err != nil {
			return fmt.Errorf("transform body for muse-spark: %w", err)
		}

		// reqObj.Model, not cleanModel: the "(level)" override and the
		// reasoning effort it encodes live in the untouched id (see
		// normalizeMuseSparkResponsesBody).
		transformedBody, err = normalizeMuseSparkResponsesBody(transformedBody, reqObj.Model)
		if err != nil {
			return fmt.Errorf("normalize muse-spark body: %w", err)
		}

		// The free-tier gate fingerprints the lowercase tool quartet; capitalised
		// variants from Claude Code CLI are renamed here and restored on the
		// response (translator.ConcealFingerprintTools).
		transformedBody, toolNameMap := translator.ConcealOpenCodeV2FingerprintTools(transformedBody, true, isOpenCodeAutoToolChoiceModel(cleanModel))
		transformedBody = translator.ApplyFingerprintSystemPrompt(transformedBody)
		req.Ctx = translator.WithToolNameMap(req.Ctx, toolNameMap)
		w = NewToolNameRestoringWriter(w, toolNameMap)

		// The lane URL is what the relay forwards to, so it is built the same way
		// for a relayed connection as for a direct one.
		cfg := *req.Config
		baseURL := strings.TrimRight(cfg.BaseURL, "/")
		baseURL = strings.TrimSuffix(baseURL, "/chat/completions")
		cfg.BaseURL = opencodeInferenceBase(baseURL) + "/responses"
		baseHeaders := cfg.StaticHeaders
		hint := opencodeSessionHint(req, true)
		cfg.StaticHeaders = proxy.BuildOpenCodeHeaders(cfg.StaticHeaders, hint, req.IsStream)
		ctx := req.Ctx
		if ctx == nil {
			ctx = context.Background()
		}
		resp, err := forwardOpenCodeRotating(ctx, func() (*http.Response, error) {
			return forwardOpencodeRequest(ctx, req.Client, &cfg, apiKey, transformedBody, req.IsStream)
		}, func() (*http.Response, error) {
			return forwardOpencodeRequestDirect(ctx, &cfg, apiKey, transformedBody, req.IsStream)
		}, rotateOpencodeStableSession(opencodeStableSessionKey(req, true), hint, func(session string) {
			cfg.StaticHeaders = proxy.BuildOpenCodeHeaders(baseHeaders, session, req.IsStream)
		}))
		if err != nil {
			return fmt.Errorf("ForwardOpencode (muse-spark responses): %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
			return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: errBody}
		}

		if req.IsStream {
			stallReader := proxy.NewStallReaderWithContext(ctx, resp.Body, 0, "opencode-responses")
			defer stallReader.Close()
			return handleCodexStream(w, req, stallReader)
		}
		return handleCodexStream(w, req, resp.Body)
	}

	body := InjectReasoningContent(req.Body, "opencode")
	// The free lane's MiMo route streams thinking as reasoning +
	// reasoning_details[] instead of reasoning_content; the passthrough lifts
	// the alias so OpenAI-shaped clients see the thinking. Every other model
	// on the lane already emits reasoning_content, so the lift is a no-op
	// there (upstream quirks.reasoningAliasFields).
	req.LiftReasoningAliases = true
	// opencode Chat Completions path: same conceal + restore of tool names.
	body, toolNameMap := translator.ConcealOpenCodeV2FingerprintTools(body, false, isOpenCodeAutoToolChoiceModel(cleanModel))
	body = translator.ApplyFingerprintSystemPrompt(body)
	req.Ctx = translator.WithToolNameMap(req.Ctx, toolNameMap)
	w = NewToolNameRestoringWriter(w, toolNameMap)
	// Strict models wrap caller tool calls as execute/CALL_TOOL; hand the
	// client back its own spelling. The bridge buffers whole SSE lines, so it
	// may only wrap a streaming response — the non-streaming lane rewrites
	// the re-aggregated JSON instead (bridgeOpenCodeExecuteToolCallsJSON).
	if req.IsStream {
		w = NewOpenCodeExecuteBridgeWriter(w)
	}

	// Upstream OpenCode free tier strictly requires stream=true.
	// Non-streaming calls are blocked with 403 FreeTierError.
	var reqMap map[string]any
	if err := json.Unmarshal(body, &reqMap); err == nil {
		reqMap["stream"] = true
		if cleanModel != "" {
			reqMap["model"] = cleanModel
		}
		// The free backend validates reasoning_effort against the plain
		// OpenAI enum and answers 400 "Invalid option" for "auto", which
		// adaptive thinking (Claude Code's thinking:{type:"adaptive"})
		// arrives as. Omitting it lets the upstream pick its own default,
		// which is what adaptive means.
		if v, ok := reqMap["reasoning_effort"].(string); ok && v == "auto" {
			delete(reqMap, "reasoning_effort")
		}
		// Deterministic marshaling keeps the serialized request prefix
		// stable across turns, which DeepSeek's prefix prompt cache
		// requires; json/v2 randomizes map member order by default.
		if b, err := marshalStable(reqMap); err == nil {
			body = b
		}
	}

	// One lane URL for both egress paths: a relay forwards to whatever this names.
	cfg := *req.Config
	cfg.BaseURL = opencodeChatURL(cfg.BaseURL)
	baseHeaders := cfg.StaticHeaders
	hint := opencodeSessionHint(req, true)
	cfg.StaticHeaders = proxy.BuildOpenCodeHeaders(cfg.StaticHeaders, hint, true)

	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	resp, err := forwardOpenCodeRotating(ctx, func() (*http.Response, error) {
		return forwardOpencodeRequest(ctx, req.Client, &cfg, apiKey, body, true)
	}, func() (*http.Response, error) {
		return forwardOpencodeRequestDirect(ctx, &cfg, apiKey, body, true)
	}, rotateOpencodeStableSession(opencodeStableSessionKey(req, true), hint, func(session string) {
		cfg.StaticHeaders = proxy.BuildOpenCodeHeaders(baseHeaders, session, true)
	}))
	if err != nil {
		return fmt.Errorf("ForwardOpencode: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
		return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: errBody}
	}

	if req.IsStream {
		return execSSEStream(w, resp.Body, req)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return fmt.Errorf("read opencode response: %w", err)
	}
	if converted, ok := sseToOpenAIJSON(data); ok {
		data = converted
	}
	data = bridgeOpenCodeExecuteToolCallsJSON(data)
	return jsonResponse(req.Ctx, w, bytes.NewReader(data), req.TranslateResp, req.ResponseBuf)
}

// opencodeInferenceBase rewrites a zen-layout base URL to the v2 inference
// gate's /inference/openai/v1 prefix. Any host mirroring the zen layout follows the
// same migration, so the rewrite keys off the prefix rather than the host. A
// base with no zen prefix — a custom endpoint, or the registry default that
// already points at /inference/openai/v1 — is returned untouched.
func opencodeInferenceBase(base string) string {
	base = strings.TrimRight(base, "/")
	base = strings.TrimSuffix(base, "/chat/completions")
	base = strings.TrimSuffix(base, "/messages")
	base = strings.TrimSuffix(base, "/responses")
	switch {
	case strings.HasSuffix(base, "/zen/v1"):
		return strings.TrimSuffix(base, "/zen/v1") + "/inference/openai/v1"
	case strings.HasSuffix(base, "/inference/openai/v1"):
		return base
	case strings.HasSuffix(base, "/inference/v1"):
		return strings.TrimSuffix(base, "/inference/v1") + "/inference/openai/v1"
	default:
		return base
	}
}

// opencodeMessagesInferenceBase keeps a zen-layout base URL on the legacy
// /inference/v1 prefix: the v2 gate's /inference/openai/v1 surface has no
// /messages route (404), while the legacy prefix still serves Claude
// Messages. Mirrors opencodeInferenceBase, keyed off the prefix rather than
// the host.
func opencodeMessagesInferenceBase(base string) string {
	base = strings.TrimRight(base, "/")
	base = strings.TrimSuffix(base, "/chat/completions")
	base = strings.TrimSuffix(base, "/messages")
	base = strings.TrimSuffix(base, "/responses")
	switch {
	case strings.HasSuffix(base, "/zen/v1"):
		return strings.TrimSuffix(base, "/zen/v1") + "/inference/v1"
	case strings.HasSuffix(base, "/inference/openai/v1"):
		return strings.TrimSuffix(base, "/inference/openai/v1") + "/inference/v1"
	case strings.HasSuffix(base, "/inference/v1"):
		return base
	default:
		return base
	}
}

func opencodeChatURL(base string) string {
	base = strings.TrimRight(base, "/")
	if !strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	trimmed := strings.TrimSuffix(base, "/chat/completions")
	if strings.HasSuffix(trimmed, "/zen/v1") {
		return strings.TrimSuffix(trimmed, "/zen/v1") + "/inference/openai/v1/chat/completions"
	}
	if strings.HasSuffix(trimmed, "/inference/v1") {
		return strings.TrimSuffix(trimmed, "/inference/v1") + "/inference/openai/v1/chat/completions"
	}
	return base
}

// isOpenCodeAutoToolChoiceModel ports the upstream forceAutoToolChoiceModels
// quirk (open-sse/executors/opencode.js): only models confirmed auto-only get
// their explicit tool_choice rewritten, so the allowlist stays closed until a
// model is probed.
func isOpenCodeAutoToolChoiceModel(cleanModel string) bool {
	return cleanModel == "muse-spark-1.3-contributor-free"
}

// isOpencodeFreeModel reports whether a bare model id belongs to the OpenCode
// free tier. Keyed zen requests for these ids answer 403 ("free tier can only
// be used from within OpenCode"), so the zen lane forwards them to the
// keyless /inference/v1 gate with public credentials. The source of truth is
// the curated registry and -free suffix matching (providers.IsOpencodeFreeTierModel).
func isOpencodeFreeModel(cleanModel string) bool {
	base := cleanModel
	if parenIdx := strings.IndexByte(base, '('); parenIdx != -1 {
		base = base[:parenIdx]
	}
	// IsOpencodeFreeTierModel handles catalog membership and -free suffix rules,
	// keeping resolution consistent across all three lanes (free, zen, go).
	for _, provider := range []string{"opencode", "opencode-zen", "opencode-go"} {
		if providers.IsOpencodeFreeTierModel(provider, base) {
			return true
		}
	}
	return false
}

var opencodeGoMessagesModels = map[string]bool{
	"claude-haiku-5-5": true,
	"minimax-m3":       true,
	"minimax-m2.7":     true,
	"minimax-m2.5":     true,
	"qwen3.8-max":      true,
	"qwen3.8-flash":    true,
	"qwen3.7-max":      true,
	"qwen3.7-plus":     true,
	"qwen3.6-plus":     true,
}

// EnsureClaudeMessages exposes the OpenAI→Claude Messages request conversion
// (see ensureMessagesMaxTokens) for the fallback path, which forwards raw
// OpenAI-format bodies to Anthropic-native upstreams.
func EnsureClaudeMessages(body []byte, model string) []byte {
	return ensureMessagesMaxTokens(body, model)
}

// EnsureClaudeMessagesFor is EnsureClaudeMessages for a Claude-shaped lane
// that is not Anthropic itself (minimax-code, opencode-zen, ...): provider
// selects the capability row that decides the thinking format.
func EnsureClaudeMessagesFor(body []byte, provider, model string) []byte {
	return ensureMessagesFor(body, provider, model)
}

// ensureMessagesMaxTokens converts an incoming request (OpenAI or Claude) into a spec-compliant
// Claude Messages API request payload:
// - Guarantees positive integer max_tokens (fallback from max_completion_tokens or default 4096)
// - Converts OpenAI tools [{type: "function", function: {name, description, parameters}}] to Claude [{name, description, input_schema}]
// - Converts OpenAI tool_choice to Claude format
// - Extracts role: "system" from messages into top-level system prompt
// - Converts assistant tool_calls to tool_use blocks and role: "tool" to user tool_result blocks
// - Merges consecutive same-role messages to uphold Claude alternating role invariant
// - Strips OpenAI-only fields like stream_options, store, max_completion_tokens, reasoning_effort
func ensureMessagesMaxTokens(body []byte, model string) []byte {
	return ensureMessagesFor(body, "claude", model)
}

func ensureMessagesFor(body []byte, provider, model string) []byte {
	var reqMap map[string]any
	if err := json.Unmarshal(body, &reqMap); err != nil {
		return body
	}
	if model != "" {
		reqMap["model"] = model
	}

	// 1. max_tokens
	maxTokensVal := 0
	if mt, ok := reqMap["max_tokens"]; ok && mt != nil {
		switch v := mt.(type) {
		case float64:
			maxTokensVal = int(v)
		case int:
			maxTokensVal = v
		case int64:
			maxTokensVal = int(v)
		}
	}
	if maxTokensVal <= 0 {
		if mct, ok := reqMap["max_completion_tokens"]; ok && mct != nil {
			switch v := mct.(type) {
			case float64:
				maxTokensVal = int(v)
			case int:
				maxTokensVal = v
			case int64:
				maxTokensVal = int(v)
			}
		}
	}
	if maxTokensVal <= 0 {
		maxTokensVal = 4096
	}
	reqMap["max_tokens"] = maxTokensVal
	delete(reqMap, "max_completion_tokens")

	// 2. tools: convert OpenAI tools to Claude {name, description, input_schema}
	if tools, ok := reqMap["tools"].([]any); ok && len(tools) > 0 {
		reqMap["tools"] = convertOpenAIToolsToClaude(tools)
	}

	// 3. tool_choice: convert OpenAI tool_choice to Claude format
	if tc, ok := reqMap["tool_choice"]; ok && tc != nil {
		if convertedTC := convertToolChoiceToClaude(tc); convertedTC != nil {
			reqMap["tool_choice"] = convertedTC
		} else {
			delete(reqMap, "tool_choice")
		}
	}

	// 3b. parallel_tool_calls is OpenAI's single-tool-call policy; Claude
	// spells it as a flag on tool_choice. Without tools there is nothing to
	// restrict, so the flag would only add noise (#4581). Either way the
	// OpenAI-only key is stripped, as step 5 does for other foreign fields.
	if ptc, hasPTC := reqMap["parallel_tool_calls"]; hasPTC {
		if ptc == false && hasClaudeTools(reqMap) {
			reqMap["tool_choice"] = withDisabledParallelToolUse(reqMap["tool_choice"])
		}
		delete(reqMap, "parallel_tool_calls")
	}

	// 4. messages & system
	if msgs, ok := reqMap["messages"].([]any); ok && len(msgs) > 0 {
		extractedSys, claudeMsgs := convertOpenAIMessagesToClaude(msgs)
		reqMap["messages"] = cleanupConvertedClaudeMessages(body, claudeMsgs)
		if extractedSys != "" {
			if existingSys, ok := reqMap["system"].(string); ok && existingSys != "" {
				reqMap["system"] = existingSys + "\n\n" + extractedSys
			} else if reqMap["system"] == nil {
				reqMap["system"] = extractedSys
			}
		}
	}

	// 5. Clean OpenAI-specific fields that strict Claude API rejects.
	// Claude returns thinking text only with thinking.display "summarized", a
	// field OpenAI has no equivalent for, so the client's ask cannot survive
	// translation on its own — capture it into the Claude field first.
	// (upstream thinkingUnified captureThinking)
	applyClaudeThinkingDisplay(reqMap)
	applyClaudeThinkingType(reqMap, provider, model)
	delete(reqMap, "stream_options")
	delete(reqMap, "store")
	delete(reqMap, "reasoning_effort")

	updated, err := marshalStable(reqMap)
	if err != nil {
		return body
	}
	return updated
}

func convertOpenAIToolsToClaude(tools []any) []any {
	if len(tools) == 0 {
		return tools
	}
	out := make([]any, 0, len(tools))
	for _, t := range tools {
		m, ok := t.(map[string]any)
		if !ok {
			out = append(out, t)
			continue
		}
		if _, hasName := m["name"]; hasName {
			if _, hasSchema := m["input_schema"]; hasSchema {
				out = append(out, t)
				continue
			}
		}
		if fn, ok := m["function"].(map[string]any); ok {
			cTool := make(map[string]any)
			if name, ok := fn["name"].(string); ok {
				cTool["name"] = name
			}
			if desc, ok := fn["description"].(string); ok && desc != "" {
				cTool["description"] = desc
			}
			if params, ok := fn["parameters"]; ok && params != nil {
				cTool["input_schema"] = params
			} else {
				cTool["input_schema"] = map[string]any{
					"type":       "object",
					"properties": map[string]any{},
				}
			}
			if cc, ok := m["cache_control"]; ok {
				cTool["cache_control"] = cc
			}
			if strict, ok := toolStrict(m, fn); ok {
				cTool["strict"] = strict
			}
			out = append(out, cTool)
			continue
		}
		if params, ok := m["parameters"]; ok {
			cTool := make(map[string]any, len(m))
			for k, v := range m {
				if k != "type" && k != "parameters" {
					cTool[k] = v
				}
			}
			cTool["input_schema"] = params
			out = append(out, cTool)
			continue
		}
		out = append(out, t)
	}
	return out
}

// toolStrict resolves an explicit tool strict mode, preferring the flat
// Responses/Codex shape and falling back to the Chat-Completions `function`
// shape (parity with decolua/9router#4543/#4573). An absent or non-boolean
// strict stays absent — the flag must never be invented.
func toolStrict(tool, fn map[string]any) (bool, bool) {
	if strict, ok := tool["strict"].(bool); ok {
		return strict, true
	}
	if fn == nil {
		return false, false
	}
	strict, ok := fn["strict"].(bool)
	return strict, ok
}

func convertToolChoiceToClaude(tc any) any {
	if tc == nil {
		return nil
	}
	switch v := tc.(type) {
	case string:
		switch v {
		case "auto":
			return map[string]any{"type": "auto"}
		case "required":
			return map[string]any{"type": "any"}
		case "none":
			// Claude has a native "none"; dropping the key instead would hand
			// the model the tool permission the client withheld (#4577).
			return map[string]any{"type": "none"}
		default:
			return map[string]any{"type": "auto"}
		}
	case map[string]any:
		if tType, _ := v["type"].(string); tType == "function" {
			if fn, ok := v["function"].(map[string]any); ok {
				if name, ok := fn["name"].(string); ok {
					return map[string]any{"type": "tool", "name": name}
				}
			}
		}
		return v
	default:
		return tc
	}
}

// hasClaudeTools reports whether the body still declares at least one tool.
// A policy about calling tools is meaningless without any.
func hasClaudeTools(reqMap map[string]any) bool {
	tools, ok := reqMap["tools"].([]any)
	return ok && len(tools) > 0
}

// withDisabledParallelToolUse returns the Claude tool_choice carrying
// disable_parallel_tool_use. An explicit choice keeps its type and name — the
// restriction is added, never substituted — and only a missing choice is
// filled in with Claude's own default, "auto" (parity #4581).
func withDisabledParallelToolUse(tc any) map[string]any {
	choice, ok := tc.(map[string]any)
	if !ok {
		choice = map[string]any{"type": "auto"}
	}
	choice["disable_parallel_tool_use"] = true
	return choice
}

// sanitizeToolUseID returns a tool id valid for the Anthropic Messages API
// (must match ^[a-zA-Z0-9_-]+$). Ids from other upstreams (e.g. Gemini
// function-call history translated to OpenAI format) may contain other
// characters; those are rewritten deterministically as "toolu_<sha256>" so
// the same id always maps to the same replacement, keeping tool_use and
// tool_result blocks paired. mapping must be shared across the whole request.
func sanitizeToolUseID(id string, mapping map[string]string) string {
	if id == "" {
		return id
	}
	if mapped, ok := mapping[id]; ok {
		return mapped
	}
	valid := true
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		valid = false
		break
	}
	if valid {
		mapping[id] = id
		return id
	}
	sum := sha256.Sum256([]byte(id))
	newID := "toolu_" + hex.EncodeToString(sum[:])[:24]
	mapping[id] = newID
	return newID
}

// cleanupConvertedClaudeMessages applies the Messages-API content rule to a
// conversation this package built itself.
//
// The OpenAI to Claude conversion passes the client content arrays through
// unchanged, so a turn the client left empty — or a turn that only held parts
// this conversion drops — reaches a Claude-shaped lane as an empty content
// array, and the provider 400s the whole request. The Claude-client path never
// had that hole: SanitizeClaudePassthrough drops those turns. This calls the
// same rule from the same place (translator.DropNonContentfulClaudeMessages)
// instead of restating it, so the two paths cannot drift apart.
//
// intentionalPrefill is read from the body the client sent: an OpenAI caller
// that ends on an assistant turn asked for a prefill, and the trailing-turn
// repair must not overwrite it (translator.ClaudeIntentionalPrefill).
//
// A conversation the filter empties completely keeps its turns. Dropping the
// last one would trade an invalid empty-content turn for an equally invalid
// empty messages array, and neither shape can be answered — this way the
// caller that emptied the turn still gets the reason the provider gives.
func cleanupConvertedClaudeMessages(clientBody []byte, claudeMsgs []any) []any {
	cleaned, dropped := translator.DropNonContentfulClaudeMessages(
		claudeMsgs, translator.ClaudeIntentionalPrefill(clientBody))
	if !dropped || len(cleaned) == 0 {
		return claudeMsgs
	}
	return cleaned
}

func convertOpenAIMessagesToClaude(messages []any) (systemText string, claudeMessages []any) {
	// Anthropic requires tool_use.id / tool_use_id to match ^[a-zA-Z0-9_-]+$.
	// History from other upstreams (e.g. Gemini) can contain ids with other
	// characters; rewrite them deterministically, with a single mapping shared
	// across the whole conversation so tool_use and tool_result stay paired.
	toolIDMap := map[string]string{}
	var systemParts []string
	intermediate := make([]map[string]any, 0, len(messages))

	for _, m := range messages {
		msgMap, ok := m.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msgMap["role"].(string)

		if role == "system" {
			switch c := msgMap["content"].(type) {
			case string:
				if strings.TrimSpace(c) != "" {
					systemParts = append(systemParts, c)
				}
			case []any:
				for _, block := range c {
					if bMap, ok := block.(map[string]any); ok {
						if text, ok := bMap["text"].(string); ok && strings.TrimSpace(text) != "" {
							systemParts = append(systemParts, text)
						}
					}
				}
			}
			continue
		}

		if role == "tool" {
			toolCallID, _ := msgMap["tool_call_id"].(string)
			toolCallID = sanitizeToolUseID(toolCallID, toolIDMap)
			contentVal := convertClaudeImageParts(msgMap["content"])
			// is_error is optional to Anthropic but REQUIRED by some gateways
			// that re-serialize the block through a strict Rust struct — Zed
			// answers "missing field `is_error`" to a request Anthropic itself
			// accepts. Emitting it always keeps one body valid on both.
			isError, _ := msgMap["is_error"].(bool)
			intermediate = append(intermediate, map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type":        "tool_result",
						"tool_use_id": toolCallID,
						"content":     contentVal,
						"is_error":    isError,
					},
				},
			})
			continue
		}

		if role == "assistant" {
			if toolCalls, hasTC := msgMap["tool_calls"].([]any); hasTC && len(toolCalls) > 0 {
				var contentBlocks []any
				if cStr, ok := msgMap["content"].(string); ok && cStr != "" {
					contentBlocks = append(contentBlocks, map[string]any{
						"type": "text",
						"text": cStr,
					})
				} else if cArr, ok := msgMap["content"].([]any); ok && len(cArr) > 0 {
					contentBlocks = append(contentBlocks, convertClaudeImageParts(cArr).([]any)...)
				}
				for _, tc := range toolCalls {
					tcMap, ok := tc.(map[string]any)
					if !ok {
						continue
					}
					id, _ := tcMap["id"].(string)
					id = sanitizeToolUseID(id, toolIDMap)
					fn, _ := tcMap["function"].(map[string]any)
					name := ""
					var inputMap any = map[string]any{}
					if fn != nil {
						name, _ = fn["name"].(string)
						if argsStr, ok := fn["arguments"].(string); ok && strings.TrimSpace(argsStr) != "" {
							var parsed any
							if err := json.Unmarshal([]byte(argsStr), &parsed); err == nil && parsed != nil {
								inputMap = parsed
							}
						}
					}
					contentBlocks = append(contentBlocks, map[string]any{
						"type":  "tool_use",
						"id":    id,
						"name":  name,
						"input": inputMap,
					})
				}
				// The rebuilt turn carries only what the Messages API accepts: the
				// client's remaining keys are its OpenAI shape, not Claude's.
				intermediate = append(intermediate, map[string]any{
					"role":    "assistant",
					"content": contentBlocks,
				})
				continue
			}
		}

		// Nothing to reshape on this turn, but it still arrives in the client's
		// OpenAI shape. Replayed assistant turns are the ones that matter: an
		// OpenAI client echoes back the `reasoning_content` this proxy streamed
		// to it, and Anthropic rejects the field as an extra input, poisoning
		// every later turn of that conversation.
		intermediate = append(intermediate, claudeMessage(msgMap))
	}

	intermediate = dropImageEmptiedMessages(intermediate)

	var merged []any
	for _, m := range intermediate {
		if len(merged) == 0 {
			merged = append(merged, m)
			continue
		}
		prev := merged[len(merged)-1].(map[string]any)
		if prev["role"] == m["role"] {
			prev["content"] = combineClaudeContent(prev["content"], m["content"])
		} else {
			merged = append(merged, m)
		}
	}

	systemText = strings.Join(systemParts, "\n\n")
	return systemText, merged
}

// claudeMessageAllowedKeys are the only keys a Messages API message object
// accepts. Anthropic answers anything else with
// "messages.<n>.<key>: Extra inputs are not permitted" and refuses the request.
var claudeMessageAllowedKeys = map[string]bool{
	"role":    true,
	"content": true,
}

// claudeMessage returns msgMap reduced to the keys the Messages API accepts.
func claudeMessage(msgMap map[string]any) map[string]any {
	clean := make(map[string]any, len(claudeMessageAllowedKeys))
	for k, v := range msgMap {
		if claudeMessageAllowedKeys[k] {
			clean[k] = v
		}
	}
	if content, has := clean["content"]; has {
		converted := convertClaudeImageParts(content)
		clean["content"] = converted
		if emptiedByImageDrop(content, converted) {
			clean[imageEmptiedMarker] = true
		}
	}
	return clean
}

func combineClaudeContent(c1, c2 any) any {
	return append(normalizeClaudeBlocks(c1), normalizeClaudeBlocks(c2)...)
}

func normalizeClaudeBlocks(c any) []any {
	if c == nil {
		return []any{}
	}
	switch v := c.(type) {
	case string:
		if v == "" {
			return []any{}
		}
		return []any{map[string]any{"type": "text", "text": v}}
	case []any:
		return v
	case map[string]any:
		return []any{v}
	default:
		return []any{map[string]any{"type": "text", "text": fmt.Sprint(v)}}
	}
}

// isOpencodeResponsesModel ports the upstream free-executor lane rule
// (open-sse/executors/opencode.js isResponsesModel): the two curated free ids
// plus any Muse Spark model (upstream isMuseSparkModel:
// /^muse[-_]?spark(?:$|[-_:.\s])/i), so future free Spark ids keep the
// Responses lane without a code change. Paid lanes must not share this
// predicate — opencode-go routes on the catalog (goTargetFormat).
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

// museSparkMinOutputTokens is the output-budget floor for Muse Spark. These
// models spend 570-1320 hidden reasoning tokens before emitting any completion
// text, so a caller-set budget below the floor is consumed entirely by
// reasoning: the upstream answers 200 with an empty message, or stalls the
// stream until the gateway's watchdog reports a 502, and the caller sees a
// failure that names neither the model nor the budget.
//
// 512 is upstream's measured boundary (max_tokens 64/100 → empty content,
// 256/512/1024 → content present), not a round number picked here. Port of
// OmniRoute MUSE_SPARK_MIN_OUTPUT_TOKENS
// (open-sse/executors/opencodeMuseSpark.ts).
const museSparkMinOutputTokens = 512

// museSparkOutputBudgetFields are the three spellings an OpenAI-shaped request
// may use for its output cap: Chat Completions' max_tokens, Responses'
// max_output_tokens, and the OpenAI-compatible max_completion_tokens. A Muse
// Spark request may arrive on either lane, so all three are clamped.
var museSparkOutputBudgetFields = []string{"max_tokens", "max_output_tokens", "max_completion_tokens"}

// clampMuseSparkOutputBudget raises a caller-set output budget to the floor and
// returns the fields it changed. It only ever raises: an explicit larger budget
// and a request that set no budget at all stay byte-identical, because
// synthesizing a cap the caller never asked for would silently shorten real
// answers.
func clampMuseSparkOutputBudget(m map[string]any, cleanModel string) []string {
	if m == nil || !isOpencodeResponsesModel(cleanModel) {
		return nil
	}
	var raised []string
	for _, field := range museSparkOutputBudgetFields {
		budget, ok := outputBudgetOf(m[field])
		if !ok || budget >= museSparkMinOutputTokens {
			continue
		}
		m[field] = museSparkMinOutputTokens
		raised = append(raised, field)
	}
	return raised
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
	body, toolNameMap := translator.ConcealOpenCodeV2FingerprintTools(body, false, isOpenCodeAutoToolChoiceModel(cleanModel))
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
		if b, err := marshalStable(reqMap); err == nil {
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
	}, rotateOpencodeStableSession(opencodeStableSessionKey(req, true), hint, func(session string) {
		cfg.StaticHeaders = proxy.BuildOpenCodeHeaders(baseHeaders, session, true)
	}))
	if err != nil {
		return fmt.Errorf("ForwardOpencodeGo (free lane): %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
		return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: errBody}
	}

	if req.IsStream {
		return execSSEStream(w, resp.Body, req)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
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

	geminiBody, err := translator.TranslateOpenAIToGeminiWithSession(req.Body, req.SessionID)
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
		return geminiStream(w, resp.Body, ctx)
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
			return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: errBody}
		}

		if req.IsStream {
			stallReader := proxy.NewStallReaderWithContext(ctx, resp.Body, 0, "opencode-go-responses")
			defer stallReader.Close()
			return handleCodexStream(w, req, stallReader)
		}
		return handleCodexStream(w, req, resp.Body)
	}

	if opencodeGoMessagesModels[reqObj.Model] || opencodeGoMessagesModels[cleanModel] {
		// Route to /zen/go/v1/messages (Anthropic/Claude format)
		messagesURL := "https://opencode.ai/zen/go/v1/messages"
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
