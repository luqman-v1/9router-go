package executor

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"9router/proxy/internal/proxy"
)

const copilotContextOverflowMsgPrefix = "Your input exceeds the context window of this model."
const ContextLengthExceededCode = "context_length_exceeded"

// copilotMessagesPath is the Claude Messages endpoint on the Copilot inference host.
const copilotMessagesPath = "/v1/messages"

// ForwardGithub handles GitHub Copilot requests, normalizing Copilot context overflow
// errors (400 invalid_request_body with "Your input exceeds...") into standard
// context_length_exceeded error codes (upstream decolua/9router PR #5280 parity).
//
// Claude models (claude-3-7-sonnet, claude-3-5-sonnet, claude-opus-*, ...) are
// forwarded directly to Copilot's native Claude Messages endpoint at
// https://api.githubcopilot.com/v1/messages using the Claude Messages wire format,
// instead of being converted to /chat/completions (OpenAI wire format).
func ForwardGithub(w http.ResponseWriter, req *Request) error {
	model, ok := githubModel(req)
	if ok && isClaudeModel(model) {
		return ForwardGithubClaudeMessages(w, req, model)
	}
	return forwardGithubOpenAI(w, req)
}

// forwardGithubOpenAI is the original GitHub Copilot behaviour: forward through the
// shared OpenAI-compat path and normalize Copilot context-overflow errors.
func forwardGithubOpenAI(w http.ResponseWriter, req *Request) error {
	err := ForwardOpenAI(w, req)
	if err == nil {
		return nil
	}

	var ue *proxy.UpstreamError
	if errors.As(err, &ue) && ue.StatusCode == http.StatusBadRequest {
		normalizeGithubContextError(ue)
	}

	return err
}

// ForwardGithubClaudeMessages forwards a Claude-model request to Copilot's native
// Claude Messages endpoint (/v1/messages) in Claude Messages format, reusing the
// Claude stream/non-stream response handlers so a Claude client gets a passthrough
// and an OpenAI client gets a Claude→OpenAI translation.
func ForwardGithubClaudeMessages(w http.ResponseWriter, req *Request, model string) error {
	body := ensureMessagesFor(req.Body, "github", model)

	base := strings.TrimRight(req.Config.BaseURL, "/")
	base = strings.TrimSuffix(base, "/chat/completions")
	url := base + copilotMessagesPath

	headers := make(map[string]string, len(req.Config.StaticHeaders)+1)
	for k, v := range req.Config.StaticHeaders {
		headers[k] = v
	}
	if !req.Config.NoAuth {
		headers[req.Config.AuthHeader] = "Bearer " + req.APIKey
	}
	if req.IsStream {
		headers["Accept"] = "text/event-stream"
	} else {
		headers["Accept"] = "application/json"
	}
	headers["Content-Type"] = "application/json"

	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	resp, err := proxy.DoRequest(ctx, req.Client, "POST", url, headers, body)
	if err != nil {
		return fmt.Errorf("ForwardGithubClaudeMessages upstream: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if req.IsStream {
		return handleClaudeMessagesStream(w, req, resp.Body)
	}
	return handleClaudeMessagesNonStream(w, req, resp.Body)
}

// githubModel returns the model id carried by the request, preferring the
// resolved ModelName and falling back to the body's "model" field.
func githubModel(req *Request) (string, bool) {
	if req.ModelName != "" {
		return req.ModelName, true
	}
	var probe struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(req.Body, &probe); err == nil && probe.Model != "" {
		return probe.Model, true
	}
	return "", false
}

// isClaudeModel reports whether a model id names an Anthropic Claude model,
// tolerating an optional provider prefix (e.g. "github/claude-3-7-sonnet").
func isClaudeModel(model string) bool {
	base := model
	if _, after, ok := strings.Cut(base, "/"); ok {
		base = after
	}
	return strings.HasPrefix(strings.ToLower(base), "claude-")
}

func normalizeGithubContextError(ue *proxy.UpstreamError) {
	if len(ue.Body) == 0 {
		return
	}

	var payload struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}

	if err := json.Unmarshal(ue.Body, &payload); err != nil {
		return
	}

	if payload.Error.Code == "invalid_request_body" && strings.HasPrefix(payload.Error.Message, copilotContextOverflowMsgPrefix) {
		payload.Error.Code = ContextLengthExceededCode
		payload.Error.Type = "invalid_request_error"
		if newBody, err := json.Marshal(payload); err == nil {
			ue.Body = newBody
		}
	}
}
