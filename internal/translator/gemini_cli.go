package translator

import (
	json "encoding/json/v2"
	"fmt"
	"strings"
)

// Cloud Code Assist ("gemini-cli") wire shape.
//
// The lane shares a host family with antigravity, but the envelope is different
// in kind: antigravity wraps for an agent surface and carries a userId, a
// requestType and a synthetic requestId. The CLI envelope has none of those —
// it is the project, the model, and the request, which is all the backend
// accepts. Upstream builds it in transformRequest
// (open-sse/executors/gemini-cli.js:28-34).

const (
	// GeminiCLIApiClient is the X-Goog-Api-Client value the CLI sends.
	GeminiCLIApiClient = "gl-node/22.17.0"
)

// GeminiCLIUserAgent mirrors the CLI's own UA. Google answers 403 when the
// model in it does not match the model in the envelope.
func GeminiCLIUserAgent(model string) string {
	return "GeminiCLI/0.20.1/" + GeminiCLIModel(model) + " (win32; x64; terminal)"
}

// GeminiCLIModel strips the provider prefix and 9router's own effort suffixes:
// the Cloud Code backend keys on the bare upstream id.
func GeminiCLIModel(model string) string {
	if _, after, ok := strings.Cut(model, "/"); ok {
		model = after
	}
	for _, suffix := range []string{"-thinking", "-agentic"} {
		model = strings.TrimSuffix(model, suffix)
	}
	if idx := strings.IndexByte(model, '('); idx > 0 {
		model = model[:idx]
	}
	return model
}

// WrapForGeminiCLI nests a Gemini payload in the Cloud Code envelope.
func WrapForGeminiCLI(geminiBody []byte, projectID, modelName string) ([]byte, error) {
	if len(geminiBody) == 0 {
		return nil, fmt.Errorf("wrap for gemini-cli: empty body")
	}
	// A body that is already an envelope is passed through: a second wrap
	// would nest it one level deeper and the backend would reject it as a
	// malformed request.
	var probe struct {
		Project string `json:"project"`
		Model   string `json:"model"`
		Request any    `json:"request"`
	}
	if json.Unmarshal(geminiBody, &probe) == nil && probe.Request != nil && probe.Model != "" {
		return geminiBody, nil
	}

	var inner any
	if err := json.Unmarshal(geminiBody, &inner); err != nil {
		return nil, fmt.Errorf("wrap for gemini-cli: parse body: %w", err)
	}
	out, err := json.Marshal(map[string]any{
		"project": projectID,
		"model":   GeminiCLIModel(modelName),
		"request": inner,
	})
	if err != nil {
		return nil, fmt.Errorf("wrap for gemini-cli: marshal envelope: %w", err)
	}
	return out, nil
}
