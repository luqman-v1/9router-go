package providers

import (
	"slices"
	"sort"
	"strings"
)

// Category mirrors the section a provider is grouped under in the dashboard.
// The values are the upstream 9router provider registry's own keys, so a
// dashboard section added upstream lands here without a rename.
type Category string

const (
	CategoryOAuth     Category = "oauth"     // OAuth & CLI sessions
	CategoryFreeTier  Category = "freeTier"  // free tier / free credits
	CategoryFree      Category = "free"      // 100% free, no key
	CategoryAPIKey    Category = "apikey"    // paid API key
	CategoryWebCookie Category = "webCookie" // web session cookie
	CategoryCustom    Category = "custom"    // openai-compatible-* / anthropic-compatible-*
)

// CategoryOrder is the order the dashboard renders the sections in. Upstream
// puts Compatible Endpoints first because those are the endpoints an operator
// creates themselves; the rest follow by how much of the setup is done for them.
var CategoryOrder = []Category{
	CategoryCustom,
	CategoryOAuth,
	CategoryFreeTier,
	CategoryFree,
	CategoryAPIKey,
	CategoryWebCookie,
}

// categorySections folds "free" into the "Free Tier" section, which is how the
// dashboard groups them: both are providers an operator can use without paying,
// and two adjacent sections labelled Free would be noise.
var categorySections = map[Category]Category{
	CategoryFree: CategoryFreeTier,
}

// Custom endpoint prefixes. A provider id carrying one of these is a
// user-defined compatible endpoint, not a registry provider.
const (
	CustomOpenAIPrefix    = "openai-compatible-"
	CustomAnthropicPrefix = "anthropic-compatible-"
	CustomEmbeddingPrefix = "custom-embedding-"
)

// IsCustomCompatible reports whether a provider ID denotes a user-defined
// OpenAI/Anthropic-compatible endpoint.
func IsCustomCompatible(providerID string) bool {
	s := strings.ToLower(strings.TrimSpace(providerID))
	return strings.HasPrefix(s, CustomOpenAIPrefix) ||
		strings.HasPrefix(s, CustomAnthropicPrefix) ||
		strings.HasPrefix(s, CustomEmbeddingPrefix)
}

// SectionCategory normalises a category to the section the dashboard renders it
// under, so a caller counting cards per section does not need to know that
// "free" is folded into "freeTier".
func SectionCategory(cat Category) Category {
	if section, ok := categorySections[cat]; ok {
		return section
	}
	return cat
}

// GetCategoryLabel returns the dashboard section title for a category.
func GetCategoryLabel(cat Category) string {
	switch SectionCategory(cat) {
	case CategoryCustom:
		return "Compatible Endpoints"
	case CategoryOAuth:
		return "OAuth Providers"
	case CategoryFreeTier:
		return "Free Tier Providers"
	case CategoryAPIKey:
		return "API Key Providers"
	case CategoryWebCookie:
		return "Web Cookie Providers"
	default:
		return "Other Providers"
	}
}

// GetProviderMeta looks up registry metadata for a provider ID, tolerating the
// custom-endpoint prefixes and resolving a short alias to its canonical entry.
//
// An alias resolves first: `ag` and `antigravity` must report the same
// category, icon and auth type, or the dashboard would render the same provider
// two different ways depending on which spelling a connection row stored.
func GetProviderMeta(id string) (ProviderMeta, bool) {
	key := strings.TrimSpace(id)
	if meta, ok := ProviderRegistry[key]; ok {
		return meta, true
	}
	if canonical, ok := ProviderAliasMap[strings.ToLower(key)]; ok {
		if meta, ok := ProviderRegistry[canonical]; ok {
			return meta, true
		}
	}
	// A compatible endpoint has no registry row but is not unknown: the
	// dashboard renders it as a Compatible Endpoint, not as a bare id.
	if IsCustomCompatible(key) {
		return ProviderMeta{
			ID:       key,
			Name:     key,
			Alias:    key,
			Category: CategoryCustom,
			AuthType: "apikey",
			Icon:     "dns",
			Color:    "#2EA043",
			Priority: 90,
		}, true
	}
	return ProviderMeta{}, false
}

// ClassifyProvider resolves the category, its dashboard label, and the display
// name for any provider string. The display name falls back to the raw id so a
// provider this registry does not know still renders with something a person
// can read rather than an empty heading.
func ClassifyProvider(providerID string) (category Category, categoryLabel string, displayName string) {
	raw := strings.TrimSpace(providerID)
	if meta, ok := GetProviderMeta(raw); ok {
		return meta.Category, GetCategoryLabel(meta.Category), meta.Name
	}
	// Known-but-unregistered self-hosted shapes. Upstream groups these with the
	// compatible endpoints because that is what they are.
	lower := strings.ToLower(raw)
	for _, prefix := range []string{"ollama", "custom", "vllm", "litellm", "local"} {
		if strings.HasPrefix(lower, prefix) {
			return CategoryCustom, GetCategoryLabel(CategoryCustom), raw
		}
	}
	return CategoryAPIKey, GetCategoryLabel(CategoryAPIKey), raw
}

