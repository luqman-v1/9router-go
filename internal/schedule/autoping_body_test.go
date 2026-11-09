package schedule

import (
	"encoding/json"
	"testing"
)

// TestCodexPingBodyCarriesTheCheapReasoningFields pins the two fields that make
// a scheduled Codex ping cheap.
//
// Without `reasoning:{effort:"none"}` the model runs its default effort, and
// Codex strips max_output_tokens (codex_token_params.go) — so the ping's cost
// is unbounded. A scheduled ping exists to start a quota window, not to spend
// one, and upstream sends both fields for exactly that reason.
func TestCodexPingBodyCarriesTheCheapReasoningFields(t *testing.T) {
	body, err := CodexPingBody(CodexPingModel)
	if err != nil {
		t.Fatalf("CodexPingBody: %v", err)
	}

	var req struct {
		Model        string `json:"model"`
		Instructions string `json:"instructions"`
		Stream       *bool  `json:"stream"`
		Reasoning    *struct {
			Effort  string `json:"effort"`
			Summary string `json:"summary"`
		} `json:"reasoning"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("body is not valid JSON: %v\n%s", err, body)
	}

	if req.Instructions != CodexInstructions {
		t.Errorf("instructions = %q, want %q", req.Instructions, CodexInstructions)
	}
	if req.Reasoning == nil {
		t.Fatal("reasoning absent: the ping runs at the model's default effort")
	}
	if req.Reasoning.Effort != "none" {
		t.Errorf("reasoning.effort = %q, want %q", req.Reasoning.Effort, "none")
	}
	// Codex only starts the 5h window once the streaming response completes.
	if req.Stream == nil || !*req.Stream {
		t.Error("stream is not true: the Codex window would start only by accident of the transform")
	}
	if req.Model != CodexPingModel {
		t.Errorf("model = %q, want %q", req.Model, CodexPingModel)
	}
	if len(req.Messages) != 1 || req.Messages[0].Content != PingText {
		t.Errorf("messages = %+v, want one %q user message", req.Messages, PingText)
	}
}

// TestClaudePingBodyStaysPlain is the control: the Codex-only fields must not
// leak onto the Claude ping, where effort:"none" and a Responses instructions
// field mean nothing.
func TestClaudePingBodyStaysPlain(t *testing.T) {
	body, err := buildPingBody("claude", ClaudePingModel)
	if err != nil {
		t.Fatalf("buildPingBody: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("body is not valid JSON: %v\n%s", err, body)
	}
	for _, field := range []string{"instructions", "reasoning"} {
		if _, ok := raw[field]; ok {
			t.Errorf("Claude ping carries %q; that field belongs to the Codex body", field)
		}
	}
}
