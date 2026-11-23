package executor

import (
	json "encoding/json/v2"
	"slices"
	"strings"

	"9router/proxy/internal/providers"
)

// Claude decides whether the thinking *text* comes back at all, not just
// whether thinking runs: a thinking block is returned as a signature only unless
// the request asks for `thinking.display: "summarized"`. OpenAI-shaped clients
// have no such field — their only way to ask for reasoning is `reasoning_effort`
// (Chat Completions) or `reasoning.summary` (Responses) — so that intent has to
// be captured during translation, before the OpenAI-only keys are stripped.
//
// Mirrors upstream open-sse/translator/concerns/thinkingUnified.js
// captureThinking/openAIThinkingDisplay.

// applyClaudeThinkingDisplay folds an OpenAI-shaped client's reasoning intent
// into the Claude `thinking` object. A body that already carries an explicit
// display wins, and a client that asked for no reasoning is left alone.
func applyClaudeThinkingDisplay(reqMap map[string]any) {
	thinking, _ := reqMap["thinking"].(map[string]any)
	if thinking != nil {
		if display, ok := thinking["display"].(string); ok && display != "" {
			return
		}
	}

	display := openAIThinkingDisplay(reqMap)
	if display == "" {
		return
	}
	if thinking == nil {
		thinking = map[string]any{}
		reqMap["thinking"] = thinking
	}
	thinking["display"] = display
}

// SanitizeClaudeThinking normalizes the thinking object for Anthropic Messages API:
//   - Removes `display` if thinking type is "disabled" (Anthropic rejects display with disabled)
//   - Normalizes invalid `display` values: on adaptive thinking, Anthropic returns 400
//     (thinking.adaptive.display: Input should be 'summarized', 'omitted') for anything other
//     than 'summarized' or 'omitted'. Experimental client values like 'updates' or 'highlights'
//     are clamped to 'summarized'.
func SanitizeClaudeThinking(reqMap map[string]any) bool {
	thinking, ok := reqMap["thinking"].(map[string]any)
	if !ok || thinking == nil {
		return false
	}
	tType, _ := thinking["type"].(string)
	if tType == "disabled" {
		if _, has := thinking["display"]; has {
			delete(thinking, "display")
			return true
		}
		return false
	}
	if d, ok := thinking["display"].(string); ok && d != "" {
		norm := strings.ToLower(strings.TrimSpace(d))
		if norm == "summarized" || norm == "omitted" {
			return false
		}
		if tType == "adaptive" {
			switch norm {
			case "none", "off", "hidden", "false":
				thinking["display"] = "omitted"
			default:
				thinking["display"] = "summarized"
			}
			return true
		}
		// Non-adaptive thinking: clamp experimental values that Anthropic rejects
		if norm == "updates" || norm == "highlights" {
			thinking["display"] = "summarized"
			return true
		}
	}
	return false
}

// SanitizeClaudeThinkingBody runs SanitizeClaudeThinking on raw JSON bytes.
func SanitizeClaudeThinkingBody(body []byte) []byte {
	var reqMap map[string]any
	if err := json.Unmarshal(body, &reqMap); err != nil {
		return body
	}
	if !SanitizeClaudeThinking(reqMap) {
		return body
	}
	out, err := json.Marshal(reqMap)
	if err != nil {
		return body
	}
	return out
}

// openAIThinkingDisplay reports the Claude display value an OpenAI-shaped body
// is asking for, or "" when it is not asking for reasoning at all.
func openAIThinkingDisplay(reqMap map[string]any) string {
	// Responses API: reasoning.summary is the explicit request for reasoning text.
	if reasoning, ok := reqMap["reasoning"].(map[string]any); ok {
		if summary, ok := reasoning["summary"].(string); ok && summary != "" && summary != "none" {
			return "summarized"
		}
	}
	// Chat Completions has no summary knob. A client setting reasoning_effort is
	// asking for reasoning; "none"/"off" asks for it to be switched off, in
	// which case there is no text to display.
	if effort, ok := reqMap["reasoning_effort"].(string); ok && effort != "" {
		switch strings.ToLower(effort) {
		case "none", "off", "minimal_off":
			return ""
		}
		return "summarized"
	}
	return ""
}

// WantsThinkingSummaries reports whether a Claude-shaped request body asks for
// thinking text. Callers use it to drop the redact-thinking beta flag, which
// asks Anthropic for signature-only thinking blocks and would blank the very
// summaries the client requested.
func WantsThinkingSummaries(body []byte) bool {
	var reqMap map[string]any
	if err := json.Unmarshal(body, &reqMap); err != nil {
		return false
	}
	thinking, ok := reqMap["thinking"].(map[string]any)
	if !ok {
		return false
	}
	display, _ := thinking["display"].(string)
	return display == "summarized"
}

