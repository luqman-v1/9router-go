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
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"9router/proxy/internal/proxy"
	"9router/proxy/internal/translator"
)

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
	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	resp, err := proxy.ForwardCodex(ctx, req.Client, req.Config, req.APIKey, transformedBody, req.IsStream)
	if err != nil {
		return fmt.Errorf("ForwardCodex: %w", err)
	}
	defer resp.Body.Close()
	return handleCodexStream(w, req, resp.Body)
}

// ForwardIflow forwards to iflow with HMAC-SHA256 signature.
// Injects stream_options and generates HMAC headers before forwarding.
func ForwardIflow(w http.ResponseWriter, req *Request) error {
	var reqMap map[string]interface{}
	if err := json.Unmarshal(req.Body, &reqMap); err != nil {
		return fmt.Errorf("parse body: %w", err)
	}
	if req.IsStream {
		reqMap["stream"] = true
		if _, ok := reqMap["stream_options"]; !ok {
			reqMap["stream_options"] = map[string]interface{}{"include_usage": true}
		}
	}
	reqBody, err := json.Marshal(reqMap)
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

	cleanedBody, err := json.Marshal(reqBody)
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
func ForwardKiro(w http.ResponseWriter, req *Request) error {
	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	resp, err := proxy.ForwardKiro(ctx, req.Client, req.Config, req.APIKey, req.Body, req.IsStream)
	if err != nil {
		return fmt.Errorf("ForwardKiro: %w", err)
	}
	defer resp.Body.Close()
	return handleKiroStream(w, req, resp.Body)
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

// buildCommandcodeBody transforms OpenAI request payload into CommandCode schema
// {threadId, memory, config, params} matching upstream openaiToCommandCodeRequest.
func buildCommandcodeBody(body []byte, model string) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return body, err
	}

	// If already wrapped in params, ensure required top-level fields
	if _, hasParams := m["params"]; hasParams {
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
				"environment":   runtime.GOOS,
				"structure":     []any{},
				"isGitRepo":     false,
				"currentBranch": "",
				"mainBranch":    "",
				"gitStatus":     "",
				"recentCommits": []any{},
			}
		}
		return json.Marshal(m)
	}

	params := make(map[string]any, len(m))
	for k, v := range m {
		params[k] = v
	}
	if model != "" {
		params["model"] = model
	}
	params["stream"] = true

	// CommandCode messages require content as array of blocks (never raw string)
	if rawMsgs, ok := m["messages"].([]any); ok {
		var systemTexts []string
		convertedMsgs := make([]any, 0, len(rawMsgs))
		for _, rawMsg := range rawMsgs {
			msgMap, ok := rawMsg.(map[string]any)
			if !ok {
				continue
			}
			role, _ := msgMap["role"].(string)
			contentVal := msgMap["content"]

			if role == "system" || role == "developer" {
				if s, ok := contentVal.(string); ok && s != "" {
					systemTexts = append(systemTexts, s)
				}
				continue
			}

			var contentBlocks []any
			if strContent, ok := contentVal.(string); ok {
				contentBlocks = append(contentBlocks, map[string]any{
					"type": "text",
					"text": strContent,
				})
			} else if arrContent, ok := contentVal.([]any); ok {
				contentBlocks = arrContent
			} else {
				contentBlocks = append(contentBlocks, map[string]any{
					"type": "text",
					"text": "",
				})
			}

			convertedMsg := map[string]any{
				"role":    role,
				"content": contentBlocks,
			}
			convertedMsgs = append(convertedMsgs, convertedMsg)
		}
		params["messages"] = convertedMsgs
		if len(systemTexts) > 0 {
			params["system"] = strings.Join(systemTexts, "\n\n")
		}
	}

	payload := map[string]any{
		"threadId": uuid.New().String(),
		"memory":   "",
		"config": map[string]any{
			"workingDir":    "/",
			"date":          time.Now().UTC().Format("2006-01-02"),
			"environment":   runtime.GOOS,
			"structure":     []any{},
			"isGitRepo":     false,
			"currentBranch": "",
			"mainBranch":    "",
			"gitStatus":     "",
			"recentCommits": []any{},
		},
		"params": params,
	}

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
	cleanModel = strings.TrimPrefix(cleanModel, "antigravity/")
	cleanModel = strings.TrimPrefix(cleanModel, "ag/")
	if parenIdx := strings.IndexByte(cleanModel, '('); parenIdx != -1 {
		cleanModel = cleanModel[:parenIdx]
	}

	if isOpencodeResponsesModel(cleanModel) {
		// Route through Responses API format: https://opencode.ai/zen/v1/responses
		transformedBody, _, err := buildResponsesBody(req.Body)
		if err != nil {
			return fmt.Errorf("transform body for muse-spark: %w", err)
		}

		transformedBody, err = normalizeMuseSparkResponsesBody(transformedBody, cleanModel)
		if err != nil {
			return fmt.Errorf("normalize muse-spark body: %w", err)
		}

		// The free-tier gate fingerprints the lowercase tool quartet; capitalised
		// variants from Claude Code CLI are renamed here and restored on the
		// response (translator.ConcealFingerprintTools).
		transformedBody, toolNameMap := translator.ConcealFingerprintTools(transformedBody)
		req.Ctx = translator.WithToolNameMap(req.Ctx, toolNameMap)
		w = NewToolNameRestoringWriter(w, toolNameMap)

		cfg := *req.Config
		if !strings.HasSuffix(cfg.BaseURL, "/responses") {
			baseURL := strings.TrimRight(cfg.BaseURL, "/")
			if strings.HasSuffix(baseURL, "/chat/completions") {
				baseURL = strings.TrimSuffix(baseURL, "/chat/completions")
			}
			cfg.BaseURL = baseURL + "/responses"
		}
		cfg.StaticHeaders = proxy.BuildOpenCodeHeaders(cfg.StaticHeaders, req.SessionID, req.IsStream)

		ctx := req.Ctx
		if ctx == nil {
			ctx = context.Background()
		}
		resp, err := proxy.ForwardOpenAI(ctx, req.Client, &cfg, apiKey, transformedBody, req.IsStream)
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
	if cleanModel == "union-alpha" {
		// Route through Messages API format: https://opencode.ai/zen/v1/messages (PR #4099)
		messagesURL := "https://opencode.ai/zen/v1/messages"
		if req.Config != nil && req.Config.BaseURL != "" && !strings.Contains(req.Config.BaseURL, "opencode.ai") {
			base := strings.TrimRight(req.Config.BaseURL, "/")
			if strings.HasSuffix(base, "/chat/completions") {
				base = strings.TrimSuffix(base, "/chat/completions")
			}
			messagesURL = base + "/messages"
		}
		headers := proxy.BuildOpenCodeHeaders(nil, req.SessionID, req.IsStream)
		headers["anthropic-version"] = "2023-06-01"
		ctx := req.Ctx
		if ctx == nil {
			ctx = context.Background()
		}
		body := ensureMessagesMaxTokens(req.Body, cleanModel)
		resp, err := proxy.DoRequest(ctx, req.Client, "POST", messagesURL, headers, body)
		if err != nil {
			return fmt.Errorf("ForwardOpencode (union-alpha messages route): %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
			return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: errBody}
		}

		if req.IsStream {
			return handleClaudeMessagesStream(w, req, resp.Body)
		}
		return handleClaudeMessagesNonStream(w, req, resp.Body)
	}

	body := InjectReasoningContent(req.Body, "opencode")
	// opencode Chat Completions path: same conceal + restore of tool names.
	body, toolNameMap := translator.ConcealFingerprintTools(body)
	req.Ctx = translator.WithToolNameMap(req.Ctx, toolNameMap)
	w = NewToolNameRestoringWriter(w, toolNameMap)

	cfg := *req.Config
	cfg.StaticHeaders = proxy.BuildOpenCodeHeaders(cfg.StaticHeaders, req.SessionID, req.IsStream)

	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	resp, err := proxy.ForwardOpenAI(ctx, req.Client, &cfg, apiKey, body, req.IsStream)
	if err != nil {
		return fmt.Errorf("ForwardOpencode: %w", err)
	}
	defer resp.Body.Close()

	if req.IsStream {
		return execSSEStream(w, resp.Body, req)
	}
	return jsonResponse(req.Ctx, w, resp.Body, req.TranslateResp, req.ResponseBuf)
}

