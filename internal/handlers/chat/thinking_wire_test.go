package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/thinking"
)

// wireHandler returns a ChatHandler with no repo: providerThinkingMode is ""
// and decode failures pass through.
func wireHandler() *ChatHandler {
	return &ChatHandler{}
}

// TestPrepareInboundKeepsAnUnchangedBodyByteIdentical pins the byte
// stability of the inbound body when the operator override has nothing to
// add: the body the client sent is the body the upstream sees, so an integer
// above 2^53 in it survives the pass.
func TestPrepareInboundKeepsAnUnchangedBodyByteIdentical(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	if err := repo.UpdateSettingsRaw(map[string]any{
		"providerThinking": map[string]any{"openai": map[string]any{"mode": "high"}},
	}); err != nil {
		t.Fatalf("UpdateSettingsRaw: %v", err)
	}
	h := NewChatHandler(repo)
	body := []byte(`{"model":"m","user_id":9007199254740993,"reasoning_effort":"low",` +
		`"messages":[{"role":"user","content":"hi"}]}`)
	out, _, _ := h.prepareInboundThinking(context.Background(), "openai", "m", body, false)
	if !bytes.Equal(out, body) {
		t.Fatalf("an unchanged inbound body must be returned byte for byte:\nin:  %s\nout: %s", body, out)
	}
}

// TestInjectProviderThinkingUsesTheClaudeDialectForAClaudeClient pins the
// dialect of the operator override. A Claude client sends the Claude wire,
// and that wire has no reasoning_effort field: Anthropic answers an unknown
// top-level field with a 400.
func TestInjectProviderThinkingUsesTheClaudeDialectForAClaudeClient(t *testing.T) {
	h := wireHandler()
	m := map[string]any{
		"model":    "claude-x",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	}
	if !h.injectProviderThinkingMode("high", m, true, "anthropic", "claude-haiku-4.5") {
		t.Fatal("the override must be reported as applied")
	}
	if _, has := m["reasoning_effort"]; has {
		t.Fatalf("a Claude body must not carry reasoning_effort: %+v", m)
	}
	th, _ := m["thinking"].(map[string]any)
	if th == nil || th["type"] == nil {
		t.Fatalf("a Claude body must carry the Claude thinking dialect: %+v", m)
	}
}

// TestPrepareInboundTakesTheSuffixFromTheRequestedModel pins where the
// suffix comes from: the client's own model field, so a combo or an alias
// name carries it too. The name is resolved without the suffix, so the
// override has to travel beside the request.
func TestPrepareInboundTakesTheSuffixFromTheRequestedModel(t *testing.T) {
	h := wireHandler()
	body := []byte(`{"model":"mycombo(high)","messages":[{"role":"user","content":"hi"}]}`)
	_, wireModel, intent := h.prepareInboundThinking(context.Background(), "openai", "gpt-5.4", body, false)
	if wireModel != "gpt-5.4" {
		t.Fatalf("wireModel = %q, want the resolved model", wireModel)
	}
	if intent == nil || intent.Mode != thinking.ModeLevel || intent.Level != "high" {
		t.Fatalf("intent = %+v, want level high", intent)
	}
}

func TestPrepareInboundFastPath(t *testing.T) {
	h := wireHandler()
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	out, wireModel, intent := h.prepareInboundThinking(context.Background(), "openai", "m", body, false)
	if string(out) != string(body) {
		t.Fatalf("fast path rewrote: %s", out)
	}
	if wireModel != "m" || intent != nil {
		t.Fatalf("fast path = (%q, %+v)", wireModel, intent)
	}
}

