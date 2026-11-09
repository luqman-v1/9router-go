package executor

import (
	"net/http"
	"testing"
	"time"

	"9router/proxy/internal/providers"
)

// X-Mavis-Agent-Id defaults to "main" even when the connection config defines
// no StaticHeaders entry for it, so every MiniMax Code / mavis request always
// carries the agent id.
func TestMinimaxCodeHeadersAgentIdDefaultsToMain(t *testing.T) {
	req := &Request{
		Ctx:       t.Context(),
		Client:    &http.Client{Timeout: 10 * time.Second},
		Config:    &providers.ProviderConfig{BaseURL: "https://api.minimax.io", Format: "claude"},
		Provider:  "minimax-code",
		ModelName: "MiniMax-M2",
		APIKey:    "sk-test",
		Body:      []byte(`{"model":"MiniMax-M2","messages":[{"role":"user","content":"hi"}]}`),
		ConnData:  map[string]any{"authType": "oauth"},
	}
	h := minimaxCodeHeaders(req)
	if got := h["X-Mavis-Agent-Id"]; got != "main" {
		t.Errorf("X-Mavis-Agent-Id = %q, want %q", got, "main")
	}
}
