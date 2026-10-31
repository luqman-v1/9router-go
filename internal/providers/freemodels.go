package providers

import (
	"sort"
	"strings"
	"sync"
)

// Free-model detection, ported from OmniRoute's
// `src/shared/utils/freeModels.ts` + `open-sse/config/freeModelCatalog.*`.
//
// The dashboard shows a green "Free" badge next to a model, so the two have to
// answer "is this free?" the same way. Two regimes decide that, on purpose,
// exactly as upstream does:
//
//   - A **provider** is considered to have a free tier when it appears in the
//     catalogued free-provider list below.
//   - A **model** is free when its id is listed in the shipped catalogue for
//     that provider, or — only for a provider with a documented free tier — it
//     carries a payload free signal: an `isFree: true` flag, an OpenRouter
//     style `:free` id suffix, or zero prompt *and* completion prices.
//
// A regime may retire a free tier behind a paid key (`discontinued`); such
// entries are filtered out at build time (see `freeBudgetGrantsAccess`) so a
// discontinued id is never reported free.
//
// The catalogue here is deliberately small: it holds only the providers this
// gateway actually routes, not OmniRoute's full feed. Adding a provider to the
// registry without adding it here is safe — it simply never shows a Free badge,
// which is the conservative default.

// freeRegime is the documented free-tier regime of a catalogue entry.
type freeRegime string

const (
	regimeRecurringDaily    freeRegime = "recurring-daily"
	regimeRecurringMonthly  freeRegime = "recurring-monthly"
	regimeRecurringCredit   freeRegime = "recurring-credit"
	regimeRecurringUncapped freeRegime = "recurring-uncapped"
	regimeOneTimeInitial    freeRegime = "one-time-initial"
	regimeKeyless           freeRegime = "keyless"
	regimeDiscontinued      freeRegime = "discontinued"
)

// freeBudgetGrantsAccess mirrors upstream `grantsFreeAccess`: every regime
// except `discontinued` still grants free access. Kept as an exhaustive switch
// so a newly added regime fails to compile rather than silently defaulting.
func freeBudgetGrantsAccess(r freeRegime) bool {
	switch r {
	case regimeRecurringDaily, regimeRecurringMonthly, regimeRecurringCredit,
		regimeRecurringUncapped, regimeOneTimeInitial, regimeKeyless:
		return true
	case regimeDiscontinued:
		return false
	default:
		return false
	}
}

// freeBudget is one catalogued free model (upstream `FreeModelBudget`, trimmed
// to the fields this gateway reads).
type freeBudget struct {
	Provider  string
	ModelID   string
	FreeType  freeRegime
	DisplayID string // friendly label, when it differs from ModelID
}

