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
		{"reasoning.effort", map[string]any{"reasoning": map[string]any{"effort": "xhigh"}}, level("xhigh")},
		{"reasoning_effort beats thinking", map[string]any{
			"reasoning_effort": "low",
			"thinking":         map[string]any{"type": "enabled", "budget_tokens": 8192.0},
		}, level("low")},
		{"thinking disabled", map[string]any{"thinking": map[string]any{"type": "disabled"}}, none},
		{"thinking enabled budget", map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": 24576.0}}, budget(24576)},
		{"thinking adaptive no budget", map[string]any{"thinking": map[string]any{"type": "adaptive"}}, auto},
		{"thinkingConfig level", map[string]any{"thinkingConfig": map[string]any{"thinkingLevel": "HIGH"}}, level("high")},
		{"thinkingBudget zero", map[string]any{"generationConfig": map[string]any{"thinkingConfig": map[string]any{"thinkingBudget": 0.0}}}, none},
		{"thinkingBudget negative", map[string]any{"thinkingConfig": map[string]any{"thinkingBudget": -1.0}}, auto},
		{"thinkingBudget positive", map[string]any{"thinkingConfig": map[string]any{"thinkingBudget": 8192.0}}, budget(8192)},
		{"envelope thinkingBudget", map[string]any{"request": map[string]any{"generationConfig": map[string]any{"thinkingConfig": map[string]any{"thinkingBudget": 4096.0}}}}, budget(4096)},
		{"enable_thinking false", map[string]any{"enable_thinking": false}, none},
		{"enable_thinking true budget", map[string]any{"enable_thinking": true, "thinking_budget": 4096.0}, budget(4096)},
		{"enable_thinking true bare", map[string]any{"enable_thinking": true}, auto},
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

func TestNormalizeThinkingConfig(t *testing.T) {
	user := map[string]any{
		"thinking": map[string]any{"type": "enabled"},
		"messages": []any{map[string]any{"role": "assistant", "content": "x"}, map[string]any{"role": "user", "content": "y"}},
	}
	NormalizeThinkingConfig(user)
	if _, ok := user["thinking"]; !ok {
		t.Fatal("dropped thinking on user-final turn")
	}
	tool := map[string]any{
		"thinking":         map[string]any{"type": "enabled"},
		"reasoning_effort": "high",
		"messages":         []any{map[string]any{"role": "user", "content": "y"}, map[string]any{"role": "assistant", "content": "x"}},
	}
	NormalizeThinkingConfig(tool)
	if _, ok := tool["thinking"]; ok {
		t.Fatal("kept thinking on non-user-final turn")
	}
	if tool["reasoning_effort"] != "high" {
		t.Fatal("touched reasoning_effort")
	}
	empty := map[string]any{"thinking": map[string]any{"type": "enabled"}}
	NormalizeThinkingConfig(empty)
	if _, ok := empty["thinking"]; !ok {
		t.Fatal("dropped thinking with no messages")
	}
}
