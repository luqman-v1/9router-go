package thinking

import (
	"reflect"
	"testing"
)

func TestExtract(t *testing.T) {
	budget := func(n int) *Intent { return &Intent{Mode: ModeBudget, Budget: n} }
	level := func(l string) *Intent { return &Intent{Mode: ModeLevel, Level: l} }
	none := &Intent{Mode: ModeNone}
	auto := &Intent{Mode: ModeAuto}
	tests := []struct {
		name string
		body map[string]any
		want *Intent
	}{
		{"nil", nil, nil},
		{"empty", map[string]any{}, nil},
		{"output_config wins", map[string]any{
			"output_config":    map[string]any{"effort": "high"},
			"reasoning_effort": "low",
		}, level("high")},
		{"reasoning_effort none", map[string]any{"reasoning_effort": "none"}, none},
		{"reasoning_effort off", map[string]any{"reasoning_effort": "off"}, none},
		{"reasoning_effort auto", map[string]any{"reasoning_effort": "auto"}, auto},
		{"reasoning_effort padded", map[string]any{"reasoning_effort": " high "}, level("high")},
		{"reasoning_effort padded none", map[string]any{"reasoning_effort": " none "}, none},
		{"reasoning_effort padded auto", map[string]any{"reasoning_effort": " auto "}, auto},
		{"reasoning.effort", map[string]any{"reasoning": map[string]any{"effort": "xhigh"}}, level("xhigh")},
		{"reasoning.effort padded", map[string]any{"reasoning": map[string]any{"effort": " xhigh "}}, level("xhigh")},
		{"reasoning.enabled false", map[string]any{"reasoning": map[string]any{"enabled": false}}, none},
		{"reasoning.enabled true", map[string]any{"reasoning": map[string]any{"enabled": true}}, auto},
		{"reasoning effort high + enabled false", map[string]any{"reasoning": map[string]any{"effort": "high", "enabled": false}}, level("high")},
		{"reasoning effort xhigh + enabled false", map[string]any{"reasoning": map[string]any{"effort": "xhigh", "enabled": false}}, level("xhigh")},
		{"reasoning effort empty + enabled false", map[string]any{"reasoning": map[string]any{"effort": "", "enabled": false}}, none},
		{"reasoning empty object", map[string]any{"reasoning": map[string]any{}}, nil},
		{"reasoning.enabled string false", map[string]any{"reasoning": map[string]any{"enabled": "false"}}, nil},
		{"reasoning_effort beats thinking", map[string]any{
			"reasoning_effort": "low",
			"thinking":         map[string]any{"type": "enabled", "budget_tokens": 8192.0},
		}, level("low")},
		{"thinking disabled", map[string]any{"thinking": map[string]any{"type": "disabled"}}, none},
		{"thinking enabled budget", map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": 24576.0}}, budget(24576)},
		{"thinking adaptive no budget", map[string]any{"thinking": map[string]any{"type": "adaptive"}}, auto},
		{"thinkingConfig level", map[string]any{"thinkingConfig": map[string]any{"thinkingLevel": "HIGH"}}, level("high")},
		{"thinkingConfig level padded", map[string]any{"thinkingConfig": map[string]any{"thinkingLevel": " HIGH "}}, level("high")},
		{"thinkingBudget zero", map[string]any{"generationConfig": map[string]any{"thinkingConfig": map[string]any{"thinkingBudget": 0.0}}}, none},
		{"thinkingBudget negative", map[string]any{"thinkingConfig": map[string]any{"thinkingBudget": -1.0}}, auto},
		{"thinkingBudget positive", map[string]any{"thinkingConfig": map[string]any{"thinkingBudget": 8192.0}}, budget(8192)},
		{"envelope thinkingBudget", map[string]any{"request": map[string]any{"generationConfig": map[string]any{"thinkingConfig": map[string]any{"thinkingBudget": 4096.0}}}}, budget(4096)},
		{"enable_thinking false", map[string]any{"enable_thinking": false}, none},
		{"enable_thinking true budget", map[string]any{"enable_thinking": true, "thinking_budget": 4096.0}, budget(4096)},
		{"enable_thinking true bare", map[string]any{"enable_thinking": true}, auto},
		// Sweep dispositions for the remaining value shapes of every key
		// Extract reads, plus the adjacent keys it deliberately ignores.
		{"output_config non-map unsupported", map[string]any{"output_config": "high"}, nil},
		{"output_config.effort empty unsupported", map[string]any{"output_config": map[string]any{"effort": ""}}, nil},
		{"output_config.effort non-string unsupported", map[string]any{"output_config": map[string]any{"effort": 5.0}}, nil},
		{"reasoning_effort empty falls through", map[string]any{
			"reasoning_effort": "",
			"thinking":         map[string]any{"type": "disabled"},
		}, none},
		{"reasoning_effort non-string unsupported", map[string]any{"reasoning_effort": 5.0}, nil},
		{"reasoning non-map unsupported", map[string]any{"reasoning": "high"}, nil},
		{"reasoning.effort empty no enabled unsupported", map[string]any{"reasoning": map[string]any{"effort": ""}}, nil},
		{"reasoning.effort non-string defers to enabled", map[string]any{"reasoning": map[string]any{"effort": 5.0, "enabled": true}}, auto},
		{"reasoning.summary display-only not extracted", map[string]any{"reasoning": map[string]any{"summary": "auto"}}, nil},
		{"thinking non-map unsupported", map[string]any{"thinking": "x"}, nil},
		{"thinking empty object unsupported", map[string]any{"thinking": map[string]any{}}, nil},
		{"thinking.type absent unsupported", map[string]any{"thinking": map[string]any{"budget_tokens": 1024.0}}, nil},
		{"thinking.type unknown unsupported", map[string]any{"thinking": map[string]any{"type": "budget"}}, nil},
		{"thinking.type disabled wins over budget", map[string]any{"thinking": map[string]any{"type": "disabled", "budget_tokens": 9.0}}, none},
		{"thinking.budget_tokens zero falls back to auto", map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": 0.0}}, auto},
		{"thinking.budget_tokens negative falls back to auto", map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": -5.0}}, auto},
		{"thinking.budget_tokens non-number falls back to auto", map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": "x"}}, auto},
		{"thinking.display display-only not extracted", map[string]any{"thinking": map[string]any{"display": "summarized"}}, nil},
		{"thinkingConfig non-map unsupported", map[string]any{"thinkingConfig": "x"}, nil},
		{"thinkingConfig empty unsupported", map[string]any{"thinkingConfig": map[string]any{}}, nil},
		{"thinkingConfig.thinkingLevel empty unsupported", map[string]any{"thinkingConfig": map[string]any{"thinkingLevel": ""}}, nil},
		{"thinkingConfig.includeThoughts not extracted", map[string]any{"thinkingConfig": map[string]any{"includeThoughts": true}}, nil},
		{"thinkingBudget numeric string is budget", map[string]any{"thinkingConfig": map[string]any{"thinkingBudget": "4096"}}, budget(4096)},
		{"thinkingBudget bool unsupported", map[string]any{"thinkingConfig": map[string]any{"thinkingBudget": true}}, nil},
		{"generationConfig thinkingLevel extracted", map[string]any{"generationConfig": map[string]any{"thinkingConfig": map[string]any{"thinkingLevel": "LOW"}}}, level("low")},
		{"envelope thinkingLevel extracted", map[string]any{"request": map[string]any{"generationConfig": map[string]any{"thinkingConfig": map[string]any{"thinkingLevel": "LOW"}}}}, level("low")},
		{"enable_thinking non-bool unsupported", map[string]any{"enable_thinking": "true"}, nil},
		{"enable_thinking true budget zero falls back to auto", map[string]any{"enable_thinking": true, "thinking_budget": 0.0}, auto},
		{"enable_thinking true budget numeric string", map[string]any{"enable_thinking": true, "thinking_budget": "2048"}, budget(2048)},
		{"thinking_budget alone unsupported", map[string]any{"thinking_budget": 4096.0}, nil},
		{"params.reasoning_effort stripped-only never extracted", map[string]any{"params": map[string]any{"reasoning_effort": "high"}}, nil},
		{"params.thinking stripped-only never extracted", map[string]any{"params": map[string]any{"thinking": map[string]any{"type": "enabled"}}}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Extract(tt.body); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Extract() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestCaptureThinking(t *testing.T) {
	got := CaptureThinking(map[string]any{"reasoning_effort": "high"})
	if got == nil || got.Mode != ModeLevel || got.Level != "high" || got.Display != "summarized" {
		t.Fatalf("CaptureThinking effort = %+v", got)
	}
	got = CaptureThinking(map[string]any{
		"reasoning": map[string]any{"effort": "high", "summary": "detailed"},
	})
	if got == nil || got.Display != "summarized" {
		t.Fatalf("CaptureThinking summary = %+v", got)
	}
	got = CaptureThinking(map[string]any{
		"reasoning": map[string]any{"effort": "high", "summary": "none"},
	})
	if got == nil || got.Display != "" {
		t.Fatalf("CaptureThinking summary none = %+v", got)
	}
	if got := CaptureThinking(map[string]any{"reasoning_effort": "none"}); got == nil || got.Mode != ModeNone || got.Display != "" {
		t.Fatalf("CaptureThinking none = %+v", got)
	}
	if got := CaptureThinking(map[string]any{"model": "x"}); got != nil {
		t.Fatalf("CaptureThinking empty = %+v", got)
	}
}

func TestNumberFieldRejectsOutOfRange(t *testing.T) {
	if _, ok := numberField(float64(1e30)); ok {
		t.Error("a budget beyond the int range must be rejected")
	}
	if _, ok := numberField("not a number"); ok {
		t.Error("a non-number must be rejected")
	}
	v, ok := numberField(float64(1024))
	if !ok || v != 1024 {
		t.Errorf("numberField(1024) = (%d, %v), want (1024, true)", v, ok)
	}
}

func TestParseSuffixRejectsAnOverflowingBudget(t *testing.T) {
	model, intent := ParseSuffix("model(99999999999999999999)")
	if model != "model" {
		t.Errorf("model = %q, want model (the suffix is still stripped)", model)
	}
	if intent != nil {
		t.Errorf("intent = %+v, want nil for a budget that does not fit", intent)
	}
	model, intent = ParseSuffix("model(8192)")
	if model != "model" || intent == nil || intent.Mode != ModeBudget || intent.Budget != 8192 {
		t.Errorf("ParseSuffix(model(8192)) = (%q, %+v)", model, intent)
	}
}

func TestParseSuffix(t *testing.T) {
	tests := []struct {
		in    string
		clean string
		want  *Intent
	}{
		{"m", "m", nil},
		{"model(high)", "model", &Intent{Mode: ModeLevel, Level: "high"}},
		{"model(ultra)", "model", &Intent{Mode: ModeLevel, Level: "ultra"}},
		{"model(8192)", "model", &Intent{Mode: ModeBudget, Budget: 8192}},
		{"model(none)", "model", &Intent{Mode: ModeNone}},
		{"model(off)", "model", &Intent{Mode: ModeNone}},
		{"model(auto)", "model", &Intent{Mode: ModeAuto}},
		{"model(bogus)", "model", nil},
		{"model()", "model()", nil},
		{"model(a(b))", "model(a(b))", nil},
		{"a(b) (high)", "a(b)", &Intent{Mode: ModeLevel, Level: "high"}},
		{"model(high)  ", "model", &Intent{Mode: ModeLevel, Level: "high"}},
		{"ns/model(low)", "ns/model", &Intent{Mode: ModeLevel, Level: "low"}},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			clean, got := ParseSuffix(tt.in)
			if clean != tt.clean || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ParseSuffix(%q) = (%q, %+v), want (%q, %+v)", tt.in, clean, got, tt.clean, tt.want)
			}
		})
	}
	if got := StripSuffix("m(low)"); got != "m" {
		t.Fatalf("StripSuffix = %q", got)
	}
}

func TestStrip(t *testing.T) {
	body := map[string]any{
		"model":            "m",
		"thinking":         map[string]any{"type": "enabled"},
		"reasoning_effort": "high",
		"reasoning":        map[string]any{"effort": "high"},
		"thinkingConfig":   map[string]any{"thinkingLevel": "high"},
		"enable_thinking":  true,
		"thinking_budget":  100.0,
		"output_config":    map[string]any{"effort": "high"},
		"generationConfig": map[string]any{"thinkingConfig": map[string]any{}},
		"request":          map[string]any{"generationConfig": map[string]any{"thinkingConfig": map[string]any{}}},
		"params":           map[string]any{"reasoning_effort": "high", "thinking": map[string]any{}},
	}
	Strip(body)
	for _, k := range []string{"thinking", "reasoning_effort", "reasoning", "thinkingConfig", "enable_thinking", "thinking_budget", "output_config"} {
		if _, ok := body[k]; ok {
			t.Fatalf("Strip left %q", k)
		}
	}
	if _, ok := body["generationConfig"].(map[string]any)["thinkingConfig"]; ok {
		t.Fatal("Strip left generationConfig.thinkingConfig")
	}
	if _, ok := body["params"].(map[string]any)["reasoning_effort"]; ok {
		t.Fatal("Strip left params.reasoning_effort")
	}
	if body["model"] != "m" {
		t.Fatal("Strip touched model")
	}
}

// hasPath reports whether a key path resolves inside body.
func hasPath(body map[string]any, path ...string) bool {
	var cur any = body
	for _, k := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return false
		}
		if cur, ok = m[k]; !ok {
			return false
		}
	}
	return true
}