func TestPrepareInboundSuffix(t *testing.T) {
	h := wireHandler()
	out, wireModel, intent := h.prepareInboundThinking(context.Background(), "deepseek", "deepseek-chat(high)",
		[]byte(`{"model":"x","messages":[]}`), false)
	if wireModel != "deepseek-chat" {
		t.Fatalf("wireModel = %q", wireModel)
	}
	if intent == nil || intent.Mode != "level" || intent.Level != "high" {
		t.Fatalf("intent = %+v", intent)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, has := m["reasoning_effort"]; has {
		t.Fatalf("inbound must not write effort: %s", out)
	}
}

func TestPrepareInboundCapture(t *testing.T) {
	h := wireHandler()
	out, wireModel, intent := h.prepareInboundThinking(context.Background(), "claude", "claude-haiku-4.5",
		[]byte(`{"reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`), false)
	if wireModel != "claude-haiku-4.5" {
		t.Fatalf("wireModel = %q", wireModel)
	}
	if intent == nil || intent.Mode != "level" || intent.Display != "summarized" {
		t.Fatalf("intent = %+v", intent)
	}
	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("unmarshal %s: %v", out, err)
	}
	if _, has := decoded["reasoning_effort"]; !has {
		t.Fatalf("inbound must keep client fields: %s", out)
	}
}

func TestPrepareInboundMalformed(t *testing.T) {
	h := wireHandler()
	bad := []byte(`{nope`)
	out, wireModel, _ := h.prepareInboundThinking(context.Background(), "openai", "m(none)", bad, false)
	if string(out) != string(bad) || wireModel != "m" {
		t.Fatalf("malformed = (%s, %q)", out, wireModel)
	}
}

func TestInjectProviderThinking(t *testing.T) {
	h := wireHandler()
	// nil repo → no-op.
	m := map[string]any{}
	h.injectProviderThinking(context.Background(), "deepseek", m)
	if len(m) != 0 {
		t.Fatalf("nil repo injected: %+v", m)
	}
}

// TestPrepareInboundKeepsTheOperatorOverrideOnAPrefill pins the order of
// the two passes. The conversation shape is normalised first — a thinking
// block is request-scoped tool state and only valid when the conversation
// ends on a user turn — and the operator's override is injected after it.
// Injected before it, the override was written and deleted again in the same
// pass, and the operator's setting silently did nothing on every prefill.
func TestPrepareInboundKeepsTheOperatorOverrideOnAPrefill(t *testing.T) {
	repo := poolRotationDB(t, `{"providerThinking":{"deepseek":{"mode":"on"}}}`)
	h := &ChatHandler{Repo: repo}
	body := []byte(`{"model":"deepseek-v4-pro","messages":[` +
		`{"role":"user","content":"hi"},{"role":"assistant","content":"prefill"}]}`)
	out, _, _ := h.prepareInboundThinking(context.Background(), "deepseek", "deepseek-v4-pro", body, false)
	if !bytes.Contains(out, []byte(`"budget_tokens":10000`)) {
		t.Fatalf("the operator override must survive a prefill turn: %s", out)
	}
	// And it has to reach the wire: the outbound pass reads the override
	// back out of the body.
	wire := thinking.ApplyJSON(out, "openai", "deepseek", "deepseek-v4-pro", nil)
	if !bytes.Contains(wire, []byte(`"reasoning_effort"`)) {
		t.Fatalf("the operator override must reach the wire: %s", wire)
	}
}

func TestPrepareInboundThinking_ToolResultTurnKeepsThinking(t *testing.T) {
	h := wireHandler()
	body := []byte(`{"model":"gpt-4o","thinking":{"type":"enabled"},"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":null,"tool_calls":[]},{"role":"tool","content":"result"}]}`)
	out, _, _ := h.prepareInboundThinking(context.Background(), "openai", "gpt-4o", body, false)
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, has := m["thinking"]; !has {
		t.Fatalf("tool result turn dropped thinking config: %s", out)
	}
}

