package thinking

import (
	"9router/proxy/internal/providers"
	json "encoding/json/v2"
	"strings"
)

// ClampDisableToMinimalJSON rewrites an explicit reasoning disable
// ("none"/"off") to "minimal" when the provider forbids disabling reasoning.
// It is a no-op when the provider allows disabling, when the body does not ask
// to disable, or when the body is not a JSON object.
//
// Both the Chat dialect (reasoning_effort) and the Responses dialect
// (reasoning.effort) are handled: the Responses-native lanes never run
// ApplyJSON, so a disable request reaches them untouched.
func ClampDisableToMinimalJSON(body []byte, caps providers.Capabilities) []byte {
	if caps.CanDisableThinking() {
		return body
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil || m == nil {
		return body
	}
	if !clampDisableValue(m) {
		return body
	}
	out, err := json.Marshal(m, json.Deterministic(true))
	if err != nil {
		return body
	}
	return out
}

// clampDisableValue rewrites a disable request to the lowest level the
// provider accepts and reports whether it changed anything.
//
// Every dialect the package reads a disable from is covered: the Chat dialect
// (reasoning_effort), the Responses dialect (reasoning.effort and
// reasoning.enabled) and the Claude dialect (thinking.type "disabled"). A
// dialect left out here is a disable that reaches a provider that forbids
// disabling, which answers 400.
func clampDisableValue(m map[string]any) bool {
	changed := false
	if v, ok := m["reasoning_effort"].(string); ok && isDisableEffort(v) {
		m["reasoning_effort"] = "minimal"
		changed = true
	}
	if r, ok := m["reasoning"].(map[string]any); ok {
		if v, ok := r["effort"].(string); ok && isDisableEffort(v) {
			r["effort"] = "minimal"
			changed = true
		}
		if en, ok := r["enabled"].(bool); ok && !en {
			// The Responses dialect expresses a disable with enabled:false.
			// The effort now carries the intent, so the flag goes.
			delete(r, "enabled")
			r["effort"] = "minimal"
			changed = true
		}
	}
	if t, ok := m["thinking"].(map[string]any); ok {
		if typ, _ := t["type"].(string); typ == "disabled" {
			t["type"] = "enabled"
			t["budget_tokens"] = ClaudeMinThinkingBudget
			changed = true
		}
	}
	return changed
}

// isDisableEffort reports whether an effort value asks for no reasoning.
func isDisableEffort(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "none", "off":
		return true
	}
	return false
}
