package thinking

import (
	"9router/proxy/internal/providers"
)

// ClaudeMinThinkingBudget is the smallest thinking budget the Claude Messages
// API accepts.
const ClaudeMinThinkingBudget = 1024

// ApplyClaudeThinkingOverride writes an operator level override into a
// Claude-shaped body, in the dialect the model's capability declares.
//
// A Claude client sends the Claude wire, and the Claude wire has no
// reasoning_effort field: Anthropic answers an unknown top-level field with
// a 400. So the override is written as the Claude dialect — a disabled
// thinking block, an adaptive block with an effort, or an enabled block with
// a budget.
//
// The bool reports whether a field was written. A body that already carries a
// thinking block is left alone: the operator fills a gap the client left.
func ApplyClaudeThinkingOverride(body map[string]any, level string, caps providers.Capabilities) bool {
	if _, has := body["thinking"]; has {
		return false
	}
	if level == "none" || level == "off" {
		body["thinking"] = map[string]any{"type": "disabled"}
		return true
	}
	if caps.ThinkingFormat == "claude-adaptive" {
		body["thinking"] = map[string]any{"type": "adaptive"}
		oc, _ := body["output_config"].(map[string]any)
		if oc == nil {
			oc = map[string]any{}
			body["output_config"] = oc
		}
		if _, set := oc["effort"]; !set {
			oc["effort"] = level
		}
		return true
	}
	body["thinking"] = map[string]any{
		"type":          "enabled",
		"budget_tokens": ClaudeBudgetForLevel(level, caps),
	}
	return true
}

// ClaudeBudgetForLevel returns the thinking budget a Claude-shaped body
// carries for an effort level. The budget is clamped into the model's
// declared range, and never below the smallest budget the Messages API
// accepts.
func ClaudeBudgetForLevel(level string, caps providers.Capabilities) int {
	budget := BudgetForLevel(level)
	if r := caps.ThinkingRange; r != nil {
		if r.Min > 0 && budget < r.Min {
			budget = r.Min
		}
		if r.Max > 0 && budget > r.Max {
			budget = r.Max
		}
	}
	if budget < ClaudeMinThinkingBudget {
		budget = ClaudeMinThinkingBudget
	}
	return budget
}
