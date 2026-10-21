package chat

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"9router/proxy/internal/thinking"
)

// wireHandler returns a ChatHandler with no repo: providerThinkingMode is ""
// and decode failures pass through.
func wireHandler() *ChatHandler {
	return &ChatHandler{}
}

// TestPrepareInboundThinkingDeterministic pins the byte-stability of the
// inbound body: the same logical request must serialize identically on every
// call, or a prefix-caching upstream misses the cache it would otherwise hit.
func TestPrepareInboundThinkingDeterministic(t *testing.T) {
	h := wireHandler()
	body := []byte(`{"model":"m","reasoning_effort":"high","max_tokens":128,"temperature":0.5,` +
		`"messages":[{"role":"user","content":"hi"}]}`)
	first, _, _ := h.prepareInboundThinking("openai", "m", body)
	for i := 0; i < 100; i++ {
		got, _, _ := h.prepareInboundThinking("openai", "m", body)
		if !bytes.Equal(got, first) {
			t.Fatalf("the inbound body is not stable:\nfirst: %s\ngot:   %s", first, got)
		}
	}
}

func TestPrepareInboundFastPath(t *testing.T) {
	h := wireHandler()
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	out, wireModel, intent := h.prepareInboundThinking("openai", "m", body)
	if string(out) != string(body) {
		t.Fatalf("fast path rewrote: %s", out)
	}
	if wireModel != "m" || intent != nil {
		t.Fatalf("fast path = (%q, %+v)", wireModel, intent)
	}
}

func TestPrepareInboundSuffix(t *testing.T) {
	h := wireHandler()
	out, wireModel, intent := h.prepareInboundThinking("deepseek", "deepseek-chat(high)",
		[]byte(`{"model":"x","messages":[]}`))
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
	out, wireModel, intent := h.prepareInboundThinking("claude", "claude-haiku-4.5",
		[]byte(`{"reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`))
	if wireModel != "claude-haiku-4.5" {
		t.Fatalf("wireModel = %q", wireModel)
	}
	if intent == nil || intent.Mode != "level" || intent.Display != "summarized" {
		t.Fatalf("intent = %+v", intent)
	}
	if !strings.Contains(string(out), "reasoning_effort") {
		t.Fatalf("inbound must keep client fields: %s", out)
	}
}

func TestPrepareInboundMalformed(t *testing.T) {
	h := wireHandler()
	bad := []byte(`{nope`)
	out, wireModel, _ := h.prepareInboundThinking("openai", "m(none)", bad)
	if string(out) != string(bad) || wireModel != "m" {
		t.Fatalf("malformed = (%s, %q)", out, wireModel)
	}
}

