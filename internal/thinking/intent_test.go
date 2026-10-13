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
		{"reasoning.enabled false", map[string]any{"reasoning": map[string]any{"enabled": false}}, none},
		{"reasoning.enabled true", map[string]any{"reasoning": map[string]any{"enabled": true}}, auto},
		{"reasoning effort high + enabled false", map[string]any{"reasoning": map[string]any{"effort": "high", "enabled": false}}, level("high")},
		{"reasoning effort xhigh + enabled false", map[string]any{"reasoning": map[string]any{"effort": "xhigh", "enabled": false}}, level("xhigh")},
		{"reasoning effort empty + enabled false", map[string]any{"reasoning": map[string]any{"enabled": false}}, none},
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

// TestOpenAIThinkingDisplayDialects dispositions every client key the display
// surface (intent.go openAIThinkingDisplay) reads, and pins how CaptureThinking
// masks it. Document-as-tested: the "none" rows record what the raw reader
// returns, not a claim that it is none-aware.
func TestOpenAIThinkingDisplayDialects(t *testing.T) {
	tests := []struct {
		name           string
		body           map[string]any
		display        string
		wantCaptureNil bool
		wantMode       Mode
		wantDisplay    string
	}{
		{
			name:           "reasoning.summary detailed asks for display",
			body:           map[string]any{"reasoning": map[string]any{"summary": "detailed"}},
			display:        "summarized",
			wantCaptureNil: true,
		},
		{
			name:        "reasoning.summary auto asks for display with a mode",
			body:        map[string]any{"reasoning": map[string]any{"summary": "auto", "effort": "high"}},
			display:     "summarized",
			wantMode:    ModeLevel,
			wantDisplay: "summarized",
		},
		{
			name:           "reasoning.summary none is not a display ask",
			body:           map[string]any{"reasoning": map[string]any{"summary": "none"}},
			wantCaptureNil: true,
		},
		{
			name:        "reasoning present short-circuits reasoning_effort",
			body:        map[string]any{"reasoning": map[string]any{"effort": "high"}, "reasoning_effort": "high"},
			wantMode:    ModeLevel,
			wantDisplay: "",
		},
		{
			name:        "reasoning non-map falls through to reasoning_effort",
			body:        map[string]any{"reasoning": "x", "reasoning_effort": "high"},
			display:     "summarized",
			wantMode:    ModeLevel,
			wantDisplay: "summarized",
		},
		{
			name:        "reasoning_effort high asks for display",
			body:        map[string]any{"reasoning_effort": "high"},
			display:     "summarized",
			wantMode:    ModeLevel,
			wantDisplay: "summarized",
		},
		{
			name:        "reasoning_effort none reads as a display ask raw but is masked",
			body:        map[string]any{"reasoning_effort": "none"},
			display:     "summarized",
			wantMode:    ModeNone,
			wantDisplay: "",
		},
		{
			name:           "reasoning_effort empty is not a display ask",
			body:           map[string]any{"reasoning_effort": ""},
			wantCaptureNil: true,
		},
		{
			name:        "reasoning.enabled true with summary carries display",
			body:        map[string]any{"reasoning": map[string]any{"enabled": true, "summary": "auto"}},
			display:     "summarized",
			wantMode:    ModeAuto,
			wantDisplay: "summarized",
		},
		{
			name:        "reasoning.enabled false carries no display",
			body:        map[string]any{"reasoning": map[string]any{"enabled": false}},
			wantMode:    ModeNone,
			wantDisplay: "",
		},
		{
			name:        "thinking dialect asks for no display",
			body:        map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": 1000.0}},
			wantMode:    ModeBudget,
			wantDisplay: "",
		},
		{
			name:        "thinkingConfig dialect asks for no display",
			body:        map[string]any{"thinkingConfig": map[string]any{"thinkingLevel": "high"}},
			wantMode:    ModeLevel,
			wantDisplay: "",
		},
		{
			name:        "enable_thinking dialect asks for no display",
			body:        map[string]any{"enable_thinking": true},
			wantMode:    ModeAuto,
			wantDisplay: "",
		},
		{
			name:        "output_config dialect asks for no display",
			body:        map[string]any{"output_config": map[string]any{"effort": "high"}},
			wantMode:    ModeLevel,
			wantDisplay: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := openAIThinkingDisplay(tt.body); got != tt.display {
				t.Errorf("openAIThinkingDisplay() = %q, want %q", got, tt.display)
			}
			got := CaptureThinking(tt.body)
			if tt.wantCaptureNil {
				if got != nil {
					t.Fatalf("CaptureThinking() = %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("CaptureThinking() = nil, want mode %q", tt.wantMode)
			}
			if got.Mode != tt.wantMode || got.Display != tt.wantDisplay {
				t.Errorf("CaptureThinking() = {Mode:%q Display:%q}, want {Mode:%q Display:%q}",
					got.Mode, got.Display, tt.wantMode, tt.wantDisplay)
			}
		})
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

// TestStripDialectKeys pins the disposition of every key the strip surface
// (suffix.go Strip) deletes: nested envelopes lose only the dialect child,
// non-map containers survive, and keys outside the list are untouched.
func TestStripDialectKeys(t *testing.T) {
	tests := []struct {
		name    string
		body    map[string]any
		absent  [][]string
		present [][]string
	}{
		{
			name:    "thinking",
			body:    map[string]any{"model": "m", "thinking": map[string]any{"type": "enabled", "budget_tokens": 1024.0}},
			absent:  [][]string{{"thinking"}},
			present: [][]string{{"model"}},
		},
		{
			name:    "reasoning_effort",
			body:    map[string]any{"model": "m", "reasoning_effort": "high"},
			absent:  [][]string{{"reasoning_effort"}},
			present: [][]string{{"model"}},
		},
		{
			name:    "reasoning with every child",
			body:    map[string]any{"model": "m", "reasoning": map[string]any{"effort": "high", "enabled": true, "summary": "auto"}},
			absent:  [][]string{{"reasoning"}},
			present: [][]string{{"model"}},
		},
		{
			name:    "thinkingConfig",
			body:    map[string]any{"model": "m", "thinkingConfig": map[string]any{"thinkingLevel": "high", "includeThoughts": true}},
			absent:  [][]string{{"thinkingConfig"}},
			present: [][]string{{"model"}},
		},
		{
			name:    "enable_thinking and thinking_budget",
			body:    map[string]any{"model": "m", "enable_thinking": true, "thinking_budget": 4096.0},
			absent:  [][]string{{"enable_thinking"}, {"thinking_budget"}},
			present: [][]string{{"model"}},
		},
		{
			name:    "output_config",
			body:    map[string]any{"model": "m", "output_config": map[string]any{"effort": "high"}},
			absent:  [][]string{{"output_config"}},
			present: [][]string{{"model"}},
		},
		{
			name: "generationConfig keeps siblings",
			body: map[string]any{"model": "m", "generationConfig": map[string]any{
				"thinkingConfig": map[string]any{"thinkingBudget": 0.0},
				"temperature":    0.5,
			}},
			absent:  [][]string{{"generationConfig", "thinkingConfig"}},
			present: [][]string{{"generationConfig"}, {"generationConfig", "temperature"}},
		},
		{
			name: "request envelope keeps siblings",
			body: map[string]any{"model": "m", "request": map[string]any{"generationConfig": map[string]any{
				"thinkingConfig":  map[string]any{"thinkingLevel": "high"},
				"maxOutputTokens": 1024.0,
			}}},
			absent:  [][]string{{"request", "generationConfig", "thinkingConfig"}},
			present: [][]string{{"request"}, {"request", "generationConfig", "maxOutputTokens"}},
		},
		{
			name: "params envelope children only",
			body: map[string]any{"model": "m", "params": map[string]any{
				"reasoning_effort": "high",
				"thinking":         map[string]any{"type": "enabled"},
				"temperature":      0.2,
			}},
			absent:  [][]string{{"params", "reasoning_effort"}, {"params", "thinking"}},
			present: [][]string{{"params"}, {"params", "temperature"}},
		},
		{
			name:    "params non-map survives untouched",
			body:    map[string]any{"model": "m", "params": "string"},
			absent:  nil,
			present: [][]string{{"params"}},
		},
		{
			name:    "generationConfig non-map survives untouched",
			body:    map[string]any{"model": "m", "generationConfig": "string"},
			absent:  nil,
			present: [][]string{{"generationConfig"}},
		},
		{
			name:    "request.generationConfig non-map survives untouched",
			body:    map[string]any{"model": "m", "request": map[string]any{"generationConfig": "string"}},
			absent:  nil,
			present: [][]string{{"request", "generationConfig"}},
		},
		{
			name:    "snake_case sibling is outside the strip list",
			body:    map[string]any{"model": "m", "reasoning_config": map[string]any{"effort": "high"}, "thinking_budget_bytes": 1.0},
			absent:  nil,
			present: [][]string{{"reasoning_config"}, {"thinking_budget_bytes"}},
		},
		{
			name:    "messages survive the strip",
			body:    map[string]any{"thinking": map[string]any{"type": "enabled"}, "messages": []any{map[string]any{"role": "user", "content": "hi"}}},
			absent:  [][]string{{"thinking"}},
			present: [][]string{{"messages"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			Strip(tt.body)
			for _, path := range tt.absent {
				if hasPath(tt.body, path...) {
					t.Errorf("Strip left %v in %+v", path, tt.body)
				}
			}
			for _, path := range tt.present {
				if !hasPath(tt.body, path...) {
					t.Errorf("Strip removed %v, want it kept in %+v", path, tt.body)
				}
			}
		})
	}
}

// TestNormalizeThinkingConfigDialectKeys dispositions the conversation-shape
// gate: `thinking` is request-scoped tool-state and drops on a non-user final
// turn through either messages or contents, while every other dialect key is
// request-level and must survive that drop.
func TestNormalizeThinkingConfigDialectKeys(t *testing.T) {
	turn := func(role string) []any {
		return []any{map[string]any{"role": role, "content": "x"}}
	}
	tests := []struct {
		name         string
		body         map[string]any
		wantThinking bool
		keep         []string
	}{
		{"messages user final keeps thinking", map[string]any{"thinking": map[string]any{"type": "enabled"}, "messages": turn("user")}, true, nil},
		{"messages assistant final drops thinking", map[string]any{"thinking": map[string]any{"type": "enabled"}, "messages": turn("assistant")}, false, nil},
		{"messages tool final drops thinking", map[string]any{"thinking": map[string]any{"type": "enabled"}, "messages": turn("tool")}, false, nil},
		{"contents user final keeps thinking", map[string]any{"thinking": map[string]any{"type": "enabled"}, "contents": turn("user")}, true, nil},
		{"contents assistant final drops thinking", map[string]any{"thinking": map[string]any{"type": "enabled"}, "contents": turn("assistant")}, false, nil},
		{"messages last not a map keeps thinking", map[string]any{"thinking": map[string]any{"type": "enabled"}, "messages": []any{"plain"}}, true, nil},
		{"messages empty keeps thinking", map[string]any{"thinking": map[string]any{"type": "enabled"}, "messages": []any{}}, true, nil},
		{"messages wrong type keeps thinking", map[string]any{"thinking": map[string]any{"type": "enabled"}, "messages": "x"}, true, nil},
		{"no conversation keeps thinking", map[string]any{"thinking": map[string]any{"type": "enabled"}}, true, nil},
		{
			name: "openai dialects survive the drop",
			body: map[string]any{
				"thinking": map[string]any{"type": "enabled"}, "reasoning_effort": "high",
				"output_config": map[string]any{"effort": "high"}, "messages": turn("assistant"),
			},
			wantThinking: false, keep: []string{"reasoning_effort", "output_config"},
		},
		{
			name: "responses and qwen dialects survive the drop",
			body: map[string]any{
				"thinking": map[string]any{"type": "enabled"}, "reasoning": map[string]any{"effort": "high"},
				"enable_thinking": true, "thinking_budget": 1024.0, "messages": turn("assistant"),
			},
			wantThinking: false, keep: []string{"reasoning", "enable_thinking", "thinking_budget"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			NormalizeThinkingConfig(tt.body)
			if _, has := tt.body["thinking"]; has != tt.wantThinking {
				t.Errorf("thinking survived = %v, want %v", has, tt.wantThinking)
			}
			for _, k := range tt.keep {
				if _, has := tt.body[k]; !has {
					t.Errorf("%q was dropped, want it kept (request-level dialect)", k)
				}
			}
		})
	}
}
