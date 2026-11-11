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

// TestCommandCodeGoatIsPingable pins the wiring that gives the Command Code
// Provider API a 5h-window auto-ping: the provider is scheduled, its ping model
// is a cheap OpenAI-lane model, and its body is the plain one — not the Codex
// reasoning envelope.
func TestCommandCodeGoatIsPingable(t *testing.T) {
	found := false
	for _, provider := range Providers {
		if provider == "commandcode-goat" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Providers = %v, want commandcode-goat", Providers)
	}
	if got := PingModel("commandcode-goat"); got != CommandCodeGoatPingModel {
		t.Errorf("PingModel(commandcode-goat) = %q, want %q", got, CommandCodeGoatPingModel)
	}

	body, err := buildPingBody("commandcode-goat", CommandCodeGoatPingModel)
	if err != nil {
		t.Fatalf("buildPingBody: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("body is not valid JSON: %v\n%s", err, body)
	}
	if raw["model"] != CommandCodeGoatPingModel {
		t.Errorf("model = %v, want %q", raw["model"], CommandCodeGoatPingModel)
	}
	for _, field := range []string{"instructions", "reasoning"} {
		if _, ok := raw[field]; ok {
			t.Errorf("commandcode-goat ping carries %q; that field is Codex-only", field)
		}
	}
}
