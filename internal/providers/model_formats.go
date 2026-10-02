package providers

import "strings"

// Wire formats a provider endpoint speaks. Upstream open-sse calls these
// FORMATS entries in the same places (open-sse/translator/formats.js), and
// opencode-zen declares one transport per format
// (open-sse/providers/registry/opencode-zen.js transports[]).
const (
	FormatOpenAI         = "openai"
	FormatClaude         = "claude"
	FormatOpenAIResponses = "openai-responses"
)

// ProviderModelFormats is the per-model wire-format metadata upstream carries on
// registry model entries as targetFormat / supportedFormats. Only providers
// with more than one transport declare it; everything else routes on the
// transport its base URL already names.
var ProviderModelFormats = map[string]map[string]ModelFormats{
	"muse":        museModelFormats,
	"ocz":         openCodeZenModelFormats,
	"opencode-zen": openCodeZenModelFormats,
}

// museModelFormats mirrors open-sse/providers/registry/muse.js models[]: Meta
// accepts Chat Completions and Responses on the same key, but Muse Spark
// reasoning only round-trips on /v1/responses, so every Muse Spark model pins
// the Responses lane for both its target and its only supported format.
var museModelFormats = map[string]ModelFormats{
	"muse-spark-1.3":           {TargetFormat: FormatOpenAIResponses, SupportedFormats: []string{FormatOpenAIResponses}},
	"muse-spark-1.2":           {TargetFormat: FormatOpenAIResponses, SupportedFormats: []string{FormatOpenAIResponses}},
	"muse-spark-1.1":           {TargetFormat: FormatOpenAIResponses, SupportedFormats: []string{FormatOpenAIResponses}},
	"muse-spark-1.3-contributor": {TargetFormat: FormatOpenAIResponses, SupportedFormats: []string{FormatOpenAIResponses}},
	"muse-spark-1.2-contributor": {TargetFormat: FormatOpenAIResponses, SupportedFormats: []string{FormatOpenAIResponses}},
}

// ModelFormats is one registry model's declared format contract.
type ModelFormats struct {
	// TargetFormat is where the request is translated to when the client
	// format has no matching transport ("openai-responses" | "claude").
	TargetFormat string
	// SupportedFormats lists the wire formats the upstream endpoint serves
	// directly. A client speaking one of them is forwarded without
	// translation; any other client is translated to TargetFormat.
	SupportedFormats []string
}

func openCodeZenFormats(target string, supported ...string) ModelFormats {
	return ModelFormats{TargetFormat: target, SupportedFormats: supported}
}

