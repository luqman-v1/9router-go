package chat

import (
	json "9router/proxy/internal/fastjson"
	"9router/proxy/internal/thinking"
)

// ApplyCodexFastMode enforces the 5-level precedence hierarchy for Codex Fast Mode,
// model suffix service tier overrides, and model name cleaning, ensuring caller
// byte immutability and provider isolation.
func ApplyCodexFastMode(body []byte, provider string, accountFastOpt *bool) ([]byte, bool) {
	if (provider != "codex" && provider != "openai") || len(body) == 0 {
		return body, false
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil || payload == nil {
		return body, false
	}

	modified := false

	// Model suffix tier override (takes precedence over request body tier) and canonical model cleaning
	if m, ok := payload["model"].(string); ok {
		clean, intent := thinking.ParseSuffix(m)
		if clean != m {
			payload["model"] = clean
			modified = true
		}
		if intent != nil && intent.Tier != "" {
			payload["service_tier"] = intent.Tier
			modified = true
		}
	}

	if modified {
		if _, hasTier := payload["service_tier"]; hasTier {
			if out, err := json.Marshal(payload); err == nil {
				return out, true
			}
		}
	}

	// For codex provider, continue with levels 1-5
	if provider == "codex" {
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
			if modified {
				out, err := json.Marshal(payload)
				if err == nil {
					return out, true
				}
			}
			return body, false
		}

		// Level 2: Speed present, whatever its value, blocks the account default.
		if _, hasSpeed := payload["speed"]; hasSpeed {
			if modified {
				out, err := json.Marshal(payload)
				if err == nil {
					return out, true
				}
			}
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
	}

	if modified {
		out, err := json.Marshal(payload)
		if err == nil {
			return out, true
		}
	}

	return body, false
}
