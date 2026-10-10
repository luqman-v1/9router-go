package thinking

import (
	"9router/proxy/internal/providers"
	"strings"
)

// EffortLevels is the ordered discrete effort set, low→high.
// Port of EFFORT_LEVELS in open-sse/translator/concerns/thinking.js.
var EffortLevels = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

// LevelToBudget maps a level to thinking tokens (Anthropic/Gemini docs).
// Port of LEVEL_TO_BUDGET in thinking.js.
var LevelToBudget = map[string]int{
	"none":    0,
	"minimal": 512,
	"low":     1024,
	"medium":  8192,
	"high":    24576,
	"xhigh":   32768,
	"max":     128000,
}

// EffortToBudget looks up a level (case-insensitive). 0 = no thinking;
// false = unrecognized. Port of effortToBudget.
func EffortToBudget(level string) (int, bool) {
	if level == "" {
		return 0, false
	}
	b, ok := LevelToBudget[strings.ToLower(level)]
	return b, ok
}

// BudgetToLevel maps tokens to the nearest discrete level via midpoints.
// "" when budget <= 0 (no reasoning). Port of budgetToLevel.
func BudgetToLevel(budget int) string {
	switch {
	case budget <= 0:
		return ""
	case budget <= 768:
		return "minimal"
	case budget <= 4096:
		return "low"
	case budget <= 16384:
		return "medium"
	case budget <= 28672:
		return "high"
	case budget <= 80384:
		return "xhigh"
	default:
		return "max"
	}
}

// BudgetToEffort maps a Gemini thinkingBudget to an OpenAI effort
// (antigravity reverse map). "" when budget <= 0. Port of budgetToEffort.
func BudgetToEffort(budget int) string {
	switch {
	case budget <= 0:
		return ""
	case budget <= 2048:
		return "low"
	case budget <= 16384:
		return "medium"
	default:
		return "high"
	}
}

// EffortToThinkingLevel maps an OpenAI effort to a Gemini thinkingLevel
// (gemini-3 enum: minimal|low|medium|high). Port of effortToThinkingLevel.
func EffortToThinkingLevel(effort string) string {
	e := strings.ToLower(strings.TrimSpace(effort))
	switch e {
	case "none", "off":
		return "minimal"
	case "xhigh", "max":
		return "high"
	default:
		return e
	}
}

// NormalizeOpenAILevel keeps max|ultra only when the model advertises them;
// ultra falls back to max, else xhigh. Port of normalizeOpenAILevel.
func NormalizeOpenAILevel(level string, supported []string) string {
	if level != "max" && level != "ultra" {
		return level
	}
	for _, s := range supported {
		if s == level {
			return level
		}
	}
	if level == "ultra" {
		for _, s := range supported {
			if s == "max" {
				return "max"
			}
		}
	}
	return "xhigh"
}

// ToLevel converts an intent to a discrete level string: level→level,
// budget→BudgetToLevel (fallback "medium"), auto→"auto", none→"".
// Port of toLevel.
func ToLevel(in *Intent) string {
	if in == nil {
		return ""
	}
	switch in.Mode {
	case ModeLevel:
		return in.Level
	case ModeBudget:
		if l := BudgetToLevel(in.Budget); l != "" {
			return l
		}
		return "medium"
	case ModeAuto:
		return "auto"
	default:
		return ""
	}
}

// ToBudget converts an intent to a token budget for budget-based formats.
// auto → (-1, true) (upstream "no budget" sentinel). Clamps to r when set.
// false when unmappable. Port of toBudget.
func ToBudget(in *Intent, r *providers.ThinkingRange) (int, bool) {
	if in == nil {
		return 0, false
	}
	var budget int
	switch in.Mode {
	case ModeBudget:
		budget = in.Budget
	case ModeLevel:
		b, ok := EffortToBudget(in.Level)
		if !ok {
			return 0, false
		}
		budget = b
	case ModeAuto:
		return -1, true
	default:
		return 0, false
	}
	if r != nil {
		if r.Min != 0 && budget < r.Min {
			budget = r.Min
		}
		if r.Max != 0 && budget > r.Max {
			budget = r.Max
		}
	}
	return budget, true
}

// ToGeminiThinkingLevel ports toGeminiThinkingLevel: auto→high, else the
// level mapped into the gemini-3 enum.
func ToGeminiThinkingLevel(in *Intent) string {
	raw := "high"
	if in != nil && in.Mode != ModeAuto {
		if l := ToLevel(in); l != "" {
			raw = l
		}
	}
	return EffortToThinkingLevel(raw)
}

// ToKimiReasoningEffort ports toKimiReasoningEffort.
func ToKimiReasoningEffort(in *Intent) string {
	level := ToLevel(in)
	switch level {
	case "auto":
		return "high"
	case "minimal":
		return "low"
	case "xhigh":
		return "max"
	case "low", "medium", "high", "max":
		return level
	default:
		return ""
	}
}