// openCodeZenModelFormats mirrors open-sse/providers/registry/opencode-zen.js
// models[]. Claude and Qwen live on /zen/v1/messages, the GPT/Grok/Muse Spark
// family on /zen/v1/responses, and the rest on /zen/v1/chat/completions.
var openCodeZenModelFormats = map[string]ModelFormats{
	// Claude (messages)
	"claude-fable-5":   openCodeZenFormats(FormatClaude, FormatClaude),
	"claude-fable-5-1": openCodeZenFormats(FormatClaude, FormatClaude),
	"claude-opus-5":    openCodeZenFormats(FormatClaude, FormatClaude),
	"claude-opus-4-8":  openCodeZenFormats(FormatClaude, FormatClaude),
	"claude-opus-4-7":  openCodeZenFormats(FormatClaude, FormatClaude),
	"claude-opus-4-6":  openCodeZenFormats(FormatClaude, FormatClaude),
	"claude-opus-4-5":  openCodeZenFormats(FormatClaude, FormatClaude),
	"claude-sonnet-5":  openCodeZenFormats(FormatClaude, FormatClaude),
	"claude-sonnet-4-6": openCodeZenFormats(FormatClaude, FormatClaude),
	"claude-sonnet-4-5": openCodeZenFormats(FormatClaude, FormatClaude),
	"claude-sonnet-4":   openCodeZenFormats(FormatClaude, FormatClaude),
	"claude-haiku-4-5":  openCodeZenFormats(FormatClaude, FormatClaude),
	// Gemini (own path, via chat completions transport)
	"gemini-3.6-flash":      openCodeZenFormats(FormatOpenAI, FormatOpenAI),
	"gemini-3.8-flash":      openCodeZenFormats(FormatOpenAI, FormatOpenAI),
	"gemini-3.7-flash":      openCodeZenFormats(FormatOpenAI, FormatOpenAI),
	"gemini-3.5-flash-lite": openCodeZenFormats(FormatOpenAI, FormatOpenAI),
	"gemini-3.5-flash":      openCodeZenFormats(FormatOpenAI, FormatOpenAI),
	"gemini-3.1-pro":        openCodeZenFormats(FormatOpenAI, FormatOpenAI),
	"gemini-3-flash":        openCodeZenFormats(FormatOpenAI, FormatOpenAI),
	// GPT / Grok / Muse Spark paid (responses)
	"gpt-6-astra":       openCodeZenResponsesFormat,
	"gpt-5.6-sol":       openCodeZenResponsesFormat,
	"gpt-5.6-terra":     openCodeZenResponsesFormat,
	"gpt-5.6-luna":      openCodeZenResponsesFormat,
	"gpt-5.5":           openCodeZenResponsesFormat,
	"gpt-5.5-pro":       openCodeZenResponsesFormat,
	"gpt-5.4":           openCodeZenResponsesFormat,
	"gpt-5.4-pro":       openCodeZenResponsesFormat,
	"gpt-5.4-mini":      openCodeZenResponsesFormat,
	"gpt-5.4-nano":      openCodeZenResponsesFormat,
	"gpt-5.3-codex-spark": openCodeZenResponsesFormat,
	"gpt-5.3-codex":     openCodeZenResponsesFormat,
	"gpt-5.2":           openCodeZenResponsesFormat,
	"gpt-5.2-codex":     openCodeZenResponsesFormat,
	"gpt-5.1":           openCodeZenResponsesFormat,
	"gpt-5.1-codex-max": openCodeZenResponsesFormat,
	"gpt-5.1-codex":     openCodeZenResponsesFormat,
	"gpt-5.1-codex-mini": openCodeZenResponsesFormat,
	"gpt-5":             openCodeZenResponsesFormat,
	"gpt-5-codex":       openCodeZenResponsesFormat,
	"gpt-5-nano":        openCodeZenResponsesFormat,
	"grok-build-0.1":    openCodeZenResponsesFormat,
	"grok-4.6":          openCodeZenResponsesFormat,
	"grok-4.5":          openCodeZenResponsesFormat,
	"muse-spark-1.3":    openCodeZenResponsesFormat,
	"muse-spark-1.2":    openCodeZenResponsesFormat,
	// Qwen paid (messages)
	"qwen3.6-plus": openCodeZenFormats(FormatClaude, FormatClaude),
	"qwen3.5-plus": openCodeZenFormats(FormatClaude, FormatClaude),
	// DeepSeek / GLM / MiniMax / Kimi / Big Pickle (chat completions)
	"deepseek-v4-pro":            openCodeZenFormats(FormatOpenAI, FormatOpenAI),
	"deepseek-v4-flash":          openCodeZenFormats(FormatOpenAI, FormatOpenAI),
	"deepseek-v4-flash-vision-exp": openCodeZenFormats(FormatOpenAI, FormatOpenAI),
	"glm-5.3-flash":              openCodeZenFormats(FormatOpenAI, FormatOpenAI),
	"glm-5.3":                    openCodeZenFormats(FormatOpenAI, FormatOpenAI),
	"glm-5.2":                    openCodeZenFormats(FormatOpenAI, FormatOpenAI),
	"glm-5.1":                    openCodeZenFormats(FormatOpenAI, FormatOpenAI),
	"glm-5":                      openCodeZenFormats(FormatOpenAI, FormatOpenAI),
	"minimax-m3":                 openCodeZenFormats(FormatOpenAI, FormatOpenAI),
	"minimax-m2.7":               openCodeZenFormats(FormatOpenAI, FormatOpenAI),
	"minimax-m2.5":               openCodeZenFormats(FormatOpenAI, FormatOpenAI),
	"kimi-k3":                    openCodeZenFormats(FormatOpenAI, FormatOpenAI),
	"kimi-k2.7-code":             openCodeZenFormats(FormatOpenAI, FormatOpenAI),
	"kimi-k2.6":                  openCodeZenFormats(FormatOpenAI, FormatOpenAI),
	"kimi-k2.5":                  openCodeZenFormats(FormatOpenAI, FormatOpenAI),
	"big-pickle":                 openCodeZenFormats(FormatOpenAI, FormatOpenAI),
	"union-alpha":                openCodeZenFormats(FormatClaude, FormatClaude),
	// Free tier on the keyed lane (chat completions)
	"deepseek-v4-flash-free":        openCodeZenFormats(FormatOpenAI, FormatOpenAI),
	"mimo-v2.6-flash-free":          openCodeZenFormats(FormatOpenAI, FormatOpenAI),
	"mimo-v2.5-free":                openCodeZenFormats(FormatOpenAI, FormatOpenAI),
	"ling-3.0-flash-fin-free":       openCodeZenFormats(FormatOpenAI, FormatOpenAI),
	"nemotron-3-ultra-free":         openCodeZenFormats(FormatOpenAI, FormatOpenAI),
	"nemotron-3.5-lightning-free":   openCodeZenFormats(FormatOpenAI, FormatOpenAI),
	// Free tier on the keyed lane (responses)
	"muse-spark-1.3-contributor-free": openCodeZenResponsesFormat,
	"muse-spark-1.2-contributor-free": openCodeZenResponsesFormat,
}

