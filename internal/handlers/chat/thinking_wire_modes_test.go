package chat

import (
	"context"
	"reflect"
	"testing"

	"9router/proxy/internal/thinking"
)

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
			name: "an unknown mode is not written to the wire",
			mode: "Enabled", client: map[string]any{},
			want: map[string]any{}, wantNil: true,
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
			h.injectProviderThinking(context.Background(), "deepseek", m)
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