// claudeLevelBudget is upstream's LEVEL_TO_BUDGET (open-sse/translator/concerns/
// thinking.js), used for models that take an explicit thinking budget.
var claudeLevelBudget = map[string]int{
	"minimal": 512,
	"low":     1024,
	"medium":  8192,
	"high":    24576,
	"xhigh":   32768,
	"max":     128000,
}

// claudeMinThinkingBudget is the smallest budget_tokens Anthropic accepts.
const claudeMinThinkingBudget = 1024

// openAIReasoningEffort returns the lower-cased effort an OpenAI-shaped body
// asks for: reasoning_effort (Chat Completions) or reasoning.effort (Responses).
func openAIReasoningEffort(reqMap map[string]any) string {
	if effort, ok := reqMap["reasoning_effort"].(string); ok && effort != "" {
		return strings.ToLower(effort)
	}
	if reasoning, ok := reqMap["reasoning"].(map[string]any); ok {
		if effort, ok := reasoning["effort"].(string); ok && effort != "" {
			return strings.ToLower(effort)
		}
	}
	return ""
}

// claudeThinkingOutputHeadroom is the room left for the visible answer when
// max_tokens is raised to fit a thinking budget (upstream open-sse/translator/
// formats/maxTokens.js).
const claudeThinkingOutputHeadroom = 1024

// applyClaudeThinkingType gives the Claude `thinking` object the `type` the
// Messages API requires. Without it an OpenAI client that sets reasoning_effort
// ends up with `thinking: {display: "summarized"}` (see
// applyClaudeThinkingDisplay) and Anthropic answers
// `400 thinking.type: Field required`.
//
// provider selects the capability table row (minimax-code, opencode-zen and
// opencode-go reach this code too and declare their own thinking format).
//
// Mirrors the claude-adaptive / claude-budget branches of upstream
// open-sse/translator/concerns/thinkingUnified.js applyThinking. Must run
// before the OpenAI-only reasoning keys are stripped.
func applyClaudeThinkingType(reqMap map[string]any, provider, model string) {
	thinking, _ := reqMap["thinking"].(map[string]any)
	if thinking != nil {
		if t, ok := thinking["type"].(string); ok && t != "" {
			return
		}
	}

	effort := openAIReasoningEffort(reqMap)
	switch effort {
	case "none", "off", "minimal_off":
		// An explicit "no reasoning" must not leave a typeless thinking
		// object behind (it would carry only `display`): same 400.
		if thinking != nil {
			reqMap["thinking"] = map[string]any{"type": "disabled"}
		}
		return
	case "":
		if thinking == nil {
			return
		}
		// Only a display was captured (Responses reasoning.summary without
		// an effort): the client wants reasoning text, so reasoning is on.
		effort = "medium"
	case "auto":
		effort = "high"
	}
	if thinking == nil {
		thinking = map[string]any{}
		reqMap["thinking"] = thinking
	}

	caps := providers.GetCapabilitiesForModel(provider, model)
	if caps.ThinkingFormat == "claude-adaptive" {
		thinking["type"] = "adaptive"
		level := effort
		switch level {
		case "minimal":
			level = "low"
		case "xhigh":
			if !slices.Contains(providers.GetThinkingLevels(provider, model), "xhigh") {
				level = "high"
			}
		}
		outCfg, _ := reqMap["output_config"].(map[string]any)
		if outCfg == nil {
			outCfg = map[string]any{}
			reqMap["output_config"] = outCfg
		}
		if _, set := outCfg["effort"]; !set {
			outCfg["effort"] = level
		}
		return
	}

	budget, ok := claudeLevelBudget[effort]
	if !ok {
		budget = claudeLevelBudget["medium"]
	}
	if r := caps.ThinkingRange; r != nil {
		if r.Min > 0 && budget < r.Min {
			budget = r.Min
		}
		if r.Max > 0 && budget > r.Max {
			budget = r.Max
		}
	}
	if budget < claudeMinThinkingBudget {
		budget = claudeMinThinkingBudget
	}

	// budget_tokens must stay strictly below max_tokens. Raise max_tokens to
	// leave room for the answer (capped at the model's output limit) rather
	// than crushing the budget; shrink the budget only when it alone already
	// reaches the ceiling. Mirrors upstream formats/claude.js + maxTokens.js.
	if maxTokens, ok := reqMap["max_tokens"].(int); ok {
		if want := budget + claudeThinkingOutputHeadroom; maxTokens < want {
			if caps.MaxOutput > 0 && want > caps.MaxOutput {
				want = caps.MaxOutput
			}
			if want > maxTokens {
				maxTokens = want
				reqMap["max_tokens"] = maxTokens
			}
		}
		if budget >= maxTokens {
			budget = maxTokens - 1
		}
	}
	if budget < claudeMinThinkingBudget {
		// No room for the minimum budget: send no thinking at all rather
		// than a body Anthropic would reject.
		delete(reqMap, "thinking")
		return
	}
	thinking["type"] = "enabled"
	thinking["budget_tokens"] = budget
}