var opencodeGoMessagesModels = map[string]bool{
	"minimax-m3":    true,
	"minimax-m2.7":  true,
	"minimax-m2.5":  true,
	"qwen3.8-max":   true,
	"qwen3.8-flash": true,
	"qwen3.7-max":   true,
	"qwen3.7-plus":  true,
	"qwen3.6-plus":  true,
	"union-alpha":   true,
}

// EnsureClaudeMessages exposes the OpenAI→Claude Messages request conversion
// (see ensureMessagesMaxTokens) for the fallback path, which forwards raw
// OpenAI-format bodies to Anthropic-native upstreams.
func EnsureClaudeMessages(body []byte, model string) []byte {
	return ensureMessagesMaxTokens(body, model)
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

	// 4. messages & system
	if msgs, ok := reqMap["messages"].([]any); ok && len(msgs) > 0 {
		extractedSys, claudeMsgs := convertOpenAIMessagesToClaude(msgs)
		reqMap["messages"] = claudeMsgs
		if extractedSys != "" {
			if existingSys, ok := reqMap["system"].(string); ok && existingSys != "" {
				reqMap["system"] = existingSys + "\n\n" + extractedSys
			} else if reqMap["system"] == nil {
				reqMap["system"] = extractedSys
			}
		}
	}

	// 5. Clean OpenAI-specific fields that strict Claude API rejects
	delete(reqMap, "stream_options")
	delete(reqMap, "store")
	delete(reqMap, "reasoning_effort")

	updated, err := json.Marshal(reqMap)
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
			return nil
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
			contentVal := msgMap["content"]
			intermediate = append(intermediate, map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type":        "tool_result",
						"tool_use_id": toolCallID,
						"content":     contentVal,
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
					contentBlocks = append(contentBlocks, cArr...)
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
				newMsg := make(map[string]any)
				for k, v := range msgMap {
					if k != "tool_calls" && k != "content" {
						newMsg[k] = v
					}
				}
				newMsg["role"] = "assistant"
				newMsg["content"] = contentBlocks
				intermediate = append(intermediate, newMsg)
				continue
			}
		}

		newMsg := make(map[string]any, len(msgMap))
		for k, v := range msgMap {
			newMsg[k] = v
		}
		intermediate = append(intermediate, newMsg)
	}

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