func TestInjectProviderThinking(t *testing.T) {
	h := wireHandler()
	// nil repo → no-op.
	m := map[string]any{}
	h.injectProviderThinking("deepseek", m)
	if len(m) != 0 {
		t.Fatalf("nil repo injected: %+v", m)
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
			out, _, intent := h.prepareInboundThinking("openai", "gpt-5.3-codex", []byte(tt.body))
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

// TestInjectProviderThinkingDialectKeys dispositions the operator-override keys
// thinking_wire writes: `thinking` and `reasoning_effort` are client-owned, so
// injection is presence-guarded, and the resulting body's Extract disposition is
// what the gateway actually routes on.
func TestInjectProviderThinkingDialectKeys(t *testing.T) {
	on := map[string]any{"type": "enabled", "budget_tokens": 10000}
	off := map[string]any{"type": "disabled"}
	tests := []struct {
		name     string
		mode     string
		client   map[string]any
		want     map[string]any
		wantNil  bool
		wantMode thinking.Mode
		wantLev  string
		wantBud  int
	}{
		{
			name: "on injects an enabled block the extractor reads as budget",
			mode: "on", client: map[string]any{},
			want: map[string]any{"thinking": on}, wantMode: thinking.ModeBudget, wantBud: 10000,
		},
		{
			name: "on leaves a client thinking block alone",
			mode: "on", client: map[string]any{"thinking": off},
			want: map[string]any{"thinking": off}, wantMode: thinking.ModeNone,
		},
		{
			name: "on keeps client effort which outranks the injected block",
			mode: "on", client: map[string]any{"reasoning_effort": "high"},
			want: map[string]any{"reasoning_effort": "high", "thinking": on}, wantMode: thinking.ModeLevel, wantLev: "high",
		},
		{
			name: "on keeps client reasoning object",
			mode: "on", client: map[string]any{"reasoning": map[string]any{"effort": "high"}},
			want:     map[string]any{"reasoning": map[string]any{"effort": "high"}, "thinking": on},
			wantMode: thinking.ModeLevel, wantLev: "high",
		},
		{
			name: "on guard is presence-based so a non-map thinking blocks injection",
			mode: "on", client: map[string]any{"thinking": "x"},
			want: map[string]any{"thinking": "x"}, wantNil: true,
		},
		{
			name: "off injects a disabled block",
			mode: "off", client: map[string]any{},
			want: map[string]any{"thinking": off}, wantMode: thinking.ModeNone,
		},
		{
			name: "off cannot outrank a client effort",
			mode: "off", client: map[string]any{"reasoning_effort": "high"},
			want: map[string]any{"reasoning_effort": "high", "thinking": off}, wantMode: thinking.ModeLevel, wantLev: "high",
		},
		{
			name: "auto writes nothing",
			mode: "auto", client: map[string]any{},
			want: map[string]any{}, wantNil: true,
		},
		{
			name: "empty mode writes nothing",
			mode: "", client: map[string]any{},
			want: map[string]any{}, wantNil: true,
		},
		{
			name: "level mode writes reasoning_effort",
			mode: "low", client: map[string]any{},
			want: map[string]any{"reasoning_effort": "low"}, wantMode: thinking.ModeLevel, wantLev: "low",
		},
		{
			name: "level mode leaves a client effort alone",
			mode: "low", client: map[string]any{"reasoning_effort": "high"},
			want: map[string]any{"reasoning_effort": "high"}, wantMode: thinking.ModeLevel, wantLev: "high",
		},
		{
			name: "level mode outranks a client thinking block",
			mode: "low", client: map[string]any{"thinking": off},
			want: map[string]any{"reasoning_effort": "low", "thinking": off}, wantMode: thinking.ModeLevel, wantLev: "low",
		},
		{
			name: "level mode outranks client reasoning.effort",
			mode: "low", client: map[string]any{"reasoning": map[string]any{"effort": "high"}},
			want:     map[string]any{"reasoning": map[string]any{"effort": "high"}, "reasoning_effort": "low"},
			wantMode: thinking.ModeLevel, wantLev: "low",
		},
		{
			name: "unknown mode is written verbatim and lowercases to a level",
			mode: "Enabled", client: map[string]any{},
			want: map[string]any{"reasoning_effort": "Enabled"}, wantMode: thinking.ModeLevel, wantLev: "enabled",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := poolRotationDB(t, `{"providerThinking":{"deepseek":{"mode":"`+tt.mode+`"}}}`)
			h := &ChatHandler{Repo: repo}
			m := map[string]any{}
			for k, v := range tt.client {
				m[k] = v
			}
			h.injectProviderThinking("deepseek", m)
			if !reflect.DeepEqual(m, tt.want) {
				t.Fatalf("body = %+v, want %+v", m, tt.want)
			}
			got := thinking.Extract(m)
			if tt.wantNil {
				if got != nil {
					t.Fatalf("Extract() = %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("Extract() = nil, want mode %q", tt.wantMode)
			}
			if got.Mode != tt.wantMode || got.Level != tt.wantLev || got.Budget != tt.wantBud {
				t.Errorf("Extract() = {Mode:%q Level:%q Budget:%d}, want {Mode:%q Level:%q Budget:%d}",
					got.Mode, got.Level, got.Budget, tt.wantMode, tt.wantLev, tt.wantBud)
			}
		})
	}
}