// GetCatalogByCategory groups the registry by the section the dashboard
// renders, in CategoryOrder. The custom section is seeded with the two endpoint
// shapes the dashboard offers before any node exists, so an empty install still
// shows what can be created.
func GetCatalogByCategory() map[Category][]ProviderMeta {
	catalog := make(map[Category][]ProviderMeta, len(CategoryOrder))
	for _, cat := range CategoryOrder {
		catalog[cat] = nil
	}
	catalog[CategoryCustom] = []ProviderMeta{
		{
			ID:           "openai-compatible",
			Name:         "OpenAI Compatible",
			Alias:        "openai-compatible",
			Category:     CategoryCustom,
			AuthType:     "apikey",
			Description:  "Any OpenAI-compatible endpoint (Ollama, vLLM, LiteLLM, LM Studio)",
			ServiceKinds: []string{"llm"},
		},
		{
			ID:           "anthropic-compatible",
			Name:         "Anthropic Compatible",
			Alias:        "anthropic-compatible",
			Category:     CategoryCustom,
			AuthType:     "apikey",
			Description:  "Any Anthropic-compatible endpoint",
			ServiceKinds: []string{"llm"},
		},
	}
	for _, meta := range ProviderRegistry {
		catalog[meta.Category] = append(catalog[meta.Category], meta)
	}
	for cat := range catalog {
		sort.SliceStable(catalog[cat], func(i, j int) bool {
			if catalog[cat][i].Priority != catalog[cat][j].Priority {
				return catalog[cat][i].Priority < catalog[cat][j].Priority
			}
			return catalog[cat][i].ID < catalog[cat][j].ID
		})
	}
	return catalog
}

// NoAuthProviders returns the registry providers that need no credential and
// can serve a chat request, sorted by ascending Priority then ID.
//
// These own no connection row, because there is nothing to store. The dashboard
// is built from connections, so without this list a keyless provider is
// invisible until someone invents an account for it.
//
// The llm requirement is load-bearing. AuthType "none" alone also matches the
// local media servers — the TTS hosts and SearXNG — and promoting those onto
// the providers page would be noise an operator cannot act on: they cannot
// answer a chat request. Only a no-auth entry declaring llm is offered.
func NoAuthProviders() []ProviderMeta {
	out := make([]ProviderMeta, 0, len(ProviderRegistry))
	for _, meta := range ProviderRegistry {
		if !meta.NoAuth {
			continue
		}
		if !slices.Contains(meta.ServiceKinds, "llm") {
			continue
		}
		out = append(out, meta)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority < out[j].Priority
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// IsNoAuthMeta reports whether a registry entry authenticates with no
// user-supplied credential. Callers that gate on "do we have a connection?"
// must also accept these, or a provider an operator can plainly chat with looks
// broken.
func IsNoAuthMeta(id string) bool {
	meta, ok := GetProviderMeta(id)
	return ok && meta.NoAuth
}

// IsChatMeta reports whether a provider can serve a chat completion. A provider
// with no declared kinds is treated as an LLM provider, matching the default
// the dashboard applies when serviceKinds is absent.
func IsChatMeta(id string) bool {
	meta, ok := GetProviderMeta(id)
	if !ok {
		return false
	}
	if len(meta.ServiceKinds) == 0 {
		return true
	}
	return slices.Contains(meta.ServiceKinds, "llm")
}

// serviceKindLabels is the display vocabulary for a service kind, mirrored from
// the upstream registry so the dashboard renders "Text To Speech" rather than
// the raw key.
var serviceKindLabels = map[string]string{
	"llm":         "LLM",
	"embedding":   "Embedding",
	"image":       "Text to Image",
	"imageToText": "Image to Text",
	"tts":         "Text To Speech",
	"stt":         "Speech To Text",
	"webSearch":   "Web Search",
	"webFetch":    "Web Fetch",
	"video":       "Video",
	"music":       "Music",
}

// ServiceKindLabels exposes the service-kind vocabulary for the UI. A copy is
// returned so a caller cannot mutate the shared map.
func ServiceKindLabels() map[string]string {
	out := make(map[string]string, len(serviceKindLabels))
	for k, v := range serviceKindLabels {
		out[k] = v
	}
	return out
}