func isOpencodeResponsesModel(model string) bool {
	base := model
	if parenIdx := strings.IndexByte(base, '('); parenIdx != -1 {
		base = base[:parenIdx]
	}
	return strings.Contains(base, "muse-spark") || base == "grok-4.6" || base == "gpt-5.6-luna"
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

func normalizeMuseSparkResponsesBody(body []byte, cleanModel string) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return body, nil
	}
	if rEffort, ok := m["reasoning_effort"].(string); ok {
		if rEffort == "max" {
			rEffort = "xhigh"
		}
		m["reasoning"] = map[string]any{
			"effort":  rEffort,
			"summary": "auto",
		}
		delete(m, "reasoning_effort")
	} else if rMap, ok := m["reasoning"].(map[string]any); ok {
		if eff, ok := rMap["effort"].(string); ok && eff == "max" {
			rMap["effort"] = "xhigh"
		}
		rMap["summary"] = "auto"
	}
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
	return json.Marshal(m)
}

// ForwardOpencodeGo handles requests for opencode-go (paid tier).
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
	cleanModel = strings.TrimPrefix(cleanModel, "antigravity/")
	cleanModel = strings.TrimPrefix(cleanModel, "ag/")
	if parenIdx := strings.IndexByte(cleanModel, '('); parenIdx != -1 {
		cleanModel = cleanModel[:parenIdx]
	}

	if isOpencodeResponsesModel(cleanModel) {
		// Route through Responses API format: https://opencode.ai/zen/go/v1/responses (#3819, #3820, v0.5.75)
		transformedBody, _, err := buildResponsesBody(req.Body)
		if err != nil {
			return fmt.Errorf("transform body for opencode-go muse-spark: %w", err)
		}

		transformedBody, err = normalizeMuseSparkResponsesBody(transformedBody, cleanModel)
		if err != nil {
			return fmt.Errorf("normalize opencode-go muse-spark body: %w", err)
		}

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
		if headers["User-Agent"] == "" || !proxy.HasValidOpenCodeVersion(headers["User-Agent"]) {
			headers["User-Agent"] = proxy.DefaultOpenCodeUA
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
		resp, err := proxy.ForwardOpenAI(ctx, req.Client, &cfg, req.APIKey, transformedBody, req.IsStream)
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

	if opencodeGoMessagesModels[reqObj.Model] || opencodeGoMessagesModels[cleanModel] || cleanModel == "union-alpha" {
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
		messagesBody := ensureMessagesMaxTokens(body, cleanModel)
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

	cfg := *req.Config
	headers := make(map[string]string)
	for k, v := range cfg.StaticHeaders {
		headers[k] = v
	}
	if headers["User-Agent"] == "" || !proxy.HasValidOpenCodeVersion(headers["User-Agent"]) {
		headers["User-Agent"] = proxy.DefaultOpenCodeUA
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
	resp, err := proxy.ForwardOpenAI(ctx, req.Client, &cfg, req.APIKey, body, req.IsStream)
	if err != nil {
		return fmt.Errorf("ForwardOpencodeGo (default route): %w", err)
	}
	defer resp.Body.Close()

	if req.IsStream {
		return execSSEStream(w, resp.Body, req)
	}
	return jsonResponse(req.Ctx, w, resp.Body, req.TranslateResp, req.ResponseBuf)
}
