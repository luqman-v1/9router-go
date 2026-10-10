package thinking

import (
	"9router/proxy/internal/providers"
)

// geminiLevelOutputFloor ports GEMINI_LEVEL_OUTPUT_FLOOR.
var geminiLevelOutputFloor = map[string]int{
	"minimal": 4096,
	"low":     8192,
	"medium":  16384,
	"high":    65535,
}

// GeminiBudgetOutputFloor ports geminiBudgetOutputFloor.
func GeminiBudgetOutputFloor(budget int) int {
	switch {
	case budget == -1:
		return 32768
	case budget <= 1024:
		return 8192
	case budget <= 8192:
		return 16384
	case budget <= 24576:
		return 32768
	default:
		return 65535
	}
}

// GeminiLevelOutputFloor ports geminiLevelOutputFloor (default high).
func GeminiLevelOutputFloor(level string) int {
	if f, ok := geminiLevelOutputFloor[level]; ok {
		return f
	}
	return geminiLevelOutputFloor["high"]
}

// GeminiConfig ports applyFormat's gemini-level / gemini-budget cases for
// callers that build a typed Gemini request (internal/translator). It
// returns the thinkingConfig object and the maxOutputTokens floor.
//
// nil intent → (nil, 0): a body that never asked keeps no thinkingConfig.
// Unknown format → (nil, 0).
func GeminiConfig(intent *Intent, format string, caps providers.Capabilities) (config map[string]any, outputFloor int) {
	if intent == nil {
		return nil, 0
	}
	cd := canDisable(caps)
	switch format {
	case "gemini-level":
		level := "minimal"
		if intent.Mode != ModeNone {
			level = ToGeminiThinkingLevel(intent)
		}
		return map[string]any{
			"thinkingLevel":   level,
			"includeThoughts": level != "minimal",
		}, GeminiLevelOutputFloor(level)
	case "gemini-budget":
		if intent.Mode == ModeNone && cd {
			return map[string]any{
				"thinkingBudget":  0,
				"includeThoughts": false,
			}, 8192
		}
		b, ok := ToBudget(intent, caps.ThinkingRange)
		if !ok {
			b = -1
		}
		return map[string]any{
			"thinkingBudget":  b,
			"includeThoughts": true,
		}, GeminiBudgetOutputFloor(b)
	default:
		return nil, 0
	}
}

// SetGeminiThinking nests tc under generationConfig — the envelope's when the
// body wraps a {request:{generationConfig}} envelope (gemini-cli/antigravity),
// else the top-level one. Port of getGeminiGenerationConfig/setGeminiThinking.
func SetGeminiThinking(body map[string]any, tc map[string]any) {
	geminiGenerationConfig(body)["thinkingConfig"] = tc
}

func geminiGenerationConfig(body map[string]any) map[string]any {
	if req, ok := body["request"].(map[string]any); ok {
		gc, _ := req["generationConfig"].(map[string]any)
		if gc == nil {
			gc = map[string]any{}
			req["generationConfig"] = gc
		}
		return gc
	}
	gc, _ := body["generationConfig"].(map[string]any)
	if gc == nil {
		gc = map[string]any{}
		body["generationConfig"] = gc
	}
	return gc
}

// EnsureGeminiOutputFloor raises maxOutputTokens to min(floor, caps.MaxOutput)
// when absent or smaller. Port of ensureGeminiOutputFloor.
func EnsureGeminiOutputFloor(body map[string]any, floor int, caps providers.Capabilities) {
	cap := floor
	if caps.MaxOutput > 0 && caps.MaxOutput < cap {
		cap = caps.MaxOutput
	}
	gc := geminiGenerationConfig(body)
	cur, ok := numberField(gc["maxOutputTokens"])
	if !ok || cur < cap {
		gc["maxOutputTokens"] = cap
	}
}
