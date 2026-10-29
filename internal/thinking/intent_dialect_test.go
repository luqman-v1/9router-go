package thinking

import (
	"testing"
)

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
