// Package thinking is the unified reasoning normalisation pass — port of
// open-sse/translator/concerns/thinkingUnified.js (upstream v0.5.99).
//
// The client may disable or tune reasoning in any of several dialects
// (reasoning_effort, thinking, reasoning, thinkingConfig, enable_thinking,
// output_config, or a "(level)" model suffix). This package captures that
// intent once, strips every dialect, and re-emits exactly the field the
// target wire format understands. Config (formats, limits, levels) comes
// from internal/providers, never hardcoded per-model here.
package thinking

import (
	"math"
	"strconv"
	"strings"
)

// Mode is the normalised reasoning request kind.
type Mode string

const (
	// ModeNone disables reasoning.
	ModeNone Mode = "none"
	// ModeAuto leaves the effort to the upstream.
	ModeAuto Mode = "auto"
	// ModeLevel requests a discrete effort level (see Levels).
	ModeLevel Mode = "level"
	// ModeBudget requests an explicit thinking-token budget.
	ModeBudget Mode = "budget"
)

// Intent is the normalised reasoning request — the Go form of upstream `cfg`.
type Intent struct {
	Mode    Mode
	Level   string // minimal|low|medium|high|xhigh|max|ultra when Mode == ModeLevel
	Budget  int    // tokens when Mode == ModeBudget
	Display string // "summarized"|"omitted"; "" when unset
}

// Clone returns a copy, safe for writers that clamp without mutating input.
func (in *Intent) Clone() *Intent {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

// Extract ports extractThinking: first match wins, in upstream order.
func Extract(body map[string]any) *Intent {
	if len(body) == 0 {
		return nil
	}

	// 1. Claude output_config.effort (explicit) — priority over everything.
	if oc, ok := body["output_config"].(map[string]any); ok {
		if s, ok := oc["effort"].(string); ok && s != "" {
			return effortString(s)
		}
	}

	// 2. OpenAI chat / Responses shape — effort first.
	if s, ok := body["reasoning_effort"].(string); ok && s != "" {
		return effortString(s)
	}
	if r, ok := body["reasoning"].(map[string]any); ok {
		if s, ok := r["effort"].(string); ok && s != "" {
			return effortString(s)
		}
		if en, ok := r["enabled"].(bool); ok {
			if !en {
				return &Intent{Mode: ModeNone}
			}
			return &Intent{Mode: ModeAuto}
		}
	}

	// 3. Claude shape.
	if t, ok := body["thinking"].(map[string]any); ok {
		if typ, _ := t["type"].(string); typ != "" {
			switch typ {
			case "disabled":
				return &Intent{Mode: ModeNone}
			case "adaptive", "enabled":
				if b, ok := numberField(t["budget_tokens"]); ok && b > 0 {
					return &Intent{Mode: ModeBudget, Budget: b}
				}
				return &Intent{Mode: ModeAuto}
			}
		}
	}

	// 4. Gemini shape (top-level, generationConfig, or request envelope).
	for _, tc := range geminiThinkingConfigs(body) {
		if s, ok := tc["thinkingLevel"].(string); ok && s != "" {
			return &Intent{Mode: ModeLevel, Level: strings.ToLower(strings.TrimSpace(s))}
		}
		if v, present := tc["thinkingBudget"]; present {
			if b, ok := numberField(v); ok {
				switch {
				case b == 0:
					return &Intent{Mode: ModeNone}
				case b < 0:
					return &Intent{Mode: ModeAuto}
				default:
					return &Intent{Mode: ModeBudget, Budget: b}
				}
			}
		}
	}

	// 5. Qwen shape.
	if en, ok := body["enable_thinking"].(bool); ok {
		if !en {
			return &Intent{Mode: ModeNone}
		}
		if b, ok := numberField(body["thinking_budget"]); ok && b > 0 {
			return &Intent{Mode: ModeBudget, Budget: b}
		}
		return &Intent{Mode: ModeAuto}
	}

	return nil
}

// CaptureThinking ports captureThinking: Extract plus the OpenAI display ask.
// executor/claude_thinking.go keeps its own copy for the Claude path.
func CaptureThinking(body map[string]any) *Intent {
	cfg := Extract(body)
	if cfg == nil || cfg.Mode == ModeNone {
		return cfg
	}
	if d := openAIThinkingDisplay(body); d != "" {
		cfg.Display = d
	}
	return cfg
}

// openAIThinkingDisplay ports openAIThinkingDisplay.
func openAIThinkingDisplay(body map[string]any) string {
	if r, ok := body["reasoning"].(map[string]any); ok {
		if s, ok := r["summary"].(string); ok && s != "" && s != "none" {
			return "summarized"
		}
		return ""
	}
	if s, ok := body["reasoning_effort"].(string); ok && s != "" {
		return "summarized"
	}
	return ""
}

// geminiThinkingConfigs yields the candidate thinkingConfig objects in
// upstream precedence: top-level, generationConfig, request envelope.
func geminiThinkingConfigs(body map[string]any) []map[string]any {
	var out []map[string]any
	if tc, ok := body["thinkingConfig"].(map[string]any); ok {
		out = append(out, tc)
	}
	if gc, ok := body["generationConfig"].(map[string]any); ok {
		if tc, ok := gc["thinkingConfig"].(map[string]any); ok {
			out = append(out, tc)
		}
	}
	if req, ok := body["request"].(map[string]any); ok {
		if gc, ok := req["generationConfig"].(map[string]any); ok {
			if tc, ok := gc["thinkingConfig"].(map[string]any); ok {
				out = append(out, tc)
			}
		}
	}
	return out
}

// effortString maps none|off → none, auto → auto, else level (trimmed and
// lowercased). The trim matters: a value the client padded with spaces is
// still that level, and the package's own helpers (KnownEffort, ToLevel)
// trim, so an untrimmed value here would be "known" in one place and
// unknown in another.
func effortString(s string) *Intent {
	e := strings.ToLower(strings.TrimSpace(s))
	switch e {
	case "none", "off":
		return &Intent{Mode: ModeNone}
	case "auto":
		return &Intent{Mode: ModeAuto}
	default:
		return &Intent{Mode: ModeLevel, Level: e}
	}
}

// numberField converts JSON numbers (float64 from encoding/json, plus int
// variants) to int. NaN/Inf and non-numbers report ok=false; upstream
// Number() semantics: finite only. A value above the token ceiling reports
// ok=false too: a float64 beyond the int range converts to an undefined
// value, and a budget that large is not a budget. A negative value converts
// fine — the callers decide what a non-positive budget means.
func numberField(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		if isFinite(n) && n <= maxTokenCount {
			return int(n), true
		}
	case float32:
		if isFinite(float64(n)) && n <= maxTokenCount {
			return int(n), true
		}
	case int:
		if n <= maxTokenCount {
			return n, true
		}
	case int64:
		if n <= maxTokenCount {
			return int(n), true
		}
	case int32:
		if n <= maxTokenCount {
			return int(n), true
		}
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(n)); err == nil && n <= maxTokenCount {
			return n, true
		}
	}
	return 0, false
}

// maxTokenCount is the ceiling a token count is accepted under. It is far
// above any real model budget and below the point where a float64 stops
// converting to an int exactly.
const maxTokenCount = 1 << 30

func isFinite(f float64) bool {
	return !math.IsNaN(f) && !math.IsInf(f, 0)
}