// TestPrepareInboundDialectCapture dispositions every client dialect key on
// the inbound wire surface: the intent it yields, and the fact that inbound
// only captures — the dialect field itself is left for the outbound strip.
func TestPrepareInboundDialectCapture(t *testing.T) {
	h := wireHandler()
	tests := []struct {
		name     string
		body     string
		keepKey  string
		wantNil  bool
		wantMode thinking.Mode
		wantLev  string
		wantBud  int
		wantDisp string
	}{
		{
			name: "reasoning_effort level with display", body: `{"reasoning_effort":"high"}`,
			keepKey: "reasoning_effort", wantMode: thinking.ModeLevel, wantLev: "high", wantDisp: "summarized",
		},
		{
			name: "reasoning.effort short-circuits the display", body: `{"reasoning":{"effort":"high"},"reasoning_effort":"high"}`,
			keepKey: "reasoning", wantMode: thinking.ModeLevel, wantLev: "high",
		},
		{
			name: "reasoning.enabled false", body: `{"reasoning":{"enabled":false}}`,
			keepKey: "reasoning", wantMode: thinking.ModeNone,
		},
		{
			name: "reasoning.enabled true", body: `{"reasoning":{"enabled":true}}`,
			keepKey: "reasoning", wantMode: thinking.ModeAuto,
		},
		{
			name: "reasoning.enabled string unsupported", body: `{"reasoning":{"enabled":"false"}}`,
			keepKey: "reasoning", wantNil: true,
		},
		{
			name: "thinking disabled", body: `{"thinking":{"type":"disabled"}}`,
			keepKey: "thinking", wantMode: thinking.ModeNone,
		},
		{
			name: "thinking budget", body: `{"thinking":{"type":"enabled","budget_tokens":8192}}`,
			keepKey: "thinking", wantMode: thinking.ModeBudget, wantBud: 8192,
		},
		{
			name: "output_config effort", body: `{"output_config":{"effort":"high"}}`,
			keepKey: "output_config", wantMode: thinking.ModeLevel, wantLev: "high",
		},
		{
			name: "thinkingConfig level", body: `{"thinkingConfig":{"thinkingLevel":"high"}}`,
			keepKey: "thinkingConfig", wantMode: thinking.ModeLevel, wantLev: "high",
		},
		{
			name: "generationConfig budget zero", body: `{"generationConfig":{"thinkingConfig":{"thinkingBudget":0}}}`,
			keepKey: "generationConfig", wantMode: thinking.ModeNone,
		},
		{
			name: "request envelope budget", body: `{"request":{"generationConfig":{"thinkingConfig":{"thinkingBudget":4096}}}}`,
			keepKey: "request", wantMode: thinking.ModeBudget, wantBud: 4096,
		},
		{
			name: "enable_thinking false", body: `{"enable_thinking":false}`,
			keepKey: "enable_thinking", wantMode: thinking.ModeNone,
		},
		{
			name: "enable_thinking with thinking_budget", body: `{"enable_thinking":true,"thinking_budget":2048}`,
			keepKey: "thinking_budget", wantMode: thinking.ModeBudget, wantBud: 2048,
		},
		{
			name: "thinking_budget alone unsupported", body: `{"thinking_budget":4096}`,
			keepKey: "thinking_budget", wantNil: true,
		},
		{
			name: "reasoning.summary is display-only", body: `{"reasoning":{"summary":"auto"}}`,
			keepKey: "reasoning", wantNil: true,
		},
		{
			name: "thinking.display is display-only", body: `{"thinking":{"display":"summarized"}}`,
			keepKey: "thinking", wantNil: true,
		},
		{
			name: "params envelope is stripped-only", body: `{"params":{"reasoning_effort":"high"}}`,
			keepKey: "params", wantNil: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, _, intent := h.prepareInboundThinking(context.Background(), "openai", "gpt-5.3-codex", []byte(tt.body), false)
			if tt.wantNil {
				if intent != nil {
					t.Fatalf("intent = %+v, want nil", intent)
				}
			} else {
				if intent == nil {
					t.Fatalf("intent = nil, want mode %q", tt.wantMode)
				}
				if intent.Mode != tt.wantMode || intent.Level != tt.wantLev ||
					intent.Budget != tt.wantBud || intent.Display != tt.wantDisp {
					t.Errorf("intent = {Mode:%q Level:%q Budget:%d Display:%q}, want {Mode:%q Level:%q Budget:%d Display:%q}",
						intent.Mode, intent.Level, intent.Budget, intent.Display,
						tt.wantMode, tt.wantLev, tt.wantBud, tt.wantDisp)
				}
			}
			var m map[string]any
			if err := json.Unmarshal(out, &m); err != nil {
				t.Fatalf("unmarshal %s: %v", out, err)
			}
			if _, has := m[tt.keepKey]; !has {
				t.Errorf("inbound stripped %q, capture must leave it for the outbound pass: %s", tt.keepKey, out)
			}
		})
	}
}