var openCodeZenResponsesFormat = openCodeZenFormats(FormatOpenAIResponses, FormatOpenAIResponses)

// openCodeFamilyFormats is the unknown-id fallback for the OpenCode endpoint
// families (open-sse/providers/models/helpers.js OPENCODE_FAMILIES). Ids that
// arrive through modelsFetcher or passthroughModels are not in the curated
// table, and guessing the wrong lane sends them to an endpoint that 404s.
var openCodeFamilyFormats = []struct {
	prefix  string
	matches ModelFormats
}{
	{"grok", openCodeZenResponsesFormat},
	{"gpt", openCodeZenResponsesFormat},
	{"muse-spark", openCodeZenResponsesFormat},
	{"muse_spark", openCodeZenResponsesFormat},
	{"deepseek-v4-pro", openCodeZenFormats(FormatOpenAI, FormatOpenAI, FormatClaude, FormatOpenAIResponses)},
	{"deepseek-v4-flash", openCodeZenFormats(FormatOpenAI, FormatOpenAI, FormatClaude, FormatOpenAIResponses)},
	{"minimax", openCodeZenFormats(FormatOpenAI, FormatOpenAI, FormatClaude)},
	{"qwen", openCodeZenFormats(FormatOpenAI, FormatOpenAI, FormatClaude)},
	{"claude-", openCodeZenFormats(FormatClaude, FormatClaude)},
}

// isOpenCodeProvider reports whether a provider id/alias is one of the OpenCode
// endpoints, which are the only ones with the family fallback above
// (open-sse/config/providerModels.js isOpenCodeAlias).
func isOpenCodeProvider(aliasOrID string) bool {
	switch aliasOrID {
	case "oc", "opencode", "ocg", "opencode-go", "ocz", "opencode-zen":
		return true
	}
	return false
}

// baseModelID strips the 9router thinking suffix "model(level)" so catalog
// lookups hit the base id (open-sse config/providerModels.js findModel).
func baseModelID(modelID string) string {
	if idx := strings.LastIndexByte(modelID, '('); idx > 0 && strings.HasSuffix(modelID, ")") {
		modelID = modelID[:idx]
	}
	return strings.TrimSpace(modelID)
}

// GetModelFormats returns the declared format contract for a model, and
// whether the model has one at all. ok=false means "no declaration": callers
// keep the provider's default transport, which is what upstream does for
// undeclared ids outside the OpenCode family fallback.
func GetModelFormats(aliasOrID, modelID string) (ModelFormats, bool) {
	base := baseModelID(modelID)
	if byModel, ok := ProviderModelFormats[aliasOrID]; ok {
		if mf, ok := byModel[base]; ok {
			return mf, true
		}
	}
	if canon := ResolveAlias(aliasOrID); canon != "" && canon != aliasOrID {
		if byModel, ok := ProviderModelFormats[canon]; ok {
			if mf, ok := byModel[base]; ok {
				return mf, true
			}
		}
	}
	if isOpenCodeProvider(aliasOrID) {
		if mf := openCodeFamilyFormatsFor(base); mf != nil {
			return *mf, true
		}
		// Upstream defaults an unknown OpenCode id to the chat lane so an
		// auto-fetched model never wrongly takes the source-format transport.
		return openCodeZenFormats(FormatOpenAI, FormatOpenAI), true
	}
	return ModelFormats{}, false
}

func openCodeFamilyFormatsFor(base string) *ModelFormats {
	lower := strings.ToLower(base)
	for _, family := range openCodeFamilyFormats {
		if strings.HasPrefix(lower, family.prefix) {
			mf := family.matches
			return &mf
		}
	}
	return nil
}

// SupportsFormat reports whether the model serves a client format directly.
func (m ModelFormats) SupportsFormat(format string) bool {
	for _, f := range m.SupportedFormats {
		if f == format {
			return true
		}
	}
	return false
}