// freeModelCatalog is the shipped baseline, transcribed from OmniRoute's
// `freeModelCatalog.data.ts` for the providers this gateway routes. Entries
// whose regime is `discontinued` are kept in the source list (so the
// provenance is legible) and filtered out when the lookup tables are built.
var freeModelCatalog = []freeBudget{
	// Antigravity: keyless free tier.
	{"antigravity", "gemini-3.7-flash-high", regimeKeyless, ""},
	{"antigravity", "gemini-3.7-flash-medium", regimeKeyless, ""},
	{"antigravity", "gemini-3.7-flash-low", regimeKeyless, ""},
	{"antigravity", "gemini-pro-agent", regimeKeyless, ""},
	{"antigravity", "gemini-3.1-pro-low", regimeKeyless, ""},
	{"antigravity", "gemini-3.1-flash-lite", regimeKeyless, ""},
	{"antigravity", "claude-opus-4-6-thinking", regimeKeyless, ""},
	{"antigravity", "claude-sonnet-4-6", regimeKeyless, ""},
	{"antigravity", "gpt-oss-120b-medium", regimeKeyless, ""},

	// Z.AI GLM (upstream "glm" + "glm-cn"). The *-flash tier is free forever.
	{"glm", "glm-4.7-flash", regimeRecurringUncapped, ""},
	{"glm", "glm-4.5-flash", regimeRecurringUncapped, ""},
	{"glm-cn", "glm-4-flash", regimeRecurringUncapped, ""},
	{"glm-cn", "glm-4.5-flash", regimeRecurringUncapped, ""},
	{"glm-cn", "glm-4.7-flash", regimeRecurringUncapped, ""},
	{"glm-cn", "glm-signup-bonus", regimeOneTimeInitial, "Z.AI — 20M signup bonus"},

	// OpenCode (short alias "oc"). A keyless free tier: every free id is
	// reachable with the literal API key "public"
	// (KnownProviders["opencode"].DefaultAPIKey), which is why the provider is
	// filed under CategoryFree / AuthType "none" rather than freeTier.
	//
	// Every id below was verified by actually calling it (POST
	// /zen/v1/chat/completions with the literal key "public"), not merely read
	// off /zen/v1/models. That distinction matters: the listing advertises ids
	// the chat endpoint then refuses.
	//
	// Two ids from the previous list were removed for exactly that reason and
	// are deliberately NOT re-added by a later "sync with /models" pass:
	//   - deepseek-v4-flash-free -> 400 "Model is unavailable"
	//   - jev-1.13-free          -> 500 Internal server error
	// Seeding dead ids is worse than seeding fewer models: the operator hits a
	// failure on a model the router itself advertised, and every such request
	// burns a fallback attempt before reaching one that works.
	//
	// Note the suffix is "-free", NOT OpenRouter's ":free", so the payload
	// signal never fires and this catalogue entry is the only thing that can
	// badge these models.
	{"opencode", "big-pickle", regimeKeyless, ""},
	{"opencode", "mimo-v2.5-free", regimeKeyless, ""},
	{"opencode", "mimo-v2.6-flash-free", regimeKeyless, ""},
	{"opencode", "ling-3.0-flash-fin-free", regimeKeyless, ""},
	{"opencode", "nemotron-3-ultra-free", regimeKeyless, ""},
	{"opencode", "nemotron-3.5-lightning-free", regimeKeyless, ""},
	{"opencode", "space-bunny-free", regimeKeyless, ""},
	// muse-spark rejects max_output_tokens < 16 with a 400; the model itself is
	// fine with a sane value. See minOutputTokensForProvider.
	{"opencode", "muse-spark-1.3-contributor-free", regimeKeyless, ""},
	{"opencode", "muse-spark-1.2-contributor-free", regimeKeyless, ""},

	// Google AI Studio (api-key free tier).
	{"gemini", "gemini-2.5-flash", regimeRecurringDaily, ""},
	{"gemini", "gemini-2.5-flash-lite", regimeRecurringDaily, ""},
	{"gemini", "gemini-3-flash-preview", regimeRecurringDaily, ""},
	{"gemini", "gemini-3.1-flash-lite", regimeRecurringDaily, ""},

	// Cloudflare Workers AI (free allocation per day).
	{"cloudflare-ai", "@cf/mistral/mistral-7b-instruct-v0.2-lora", regimeRecurringDaily, ""},
	{"cloudflare-ai", "@cf/qwen/qwen2.5-coder-32b-instruct", regimeRecurringDaily, ""},
	{"cloudflare-ai", "@cf/deepseek-ai/deepseek-r1-distill-qwen-32b", regimeRecurringDaily, ""},
	{"cloudflare-ai", "@cf/meta/llama-3.3-70b-instruct-fp8-fast", regimeRecurringDaily, ""},
	{"cloudflare-ai", "@cf/meta/llama-3.2-3b-instruct", regimeRecurringDaily, ""},
	{"cloudflare-ai", "@cf/qwen/qwq-32b", regimeRecurringDaily, ""},
	{"cloudflare-ai", "@cf/zai-org/glm-4.7-flash", regimeRecurringDaily, ""},
	{"cloudflare-ai", "@cf/moonshotai/kimi-k2.6", regimeRecurringDaily, ""},
	{"cloudflare-ai", "@cf/google/gemma-4-26b-a4b-it", regimeRecurringDaily, ""},

	// NVIDIA NIM (developer-program free access).
	{"nvidia", "google/gemma-4-31b-it", regimeRecurringCredit, ""},
	{"nvidia", "nvidia/nemotron-3-super-120b-a12b", regimeRecurringCredit, ""},
	{"nvidia", "openai/gpt-oss-120b", regimeRecurringCredit, ""},

	// Ollama Cloud free tier.
	{"ollama", "deepseek-v4-pro", regimeRecurringDaily, ""},
	{"ollama", "deepseek-v4-flash", regimeRecurringDaily, ""},
	{"ollama", "kimi-k2.6", regimeRecurringDaily, ""},
	{"ollama", "glm-5.1", regimeRecurringDaily, ""},
	{"ollama", "minimax-m2.7", regimeRecurringDaily, ""},
	{"ollama", "gemma4:31b", regimeRecurringDaily, ""},
	{"ollama", "nemotron-3-super", regimeRecurringDaily, ""},
	{"ollama", "qwen3.5:397b", regimeRecurringDaily, ""},

	// OpenRouter free tier (the ":free" models are handled by suffix too).
	{"openrouter", "liquid/lfm-2.5-2.6b:free", regimeRecurringDaily, ""},

	// Nous Research Portal free tier (verified against the live /v1/models feed).
	{"nous", "meituan/longcat-2.5-preview:free", regimeRecurringDaily, ""},
	{"nous", "meituan/longcat-2.0:free", regimeRecurringDaily, ""},
	{"nous", "stepfun/step-5-preview:free", regimeRecurringDaily, ""},
	{"nous", "stepfun/step-3.7-flash:free", regimeRecurringDaily, ""},
	{"nous", "upstage/solar-mini4:free", regimeRecurringDaily, ""},
	{"nous", "poolside/laguna-s-2.1:free", regimeRecurringDaily, ""},
	{"nous", "poolside/laguna-xs-2.1:free", regimeRecurringDaily, ""},

	// Vertex AI ("$300 free credits").
	{"vertex", "gemini-3.1-pro-preview", regimeOneTimeInitial, ""},
	{"vertex", "gemini-3.1-flash-lite", regimeOneTimeInitial, ""},
	{"vertex", "gemini-3-flash-preview", regimeOneTimeInitial, ""},
	{"vertex", "gemma-4-31b-it", regimeOneTimeInitial, ""},
	{"vertex", "DeepSeek-V4-Flash", regimeOneTimeInitial, ""},
	{"vertex", "DeepSeek-V4-Pro", regimeOneTimeInitial, ""},
	{"vertex", "Qwen3.6-35B-A3B", regimeOneTimeInitial, ""},
	{"vertex", "GLM-5.1-FP8", regimeOneTimeInitial, ""},
	{"vertex", "claude-opus-4-7", regimeOneTimeInitial, ""},
	{"vertex", "claude-sonnet-4-6", regimeOneTimeInitial, ""},

	// Qoder (free plan models).
	{"qoder", "qwen3.8-max-preview", regimeRecurringDaily, ""},
	{"qoder", "qwen3.7-max", regimeRecurringDaily, ""},
	{"qoder", "qwen3.7-plus", regimeRecurringDaily, ""},
	{"qoder", "kimi-k3", regimeRecurringDaily, ""},
	{"qoder", "kimi-k2.7-code", regimeRecurringDaily, ""},
	{"qoder", "glm-5.2", regimeRecurringDaily, ""},
	{"qoder", "deepseek-v4-pro", regimeRecurringDaily, ""},
	{"qoder", "deepseek-v4-flash", regimeRecurringDaily, ""},
	{"qoder", "minimax-m3", regimeRecurringDaily, ""},
}

