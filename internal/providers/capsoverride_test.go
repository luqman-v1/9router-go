package providers

import (
	"sync"
	"testing"
)

// The override is sparse and the normalization is deliberately forgiving:
// unknown keys and unparseable values are DROPPED, never fatal, because a
// legacy row or a hand-edited import must not make the whole payload useless.
// These tests pin both halves so a future "strict mode" refactor cannot turn a
// junk field into a 400 for the fields that were fine.
func TestNormalizeModelCapsOverride(t *testing.T) {
	tests := []struct {
		name string
		in   map[string]any
		want ModelCapsOverride
	}{
		{
			name: "booleans kept",
			in:   map[string]any{"vision": true, "tools": false},
			want: ModelCapsOverride{Vision: boolPtr(true), Tools: boolPtr(false)},
		},
		{
			name: "string booleans dropped",
			in:   map[string]any{"vision": "true"},
			want: ModelCapsOverride{},
		},
		{
			name: "numeric string accepted",
			in:   map[string]any{"contextWindow": "200000"},
			want: ModelCapsOverride{ContextWindow: intPtr(200000)},
		},
		{
			name: "fractional dropped",
			in:   map[string]any{"maxOutput": 1024.5},
			want: ModelCapsOverride{},
		},
		{
			name: "zero is Auto, not a pin",
			in:   map[string]any{"contextWindow": 0},
			want: ModelCapsOverride{},
		},
		{
			name: "above ceiling dropped",
			in:   map[string]any{"contextWindow": ModelCapsOverrideMaxTokens + 1},
			want: ModelCapsOverride{},
		},
		{
			name: "at ceiling kept",
			in:   map[string]any{"contextWindow": ModelCapsOverrideMaxTokens},
			want: ModelCapsOverride{ContextWindow: intPtr(ModelCapsOverrideMaxTokens)},
		},
		{
			name: "unknown keys dropped",
			in:   map[string]any{"vision": true, "nonsense": 1},
			want: ModelCapsOverride{Vision: boolPtr(true)},
		},
		{
			name: "null means Auto",
			in:   map[string]any{"vision": nil},
			want: ModelCapsOverride{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeModelCapsOverride(tt.in)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !equalOverride(got, tt.want) {
				t.Errorf("got %#v, want %#v", got, tt.want)
			}
		})
	}

	if _, err := NormalizeModelCapsOverride(nil); err == nil {
		t.Error("a nil map must error: null is not an override object")
	}
}

// The stored key strips the vendor prefix and the :suffix so one operator
// declaration covers "vendor/claude-x:free" and "claude-x". Without this the
// editor would save one spelling and detection would read another.
func TestCapsOverrideModelKey(t *testing.T) {
	tests := []struct{ in, want string }{
		{"claude-x", "claude-x"},
		{"vendor/claude-x", "claude-x"},
		{"vendor/claude-x:free", "claude-x"},
		{"CLAUDE-X", "claude-x"},
		{"hf:openai/gpt-oss-120b", "gpt-oss-120b"},
	}
	for _, tt := range tests {
		if got := CapsOverrideModelKey(tt.in); got != tt.want {
			t.Errorf("CapsOverrideModelKey(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// A pinned field must win over detection, and an absent field must leave the
// detected value untouched. This is the whole contract of a sparse override.
func TestOverrideAppliesToCapabilities(t *testing.T) {
	o := ModelCapsOverride{Vision: boolPtr(true), ContextWindow: intPtr(500000)}
	c := Capabilities{Vision: false, Reasoning: true, ContextWindow: 128000}
	o.applyToCapabilities(&c)
	if !c.Vision {
		t.Error("pinned vision did not win over detection")
	}
	if c.ContextWindow != 500000 {
		t.Errorf("contextWindow = %d, want the pinned 500000", c.ContextWindow)
	}
	if !c.Reasoning {
		t.Error("an absent field overwrote detection: reasoning was not pinned")
	}

	// A nil override is a no-op, which is the fail-open path.
	var nilOverride *ModelCapsOverride
	c2 := Capabilities{Vision: false}
	nilOverride.applyToCapabilities(&c2)
	if c2.Vision {
		t.Error("nil override changed a capability")
	}
}

// The index is installed by the server and read on the hot path. A write must
// be visible on the next lookup, and a re-install must replace the source so a
// stale loader cannot keep answering after a restart.
func TestInstallAndInvalidateModelCapsOverride(t *testing.T) {
	modelCapsOverrideSnap.Store(nil)
	modelCapsOverrideMu.Lock()
	modelCapsOverrideLoad = nil
	modelCapsOverrideMu.Unlock()
	t.Cleanup(func() {
		modelCapsOverrideSnap.Store(nil)
		modelCapsOverrideMu.Lock()
		modelCapsOverrideLoad = nil
		modelCapsOverrideMu.Unlock()
	})

	var mu sync.Mutex
	rows := map[string]string{"antigravity|claude-sonnet-4": `{"vision":true,"contextWindow":200000}`}
	InstallModelCapsOverrideLoader(func() (map[string]string, error) {
		mu.Lock()
		defer mu.Unlock()
		out := make(map[string]string, len(rows))
		for k, v := range rows {
			out[k] = v
		}
		return out, nil
	})

	got := LookupModelCapsOverride("antigravity", "claude-sonnet-4")
	if got == nil {
		t.Fatal("installed override not found")
	}
	if got.ContextWindow == nil || *got.ContextWindow != 200000 {
		t.Errorf("contextWindow = %v, want 200000", got.ContextWindow)
	}

	// The alias resolves to the same row: a caller that has "ag" must not miss
	// the override written under the canonical id.
	if aliasHit := LookupModelCapsOverride("ag", "claude-sonnet-4"); aliasHit == nil {
		t.Error("alias lookup missed the canonical override")
	}

	mu.Lock()
	rows = map[string]string{"antigravity|claude-sonnet-4": `{"pdf":true}`}
	mu.Unlock()
	ReloadModelCapsOverrides()
	got = LookupModelCapsOverride("antigravity", "claude-sonnet-4")
	if got == nil || got.PDF == nil || !*got.PDF {
		t.Errorf("reload did not pick up the new payload: %#v", got)
	}
	if got.ContextWindow != nil {
		t.Error("reload kept a field the new payload dropped")
	}

	// A model with no row must answer nil, never a zero struct that would
	// silently pin false on every boolean.
	if LookupModelCapsOverride("antigravity", "unknown-model") != nil {
		t.Error("lookup invented an override for an unstored model")
	}
}

func boolPtr(b bool) *bool { return &b }
func intPtr(n int) *int    { return &n }

func equalOverride(a, b ModelCapsOverride) bool {
	eqBool := func(x, y *bool) bool {
		if x == nil || y == nil {
			return x == nil && y == nil
		}
		return *x == *y
	}
	eqInt := func(x, y *int) bool {
		if x == nil || y == nil {
			return x == nil && y == nil
		}
		return *x == *y
	}
	return eqBool(a.Vision, b.Vision) && eqBool(a.PDF, b.PDF) &&
		eqBool(a.AudioInput, b.AudioInput) && eqBool(a.VideoInput, b.VideoInput) &&
		eqBool(a.ImageOutput, b.ImageOutput) && eqBool(a.AudioOutput, b.AudioOutput) &&
		eqBool(a.Search, b.Search) && eqBool(a.Tools, b.Tools) &&
		eqBool(a.Reasoning, b.Reasoning) &&
		eqInt(a.ContextWindow, b.ContextWindow) && eqInt(a.MaxOutput, b.MaxOutput)
}
