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

// BudgetForLevel returns the thinking budget a level stands for. An unknown
// level uses the medium budget.
func BudgetForLevel(level string) int {
	if b, ok := EffortToBudget(level); ok && b > 0 {
		return b
	}
	return LevelToBudget["medium"]
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
//
// Port surface: the upstream effort tables this port carries, with no
// production caller in this repo yet.
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
	case "xhigh", "max", "ultra":
		return "high"
	case "minimal", "low", "medium", "high":
		return e
	default:
		return "high"
	}
}

// KnownEffort reports whether a level is one the effort dialects accept. A
// level outside the set is not a value a provider understands, so it is
// mapped to a known one instead of being forwarded.
func KnownEffort(level string) bool {
	e := strings.ToLower(strings.TrimSpace(level))
	for _, l := range EffortLevels {
		if e == l {
			return true
		}
	}
	return false
}

// effortRank orders the levels from the lowest to the highest, so a level can
// be mapped onto the closest one a model advertises.
func effortRank(level string) int {
	for i, l := range EffortLevels {
		if l == level {
			return i
		}
	}
	return -1
}

// NormalizeOpenAILevel keeps max|ultra only when the model advertises them;
// ultra falls back to max, else xhigh. A level outside the effort set becomes
// medium. Port of normalizeOpenAILevel.
func NormalizeOpenAILevel(level string, supported []string) string {
	if level != "max" && level != "ultra" {
		if KnownEffort(level) {
			return strings.ToLower(strings.TrimSpace(level))
		}
		return "medium"
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
//
// A negative budget is not a budget: it clamps to 0, and the format writers
// turn 0 into their own default rather than putting a negative token count on
// the wire.
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
	if budget < 0 {
		budget = 0
	}
	if r != nil {
		if r.Min > 0 && budget < r.Min {
			budget = r.Min
		}
		if r.Max > 0 && budget > r.Max {
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
