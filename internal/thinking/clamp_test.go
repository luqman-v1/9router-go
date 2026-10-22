package thinking

import (
	"9router/proxy/internal/providers"
	json "encoding/json/v2"
	"testing"
)

func TestClampDisableToMinimalJSON(t *testing.T) {
	canDisable := providers.Capabilities{}
	cannotDisable := providers.Capabilities{ThinkingCanDisable: new(false)}

	tests := []struct {
		name       string
		body       string
		canDisable bool
		want       string
	}{
		{"chat dialect none clamps", `{"reasoning_effort":"none"}`, false, `{"reasoning_effort":"minimal"}`},
		{"chat dialect off clamps", `{"reasoning_effort":"off"}`, false, `{"reasoning_effort":"minimal"}`},
		{"chat dialect NONE clamps", `{"reasoning_effort":"NONE"}`, false, `{"reasoning_effort":"minimal"}`},
		{"responses dialect none clamps", `{"reasoning":{"effort":"none"}}`, false, `{"reasoning":{"effort":"minimal"}}`},
		{"responses dialect enabled false clamps", `{"reasoning":{"enabled":false}}`, false, `{"reasoning":{"effort":"minimal"}}`},
		{"claude dialect disabled clamps", `{"thinking":{"type":"disabled"}}`, false, `{"thinking":{"budget_tokens":1024,"type":"enabled"}}`},
		{"a provider that allows disabling is untouched", `{"reasoning_effort":"none"}`, true, `{"reasoning_effort":"none"}`},
		{"another level is untouched", `{"reasoning_effort":"high"}`, false, `{"reasoning_effort":"high"}`},
		{"a body without reasoning is untouched", `{"model":"m"}`, false, `{"model":"m"}`},
		{"a non-object body is untouched", `[1,2,3]`, false, `[1,2,3]`},
		{"a malformed body is untouched", `not json`, false, `not json`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			caps := canDisable
			if !tt.canDisable {
				caps = cannotDisable
			}
			got := ClampDisableToMinimalJSON([]byte(tt.body), caps)
			if string(got) != tt.want {
				t.Fatalf("ClampDisableToMinimalJSON() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestClampDisableToMinimalJSONIsDeterministic(t *testing.T) {
	body := []byte(`{"model":"m","reasoning_effort":"none","messages":[{"role":"user","content":"hi"}]}`)
	caps := providers.Capabilities{ThinkingCanDisable: new(false)}
	first := string(ClampDisableToMinimalJSON(body, caps))
	for i := 0; i < 50; i++ {
		if got := string(ClampDisableToMinimalJSON(body, caps)); got != first {
			t.Fatalf("clamp output is not stable: %s != %s", got, first)
		}
	}
}

func TestClampDisableToMinimalJSONLeavesTheRestOfTheBodyIntact(t *testing.T) {
	body := []byte(`{"model":"m","reasoning_effort":"none","max_tokens":128}`)
	caps := providers.Capabilities{ThinkingCanDisable: new(false)}
	var wire map[string]any
	if err := json.Unmarshal(ClampDisableToMinimalJSON(body, caps), &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if wire["reasoning_effort"] != "minimal" {
		t.Errorf("reasoning_effort = %v, want minimal", wire["reasoning_effort"])
	}
	if wire["model"] != "m" {
		t.Errorf("model = %v, want m", wire["model"])
	}
	if n, _ := wire["max_tokens"].(float64); n != 128 {
		t.Errorf("max_tokens = %v, want 128", wire["max_tokens"])
	}
}