var (
	freeCatalogOnce sync.Once
	// providersWithFreeModels is the set of provider ids that have at least one
	// documented free model.
	providersWithFreeModels map[string]struct{}
	// freeModelIDsByProvider maps a provider id to the set of catalogued free
	// model ids.
	freeModelIDsByProvider map[string]map[string]struct{}
	// keylessModelIDsByProvider is the subset whose regime is `keyless`: models
	// reachable with no credential, which a fresh install can seed.
	keylessModelIDsByProvider map[string]map[string]struct{}
)

func buildFreeCatalog() {
	providersWithFreeModels = make(map[string]struct{})
	freeModelIDsByProvider = make(map[string]map[string]struct{})
	keylessModelIDsByProvider = make(map[string]map[string]struct{})
	for _, entry := range freeModelCatalog {
		if !freeBudgetGrantsAccess(entry.FreeType) {
			continue
		}
		providersWithFreeModels[entry.Provider] = struct{}{}
		ids := freeModelIDsByProvider[entry.Provider]
		if ids == nil {
			ids = make(map[string]struct{})
			freeModelIDsByProvider[entry.Provider] = ids
		}
		ids[entry.ModelID] = struct{}{}
		if entry.FreeType == regimeKeyless {
			keyless := keylessModelIDsByProvider[entry.Provider]
			if keyless == nil {
				keyless = make(map[string]struct{})
				keylessModelIDsByProvider[entry.Provider] = keyless
			}
			keyless[entry.ModelID] = struct{}{}
		}
	}
}

// ProviderHasFreeModels reports whether the provider (id or alias) exposes any
// documented free model.
func ProviderHasFreeModels(providerID string) bool {
	if providerID == "" {
		return false
	}
	freeCatalogOnce.Do(buildFreeCatalog)
	if _, ok := providersWithFreeModels[providerID]; ok {
		return true
	}
	_, ok := providersWithFreeModels[ResolveAlias(providerID)]
	return ok
}

