package executor

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/log"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
)

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
	return buildCommandcodeBodyForSession(body, model, "")
}

// buildCommandcodeBodyForSession is buildCommandcodeBody with a
// conversation-stable threadId derived from commandcodeSessionKey (headers,
// body session fields, conversation prefix, connection id — in that order),
// matching upstream toCommandCodeSessionUuid. An empty key yields a random
// UUID.
func buildCommandcodeBodyForSession(body []byte, model, sessionID string) ([]byte, error) {
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
			m["threadId"] = commandcodeSessionUUID(sessionID)
		}
		if _, hasMem := m["memory"]; !hasMem {
			m["memory"] = ""
		}
		if _, hasCfg := m["config"]; !hasCfg {
			m["config"] = map[string]any{
				"workingDir":    commandcodeWorkingDir,
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
		"threadId": commandcodeSessionUUID(sessionID),
		"memory":   "",
		"config": map[string]any{
			"workingDir":    commandcodeWorkingDir,
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

	// json.Marshal is fine here: encoding/json sorts map keys, and threadId
	// is derived from the client session, so a repeat turn from one session
	// serialises byte-identically for the prefix cache. The caller-shaped
	// `params` path above keeps marshalStable for the same reason.
	return json.Marshal(payload)
}

// ForwardCommandcode forwards to CommandCode with NDJSON→SSE translation.
// 502/503/504 (HTTP status or in-stream error before any byte is written)
// are retried twice with a linear backoff, as upstream's executor does.
func ForwardCommandcode(w http.ResponseWriter, req *Request) error {
	var oreq struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(req.Body, &oreq); err != nil {
		log.Warn("executor", "commandcode unmarshal body", "error", err)
	}

	body := InjectReasoningContent(req.Body, "commandcode")
	// commandcodeSessionKey, not req.SessionID alone: the upstream prompt
	// cache is keyed on the threadId/x-session-id pair, and a client that
	// carries its session in the body (Claude Code's metadata.user_id) or
	// sends no session at all must not fall through to a per-request random
	// id — that re-cached the whole prefix on every request (upstream
	// decolua/9router#5006 measured 0 cache reads on 4.7B prompt tokens).
	reqBody, err := buildCommandcodeBodyForSession(body, oreq.Model,
		commandcodeSessionKey(body, req.SessionID, req.ConnectionID))
	if err != nil {
		return fmt.Errorf("marshal commandcode body: %w", err)
	}
	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	var envelope struct {
		ThreadID string `json:"threadId"`
	}
	_ = json.Unmarshal(reqBody, &envelope)
	for attempt := 0; ; attempt++ {
		err = forwardCommandcodeOnce(ctx, w, req, reqBody, envelope.ThreadID, oreq.Model)
		if attempt >= commandcodeMaxRetries || !isCommandcodeRetryable(err) {
			return err
		}
		timer := time.NewTimer(time.Duration(attempt+1) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func isCommandcodeRetryable(err error) bool {
	var ue *proxy.UpstreamError
	if !errors.As(err, &ue) {
		return false
	}
	return ue.StatusCode == http.StatusBadGateway ||
		ue.StatusCode == http.StatusServiceUnavailable ||
		ue.StatusCode == http.StatusGatewayTimeout
}

func forwardCommandcodeOnce(ctx context.Context, w http.ResponseWriter, req *Request, reqBody []byte, threadID, model string) error {
	r, err := http.NewRequestWithContext(ctx, "POST", req.Config.BaseURL, bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+req.APIKey)
	// x-session-id mirrors the body's threadId, as the official CLI does.
	r.Header.Set("x-session-id", threadID)
	r.Header.Set("x-project-slug", commandcodeProjectSlug)
	// Defaults mirror the registry StaticHeaders (command-code@1.88.0);
	// the loop below lets a configured value win.
	r.Header.Set("User-Agent", "cli")
	r.Header.Set("x-command-code-version", "1.88.0")
	r.Header.Set("x-cli-environment", "production")
	r.Header.Set("x-cli-surface", "cli")
	r.Header.Set("x-taste-learning", "false")
	r.Header.Set("Accept", "text/event-stream")
	if req.Config != nil && req.Config.StaticHeaders != nil {
		for k, v := range req.Config.StaticHeaders {
			r.Header.Set(k, v)
		}
	}
	if r.Header.Get("Accept-Encoding") == "" {
		r.Header.Set("Accept-Encoding", "identity")
	}
	resp, err := req.Client.Do(r)
	if err != nil {
		return fmt.Errorf("upstream request failed: %w", err)
	}
	defer resp.Body.Close()
	proxy.UnwrapIdentityGzip(r, resp)

	if resp.StatusCode != http.StatusOK {
		errBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
		if readErr != nil {
			return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: []byte("failed to read error body")}
		}
		return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: errBody}
	}

	if !req.IsStream {
		return handleCommandcodeNonStream(w, req, resp.Body, model)
	}
	return handleCommandcodeStream(w, req, resp.Body, model)
}

// ForwardOpencode handles requests for opencode (free tier).
