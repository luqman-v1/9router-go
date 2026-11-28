package executor

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"net/http"

	"9router/proxy/internal/proxy"
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
