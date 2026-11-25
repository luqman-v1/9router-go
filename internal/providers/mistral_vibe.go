package providers

import "strings"

// Mistral Vibe subscriptions are exposed as a Mistral connection whose
// providerSpecificData carries usageLane = "vibe". The same Mistral API key can
// be used with usageLane = "api" (or no lane at all), which must keep the
// ordinary provider behaviour. The reference gateway is
// github.com/Classic298/mistral-gateway (gateway.py).
const MistralVibeUsageLane = "vibe"

// MistralVibeAlias is one gateway-only model name and the canonical id Mistral
// actually serves it as. Order is the reference gateway's MODEL_ALIASES order;
// the /v1/models list uses it so the published catalog is deterministic.
type MistralVibeAlias struct {
	Alias  string
	Target string
}

// MistralVibeAliases mirrors gateway.py MODEL_ALIASES. These names are only
// exposed on the vibe lane; the target ids are the real upstream model ids.
var MistralVibeAliases = []MistralVibeAlias{
	{"mistral-large", "mistral-large-4"},
	{"mistral-large-latest", "mistral-large-4"},
	{"deepseek-v4.1-flash", "deepseek-v4-1-flash-1017"},
	{"deepseek-v4.1-flash-1017", "deepseek-v4-1-flash-1017"},
	{"deepseek-v4-1-flash", "deepseek-v4-1-flash-1017"},
	{"deepseek-v4-flash-1017", "deepseek-v4-1-flash-1017"},
	{"zai-glm-5.3", "zai-glm-5-3"},
	{"zai-glm-5.3-flash", "zai-glm-5-3-flash"},
	{"glm-5.3", "zai-glm-5-3"},
	{"glm-5.3-flash", "zai-glm-5-3-flash"},
	{"zai-glm-5.2", "zai-glm-5-2"},
	{"glm-5.2", "zai-glm-5-2"},
}

// MistralVibeCanonicalModels is the chat model set documented in the reference
// gateway's README. It is the fallback catalog when the live Mistral /v1/models
// feed cannot be reached; the feed itself remains authoritative when available.
var MistralVibeCanonicalModels = []string{
	"mistral-large-4",
	"mistral-medium-latest",
	"mistral-small-latest",
	"zai-glm-5-3",
	"zai-glm-5-3-flash",
	"deepseek-v4-1-flash-1017",
	"mistral-large-2512",
	"codestral-latest",
	"ministral-14b-latest",
	"ministral-8b-latest",
	"ministral-3b-latest",
	"voxtral-small-latest",
}

// IsMistralVibeConnection reports whether a connection's providerSpecificData
// opts into the Mistral Vibe wire contract. A nil map is the ordinary API lane.
func IsMistralVibeConnection(data map[string]any) bool {
	if len(data) == 0 {
		return false
	}
	lane, _ := data["usageLane"].(string)
	return strings.EqualFold(strings.TrimSpace(lane), MistralVibeUsageLane)
}

// CanonicalMistralVibeModel resolves a gateway alias to its upstream model id.
// Unknown ids are returned unchanged so new upstream models keep working.
func CanonicalMistralVibeModel(model string) string {
	for _, alias := range MistralVibeAliases {
		if alias.Alias == model {
			return alias.Target
		}
	}
	return model
}

// IsMistralVibeAlias reports whether an id is one of the gateway-only names.
func IsMistralVibeAlias(id string) bool {
	for _, alias := range MistralVibeAliases {
		if alias.Alias == id {
			return true
		}
	}
	return false
}
