package chat

import (
	json "9router/proxy/internal/fastjson"
)

// ApplyCodexFastMode enforces the 5-level precedence hierarchy for Codex Fast Mode,
// ensuring caller byte immutability and provider isolation.
//
// Levels:
//  1. Explicit service_tier: "fast" -> "priority" (modified=true); any other value preserves body and blocks account default.
//  2. Speed present (any value): preserves body and blocks account default.
//     "speed" is a client hint, not a Codex service_tier, so it never promotes
//     a request the caller did not ask to prioritise — executor
//     normalizeCodexServiceTier drops it and pins the same rule in
//     internal/integration/codex_service_tier_test.go.
//  3. Intent absent (neither service_tier nor speed): checks accountFastOpt (*accountFastOpt == true) -> service_tier = "priority" (modified=true).
func ApplyCodexFastMode(body []byte, provider string, accountFastOpt *bool) ([]byte, bool) {
	if provider != "codex" || len(body) == 0 {
		return body, false
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil || payload == nil {
		return body, false
	}

	// Level 1: Explicit service_tier presence
	if stVal, hasServiceTier := payload["service_tier"]; hasServiceTier {
		if stStr, ok := stVal.(string); ok && stStr == "fast" {
			payload["service_tier"] = "priority"
			out, err := json.Marshal(payload)
			if err != nil {
				return body, false
			}
			return out, true
		}
		// Any other value (e.g. "standard", "flex", null, 123) blocks lower levels.
		return body, false
	}

	// Level 2: Speed present, whatever its value, blocks the account default.
	// "speed" is a client hint rather than a Codex service_tier: the executor's
	// normalizeCodexServiceTier drops it and never promotes on it, and
	// internal/integration/codex_service_tier_test.go pins "speed alone does not
	// promote". Promoting here would send a tier the caller never asked for.
	if _, hasSpeed := payload["speed"]; hasSpeed {
		return body, false
	}

	// Level 3: Intent absent -> check per-account preference (strict boolean true)
	if accountFastOpt != nil && *accountFastOpt {
		payload["service_tier"] = "priority"
		out, err := json.Marshal(payload)
		if err != nil {
			return body, false
		}
		return out, true
	}

	return body, false
}