// KeylessModelIDs returns the catalogued model ids for a provider that
// authenticates with no credential at all (regime `keyless`), or nil when the
// provider owns none.
//
// This is the seed for a fresh install: a keyless provider owns no connection
// row and therefore no cached models, so without it OpenCode Free lists zero
// models on a brand-new database and the operator must click Import before the
// provider works — even though nothing about it needs configuring.
func KeylessModelIDs(providerID string) []string {
	if providerID == "" {
		return nil
	}
	freeCatalogOnce.Do(buildFreeCatalog)
	ids := keylessModelIDsByProvider[providerID]
	if ids == nil {
		ids = keylessModelIDsByProvider[ResolveAlias(providerID)]
	}
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// CatalogModelIDs returns every catalogued model id for a provider — the same
// list `KeylessModelIDs` seeds from, but without requiring the keyless regime.
func CatalogModelIDs(providerID string) []string {
	if providerID == "" {
		return nil
	}
	freeCatalogOnce.Do(buildFreeCatalog)
	ids := freeModelIDsByProvider[providerID]
	if ids == nil {
		ids = freeModelIDsByProvider[ResolveAlias(providerID)]
	}
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// isCatalogFreeModel reports whether the model id is listed as free for the
// provider (id or alias) in the shipped catalogue.
func isCatalogFreeModel(providerID, modelID string) bool {
	if modelID == "" {
		return false
	}
	freeCatalogOnce.Do(buildFreeCatalog)
	if _, ok := freeModelIDsByProvider[providerID][modelID]; ok {
		return true
	}
	_, ok := freeModelIDsByProvider[ResolveAlias(providerID)][modelID]
	return ok
}

// FreeModelCandidate is the subset of a model entry the free check reads.
type FreeModelCandidate struct {
	ID          string
	DisplayName string
	IsFree      *bool
	// Free is a provider-supplied `free` field; a truthy-but-not-`true` value
	// (e.g. the string "false") is NOT a signal, matching upstream.
	Free any
	// PromptPrice / CompletionPrice are upstream usage prices, when reported.
	PromptPrice     string
	CompletionPrice string
}

func zeroPrice(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	// Accept "0", "0.0", "0.000000" and friends without pulling in strconv's
	// error path: everything but digits and a dot means "not a number".
	for _, r := range value {
		if r != '.' && (r < '0' || r > '9') {
			return false
		}
	}
	for _, r := range value {
		if r >= '1' && r <= '9' {
			return false
		}
	}
	return true
}

// hasPayloadFreeSignal mirrors upstream `hasPayloadFreeSignal`.
func hasPayloadFreeSignal(model FreeModelCandidate) bool {
	if model.IsFree != nil && *model.IsFree {
		return true
	}
	if strings.HasSuffix(model.ID, ":free") {
		return true
	}
	return zeroPrice(model.PromptPrice) && zeroPrice(model.CompletionPrice)
}

// IsFreeModel reports whether a single model qualifies as free for the given
// provider (id or alias): a catalogue entry, or a payload signal on a provider
// with a documented free tier.
func IsFreeModel(providerID string, model FreeModelCandidate) bool {
	if isCatalogFreeModel(providerID, model.ID) {
		return true
	}
	return ProviderHasFreeModels(providerID) && hasPayloadFreeSignal(model)
}

// IsModelFreeBadge mirrors upstream `isModelFreeBadge` with the default
// (non-strict) rule, which is what the provider page uses: any truthy `free`
// field, a `:free` id suffix, "free"/"grátis" in the display name, or
// `IsFreeModel`.
func IsModelFreeBadge(providerID string, model FreeModelCandidate) bool {
	if truthy(model.Free) {
		return true
	}
	if strings.HasSuffix(model.ID, ":free") {
		return true
	}
	if containsFreeWord(model.DisplayName) {
		return true
	}
	return IsFreeModel(providerID, model)
}

// truthy covers the values a JSON-ish `free` field can carry. Only boolean true
// and the strings "true"/"free" count — upstream explicitly excludes a
// truthy-but-not-`true` value such as the string "false".
func truthy(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		s := strings.ToLower(strings.TrimSpace(v))
		return s == "true" || s == "free"
	default:
		return false
	}
}

// containsFreeWord matches upstream's `/\bgr[aá]tis\b|\bfree\b/i`.
func containsFreeWord(name string) bool {
	if name == "" {
		return false
	}
	lower := strings.ToLower(name)
	for _, needle := range []string{"free", "grátis", "gratis"} {
		idx := strings.Index(lower, needle)
		for idx >= 0 {
			before := byte(' ')
			if idx > 0 {
				before = lower[idx-1]
			}
			after := byte(' ')
			if idx+len(needle) < len(lower) {
				after = lower[idx+len(needle)]
			}
			if !isWordByte(before) && !isWordByte(after) {
				return true
			}
			next := strings.Index(lower[idx+1:], needle)
			if next < 0 {
				break
			}
			idx = idx + 1 + next
		}
	}
	return false
}

func isWordByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b >= 0x80
}
