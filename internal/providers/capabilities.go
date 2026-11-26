package providers

import (
	"path"
	"slices"
	"strings"
	"sync"
	"unique"
)

var (
	capsCacheMu sync.RWMutex
	capsCache   = make(map[unique.Handle[string]]Capabilities)
)

var (
	customCapsMu sync.RWMutex
	customCaps   = map[string]Capabilities{}
)

// InvalidateCapabilitiesCache resets the cached model capabilities.
func InvalidateCapabilitiesCache() {
	capsCacheMu.Lock()
	capsCache = make(map[unique.Handle[string]]Capabilities)
	capsCacheMu.Unlock()
	tokenLimitsCacheMu.Lock()
	tokenLimitsCache = make(map[unique.Handle[string]][2]int)
	tokenLimitsCacheMu.Unlock()
}

// SetCustomModelCaps registers caps for a custom model (provider/model).
//
// A model id the provider's static registry catalog already owns is refused
// here: the catalog is what the gateway advertises for a known model (context
// window, max output, modality flags), and a custom row saved under the same id
// must not replace those numbers. The refusal lives at this one writer so that
// every publisher obeys it — GET /v1/models and /api/models/caps both call this
// — instead of each route remembering to filter its own input first. The map is
// process-wide, so publishing on the wrong route used to change every later
// capability read for the lifetime of the process.
func SetCustomModelCaps(provider, model string, caps Capabilities) {
	if staticCatalogOwnsModel(provider, model) {
		return
	}
	key := provider + "||" + model
	customCapsMu.Lock()
	customCaps[key] = caps
	// also store base model variant
	base := model
	if _, after, ok := strings.CutLast(model, "/"); ok {
		base = after
	}
	if base != model {
		customCaps[provider+"||"+base] = caps
	}
	customCapsMu.Unlock()
	// Invalidate cache so new caps are picked up
	InvalidateCapabilitiesCache()
}

// staticCatalogOwnsModel reports whether the static registry catalog for
// provider already lists model. GetProviderModels resolves provider aliases
// itself ("ocz" finds the opencode-zen catalog), and a provider-qualified id
// ("ocz/mimo-v2.6-flash-free") is matched by its bare id as well, mirroring the
// two keys SetCustomModelCaps writes. A provider node prefix has no static
// catalog, so nothing published under one is refused.
func staticCatalogOwnsModel(provider, model string) bool {
	ids := GetProviderModels(provider)
	if len(ids) == 0 {
		return false
	}
	if _, base, ok := strings.CutLast(model, "/"); ok {
		return slices.Contains(ids, model) || slices.Contains(ids, base)
	}
	return slices.Contains(ids, model)
}

// GetCustomModelCaps returns custom caps if present.
func GetCustomModelCaps(provider, model string) (Capabilities, bool) {
	key := provider + "||" + model
	customCapsMu.RLock()
	caps, ok := customCaps[key]
	customCapsMu.RUnlock()
	if ok {
		return caps, true
	}
	// try base model
	if _, after, ok := strings.CutLast(model, "/"); ok {
		base := after
		customCapsMu.RLock()
		caps, ok = customCaps[provider+"||"+base]
		customCapsMu.RUnlock()
		return caps, ok
	}
	return Capabilities{}, false
}

// ClearCustomModelCaps clears all custom caps (for tests).
func ClearCustomModelCaps() {
	customCapsMu.Lock()
	customCaps = map[string]Capabilities{}
	customCapsMu.Unlock()
	InvalidateCapabilitiesCache()
}

// jailbreakNameMarkers are the tokens a model id carries when the model is
// sold as uncensored. They are matched as a hyphen/underscore-delimited
// segment, not a substring: "unrestricted" inside "unrestrictedly-fast" is a
// false positive, and a bare "jail" is not a marker at all.
var jailbreakNameMarkers = []string{
	"uncensored",
	"unrestricted",
	"unfiltered",
	"jailbreak",
	"jailbroken",
	"nsfw",
	"nofilter",
}

// jailbreakNamePairs are two-token markers: the id spells them as two
// hyphen-delimited segments ("no-filter"), so a single-segment comparison
// never sees them. Both segments must be adjacent and in order.
var jailbreakNamePairs = [][2]string{
	{"no", "filter"},
	{"no", "moderation"},
	{"no", "limits"},
}

// IsJailbreakModel reports whether a model id names itself uncensored.
//
// This is the fallback half of the Jailbreak capability: an operator who never
// opened the Add Custom Model dialog still gets the chip lit on a model whose
// id already says what it is. The explicit flag wins either way — the caller
// ORs the two — so a model marked by hand is never un-marked by a name that
// happens to contain none of these tokens.
//
// A thinking suffix ("(high)") is stripped first, because a level marker is
// not a name segment and "claude-x(high)" must not be read as a match.
func IsJailbreakModel(model string) bool {
	clean := model
	if paren := strings.IndexByte(clean, '('); paren != -1 {
		clean = strings.TrimSpace(clean[:paren])
	}
	// The bare id is what a marker would be attached to: "vendor/model-jailbreak"
	// carries it after the slash, and the vendor half is not part of the name.
	if _, after, ok := strings.CutLast(clean, "/"); ok {
		clean = after
	}
	lower := strings.ToLower(clean)
	segments := strings.FieldsFunc(lower, func(r rune) bool {
		return r == '-' || r == '_' || r == '.' || r == ' '
	})
	for _, seg := range segments {
		for _, marker := range jailbreakNameMarkers {
			if seg == marker {
				return true
			}
		}
	}
	for _, pair := range jailbreakNamePairs {
		for i := 0; i+1 < len(segments); i++ {
			if segments[i] == pair[0] && segments[i+1] == pair[1] {
				return true
			}
		}
	}
	return false
}

// Capabilities represents what a model can do beyond plain text, plus the shape
// of its thinking control on the wire. The thinking fields are only meaningful
// when Reasoning is true; an empty ThinkingFormat means "derive from transport".
type Capabilities struct {
	Vision      bool
	PDF         bool
	AudioInput  bool
	VideoInput  bool
	ImageOutput bool
	AudioOutput bool
	Search      bool
	Tools       bool
	Reasoning   bool
	// Jailbreak reports that the model is uncensored / unrestricted — it
	// accepts prompts a policy-filtered model refuses. It is not a modality
	// and not a wire feature: nothing in the request path reads it, and no
	// upstream table declares it. It exists so an operator can mark the
	// models they know behave this way and filter the dashboard list on it,
	// instead of recognising them by name. The name heuristic below is the
	// fallback for a model whose id already says so.
	Jailbreak bool
	// ThinkingFormat is the wire shape of the thinking control
	// (openai, claude-adaptive, claude-budget, gemini-level, gemini-budget,
	// zai, qwen, kimi, deepseek, commandcode, minimax, hunyuan, step).
	ThinkingFormat string
	// ThinkingCanDisable reports whether thinking can be turned off. It is a
	// pointer because a plain bool cannot say "not specified": upstream's
	// default is true, and a table entry that names a thinking format without
	// this flag must still resolve to true. See CanDisableThinking.
	ThinkingCanDisable *bool
	// ThinkingOffType declares how the model wants a "thinking off" request expressed
	// (e.g. "between_tools" for Claude Sonnet 5.5, which takes thinkingOffType in place
	// of "disabled"). Empty means "use the standard disabled shape".
	ThinkingOffType string
	// ThinkingRange is the {min, max} budget clamp for budget-based formats.
	ThinkingRange *ThinkingRange
	// ThinkingEffortSupported reports that the model accepts a reasoning_effort
	// level on the wire.
	ThinkingEffortSupported bool
	// ForcedToolChoice reports whether the model rejects forced tool_choice pinning
	// (tool_choice:"any"/"required" or a named tool). false means the model 400s on
	// such choices. Port of upstream capabilities.js forcedToolChoice:false.
	ForcedToolChoice *bool
	// ContextWindow and MaxOutput are the token limits declared by the provider
	// entry. Zero means "unknown, fall back to the model token-limit table".
	ContextWindow int
	MaxOutput     int
}

// ThinkingRange is the inclusive thinking-token budget window a model accepts.
// It is only set for budget-based formats (claude-budget, gemini-budget).
type ThinkingRange struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

// CanDisableThinking resolves the tri-state ThinkingCanDisable flag: an unset
// entry means "not specified", which is upstream's default — thinking can be
// switched off.
func (c Capabilities) CanDisableThinking() bool {
	return c.ThinkingCanDisable == nil || *c.ThinkingCanDisable
}

// CanForceToolChoice resolves the tri-state ForcedToolChoice flag: an unset
// entry means "not specified", which is upstream's default — forced tool_choice
// is accepted. false means the model 400s on tool_choice "any"/"required" or a
// named tool.
func (c Capabilities) CanForceToolChoice() bool {
	return c.ForcedToolChoice == nil || *c.ForcedToolChoice
}

var DefaultCapabilities = Capabilities{
	Vision:             false,
	PDF:                false,
	AudioInput:         false,
	VideoInput:         false,
	ImageOutput:        false,
	AudioOutput:        false,
	Search:             false,
	Tools:              true,
	Reasoning:          false,
	ThinkingFormat:     "",
	ThinkingCanDisable: nil,
	ThinkingRange:      nil,
}

var modelCapabilities = map[string]Capabilities{
	"claude-opus-5":                  {Vision: true, PDF: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ContextWindow: 1000000, MaxOutput: 128000},
	"claude-opus-5-thinking":         {Vision: true, PDF: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ContextWindow: 1000000, MaxOutput: 128000},
	"claude-opus-5-agentic":          {Vision: true, PDF: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ContextWindow: 1000000, MaxOutput: 128000},
	"claude-opus-5-thinking-agentic": {Vision: true, PDF: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ContextWindow: 1000000, MaxOutput: 128000},
	// Opus 5.5 — experimental preview on Kiro (1M context, 2x credits). Dotted ids
	// (GitHub Copilot / Kiro / Token Harbor). GitHub Copilot applies the same
	// Anthropic per-model thinking rules as the dashed ids: it rejects
	// thinking.type "disabled" (400 "thinking.type.disabled is not supported")
	// and forced tool_choice (400). thinkingCanDisable:false is set here and
	// forcedToolChoice:false is read by the Claude request builder to downgrade a
	// forced tool_choice to auto. Port of upstream decolua/9router commit f3e1f716.
	"claude-opus-5.5":                  {Vision: true, PDF: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ContextWindow: 1000000, MaxOutput: 128000, ThinkingCanDisable: new(false), ForcedToolChoice: new(false)},
	"claude-opus-5.5-thinking":         {Vision: true, PDF: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ContextWindow: 1000000, MaxOutput: 128000, ThinkingCanDisable: new(false), ForcedToolChoice: new(false)},
	"claude-opus-5.5-agentic":          {Vision: true, PDF: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ContextWindow: 1000000, MaxOutput: 128000, ThinkingCanDisable: new(false), ForcedToolChoice: new(false)},
	"claude-opus-5.5-thinking-agentic": {Vision: true, PDF: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ContextWindow: 1000000, MaxOutput: 128000, ThinkingCanDisable: new(false), ForcedToolChoice: new(false)},
	// Haiku 5.5 is adaptive-only like the rest of the 5.x line (upstream
	// MODEL_CAPABILITIES): the *claude*haiku* budget pattern would 400 on
	// thinking.type.enabled.
	"claude-haiku-5-5": {Vision: true, PDF: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ContextWindow: 1000000, MaxOutput: 128000},
	// Sonnet 5.5 dotted id (GitHub Copilot / Kiro / Token Harbor), same model as the
	// dashed rows below. GitHub Copilot applies Anthropic's per-model thinking rules:
	// Sonnet 5.5 takes thinkingOffType "between_tools" in place of "disabled" and
	// rejects forced tool_choice (any/tool). The Claude request builder reads
	// thinkingOffType to rewrite a disabled thinking block, and forcedToolChoice:false
	// to downgrade a forced tool_choice to auto. Port of upstream decolua/9router
	// commit f3e1f716.
	"claude-sonnet-5.5": {Vision: true, PDF: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ContextWindow: 1000000, MaxOutput: 128000, ThinkingOffType: "between_tools", ForcedToolChoice: new(false)},

	// Opus 5.5 on Antigravity (upstream v0.5.99, a07ed95b): all five
	// effort spellings, 1M window / 128K output. thinkingCanDisable:false is
	// representable here — without it CanDisableThinking defaults to true and
	// the translator drops thinking on any turn that asked for no reasoning,
	// which Anthropic answers with a 400 on the thinking.type field.
	//
	// DEFERRED, not forgotten: upstream's forcedToolChoice:false has no
	// Capabilities field and nothing on the request path reads it, so it
	// cannot be expressed. It marks these models as rejecting tool_choice
	// "any"/forced pinning — a request that forces a tool choice on them is
	// answered with a 400 from the upstream provider. Adding the field means
	// adding it to the Anthropic request builder too; until then these entries
	// are knowingly incomplete on that one flag.
	"claude-opus-5-5":          {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ThinkingCanDisable: new(false), ContextWindow: 1000000, MaxOutput: 128000},
	"claude-opus-5-5-max":      {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ThinkingCanDisable: new(false), ContextWindow: 1000000, MaxOutput: 128000},
	"claude-opus-5-5-thinking": {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ThinkingCanDisable: new(false), ContextWindow: 1000000, MaxOutput: 128000},
	"claude-opus-5-5-high":     {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ThinkingCanDisable: new(false), ContextWindow: 1000000, MaxOutput: 128000},
	"claude-opus-5-5-medium":   {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ThinkingCanDisable: new(false), ContextWindow: 1000000, MaxOutput: 128000},
	"claude-opus-5-5-low":      {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ThinkingCanDisable: new(false), ContextWindow: 1000000, MaxOutput: 128000},
	"claude-opus-4.6":          {Vision: true, PDF: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ContextWindow: 1000000, MaxOutput: 128000},
	"claude-opus-4.7":          {Vision: true, PDF: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ContextWindow: 1000000, MaxOutput: 128000},
	"claude-opus-4-7":          {Vision: true, PDF: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ContextWindow: 1000000, MaxOutput: 128000},
	"claude-opus-4.8":          {Vision: true, PDF: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ContextWindow: 1000000, MaxOutput: 128000},
	"claude-opus-4-6":          {Vision: true, PDF: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ContextWindow: 1000000, MaxOutput: 128000},
	"claude-opus-4-8":          {Vision: true, PDF: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ContextWindow: 1000000, MaxOutput: 128000},
	"claude-opus-4.8-thinking": {Vision: true, PDF: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ContextWindow: 1000000, MaxOutput: 128000},
	"claude-opus-4-8-thinking": {Vision: true, PDF: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ContextWindow: 1000000, MaxOutput: 128000},
	"claude-sonnet-4.6":        {Vision: true, PDF: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ContextWindow: 1000000, MaxOutput: 128000},
	"claude-sonnet-4-6":        {Vision: true, PDF: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ContextWindow: 1000000, MaxOutput: 128000},
	// Sonnet 5.5 on Antigravity (upstream v0.5.99, a07ed95b): the bare id
	// already had a row, but only the base spelling — the four effort
	// variants fell through to the *claude*sonnet* pattern and lost the 1M
	// window. Extended in place rather than duplicated.
	//
	// DEFERRED, not forgotten: upstream sets thinkingOffType:"between_tools"
	// and forcedToolChoice:false on these five ids, and Capabilities has no
	// field for either — nothing on the request path reads them. Lost means:
	//   - thinkingOffType — these models only end a thinking block between
	//     tool calls; a turn that closes thinking at the end of the turn gets
	//     a 400 from Anthropic on the thinking block shape.
	//   - forcedToolChoice:false — the models reject forced tool_choice
	//     pinning ("any"/a named tool); a pinned choice gets a 400 from the
	//     upstream provider.
	// Adding either field means teaching the Anthropic request builder to
	// emit them, so these entries are knowingly incomplete on those flags.
	"claude-sonnet-5-5":                {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ContextWindow: 1000000, MaxOutput: 128000},
	"claude-sonnet-5-5-thinking":       {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ContextWindow: 1000000, MaxOutput: 128000},
	"claude-sonnet-5-5-high":           {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ContextWindow: 1000000, MaxOutput: 128000},
	"claude-sonnet-5-5-medium":         {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ContextWindow: 1000000, MaxOutput: 128000},
	"claude-sonnet-5-5-low":            {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ContextWindow: 1000000, MaxOutput: 128000},
	"claude-sonnet-5-5-max":            {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ContextWindow: 1000000, MaxOutput: 128000},
	"gemini-4-neon":                    {Vision: true, PDF: true, AudioInput: true, VideoInput: true, Reasoning: true, Search: true, Tools: true, ContextWindow: 1000000, MaxOutput: 65536},
	"gemini-4-neon-high":               {Vision: true, PDF: true, AudioInput: true, VideoInput: true, Reasoning: true, Search: true, Tools: true, ContextWindow: 1000000, MaxOutput: 65536},
	"gemini-4-neon-xhigh":              {Vision: true, PDF: true, AudioInput: true, VideoInput: true, Reasoning: true, Search: true, Tools: true, ContextWindow: 1000000, MaxOutput: 65536},
	"gemini-4-neon-medium":             {Vision: true, PDF: true, AudioInput: true, VideoInput: true, Reasoning: true, Search: true, Tools: true, ContextWindow: 1000000, MaxOutput: 65536},
	"gemini-4-neon-low":                {Vision: true, PDF: true, AudioInput: true, VideoInput: true, Reasoning: true, Search: true, Tools: true, ContextWindow: 1000000, MaxOutput: 65536},
	"claude-sonnet-5":                  {Vision: true, PDF: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ContextWindow: 1000000, MaxOutput: 128000},
	"claude-sonnet-5-thinking":         {Vision: true, PDF: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ContextWindow: 1000000, MaxOutput: 128000},
	"claude-sonnet-5-agentic":          {Vision: true, PDF: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ContextWindow: 1000000, MaxOutput: 128000},
	"claude-sonnet-5-thinking-agentic": {Vision: true, PDF: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ContextWindow: 1000000, MaxOutput: 128000},
	"gpt-image-1":                      {ImageOutput: true},
	// Agnes AI (apihub.agnes-ai.com). Both ids take text AND image URLs, so
	// without these rows every Agnes request resolved through
	// DefaultCapabilities (vision:false) and the image blocks were stripped
	// before dispatch, with no error. Limits are the vendor's per-model doc
	// pages (agnes-30-pro / agnes-30-flash).
	//
	// The 2.5 line gets no row on purpose: no vendor figures are published for
	// it, so those ids keep the default instead of inheriting 3.0's numbers,
	// and no `agnes*` glob is added — a pattern would also capture any future
	// id this provider ships (see the pattern-ownership rule).
	"agnes-3.0-pro":   {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 512000, MaxOutput: 65536},
	"agnes-3.0-flash": {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 512000, MaxOutput: 65536},
	// GLM-5.3-FLASH is the multimodal z.ai line and it carries the full 1M
	// window. Upstream decolua/9router#4656: z.ai's docs state that "GLM-5.3
	// and GLM-5.3-FLASH no longer support disabling thinking (an error will
	// occur if the thinking.type parameter is set to disabled)". Left unset,
	// CanDisableThinking reports true and the translator emits
	// enable_thinking:false on any turn that asked for no reasoning — z.ai
	// answers 400 code 1210 "Invalid API parameter". It looked intermittent
	// because only some turns ask.

	"glm-5.3-flash": {Vision: true, VideoInput: true, PDF: true, Reasoning: true, Tools: true, ThinkingFormat: "zai", ThinkingCanDisable: new(false), ContextWindow: 1000000, MaxOutput: 131072},
	// claude-fable-5 (no -1 suffix) is adaptive-only like fable-5-1; upstream
	// capabilities.js only matches it through *claude*fable* -> claude-budget,
	// which produced thinking{type:"enabled"} and a 400 on the real API.
	"claude-fable-5":   {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ContextWindow: 1000000, MaxOutput: 128000},
	"claude-fable-5-1": {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ThinkingCanDisable: new(false), ContextWindow: 1000000, MaxOutput: 128000},
	"claude-fable-5-2": {Vision: true, PDF: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ThinkingCanDisable: new(false), ContextWindow: 1000000, MaxOutput: 128000},
	"glm-5.2":          {Reasoning: true, Tools: true, ThinkingFormat: "zai", ThinkingEffortSupported: true, ThinkingCanDisable: new(false), ContextWindow: 1000000, MaxOutput: 131072},
	// Upstream b6a9ca43: the exact glm-5.2 entry shadows the *glm-5.2*
	// pattern completely, so it must repeat the pattern's
	// ThinkingEffortSupported or reasoning_effort is dropped for GLM-5.2
	// (supported from 5.2 onward).
	// OpenCode serves the same weights under this free id from its own
	// inference service, which validates the body strictly: the zai thinking
	// object the *glm-5.3* pattern emits answers 400 unknown field
	// "thinking" while reasoning_effort is accepted (upstream 32c5f30a,
	// probed live on /inference/v1 and /zen/v1). Global like upstream: no
	// other provider serves this id.
	"glm-5.3-flash-free":           {Vision: true, VideoInput: true, PDF: true, Reasoning: true, ThinkingFormat: "openai", ThinkingCanDisable: new(false), ContextWindow: 1000000, MaxOutput: 128000},
	"glm-4.6v":                     {Vision: true, VideoInput: true, Reasoning: true, Tools: true, ThinkingFormat: "zai", ContextWindow: 128000, MaxOutput: 32768},
	"glm-4.5v":                     {Vision: true, VideoInput: true, Reasoning: true, Tools: true, ThinkingFormat: "zai", ContextWindow: 64000, MaxOutput: 16384},
	"deepseek-v4-flash-vision-exp": {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "deepseek", ContextWindow: 1000000, MaxOutput: 384000},
	"sensenova-6.8-flash":          {Vision: true, Reasoning: true, ThinkingFormat: "openai", ContextWindow: 1000000, MaxOutput: 131072},
	"sensenova-6.8-flash-lite":     {Vision: true, Reasoning: true, ThinkingFormat: "openai", ContextWindow: 1000000, MaxOutput: 131072},
	"deepseek-v4-vision":           {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "deepseek", ThinkingEffortSupported: true},
	"deepseek-v4.1-flash":          {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "deepseek", ContextWindow: 1000000, MaxOutput: 384000},
	// Some gateways publish this model under the hyphenated id (dash instead
	// of dot); without the row it fell through to the text-only `*deepseek-v4*`
	// pattern and lost its vision and 1M window.
	"deepseek-v4-1-flash": {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "deepseek", ContextWindow: 1000000, MaxOutput: 384000},
	"deepseek-flash":      {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "deepseek", ContextWindow: 128000, MaxOutput: 64000},
	// Muse Spark contributor ids carry the same 1M window as their -free
	// siblings (models.dev meta/muse-spark-1.x-contributor); without it the
	// muse lane fell to the 128k floor.
	"muse-spark-1.2-contributor":      {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(false), ContextWindow: 1048576, MaxOutput: 131072},
	"muse-spark-1.3-contributor":      {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(false), ContextWindow: 1048576, MaxOutput: 131072},
	"muse-spark-1.2-contributor-free": {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(false), ContextWindow: 1048576, MaxOutput: 131072},
	"muse-spark-1.3-contributor-free": {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(false), ContextWindow: 1048576, MaxOutput: 131072},
	// OpenCode Free Union Alpha — multimodal, 262K context, 131K output.
	"union-alpha": {Vision: true, Tools: true, ContextWindow: 262144, MaxOutput: 131072},
	// OpenCode Free ids newer than the tables above (upstream #2690): without
	// a row they fall through to text-only defaults and the client's
	// reasoning_effort is stripped before it reaches the upstream. Modality
	// and limit values are taken verbatim from upstream (models.dev).
	"space-bunny-free":            {Vision: true, VideoInput: true, Reasoning: true, ThinkingFormat: "openai", ContextWindow: 1048576, MaxOutput: 524288},
	"space-bunny":                 {Vision: true, VideoInput: true, Reasoning: true, ThinkingFormat: "openai", ContextWindow: 1048576, MaxOutput: 524288},
	"big-pickle":                  {Reasoning: true, ThinkingFormat: "openai", ContextWindow: 200000, MaxOutput: 32000},
	"exo-free":                    {Vision: true, Reasoning: true, ThinkingFormat: "openai", ContextWindow: 1048576, MaxOutput: 131072},
	"fledge-alpha-free":           {Vision: true, Reasoning: true, ThinkingFormat: "openai", ContextWindow: 1048576, MaxOutput: 131072},
	"longcat-2.0":                 {Reasoning: true, ThinkingFormat: "openai", ContextWindow: 1000000, MaxOutput: 131072},
	"longcat-2.0-free":            {Reasoning: true, ThinkingFormat: "openai", ContextWindow: 1000000, MaxOutput: 131072},
	"longcat-2.5-preview-free":    {Vision: true, Reasoning: true, ThinkingFormat: "openai", ContextWindow: 1000000, MaxOutput: 131072},
	"north-mini-code-free":        {Reasoning: true, ThinkingFormat: "openai", ContextWindow: 256000, MaxOutput: 64000},
	"ring-2.6-1t-free":            {Reasoning: true, ThinkingFormat: "openai", ContextWindow: 262000, MaxOutput: 66000},
	"x-preview-f-free":            {Vision: true, VideoInput: true, Reasoning: true, ThinkingFormat: "openai", ContextWindow: 1000000, MaxOutput: 131072},
	"longcat-2.5-free":            {Reasoning: true, ThinkingFormat: "openai", ContextWindow: 1000000, MaxOutput: 262144},
	"iceman-free":                 {Reasoning: true, ThinkingFormat: "openai", ContextWindow: 262144, MaxOutput: 131072},
	"inkling-2-small-free":        {Reasoning: true, ThinkingFormat: "openai", ContextWindow: 1048576, MaxOutput: 131072},
	"beam-free":                   {ContextWindow: 1048576, MaxOutput: 131072},
	"flint-40b-free":              {Reasoning: true, ThinkingFormat: "openai", ContextWindow: 262144, MaxOutput: 65536},
	"nemotron-3.5-super-vl-free":  {Reasoning: true, ThinkingFormat: "openai", ContextWindow: 1048576, MaxOutput: 131072},
	"jev-1.13-free":               {ContextWindow: 64000, MaxOutput: 32000},
	"nemotron-3.5-lightning-free": {ContextWindow: 262144, MaxOutput: 262144},
	"hy4-flash-free":              {Reasoning: true, ThinkingFormat: "openai", ContextWindow: 1048576, MaxOutput: 65536},
	"tidepool-alpha-free":         {Reasoning: true, ThinkingFormat: "openai", ContextWindow: 1048576, MaxOutput: 131072},
	"vision-model":                {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "qwen", ContextWindow: 1000000},
	"coder-model":                 {Reasoning: true, Tools: true, ThinkingFormat: "qwen", ContextWindow: 1000000},
	"kimi-k3":                     {Vision: true, VideoInput: true, Reasoning: true, Tools: true, ThinkingFormat: "kimi", ThinkingCanDisable: new(false), ContextWindow: 1048576, MaxOutput: 131072},
	"k3":                          {Vision: true, VideoInput: true, Reasoning: true, Tools: true, ThinkingFormat: "kimi", ThinkingCanDisable: new(false), ContextWindow: 1048576, MaxOutput: 131072},
	"kimi-k3.1-pro":               {Vision: true, VideoInput: true, Reasoning: true, Tools: true, ThinkingFormat: "kimi", ThinkingCanDisable: new(false), ContextWindow: 1048576, MaxOutput: 131072},
	"k3.1-pro":                    {Vision: true, VideoInput: true, Reasoning: true, Tools: true, ThinkingFormat: "kimi", ThinkingCanDisable: new(false), ContextWindow: 1048576, MaxOutput: 131072},
	"k3.1-pro-highspeed":          {Vision: true, VideoInput: true, Reasoning: true, Tools: true, ThinkingFormat: "kimi", ThinkingCanDisable: new(false), ContextWindow: 1048576, MaxOutput: 131072},
	"kimi-for-coding":             {Vision: true, VideoInput: true, Reasoning: true, Tools: true, ThinkingFormat: "kimi", ThinkingCanDisable: new(false), ContextWindow: 262144, MaxOutput: 65536},
	"kimi-for-coding-highspeed":   {Vision: true, VideoInput: true, Reasoning: true, Tools: true, ThinkingFormat: "kimi", ThinkingCanDisable: new(false), ContextWindow: 262144, MaxOutput: 65536},
	"kimi-k2.7-code":              {Vision: true, VideoInput: true, Reasoning: true, Tools: true, ThinkingFormat: "kimi", ThinkingCanDisable: new(false), ContextWindow: 262144, MaxOutput: 65536},
	"kimi-k2.7-code-highspeed":    {Vision: true, VideoInput: true, Reasoning: true, Tools: true, ThinkingFormat: "kimi", ThinkingCanDisable: new(false), ContextWindow: 262144, MaxOutput: 65536},
}

// codexExtendedContextWindow is the context window the codex registry
// publishes for the `[1m]` extended-context variants. The suffix is a 9router
// catalog id, not a wire id — upstream resolves it to the base model through
// `upstreamModelId` — so these entries carry their own window rather than
// inheriting the base model's.
const codexExtendedContextWindow = 872000

// codexGpt56Caps builds the shared Codex capability block at a given context
// window. Every codex model reports vision, reasoning, search and tools with
// OpenAI-style reasoning_effort; only the window differs between families.
func codexGpt56Caps(contextWindow int) Capabilities {
	return Capabilities{
		Vision: true, Reasoning: true, Search: true, Tools: true,
		ThinkingFormat: "openai", ContextWindow: contextWindow, MaxOutput: 128000,
	}
}

// devinCLIGPTCaps is Devin CLI's own GPT declaration (upstream
// DEVIN_CLI_GPT_CAPS): a 200k window even though the models are GPT-5.4/5.5.
var devinCLIGPTCaps = Capabilities{
	Vision: true, Reasoning: true, Search: true, Tools: true,
	ThinkingFormat: "openai", ContextWindow: 200000, MaxOutput: 128000,
}

// qoderCapabilities is the Qoder model capability table. The model ids are
// opaque (dmodel, kmodel, qmodel_38max), so the name heuristics in the
// pattern table match none of them — without this every Qoder model is
// reported text-only and an image sent by a client is replaced by a
// placeholder before it ever reaches the model. Context/output limits mirror
// what /algo/api/v2/model/list reports.
var qoderCapabilities = map[string]Capabilities{
	// Tier aliases resolve to a variable target upstream; keep them text-only
	// and on the default window rather than guessing.
	"auto":      {ContextWindow: 200000, MaxOutput: 32768},
	"efficient": {ContextWindow: 200000, MaxOutput: 32768},
	"lite":      {ContextWindow: 200000, MaxOutput: 32768},
	// Concrete ids read images back, so they are vision-capable.
	"ultimate":      {Vision: true, Reasoning: true, ContextWindow: 200000, MaxOutput: 65536},
	"performance":   {Vision: true, ContextWindow: 200000, MaxOutput: 65536},
	"qmodel_38max":  {Vision: true, Reasoning: true, ContextWindow: 200000, MaxOutput: 65536},
	"qmodel_latest": {Vision: true, ContextWindow: 200000, MaxOutput: 65536},
	"qmodel":        {Vision: true, ContextWindow: 200000, MaxOutput: 65536},
	"qfmodel":       {Vision: true, Reasoning: true, ContextWindow: 200000, MaxOutput: 65536},
	"kmodel_latest": {Vision: true, ContextWindow: 200000, MaxOutput: 65536},
	"kmodel":        {Vision: true, ContextWindow: 200000, MaxOutput: 65536},
	"gmodel":        {Vision: true, Reasoning: true, ContextWindow: 200000, MaxOutput: 65536},
	"gfmodel":       {Vision: true, Reasoning: true, ContextWindow: 200000, MaxOutput: 65536},
	"dmodel":        {Vision: true, Reasoning: true, ContextWindow: 200000, MaxOutput: 65536},
	"dfmodel":       {Vision: true, Reasoning: true, ContextWindow: 200000, MaxOutput: 65536},
	"mmodel":        {Vision: true, ContextWindow: 200000, MaxOutput: 65536},
	// Frontier reasoning pair; absent from many per-account catalogs, so they
	// are routed through a static model_config.
	"smodel": {Vision: true, Reasoning: true, ContextWindow: 180000, MaxOutput: 65536},
	"cmodel": {Vision: true, Reasoning: true, ContextWindow: 180000, MaxOutput: 65536},
}

var providerCapabilities = map[string]map[string]Capabilities{
	"qoder": qoderCapabilities,
	// Qoder CN serves the identical model catalog from the CN gateway, so it
	// shares the intl Qoder capability table verbatim.
	"qoder-cn": qoderCapabilities,
	// OpenCode Free (oc): the free lane's MiMo route is OpenRouter-backed and
	// takes the plain OpenAI reasoning_effort enum verbatim ("none" really
	// drops thinking). The global *mimo*v2.6* pattern describes Xiaomi's own
	// API (DeepSeek-shaped thinking object, always on), which this lane
	// ignores: thinking.type:disabled still streamed reasoning. Limits repeat
	// the family pattern deliberately: the synced catalog carries no opencode
	// limit delta for this id, so 1M/131k is what the lane already published.
	"opencode": {
		"mimo-v2.6-flash-free": {Vision: true, AudioInput: true, VideoInput: true, Reasoning: true, ThinkingFormat: "openai", ThinkingCanDisable: new(true), ContextWindow: 1048576, MaxOutput: 131072},
		// The free lane's deepseek-v4.1-flash rejects prompts above ~330k tokens
		// (upstream #5097); pin the published window so clients compact instead
		// of hard-failing mid-session.
		"deepseek-v4.1-flash-free": {Vision: true, Reasoning: true, ThinkingFormat: "deepseek", ContextWindow: 330000, MaxOutput: 128000},
		// Free ids the family patterns mis-guess: models.dev's opencode entry
		// is the per-gateway source (the free lane truncates differently from
		// the vendor API). Without these rows ling/nemotron fell to the
		// *ling-*/*nemotron* 128k floor.
		"ling-3.0-flash-fin-free":     {Reasoning: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 262144, MaxOutput: 32768},
		"nemotron-3-ultra-free":       {Reasoning: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 1000000, MaxOutput: 128000},
		"nemotron-3.5-lightning-free": {Reasoning: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 262144, MaxOutput: 262144},
		// hy4 free ids: the provider's own zen feed (limit.context/output,
		// probed live) and models.dev's opencode entry both file 1M/65536.
		// Without a row hy4-flash-free fell to the 128k floor (no *hy4*
		// pattern exists). Window pinned only; thinking stays on the family
		// resolution (the fixture pins it).
		"hy4-flash-free":             {Tools: true, ContextWindow: 1048576, MaxOutput: 65536},
		"longcat-2.5-free":           {ContextWindow: 1000000, MaxOutput: 262144},
		"iceman-free":                {ContextWindow: 262144, MaxOutput: 131072},
		"inkling-2-small-free":       {ContextWindow: 1048576, MaxOutput: 131072},
		"beam-free":                  {ContextWindow: 1048576, MaxOutput: 131072},
		"flint-40b-free":             {ContextWindow: 262144, MaxOutput: 65536},
		"nemotron-3.5-super-vl-free": {ContextWindow: 1048576, MaxOutput: 131072},
		"jev-1.13-free":              {ContextWindow: 64000, MaxOutput: 32000},
		"tidepool-alpha-free":        {ContextWindow: 1048576, MaxOutput: 131072},
	},
	"nvidia": {
		"nvidia/nemotron-3.5-bolt-e4b":                  {Tools: true, ContextWindow: 128000, MaxOutput: 8192},
		"nvidia/nemotron-3.5-lightning-30b-a3b":         {Reasoning: true, ThinkingFormat: "openai", ContextWindow: 128000, MaxOutput: 8192},
		"nvidia/nemotron-3-nano-omni-30b-a3b-reasoning": {Reasoning: true, ThinkingFormat: "openai", ContextWindow: 128000, MaxOutput: 8192},
		"nvidia/nemotron-3-ultra-550b-a55b":             {Tools: true, Reasoning: true, ThinkingFormat: "openai", ContextWindow: 1000000, MaxOutput: 8192},
		"poolside/laguna-xs-2.1":                        {Tools: true, ContextWindow: 200000, MaxOutput: 32000},
		"meta/llama-guard-4-12b":                        {ContextWindow: 128000, MaxOutput: 4096},
		"nvidia/riva-translate-4b-instruct-v2":          {ContextWindow: 128000, MaxOutput: 4096},
		"meta/llama-3.3-70b-instruct":                   {Tools: true, ContextWindow: 128000, MaxOutput: 4096},
		"minimaxai/minimax-m2.7":                        {Reasoning: true, ThinkingFormat: "openai", ThinkingCanDisable: new(false), ContextWindow: 200000, MaxOutput: 131072},
		"minimaxai/minimax-m3":                          {Vision: true, Reasoning: true, ThinkingFormat: "openai", ThinkingCanDisable: new(false), ContextWindow: 512000, MaxOutput: 131072},
		"z-ai/glm-5.2":                                  {Reasoning: true, ThinkingFormat: "openai", ContextWindow: 200000, MaxOutput: 128000},
		"deepseek-ai/deepseek-v4-pro":                   {Reasoning: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 1000000, MaxOutput: 65536},
		"deepseek-ai/deepseek-v4-flash":                 {Reasoning: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 1000000, MaxOutput: 65536},
		// Free-tier ids the family patterns mis-guess: gemma-4 carries a 256k
		// window on NIM (models.dev), gpt-oss-120b matches the sibling 20b's
		// 131072/131072 declaration.
		"google/gemma-4-31b-it": {Vision: true, Reasoning: true, Tools: true, ContextWindow: 256000, MaxOutput: 16384},
		"openai/gpt-oss-120b":   {Reasoning: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 131072, MaxOutput: 131072},
	},
	"codex": {
		// Codex OAuth reports its own context windows (open-sse/providers/
		// capabilities.js), which differ from both the OpenAI API's and the
		// sibling GPT-5.6 models' — Sol is wider than Terra/Luna, and the [1m]
		// variants are wider again.
		"gpt-6-astra":   codexGpt56Caps(272000),
		"gpt-6-sol":     codexGpt56Caps(272000),
		"gpt-6-luna":    codexGpt56Caps(272000),
		"gpt-6.1-sol":   codexGpt56Caps(272000),
		"gpt-6.2-astra": codexGpt56Caps(272000),
		"gpt-6.2-sol":   codexGpt56Caps(272000),
		// codex-rs/models-manager/models.json: gpt-6.1-terra carries a 500k
		// window on Codex (the API model is 1.05M; Codex caps the default
		// window like its siblings).
		"gpt-6.1-terra":          codexGpt56Caps(500000),
		"gpt-6-astra[1m]":        codexGpt56Caps(codexExtendedContextWindow),
		"gpt-6-sol[1m]":          codexGpt56Caps(codexExtendedContextWindow),
		"gpt-6.2-astra[1m]":      codexGpt56Caps(codexExtendedContextWindow),
		"gpt-6.2-sol[1m]":        codexGpt56Caps(codexExtendedContextWindow),
		"gpt-6-luna[1m]":         codexGpt56Caps(codexExtendedContextWindow),
		"gpt-5.6-sol":            codexGpt56Caps(372000),
		"gpt-5.6-sol[1m]":        codexGpt56Caps(codexExtendedContextWindow),
		"gpt-5.6-sol-review":     codexGpt56Caps(372000),
		"gpt-5.6-terra":          codexGpt56Caps(272000),
		"gpt-5.6-terra[1m]":      codexGpt56Caps(codexExtendedContextWindow),
		"gpt-5.6-terra-review":   codexGpt56Caps(272000),
		"gpt-5.6-luna":           codexGpt56Caps(272000),
		"gpt-5.6-luna[1m]":       codexGpt56Caps(codexExtendedContextWindow),
		"gpt-5.6-luna-review":    codexGpt56Caps(272000),
		"gpt-5.6-sol-image":      {ImageOutput: true, Tools: true},
		"gpt-5.6-terra-image":    {ImageOutput: true, Tools: true},
		"gpt-5.6-luna-image":     {ImageOutput: true, Tools: true},
		"gpt-image-2.5":          {ImageOutput: true, Tools: true},
		"gpt-image-2.5-flare":    {ImageOutput: true, Tools: true},
		"gpt-image-2.5-sunburst": {ImageOutput: true, Tools: true},
		"gpt-image-2":            {ImageOutput: true, Tools: true},
		"gpt-image-1.5":          {ImageOutput: true, Tools: true},
	},
	// Catalog mirrors the codebuddy.cn product-config payload, snapshot
	// 2026-09-30 (upstream #4614). ContextWindow AND MaxOutput are both
	// declared on purpose: GetCapabilitiesDetailForModel only consults the
	// models.dev catalog while both are zero, so setting one alone would
	// silently disable the catalog lookup for these ids.
	"codebuddy-cn": {
		"glm-5.2":       {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(true), ContextWindow: 1000000, MaxOutput: 131072},
		"glm-5.1":       {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(false), ContextWindow: 200000, MaxOutput: 48000},
		"glm-5.0-turbo": {Reasoning: true, Tools: true},
		"minimax-m3":    {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(false), ContextWindow: 512000, MaxOutput: 524288},
		"minimax-m2.7":  {Reasoning: true, Tools: true},
		"kimi-k2.5":     {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(false), ContextWindow: 164000, MaxOutput: 32000},
		// Upstream #4614 drops glm-5v-turbo / kimi-k2.7 / kimi-k2.6 from the
		// published server list, and v0.5.99 keeps them dropped. They stay here
		// on purpose: a saved combo or connection naming one of them is still
		// routable here, and dropping the row would make a shipped model
		// unroutable for no user-visible gain. The level divergence this
		// causes is recorded in thinking_levels_fixture_test.go. The limits
		// are the server's own figures.
		"glm-5v-turbo":  {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(false), ContextWindow: 200000, MaxOutput: 64000},
		"kimi-k2.7":     {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(false), ContextWindow: 256000, MaxOutput: 32000},
		"kimi-k2.6":     {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(false), ContextWindow: 256000, MaxOutput: 32000},
		"hy3-preview":   {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(false), ContextWindow: 192000, MaxOutput: 64000},
		"hy3":           {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(false), ContextWindow: 192000, MaxOutput: 64000},
		"hy3-x":         {Vision: true, Reasoning: true, Tools: true},
		"hy4-preview":   {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(false), ContextWindow: 1000000, MaxOutput: 64000},
		"hy4-preview-x": {Vision: true, Reasoning: true, Tools: true},
		"glm-5.3":       {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(true), ContextWindow: 1000000, MaxOutput: 131072},
		"glm-5.3-flash": {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(true), ContextWindow: 1000000, MaxOutput: 131072},
		"kimi-k3-1":     {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(false), ContextWindow: 1000000, MaxOutput: 1048576},
		// contextWindow is the server's contextWindow.defaultLength (300000),
		// NOT maxInputTokens: k2.8 publishes supportedLengths [300000, 1000000]
		// and the gateway only serves 1M to callers who opt in, which this
		// executor never does. Budgeting 1M here would let the capacity
		// adapter plan against a window the model does not actually have.
		"kimi-k2.8-preview":   {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(true), ContextWindow: 300000, MaxOutput: 131072},
		"deepseek-v4-pro":     {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(true), ContextWindow: 1000000, MaxOutput: 393216},
		"deepseek-v4.1-flash": {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(true), ContextWindow: 1000000, MaxOutput: 393216},
		"deepseek-v4-flash":   {Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(false), ContextWindow: 1000000, MaxOutput: 50000},
		"deepseek-v3-2-volc":  {Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(false), ContextWindow: 96000, MaxOutput: 32000},
	},
	"hcnsec": {
		"auto":                     {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai"},
		"DeepSeek-V4-Flash":        {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 1000000, MaxOutput: 384000},
		"DeepSeek-V4-Pro":          {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 1000000, MaxOutput: 384000},
		"DeepSeek-V4.1-Flash":      {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 1000000, MaxOutput: 128000},
		"glm-5.3":                  {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 1000000, MaxOutput: 131072},
		"glm-5.3-flash":            {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 1000000, MaxOutput: 131072},
		"MiMo-V2.6-Flash":          {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 1048576, MaxOutput: 131072},
		"MiniCPM-V-4.7-35B-A3B":    {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 262144, MaxOutput: 65536},
		"MiniMax-M3.1-Flash":       {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 1000000, MaxOutput: 131072},
		"Qwen3-Embedding-8B":       {},
		"Qwen3.8-27B":              {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 200000, MaxOutput: 65536},
		"Qwen3.8-Flash-Next":       {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 262144, MaxOutput: 65536},
		"sensenova-6.8-flash":      {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 1000000, MaxOutput: 131072},
		"sensenova-6.8-flash-lite": {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 1000000, MaxOutput: 131072},
		"sensenova-u1.5-lite":      {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai"},
		"step-5-preview":           {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 1000000, MaxOutput: 64000},
		"stepaudio-2.5-asr":        {},
		"stepaudio-2.5-chat":       {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai"},
		"stepaudio-2.5-realtime":   {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai"},
		"stepaudio-2.5-tts":        {},
	},
	"poolside": {
		"laguna-s-2.1":  {Reasoning: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 1000000, MaxOutput: 32000},
		"laguna-xs-2.1": {Reasoning: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 200000, MaxOutput: 32000},
	},
	// sensenova-intl serves the same OpenAI wire as the China lane; the 6.8 pair
	// mirrors the sensenova rows above and u1.5-lite is the image-gen id served
	// through the image lane (upstream registry/sensenova-intl.js, 14b27dc7).
	"sensenova-intl": {
		"sensenova-6.8-flash":      {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 262144, MaxOutput: 65536},
		"sensenova-6.8-flash-lite": {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 262144, MaxOutput: 65536},
		"sensenova-6.7-flash-lite": {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 262144, MaxOutput: 65536},
		"sensenova-u1.5-lite":      {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai"},
	},
	// Devin CLI's registry declares 200k for these GPT variants; the generic
	// gpt-5.4/5.5 rows now publish the 1.05M API window, so the gateway's own
	// number has to be recorded here (upstream decolua/9router 89ffac5a).
	"devin-cli": {
		"gpt-5.4-high":   devinCLIGPTCaps,
		"gpt-5.4-medium": devinCLIGPTCaps,
		"gpt-5.4-low":    devinCLIGPTCaps,
		"gpt-5.5-xhigh":  devinCLIGPTCaps,
		"gpt-5.5-high":   devinCLIGPTCaps,
		"gpt-5.5-medium": devinCLIGPTCaps,
		"gpt-5.5-low":    devinCLIGPTCaps,
	},
	// agy-acp: models served by the local agy 1.5.9 CLI over ACP. The roster
	// is explicit (no cross-provider aliasing); context window is agy's own
	// 1M-token session window reported by session/new usage_update.
	// agy-acp: full Antigravity roster served by the local agy CLI over ACP.
	// Synced with internal/providers/registry_models.go. Context window is agy's
	// own 1M-token session window; models an account cannot select still list
	// here and fail with a clear error at session/new, not silently.
	"agy-acp": {
		"gemini-4-carbon-high":     {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000},
		"gemini-4-carbon-medium":   {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000},
		"gemini-4-carbon-low":      {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000},
		"gemini-4-carbon":          {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000},
		"gemini-4-argon-high":      {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000},
		"gemini-4-argon-medium":    {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000},
		"gemini-4-argon-low":       {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000},
		"gemini-4-argon":           {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000},
		"gemini-4-hydrogen-xhigh":  {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000},
		"gemini-4-hydrogen-high":   {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000},
		"gemini-4-hydrogen-medium": {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000},
		"gemini-4-hydrogen-low":    {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000},
		"gemini-4-hydrogen":        {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000},
		"gemini-4-neon-xhigh":      {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000},
		"gemini-4-neon-high":       {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000},
		"gemini-4-neon-medium":     {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000},
		"gemini-4-neon-low":        {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000},
		"gemini-4-neon":            {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000},
		"gemini-3.8-flash-high":    {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000},
		"gemini-3.8-flash-medium":  {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000},
		"gemini-3.8-flash-low":     {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000},
		"gemini-3.8-flash":         {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000},
		"gemini-3.7-flash-high":    {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000},
		"gemini-3.7-flash-medium":  {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000},
		"gemini-3.7-flash-low":     {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000},
		"gemini-3.1-pro-high":      {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000},
		"gemini-pro-agent":         {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000},
		"gemini-3.1-pro-low":       {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000},
		"claude-sonnet-5-5-high":   {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000},
		"claude-sonnet-5-5-medium": {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000},
		"claude-sonnet-5-5-low":    {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000},
		"claude-sonnet-5-5":        {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000},
		"claude-opus-5-5-high":     {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000},
		"claude-opus-5-5-medium":   {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000},
		"claude-opus-5-5-low":      {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000},
		"claude-opus-5-5":          {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000},
		"claude-sonnet-5-5-max":    {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000},
		"claude-opus-5-5-max":      {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000},
		"claude-sonnet-4-6":        {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000},
		"claude-opus-4-6-thinking": {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000},
		"gpt-oss-120b-medium":      {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000},
		"gpt-oss-120b":             {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000},
		"claude-sonnet":            {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000},
		"claude-opus":              {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000},
	},
	// MiniMax Code (mcode) credits lane — Anthropic messages on the mavis
	// gateway, thinking as adaptive effort via output_config.effort
	// (claude-adaptive). Limits are the static catalog magpie's minimax plugin
	// ships; the M2.7 pair always thinks and takes no effort knob in MiniMax
	// Code itself, hence thinkingCanDisable:false. Port of upstream
	// decolua/9router open-sse/providers/capabilities.js.
	"minimax-code": {
		"MiniMax-M3.1-Flash-Preview": {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "claude-adaptive", ThinkingCanDisable: new(false), ContextWindow: 512000, MaxOutput: 128000},
		"MiniMax-M3":                 {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "claude-adaptive", ContextWindow: 512000, MaxOutput: 128000},
		"MiniMax-M2.7":               {Reasoning: true, Tools: true, ThinkingFormat: "claude-adaptive", ThinkingCanDisable: new(false), ContextWindow: 200000, MaxOutput: 128000},
		"MiniMax-M2.7-highspeed":     {Reasoning: true, Tools: true, ThinkingFormat: "claude-adaptive", ThinkingCanDisable: new(false), ContextWindow: 200000, MaxOutput: 128000},
	},
	// Zed's hosted Anthropic lane serves claude-haiku-5-5 with adaptive
	// thinking. The global *claude*haiku* pattern emits the budget shape
	// (thinking.type.enabled + budget_tokens), which Zed rejects with 400:
	// `"thinking.type.enabled" is not supported for this model. Use
	// "thinking.type.adaptive" and "output_config.effort"`. claude-adaptive
	// emits exactly that pair, so the id is pinned here for the zed lane only
	// — other lanes keep the budget shape for the same id. Port of upstream
	// decolua/9router open-sse/providers/capabilities.js, which pins the same
	// id for opencode-go; Zed routes this model through the same Anthropic
	// backend. Limits follow the model, not the pattern: the Zed catalogue
	// reports 1M context / 128k output, not the 200k/8192 the haiku substring
	// table guesses.
	"zed": {
		"claude-haiku-5-5": {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ContextWindow: 1000000, MaxOutput: 128000},
	},
	// GitHub Copilot routes Claude to its Anthropic-native /v1/messages shim,
	// which applies Anthropic's per-model thinking rules: claude-haiku-5.5 is
	// adaptive-only there too, and the budget shape the global *claude*haiku*
	// pattern sends comes back 400 "thinking.type.enabled" is not supported
	// (live probe). Limits are stated because this override short-circuits the
	// catalog overlay; they match the synced catalog's github entry. Port of
	// upstream decolua/9router open-sse/providers/capabilities.js (8aef7480).
	"github": {
		"claude-haiku-5.5": {Vision: true, PDF: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ContextWindow: 1000000, MaxOutput: 128000},
	},
	// commandcode pins haiku-5-5 to the catalog values: the generic commandcode
	// fallback (200000/8192 for every claude-haiku id) understates the real
	// window — GET https://api.commandcode.ai/provider/v1/models reports
	// context_length 1000000 for this id (claude-haiku-4-5-20251001 stays 200000,
	// so the 1M is per-model, not gateway-wide) and maxOutput 128000. Thinking
	// stays the lane's commandcode format with effort. Port of upstream
	// decolua/9router open-sse/providers/capabilities.js (8aef7480).
	"commandcode": {
		"claude-haiku-5-5": {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "commandcode", ThinkingEffortSupported: true, ContextWindow: 1000000, MaxOutput: 128000},
	},
	"alitp-intl": {
		"qwen4-max":            {Vision: true, VideoInput: true, Reasoning: true, ThinkingFormat: "qwen", ContextWindow: 1000000, MaxOutput: 393216},
		"qwen4-max-preview":    {Vision: true, VideoInput: true, Reasoning: true, ThinkingFormat: "qwen", ContextWindow: 1000000, MaxOutput: 393216},
		"qwen3.8-max":          {Vision: true, VideoInput: true, Reasoning: true, ThinkingFormat: "qwen", ContextWindow: 1000000, MaxOutput: 131072},
		"qwen3.8-flash":        {Vision: true, Reasoning: true, ThinkingFormat: "qwen", ContextWindow: 1000000, MaxOutput: 131072},
		"qwen3.7-max":          {Vision: false, Reasoning: true, ThinkingFormat: "qwen", ContextWindow: 1000000, MaxOutput: 131072},
		"qwen3.7-plus":         {Vision: true, Reasoning: true, ThinkingFormat: "qwen", ContextWindow: 1000000, MaxOutput: 131072},
		"deepseek-v4-pro-0813": {Reasoning: true, ThinkingFormat: "deepseek", ThinkingEffortSupported: true, ContextWindow: 1000000, MaxOutput: 393216},
	},
	"qwencloud": {
		"qwen4-max":            {Vision: true, VideoInput: true, Reasoning: true, ThinkingFormat: "qwen", ContextWindow: 1000000, MaxOutput: 393216},
		"qwen4-max-preview":    {Vision: true, VideoInput: true, Reasoning: true, ThinkingFormat: "qwen", ContextWindow: 1000000, MaxOutput: 393216},
		"qwen3.8-max":          {Vision: true, VideoInput: true, Reasoning: true, ThinkingFormat: "qwen", ContextWindow: 1000000, MaxOutput: 131072},
		"qwen3.8-flash":        {Vision: true, Reasoning: true, ThinkingFormat: "qwen", ContextWindow: 1000000, MaxOutput: 131072},
		"qwen3.7-max":          {Vision: false, Reasoning: true, ThinkingFormat: "qwen", ContextWindow: 1000000, MaxOutput: 131072},
		"qwen3.7-plus":         {Vision: true, Reasoning: true, ThinkingFormat: "qwen", ContextWindow: 1000000, MaxOutput: 131072},
		"deepseek-v4-pro-0813": {Reasoning: true, ThinkingFormat: "deepseek", ThinkingEffortSupported: true, ContextWindow: 1000000, MaxOutput: 393216},
	},
}

func init() {
	kiroGpt56 := Capabilities{
		Vision: true, Reasoning: true, Search: true, Tools: true,
		ThinkingFormat: "openai", ThinkingCanDisable: new(true),
		ContextWindow: 272000, MaxOutput: 128000,
	}
	providerCapabilities["kiro"] = map[string]Capabilities{
		"gpt-5.6-sol":                    kiroGpt56,
		"gpt-5.6-terra":                  kiroGpt56,
		"gpt-5.6-luna":                   kiroGpt56,
		"gpt-5.6-sol-thinking":           kiroGpt56,
		"gpt-5.6-terra-thinking":         kiroGpt56,
		"gpt-5.6-luna-thinking":          kiroGpt56,
		"gpt-5.6-sol-agentic":            kiroGpt56,
		"gpt-5.6-terra-agentic":          kiroGpt56,
		"gpt-5.6-luna-agentic":           kiroGpt56,
		"gpt-5.6-sol-thinking-agentic":   kiroGpt56,
		"gpt-5.6-terra-thinking-agentic": kiroGpt56,
		"gpt-5.6-luna-thinking-agentic":  kiroGpt56,
	}

	providerCapabilities["ollama"] = map[string]Capabilities{
		"deepseek-v4.1-flash:cloud": {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "deepseek", ContextWindow: 1000000, MaxOutput: 384000},
	}

	// gemini-web overrides the generic `*gemini-3*` pattern row. The web surface
	// and the API surface both answer `gemini-3*` requests, but they are not the
	// same product, and the pattern row was written for the API. What the site's
	// own composer offers is text, images and three thinking levels: no audio,
	// no video, and no search toggle the executor can set. Declaring them
	// would advertise controls that silently do nothing.
	//
	// Vision is set because the site's own composer accepts an image: the
	// bytes go to Google's content-push service and the path it returns
	// travels with the turn. That is the same attachment mechanism the
	// reference bridge uses, and it was verified live against
	// gemini.google.com on 2026-10-20.
	//
	// "gemini-level" is the site's own enum (low/medium/high) and it cannot be
	// turned off: level 1 is thinking's own floor.
	//
	// Every catalogued name is listed, not only the versioned ones: a name that
	// falls through to the generic `*gemini-3*` pattern row would advertise the
	// API surface's search flag, which is the outcome this override exists to
	// prevent.
	geminiWebCaps := Capabilities{Vision: true, Tools: true, Reasoning: true, ThinkingFormat: "gemini-level", ThinkingCanDisable: new(false)}
	// The Lite models do not think: the site's registry publishes no
	// reasoning level for them, and their own names say so. Advertising a
	// thinking level for a model that has none is the outcome the override
	// exists to prevent.
	geminiWebLiteCaps := Capabilities{Vision: true, Tools: true, ThinkingFormat: "gemini-level", ThinkingCanDisable: new(false)}
	providerCapabilities["gemini-web"] = map[string]Capabilities{
		"gemini-3.1-pro":        geminiWebCaps,
		"gemini-3.7-flash":      geminiWebCaps,
		"gemini-3.1-flash-lite": geminiWebLiteCaps,
		"gemini-pro":            geminiWebCaps,
		"gemini-flash":          geminiWebCaps,
		"gemini-flash-lite":     geminiWebLiteCaps,
		"gemini-argon":          geminiWebCaps,
		"gemini-3-pro":          geminiWebCaps,
		"gemini-4-argon":        geminiWebCaps,
		"gemini-hydrogen":       geminiWebCaps,
		"gemini-4-hydrogen":     geminiWebCaps,
	}

	providerCapabilities["opencode-go"] = map[string]Capabilities{
		"glm-5.3-flash": {
			Vision: true, VideoInput: true, PDF: true, Reasoning: true, Tools: true,
			ThinkingFormat: "openai", ThinkingCanDisable: new(false),
			ContextWindow: 1000000, MaxOutput: 131072,
		},
		// OpenCode Go's claude-haiku-5-5 rejects `thinking.type.enabled` — the shape
		// the global *claude*haiku* pattern sends — with 400: `"thinking.type.enabled"
		// is not supported for this model. Use "thinking.type.adaptive" and
		// "output_config.effort"`. claude-adaptive emits exactly that pair, so the id
		// is pinned here for this lane only. Limits follow the model, not the
		// pattern: the upstream catalog reports 1M context / 128k output, and the
		// provider override short-circuits the catalog overlay, so maxOutput must
		// be stated here too. Port of upstream decolua/9router
		// open-sse/providers/capabilities.js (opencode-go pin, 8aef7480).
		"claude-haiku-5-5": {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ContextWindow: 1000000, MaxOutput: 128000},
		// Per-gateway windows the family patterns mis-guess, pinned to the
		// provider's own go feed (limit.context/output, probed live) — the same
		// numbers models.dev's opencode-go entry files. Only the window is
		// pinned here; maxOutput and thinking shape stay on the upstream-synced
		// family resolution (the fixture pins those), and the live feed supplies
		// the gateway's exact output cap when a connection exists.
		"hy4-preview":                {Tools: true, ContextWindow: 1024000},
		"hy4":                        {Tools: true, ContextWindow: 1048576, MaxOutput: 65536},
		"hy4-flash-free":             {Tools: true, ContextWindow: 1048576, MaxOutput: 65536},
		"hy3":                        {Reasoning: true, Tools: true, ThinkingFormat: "hunyuan", ContextWindow: 256000},
		"muse-spark-1.2-contributor": {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 1048576, MaxOutput: 131072},
		"muse-spark-1.3-contributor": {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 1048576, MaxOutput: 131072},
		"beam-free":                  {Tools: true, ContextWindow: 1048576, MaxOutput: 131072},
	}

	// OpenCode Zen serves the same free ids as the free lane on the fast PAYG
	// lane; the family patterns mis-guess ling/nemotron there too. Only the
	// window is pinned — the thinking shape stays on the upstream pattern
	// resolution (the fixture pins it), and the live zen feed supplies the
	// gateway's exact numbers when a connection exists.
	providerCapabilities["opencode-zen"] = map[string]Capabilities{
		"ling-3.0-flash-fin-free":     {Reasoning: true, Tools: true, ContextWindow: 262144},
		"nemotron-3-ultra-free":       {Reasoning: true, Tools: true, ContextWindow: 1000000},
		"nemotron-3.5-lightning-free": {Reasoning: true, Tools: true, ContextWindow: 262144, MaxOutput: 262144},
		"hy4-flash-free":              {Tools: true, ContextWindow: 1048576, MaxOutput: 65536},
	}

	providerCapabilities["nous"] = map[string]Capabilities{
		"meituan/longcat-2.0:free":         {Tools: true, Reasoning: true, ThinkingFormat: "nous", ContextWindow: 1048756, MaxOutput: 262144},
		"meituan/longcat-2.5-preview:free": {Tools: true, Vision: true, Reasoning: true, ThinkingFormat: "nous", ContextWindow: 1048576, MaxOutput: 131072},
		"stepfun/step-5-preview:free":      {Tools: true, Vision: true, VideoInput: true, Reasoning: true, ThinkingFormat: "nous", ContextWindow: 1000000, MaxOutput: 64000},
		"upstage/solar-mini4:free":         {Tools: true, Reasoning: true, ThinkingFormat: "nous", ContextWindow: 524288, MaxOutput: 131072},
		"stepfun/step-3.7-flash:free":      {Tools: true, Vision: true, VideoInput: true, Reasoning: true, ThinkingFormat: "nous", ContextWindow: 262144, MaxOutput: 256000},
		"poolside/laguna-s-2.1:free":       {Tools: true, Reasoning: true, ThinkingFormat: "nous", ContextWindow: 262144, MaxOutput: 131072},
		"poolside/laguna-xs-2.1:free":      {Tools: true, Reasoning: true, ThinkingFormat: "nous", ContextWindow: 262144, MaxOutput: 32768},
		"stealth/missingno":                {Tools: true, Reasoning: true, ThinkingFormat: "nous", ContextWindow: 262144, MaxOutput: 131072},
	}

	// `cx` is the alias codex publishes under, so it serves the same catalog
	// upstream does (open-sse/providers/capabilities.js:
	// PROVIDER_CAPABILITIES.cx = PROVIDER_CAPABILITIES.codex).
	providerCapabilities["cx"] = providerCapabilities["codex"]
	providerCapabilities["oc"] = providerCapabilities["opencode"]
	providerCapabilities["ocz"] = providerCapabilities["opencode-zen"]

	// The global mcode site serves the identical catalog, so it shares the
	// table rather than restating it (upstream
	// PROVIDER_CAPABILITIES["minimax-code-global"] = PROVIDER_CAPABILITIES["minimax-code"]).
	providerCapabilities["minimax-code-global"] = providerCapabilities["minimax-code"]

	// zcode: ZCode start-plan lane. The gateway accepts the Anthropic Messages
	// wire with the same GLM models as the apikey lane; thinking follows the
	// zai format (the executor already maps it).
	//
	// glm-5.4 is published by the models.dev coding-plan feed
	// (`zai-coding-plan`/`zhipuai-coding-plan`) with a 1M window. It is not yet
	// in upstream's static capabilities table, whose `*glm-5*` catch-all would
	// otherwise answer 200k. Keep the static fallback here, and let the
	// catalog alias in catalog_sync.go make the feed authoritative when loaded.
	providerCapabilities["zcode"] = map[string]Capabilities{
		"glm-5.3":       {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "zai", ThinkingCanDisable: new(false), ContextWindow: 1000000, MaxOutput: 131072},
		"glm-5.3-flash": {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "zai", ThinkingCanDisable: new(false), ContextWindow: 1000000, MaxOutput: 131072},
		"glm-5.4":       {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "zai", ThinkingCanDisable: new(false), ContextWindow: 1000000, MaxOutput: 131072},
	}
	// DaoXE (daoxe) — OpenAI-compatible free tier
	providerCapabilities["daoxe"] = map[string]Capabilities{
		"gpt-4o":        {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 128000, MaxOutput: 16384},
		"gpt-4o-mini":   {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 128000, MaxOutput: 16384},
		"gpt-3.5-turbo": {Tools: true},
	}

	// Cloudflare AI (workers-ai). Mistral Small 3.1 and Kimi K2.6 are vision
	// models: keep their image_url parts instead of flattening them to text.
	// Cloudflare AI speaks the OpenAI wire format, so they take openai thinking.
	// Kimi K2.6 uses the kimi thinking level set (levelMax) per the fixture.
	providerCapabilities["cloudflare-ai"] = map[string]Capabilities{
		"@cf/mistralai/mistral-small-3.1-24b-instruct": {Vision: true, Tools: true, ThinkingFormat: "openai"},
		"@cf/moonshotai/kimi-k2.6":                     {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "kimi", ContextWindow: 262144, MaxOutput: 256000},
		// Workers AI truncates well below the vendor API window; models.dev's
		// cloudflare-workers-ai entry carries the per-gateway figures. Without
		// them the family patterns published the vendor window (1M for a 32k
		// qwen-coder endpoint) or the 128k floor.
		// Thinking shapes follow upstream's pattern resolution (the fixture pins
		// them): *qwen*coder* and the generic *qwen* row give the qwen format,
		// and deepseek-r1-distill matches *qwen* before *deepseek-r* upstream.
		// Only the limits are the models.dev gateway figures.
		"@cf/qwen/qwen2.5-coder-32b-instruct":          {Reasoning: true, ThinkingFormat: "qwen", ContextWindow: 32768, MaxOutput: 32768},
		"@cf/qwen/qwq-32b":                             {Reasoning: true, ThinkingFormat: "qwen", ThinkingCanDisable: new(false), ContextWindow: 24000, MaxOutput: 24000},
		"@cf/deepseek-ai/deepseek-r1-distill-qwen-32b": {Reasoning: true, ThinkingFormat: "qwen", ContextWindow: 80000, MaxOutput: 80000},
		"@cf/meta/llama-3.3-70b-instruct-fp8-fast":     {Tools: true, ContextWindow: 24000, MaxOutput: 24000},
		"@cf/google/gemma-4-26b-a4b-it":                {Vision: true, Tools: true, ContextWindow: 256000, MaxOutput: 16384},
	}

	// NaN.Builders (nanbuilders) — OpenAI-compatible free tier
	providerCapabilities["nanbuilders"] = map[string]Capabilities{
		"gpt-4o":        {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 128000, MaxOutput: 16384},
		"gpt-4o-mini":   {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 128000, MaxOutput: 16384},
		"gpt-3.5-turbo": {Tools: true},
	}

	// OpenAdapter (openadapter) — OpenAI-compatible free tier
	providerCapabilities["openadapter"] = map[string]Capabilities{
		"gpt-4o":        {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 128000, MaxOutput: 16384},
		"gpt-4o-mini":   {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 128000, MaxOutput: 16384},
		"gpt-3.5-turbo": {Tools: true},
	}

	// Aliases share the same capabilities catalog
	providerCapabilities["dx"] = providerCapabilities["daoxe"]
	providerCapabilities["nan"] = providerCapabilities["nanbuilders"]
	providerCapabilities["hc"] = providerCapabilities["hcnsec"]
	providerCapabilities["oad"] = providerCapabilities["openadapter"]

	// Doubao Seed 2.0 family — volcengine/byteplus publish 256000 (pro/lite/mini)
	// and 262144 (code) per models.dev; the ark/sumopod resellers serve the same
	// weights. Only the window is pinned: upstream declares no reasoning for
	// these ids (the fixture pins that), so adding Reasoning here would offer
	// thinking levels the provider does not publish.
	seedPro := Capabilities{Tools: true, ContextWindow: 256000, MaxOutput: 128000}
	seedLite := Capabilities{Tools: true, ContextWindow: 256000, MaxOutput: 131072}
	seedCode := Capabilities{Tools: true, ContextWindow: 262144, MaxOutput: 131072}
	providerCapabilities["volcengine-ark"] = map[string]Capabilities{
		"Doubao-Seed-2.0-pro":  seedPro,
		"Doubao-Seed-2.0-lite": seedLite,
		"Doubao-Seed-2.0-Code": seedCode,
		"Doubao-Seed-Code":     seedCode,
	}
	providerCapabilities["ark"] = providerCapabilities["volcengine-ark"]
	providerCapabilities["byteplus"] = map[string]Capabilities{
		"seed-2-0-pro":         seedPro,
		"seed-2-0-lite":        seedLite,
		"seed-2-0-lite-260228": seedLite,
		"seed-2-0-mini":        seedLite,
		"seed-2-0-mini-260215": seedLite,
		"seed-2-0-code":        seedCode,
	}
	providerCapabilities["bpm"] = providerCapabilities["byteplus"]
	providerCapabilities["sumopod"] = map[string]Capabilities{
		"seed-2-0-pro":  seedPro,
		"seed-2-0-lite": seedLite,
		"seed-2-0-mini": seedLite,
		"seed-2-0-code": seedCode,
		"gpt-4o":        {Vision: true, Search: true, Tools: true, ContextWindow: 128000, MaxOutput: 16384},
		"gpt-4o-mini":   {Vision: true, Search: true, Tools: true, ContextWindow: 128000, MaxOutput: 16384},
	}
	providerCapabilities["sp"] = providerCapabilities["sumopod"]

	// llama-3.3-70b / llama-3.2-3b / Hermes-3 / QwQ carry the vendor 131072
	// window (models.dev openrouter meta-llama entries); the reseller lanes
	// (hyperbolic/nebius/fireworks/cerebras/openadapter/venice) had no row and
	// fell to the 128k floor. maxOutput stays on the family resolution.
	llama33 := Capabilities{Tools: true, ContextWindow: 131072}
	llama32_3b := Capabilities{Tools: true, ContextWindow: 131072}
	providerCapabilities["hyperbolic"] = map[string]Capabilities{
		"meta-llama/Llama-3.3-70B-Instruct":   llama33,
		"meta-llama/Llama-3.2-3B-Instruct":    llama32_3b,
		"NousResearch/Hermes-3-Llama-3.1-70B": {Tools: true, ContextWindow: 131072},
		"Qwen/QwQ-32B":                        {Reasoning: true, Tools: true, ThinkingFormat: "qwen", ThinkingCanDisable: new(false), ContextWindow: 131072},
	}
	providerCapabilities["hyp"] = providerCapabilities["hyperbolic"]
	providerCapabilities["nebius"] = map[string]Capabilities{
		"meta-llama/Llama-3.3-70B-Instruct": llama33,
	}
	providerCapabilities["fireworks"] = map[string]Capabilities{
		"accounts/fireworks/models/llama-v3p3-70b-instruct": llama33,
	}
	providerCapabilities["cerebras"] = map[string]Capabilities{
		"llama-3.3-70b": llama33,
	}
	providerCapabilities["openadapter"]["meta-llama/llama-3.3-70b-instruct"] = llama33
	providerCapabilities["venice"] = map[string]Capabilities{
		"hermes-3-llama-3.1-405b": {Tools: true, ContextWindow: 131072},
	}

	// nemotron-3-super-120b-a12b: 262144 on every gateway that files it
	// (models.dev openrouter/kilo/vercel). bazaarlink/bzl had no row.
	nemotronSuper := Capabilities{Reasoning: true, Tools: true, ContextWindow: 262144, MaxOutput: 16384}
	providerCapabilities["bazaarlink"] = map[string]Capabilities{"nemotron-3-super-120b-a12b": nemotronSuper}
	providerCapabilities["bzl"] = providerCapabilities["bazaarlink"]

	// pixtral-12b: mistral files 128000/128000 (models.dev); the dated id is
	// the same model. pixtral-large/mistral-small-4/saba have no published
	// window yet and keep the conservative default.
	providerCapabilities["mistral"] = map[string]Capabilities{
		"mistral-large-latest": {Vision: true, Tools: true, ContextWindow: 1048576},
		"mistral-large-4":      {Vision: true, Tools: true, ContextWindow: 1048576},
		"mistral-large-4-0":    {Vision: true, Tools: true, ContextWindow: 1048576},
		"pixtral-12b-2409":     {Vision: true, Tools: true, ContextWindow: 128000, MaxOutput: 128000},
	}

	// Cloudflare llama-3.1 fast/awq variants share the 32000 window the
	// fp8 sibling files (models.dev cloudflare-workers-ai).
	providerCapabilities["cloudflare-ai"]["@cf/meta/llama-3.1-8b-instruct-fp8-fast"] = Capabilities{ContextWindow: 32000, MaxOutput: 32000}
	providerCapabilities["cloudflare-ai"]["@cf/meta/llama-3.1-8b-instruct-awq"] = Capabilities{ContextWindow: 32000, MaxOutput: 32000}
	providerCapabilities["cloudflare-ai"]["@cf/meta/llama-3.1-70b-instruct-fp8-fast"] = Capabilities{ContextWindow: 32000, MaxOutput: 32000}

	// Cursor serves the real Anthropic models under its own ids: claude-4.5-*
	// are the 4.5 family (200000/64000 vendor window), claude-4.6-* the 4.6
	// family (1M/128000). The *claude* pattern carries no window, so these fell
	// to the 128k floor. Reasoning/thinking stay on the pattern (fixture-pinned).
	cursorClaude45 := Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-budget", ContextWindow: 200000, MaxOutput: 64000}
	cursorClaude46 := Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-budget", ContextWindow: 1000000, MaxOutput: 128000}
	providerCapabilities["cursor"] = map[string]Capabilities{
		"claude-4.5-haiku":                  cursorClaude45,
		"claude-4.5-sonnet":                 cursorClaude45,
		"claude-4.5-sonnet-thinking":        cursorClaude45,
		"claude-4.5-opus":                   cursorClaude45,
		"claude-4.5-opus-high":              cursorClaude45,
		"claude-4.5-opus-high-thinking":     cursorClaude45,
		"claude-4.6-opus-max":               cursorClaude46,
		"claude-4.6-sonnet-medium-thinking": cursorClaude46,
	}
	providerCapabilities["cu"] = providerCapabilities["cursor"]

	// perplexity-agent routes claude-sonnet-4-6 to the vendor model (1M/128000).
	providerCapabilities["perplexity-agent"] = map[string]Capabilities{
		"anthropic/claude-sonnet-4-6": {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-budget", ContextWindow: 1000000, MaxOutput: 128000},
	}
	providerCapabilities["pa"] = providerCapabilities["perplexity-agent"]

	// kenari free ids: llama-3.3-70b carries the vendor 131072 window; the
	// nemotron-3.5-super-vl free tier files 1M/131072 (models.dev openrouter).
	providerCapabilities["kenari"] = map[string]Capabilities{
		"llama-3-3-70b-instruct:free":       {Tools: true, ContextWindow: 131072},
		"nemotron-3-5-super-vl:free":        {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000, MaxOutput: 131072},
		"nvidia/nemotron-3.5-super-vl:free": {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000, MaxOutput: 131072},
	}

	// synthetic's NVFP4 quantization of nemotron-3-super-120b-a12b serves the
	// same 262144 window as the base weights (models.dev openrouter/kilo).
	providerCapabilities["synthetic"] = map[string]Capabilities{
		"hf:nvidia/NVIDIA-Nemotron-3-Super-120B-A12B-NVFP4": {Reasoning: true, Tools: true, ContextWindow: 262144, MaxOutput: 16384},
	}
	providerCapabilities["syn"] = providerCapabilities["synthetic"]

	// kenari's dots-3-note-preview free tier files 512000 (models.dev openrouter
	// dots-studio/dots-3-note-preview:free).
	providerCapabilities["kenari"]["dots-3-note-preview:free"] = Capabilities{Reasoning: true, Tools: true, ContextWindow: 512000}

	// kimchi's nemotron-3-ultra-fp4 is the FP4 quantization of nemotron-3-ultra
	// (1M window on the free tier, models.dev openrouter/nvidia).
	providerCapabilities["kimchi"] = map[string]Capabilities{
		"nemotron-3-ultra-fp4": {Vision: true, Reasoning: true, Tools: true, ContextWindow: 1000000},
	}

	initPatternIndex()
}

type patternCapability struct {
	pattern string
	caps    Capabilities
	frags   []string
}

func initPatternIndex() {
	for i := range patternCapabilities {
		p := &patternCapabilities[i]
		if p.frags != nil {
			continue
		}
		lower := strings.ToLower(p.pattern)
		for _, frag := range strings.Split(lower, "*") {
			if frag != "" {
				p.frags = append(p.frags, frag)
			}
		}
	}
}

var patternCapabilities = []patternCapability{
	// Ahead of the generic sonnet row: vendor-prefixed 5.x ids
	// ("anthropic/claude-sonnet-5", "openrouter/claude-sonnet-5") have no exact
	// entry, and budget thinking on them sends a token budget Anthropic no
	// longer accepts.
	{pattern: "*claude*sonnet-5*", caps: Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive"}},
	{pattern: "*claude*opus-5*", caps: Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive"}},
	{pattern: "*claude*opus-4.6*", caps: Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive"}},
	{pattern: "*claude*opus-4.7*", caps: Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive"}},
	{pattern: "*claude*opus-4.8*", caps: Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive"}},
	{pattern: "*claude*sonnet-4.6*", caps: Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive"}},
	{pattern: "*claude*sonnet-4.7*", caps: Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive"}},
	// Dashed ids (Bedrock `us.anthropic.claude-opus-4-7`) miss the dotted patterns above and
	// would fall to *claude*opus-4* (budget), which Opus 4.7/4.8 reject with a 400.
	{pattern: "*claude*opus-4-7*", caps: Capabilities{Vision: true, PDF: true, Reasoning: true, Search: true, ThinkingFormat: "claude-adaptive"}},
	{pattern: "*claude*opus-4-8*", caps: Capabilities{Vision: true, PDF: true, Reasoning: true, Search: true, ThinkingFormat: "claude-adaptive"}},
	{pattern: "*claude*haiku*", caps: Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-budget"}},
	{pattern: "*claude*opus*", caps: Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-budget"}},
	{pattern: "*claude*sonnet*", caps: Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-budget"}},
	// Fable/Mythos: thinking is always on — `enabled`+budget_tokens and `disabled` both 400.
	// Covers prefixed ids such as Bedrock `us.anthropic.claude-fable-5-1`.
	{pattern: "*claude*fable*", caps: Capabilities{Vision: true, PDF: true, Reasoning: true, Search: true, ThinkingFormat: "claude-adaptive", ThinkingCanDisable: new(false), ContextWindow: 1000000, MaxOutput: 128000}},
	{pattern: "*claude*mythos*", caps: Capabilities{Vision: true, PDF: true, Reasoning: true, Search: true, ThinkingFormat: "claude-adaptive", ThinkingCanDisable: new(false), ContextWindow: 1000000, MaxOutput: 128000}},
	{pattern: "*claude-3*", caps: Capabilities{Vision: true, Tools: true}},
	{pattern: "*claude*", caps: Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-budget"}},

	{pattern: "*gemini*image*", caps: Capabilities{Vision: true, ImageOutput: true, Tools: true, ContextWindow: 1048576}},
	// Gemini Omni is a reasoning model on the gemini-level wire with thinking
	// always on. Absent this row the generic *gemini* fallback (Reasoning
	// false) won, so GetThinkingLevels returned nil and the level never got
	// clamped. MUST stay above *gemini-4* / *gemini-3.8* for the same reason
	// as its patternThinking twin.
	{pattern: "*gemini*omni*", caps: Capabilities{Vision: true, PDF: true, AudioInput: true, VideoInput: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "gemini-level", ThinkingCanDisable: new(false)}},
	// Gemini 4 (Argon): LOW/MEDIUM/HIGH only — no "minimal" tier, no
	// thinkingBudget, and thinking cannot be disabled. Same row upstream's
	// PATTERN_CAPABILITIES carries; without it "gemini-argon" matched nothing
	// and every consumer fell through to the 128k/4096 floor. The bridge
	// publishes the bare id "gemini-argon" as well as the "gemini-4-argon"
	// alias, so both spellings have to hit this row.
	{pattern: "*gemini*argon*", caps: Capabilities{Vision: true, PDF: true, AudioInput: true, VideoInput: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "gemini-level", ThinkingCanDisable: new(false)}},
	// Gemini 4 Hydrogen: LOW/MEDIUM/HIGH/XHIGH — adds an xhigh tier above Argon,
	// thinking cannot be disabled. Same wire shape as Argon, higher ceiling.
	{pattern: "*gemini*hydrogen*", caps: Capabilities{Vision: true, PDF: true, AudioInput: true, VideoInput: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "gemini-level", ThinkingCanDisable: new(false)}},
	{pattern: "*gemini-4*", caps: Capabilities{Vision: true, AudioInput: true, VideoInput: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "gemini-level", ThinkingCanDisable: new(false), ContextWindow: 1048576, MaxOutput: 65536}},
	{pattern: "*gemini-3.8*", caps: Capabilities{Vision: true, AudioInput: true, VideoInput: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "gemini-level", ThinkingCanDisable: new(false), ContextWindow: 1048576, MaxOutput: 65536}},
	{pattern: "*gemini-3.7*", caps: Capabilities{Vision: true, AudioInput: true, VideoInput: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "gemini-level", ThinkingCanDisable: new(false), ContextWindow: 1048576, MaxOutput: 65536}},
	{pattern: "*gemini-3*pro*", caps: Capabilities{Vision: true, AudioInput: true, VideoInput: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "gemini-level", ThinkingCanDisable: new(false), ContextWindow: 1048576, MaxOutput: 65535}},
	{pattern: "*gemini-3*", caps: Capabilities{Vision: true, AudioInput: true, VideoInput: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "gemini-level", ThinkingCanDisable: new(false), ContextWindow: 1048576, MaxOutput: 65536}},
	{pattern: "*gemini-2.5*", caps: Capabilities{Vision: true, AudioInput: true, VideoInput: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "gemini-budget", ThinkingRange: &ThinkingRange{Min: 0, Max: 24576}, ContextWindow: 1048576, MaxOutput: 65536}},
	{pattern: "*gemini-2*", caps: Capabilities{Vision: true, AudioInput: true, VideoInput: true, Search: true, Tools: true, ContextWindow: 1048576, MaxOutput: 65536}},
	{pattern: "*gemini*", caps: Capabilities{Vision: true, Search: true, Tools: true, ContextWindow: 1048576}},
	{pattern: "*gemma*", caps: Capabilities{Vision: true, Tools: true, ContextWindow: 128000}},
	{pattern: "*nanobanana*", caps: Capabilities{Vision: true, ImageOutput: true, Tools: true}},

	// 1.05M is the API window for the whole gpt-6 family (astra, luna, sol alike).
	// It used to carry one gateway's 272k truncation, so every other provider's
	// gpt-6 models were published at 3.9x under their real window. A gateway
	// that really truncates lower says so in its own row below.
	// Image variants are image models, not chat models — first match wins, so
	// the row precedes the family entry (upstream PATTERN_CAPABILITIES).
	{pattern: "*gpt-6*image*", caps: Capabilities{ImageOutput: true, Tools: true}},
	{pattern: "*gpt-6*", caps: Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 1050000, MaxOutput: 128000}},
	{pattern: "*gpt-5*image*", caps: Capabilities{ImageOutput: true, Tools: true}},
	// The codex row precedes the numbered tiers (upstream order): gpt-5.6-codex
	// carries the 400k codex window, not the 1.05M tier window.
	{pattern: "*gpt-5*codex*", caps: Capabilities{Reasoning: true, Search: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 400000, MaxOutput: 128000}},
	// gpt-5.4 is where the 1.05M window starts, but the mini and nano tiers
	// stayed at 400k — first match wins, so those two have to be listed ahead of
	// it. The image row has to be ahead of all of them: gpt-5.6-sol-image would
	// otherwise match *gpt-5.6* and be published as a reasoning model, which is
	// how seven image entries ended up offering thinking levels that upstream
	// declares none for.
	{pattern: "*gpt-5.4-mini*", caps: Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 400000, MaxOutput: 128000}},
	{pattern: "*gpt-5.4-nano*", caps: Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 400000, MaxOutput: 128000}},
	{pattern: "*gpt-5.4*", caps: Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 1050000, MaxOutput: 128000}},
	{pattern: "*gpt-5.5*", caps: Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 1050000, MaxOutput: 128000}},
	{pattern: "*gpt-5.6*", caps: Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 1050000, MaxOutput: 128000}},

	{pattern: "*gpt-image*", caps: Capabilities{ImageOutput: true, Tools: true}},
	{pattern: "*gpt-5*", caps: Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 400000, MaxOutput: 128000}},
	{pattern: "*gpt-4o*", caps: Capabilities{Vision: true, Search: true, Tools: true, ContextWindow: 128000, MaxOutput: 16384}},
	{pattern: "*gpt-4.1*", caps: Capabilities{Vision: true, Tools: true, ContextWindow: 1000000, MaxOutput: 32768}},
	{pattern: "*gpt-4-turbo*", caps: Capabilities{Vision: true, Tools: true, ContextWindow: 128000}},
	{pattern: "*gpt-4*", caps: Capabilities{Tools: true, ContextWindow: 128000}},
	{pattern: "*gpt-3.5*", caps: Capabilities{Tools: true, ContextWindow: 16385, MaxOutput: 4096}},
	// gpt-oss carries a 131072 window on every gateway that publishes it
	// (models.dev: groq/cerebras/vercel/openrouter all agree); the old 128000
	// was a rounded guess. maxOutput stays unset so each gateway's catalog
	// figure (65536 groq/openrouter, 131072 vercel) fills it.
	{pattern: "*gpt-oss*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 131072}},
	{pattern: "*solar-pro*", caps: Capabilities{Reasoning: true, Tools: true}},

	{pattern: "*o1-mini*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 128000}},
	{pattern: "*o1*", caps: Capabilities{Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 200000, MaxOutput: 100000}},
	{pattern: "*o3*", caps: Capabilities{Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 200000, MaxOutput: 100000}},
	{pattern: "*o4*", caps: Capabilities{Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 200000, MaxOutput: 100000}},

	{pattern: "*grok*image*", caps: Capabilities{ImageOutput: true, Tools: true}},
	{pattern: "*grok-code*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 256000}},
	// Grok 4.7 needs its own row: without it the id falls to the *grok-4*
	// catch-all and then to the token-limit table's 128000/4096 floor, and
	// combos report the smallest member window. Upstream b0655314 probed the
	// 500k limit on the Grok CLI proxy (476k prompt accepted, 614k rejected
	// with "613738 tokens > 500000 tokens").
	{pattern: "*grok-4.7*", caps: Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 500000, MaxOutput: 500000}},
	{pattern: "*grok-4.6*", caps: Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 500000, MaxOutput: 500000}},
	{pattern: "*grok-4.5*", caps: Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 500000, MaxOutput: 64000}},
	{pattern: "*grok-4*", caps: Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 256000}},
	{pattern: "*grok-3*", caps: Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 131072}},
	{pattern: "*grok*", caps: Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 256000}},

	// Limits and modalities mirror upstream PATTERN_CAPABILITIES (master
	// aba4c45d..1855aa44). Without them every qwen id fell to the substring
	// table's 131072/8192 floor — nous/qwen4-max published 131k against the
	// 1M window the provider's own /v1/models declares.
	{pattern: "*qwen*vl*", caps: Capabilities{Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "qwen", ContextWindow: 262144}},
	{pattern: "*qwen*omni*", caps: Capabilities{Vision: true, AudioInput: true, VideoInput: true, Reasoning: true, Tools: true, ThinkingFormat: "qwen", ContextWindow: 262144, MaxOutput: 65536}},
	{pattern: "*qwen*coder*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "qwen", ContextWindow: 1000000}},
	{pattern: "*qwen*max*", caps: Capabilities{Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "qwen", ContextWindow: 1000000, MaxOutput: 65536}},
	{pattern: "*qwen3.5*", caps: Capabilities{Vision: true, VideoInput: true, Reasoning: true, Tools: true, ThinkingFormat: "qwen", ContextWindow: 1000000, MaxOutput: 65536}},
	{pattern: "*qwen3.6*", caps: Capabilities{Vision: true, VideoInput: true, Reasoning: true, Tools: true, ThinkingFormat: "qwen", ContextWindow: 1000000, MaxOutput: 65536}},
	{pattern: "*qwen3.7*", caps: Capabilities{Vision: true, VideoInput: true, Reasoning: true, Tools: true, ThinkingFormat: "qwen", ContextWindow: 1000000, MaxOutput: 65536}},
	// *qwen3.8* is a Go-only row (upstream has none): limits follow the 3.5–3.7
	// family, but ThinkingFormat stays unset — the qwen wire dialect on these
	// ids is unverified upstream, so the transport default keeps serving them.
	{pattern: "*qwen3.8*", caps: Capabilities{Vision: true, VideoInput: true, Reasoning: true, Tools: true, ContextWindow: 1000000, MaxOutput: 65536}},
	{pattern: "*qwen*plus*", caps: Capabilities{Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "qwen", ContextWindow: 1000000, MaxOutput: 65536}},
	{pattern: "*qwen*235b*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "qwen", ContextWindow: 262144}},
	{pattern: "*qwq*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "qwen", ThinkingCanDisable: new(false), ContextWindow: 131072}},
	{pattern: "*qwen*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "qwen", ContextWindow: 262144}},

	{pattern: "*kimi*k3*", caps: Capabilities{Vision: true, VideoInput: true, Reasoning: true, Tools: true, ThinkingFormat: "kimi", ThinkingCanDisable: new(false), ContextWindow: 1048576, MaxOutput: 131072}},
	{pattern: "*kimi*for-coding*", caps: Capabilities{Vision: true, VideoInput: true, Reasoning: true, Tools: true, ThinkingFormat: "kimi", ThinkingCanDisable: new(false), ContextWindow: 262144, MaxOutput: 65536}},
	{pattern: "*kimi*k2.7*code*", caps: Capabilities{Vision: true, VideoInput: true, Reasoning: true, Tools: true, ThinkingFormat: "kimi", ThinkingCanDisable: new(false), ContextWindow: 262144, MaxOutput: 65536}},
	{pattern: "*kimi*k2*", caps: Capabilities{Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "kimi", ContextWindow: 262144, MaxOutput: 262144}},
	{pattern: "*kimi*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "kimi", ContextWindow: 262144}},

	// GLM-5.2/5.3 carry a 1M window; the `*glm-5*` catch-all below is 200k, so
	// both versions need their own row (upstream #4544). The thinking
	// disable rule is set on the pattern rather than only on the exact
	// glm-5.3-flash entry above, so plain `glm-5.3` gets it too.
	//
	// glm-5.2 deliberately keeps no ThinkingCanDisable: the exact entry at
	// line 178 already declares it false and that predates #4656. Upstream
	// considers that entry probably wrong — z.ai's docs suggest 5.2 can
	// disable thinking — but left it out of scope; don't "fix" it here.
	{pattern: "*glm-5.3*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "zai", ThinkingEffortSupported: true, ThinkingCanDisable: new(false), ContextWindow: 1000000, MaxOutput: 128000}},
	{pattern: "*glm-5.2*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "zai", ThinkingEffortSupported: true, ContextWindow: 1000000, MaxOutput: 128000}},
	{pattern: "*glm-5*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "zai", ContextWindow: 200000, MaxOutput: 128000}},
	{pattern: "*glm-4.7*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "zai", ContextWindow: 200000, MaxOutput: 128000}},
	{pattern: "*glm-4*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "zai", ContextWindow: 200000}},
	{pattern: "*glm*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "zai", ContextWindow: 200000}},
	{pattern: "*z-ai*", caps: Capabilities{Reasoning: true, Tools: true}},
	{pattern: "*zai*", caps: Capabilities{Reasoning: true, Tools: true}},

	{pattern: "*deepseek-v4.*", caps: Capabilities{Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "deepseek", ThinkingEffortSupported: true, ContextWindow: 1000000, MaxOutput: 128000}},
	{pattern: "*deepseek-v4*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "deepseek", ThinkingEffortSupported: true, ContextWindow: 1000000, MaxOutput: 384000}},
	{pattern: "*deepseek*flash*", caps: Capabilities{Vision: true, Reasoning: true, Tools: true}},
	{pattern: "*reasoner*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "deepseek", ThinkingCanDisable: new(false), ContextWindow: 128000}},
	{pattern: "*deepseek-r*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "deepseek", ThinkingCanDisable: new(false), ContextWindow: 128000}},
	{pattern: "*deepseek-chat*", caps: Capabilities{Tools: true, ContextWindow: 128000}},
	{pattern: "*deepseek*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "deepseek", ContextWindow: 128000}},

	{pattern: "*minimax*image*", caps: Capabilities{ImageOutput: true, Tools: true}},
	{pattern: "*minimax-m3*", caps: Capabilities{Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "minimax", ContextWindow: 1000000, MaxOutput: 131072}},
	{pattern: "*minimax-m2.7*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "minimax", ThinkingCanDisable: new(false), ContextWindow: 204800, MaxOutput: 131072}},
	{pattern: "*minimax-m2.5*", caps: Capabilities{Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "minimax", ThinkingCanDisable: new(false), ContextWindow: 204800, MaxOutput: 131072}},
	{pattern: "*minimax*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "minimax", ThinkingCanDisable: new(false), ContextWindow: 200000, MaxOutput: 131072}},

	{pattern: "*mimo*v2.6*", caps: Capabilities{Vision: true, AudioInput: true, VideoInput: true, Reasoning: true, Tools: true, ThinkingFormat: "deepseek", ThinkingCanDisable: new(false), ContextWindow: 1048576, MaxOutput: 131072}},
	{pattern: "*mimo*v2.5*", caps: Capabilities{Vision: true, AudioInput: true, VideoInput: true, Reasoning: true, Tools: true, ThinkingFormat: "deepseek", ThinkingCanDisable: new(false), ContextWindow: 1048576, MaxOutput: 131072}},
	{pattern: "*mimo*omni*", caps: Capabilities{Vision: true, AudioInput: true, Reasoning: true, Tools: true, ThinkingFormat: "deepseek", ThinkingCanDisable: new(false), ContextWindow: 262144, MaxOutput: 131072}},
	{pattern: "*mimo*", caps: Capabilities{Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "deepseek", ThinkingCanDisable: new(false), ContextWindow: 262144, MaxOutput: 131072}},

	{pattern: "*llama-4*", caps: Capabilities{Vision: true, Tools: true, ContextWindow: 1000000}},
	{pattern: "*llama*", caps: Capabilities{Tools: true, ContextWindow: 128000}},

	{pattern: "*codestral*", caps: Capabilities{Tools: true, ContextWindow: 256000}},
	{pattern: "*devstral*", caps: Capabilities{Vision: true, Tools: true}},
	{pattern: "*mistral-large*", caps: Capabilities{Vision: true, Tools: true, ContextWindow: 256000}},
	{pattern: "*mistral*", caps: Capabilities{Tools: true, ContextWindow: 128000}},

	{pattern: "*command-a-vision*", caps: Capabilities{Vision: true, Tools: true, ContextWindow: 128000}},
	{pattern: "*command*", caps: Capabilities{Tools: true, ContextWindow: 128000}},

	{pattern: "*sonar*", caps: Capabilities{Search: true, Tools: true, ContextWindow: 128000}},
	{pattern: "*pplx*", caps: Capabilities{Search: true, Tools: true, ContextWindow: 128000}},
	{pattern: "*perplexity*", caps: Capabilities{Search: true, Tools: true, ContextWindow: 128000}},

	{pattern: "*laguna-s-2.1*free*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 200000, MaxOutput: 32000}},
	{pattern: "*laguna-s-2.1*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 1000000, MaxOutput: 32000}},
	{pattern: "*laguna*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 200000, MaxOutput: 32000}},

	{pattern: "*muse*spark*", caps: Capabilities{Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 1048576, MaxOutput: 131072}},
	{pattern: "*hunyuan*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "hunyuan", ContextWindow: 262144, MaxOutput: 262144}},
	{pattern: "hy3*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "hunyuan", ContextWindow: 262144, MaxOutput: 262144}},
	{pattern: "*step-*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "step", ContextWindow: 128000}},
	{pattern: "*nemotron*", caps: Capabilities{Reasoning: true, Tools: true, ContextWindow: 128000}},
	{pattern: "*ling-*", caps: Capabilities{Reasoning: true, Tools: true, ContextWindow: 128000}},
	{pattern: "*muse-spark*", caps: Capabilities{Vision: true, Reasoning: true, Tools: true}},
}

func fragsMatch(p *patternCapability, lower string) bool {
	for _, frag := range p.frags {
		if !strings.Contains(lower, frag) {
			return false
		}
	}
	return true
}

// matchPattern checks if a string matches a glob pattern (only supports * as wildcard)
func matchPattern(pattern, s string) bool {
	matched, err := path.Match(strings.ToLower(pattern), strings.ToLower(s))
	if err != nil {
		return false
	}
	return matched
}

var (
	tokenLimitsCacheMu  sync.RWMutex
	tokenLimitsCache    = make(map[unique.Handle[string]][2]int)
	tokenLimitsCacheMax = 20000
)

// GetModelTokenLimits returns the context window and maximum output tokens for a model.
func GetModelTokenLimits(model string) (contextWindow int, maxOutput int) {
	key := unique.Make(model)
	tokenLimitsCacheMu.RLock()
	if cached, ok := tokenLimitsCache[key]; ok {
		tokenLimitsCacheMu.RUnlock()
		return cached[0], cached[1]
	}
	tokenLimitsCacheMu.RUnlock()
	res := lookupModelTokenLimits(model)
	tokenLimitsCacheMu.Lock()
	if len(tokenLimitsCache) >= tokenLimitsCacheMax {
		tokenLimitsCache = make(map[unique.Handle[string]][2]int, tokenLimitsCacheMax)
	}
	tokenLimitsCache[key] = res
	tokenLimitsCacheMu.Unlock()
	return res[0], res[1]
}

// providerTokenLimits holds per-provider limit declarations for gateways whose
// figures differ from what the provider-blind substring table would guess.
//
// The substring table answers from the model name alone, so it has to pick one
// number for every gateway serving that name. Where a gateway publishes its own
// catalog — CodeBuddy declares 393216 output for deepseek-v4.1-flash — the
// provider-qualified figure belongs here instead of in the shared table.
//
// Entries here are consulted by GetCapabilitiesDetailForModel before the
// models.dev catalog and the substring table, and they carry limits only: the
// capability flags and thinking shape still resolve through the normal chain,
// which is why this is not expressed as a providerCapabilities row (those win
// over patterns and would drag their thinking format along).
var providerTokenLimits = map[string]map[string][2]int{
	// CodeBuddy's product-config catalog, the same figures codebuddy-cn
	// declares in providerCapabilities. codebuddy-intl serves the same ids
	// but resolves its own thinking levels, so only the limits are shared.
	"codebuddy-intl": {
		"glm-5.2":             {1000000, 131072},
		"glm-5.1":             {200000, 48000},
		"glm-5.3":             {1000000, 131072},
		"glm-5.3-flash":       {1000000, 131072},
		"minimax-m3":          {512000, 524288},
		"kimi-k2.5":           {164000, 32000},
		"deepseek-v4-pro":     {1000000, 393216},
		"deepseek-v4.1-flash": {1000000, 393216},
	},
}

// providerCapabilityKeys returns the published provider id first, then its
// canonical id. The static capability tables are keyed by canonical ids, but
// /v1/models publishes short aliases (`oc`, `cf`, ...), so an alias-only lookup
// silently skipped the provider's own limits and inherited a generic guess.
func providerCapabilityKeys(provider string) []string {
	keys := []string{provider}
	if canonical := ResolveAlias(provider); canonical != "" && canonical != provider {
		keys = append(keys, canonical)
	}
	return keys
}

// lookupProviderTokenLimits returns the limits a provider declares for a model,
// or (0, 0) when it declares none. It tries the published id and its canonical
// id so an alias sees the same provider-qualified declaration.
func lookupProviderTokenLimits(provider, model string) (contextWindow, maxOutput int) {
	base := model
	if _, after, found := strings.CutLast(model, "/"); found {
		base = after
	}
	for _, key := range providerCapabilityKeys(provider) {
		byModel, ok := providerTokenLimits[strings.ToLower(key)]
		if !ok {
			continue
		}
		if limits, found := byModel[base]; found {
			return limits[0], limits[1]
		}
		if limits, found := byModel[model]; found {
			return limits[0], limits[1]
		}
	}
	return 0, 0
}

// lookupCanonicalStaticLimits copies only the token limits from the canonical
// provider's static capability entry when the published alias has none. It must
// not copy thinking shape or modalities: those stay keyed by the published id,
// which is what the thinking-level fixtures assert.
func lookupCanonicalStaticLimits(provider, model string) (contextWindow, maxOutput int) {
	canonical := ResolveAlias(provider)
	if canonical == "" || canonical == provider {
		return 0, 0
	}
	pCaps, ok := providerCapabilities[canonical]
	if !ok {
		return 0, 0
	}
	baseModel := model
	if _, after, found := strings.CutLast(model, "/"); found {
		baseModel = after
	}
	if c, found := pCaps[model]; found {
		return c.ContextWindow, c.MaxOutput
	}
	if c, found := pCaps[baseModel]; found {
		return c.ContextWindow, c.MaxOutput
	}
	return 0, 0
}

func lookupModelTokenLimits(model string) [2]int {
	m := strings.ToLower(model)

	switch {
	case strings.Contains(m, "gpt-5.4-mini") || strings.Contains(m, "gpt-5.4-nano"):
		return [2]int{400000, 128000}
	case strings.Contains(m, "gpt-5.4") || strings.Contains(m, "gpt-5.5") || strings.Contains(m, "gpt-5.6") || strings.Contains(m, "gpt-6"):
		return [2]int{1050000, 128000}
	case strings.Contains(m, "deepseek-v4.1-flash") || strings.Contains(m, "deepseek-v4-flash"):
		return [2]int{1000000, 128000}
	case strings.Contains(m, "gemini-1.5") || strings.Contains(m, "gemini-2.0") || strings.Contains(m, "gemini-2.5") || strings.Contains(m, "gemini-3") || strings.Contains(m, "gemini-4") || strings.Contains(m, "gemini-argon") || strings.Contains(m, "glm-5.3-flash"):
		return [2]int{1048576, 65536}
	case strings.Contains(m, "grok-4.7"):
		return [2]int{500000, 500000}
	case strings.Contains(m, "grok-4.5") || strings.Contains(m, "grok-4.6"):
		return [2]int{524288, 32768}
	case strings.Contains(m, "claude-3") || strings.Contains(m, "claude-sonnet") || strings.Contains(m, "claude-opus") || strings.Contains(m, "claude-haiku"):
		return [2]int{200000, 8192}
	case strings.Contains(m, "gpt-4o") || strings.Contains(m, "gpt-4-turbo") || strings.Contains(m, "gpt-4.1") || strings.Contains(m, "gpt-5"):
		return [2]int{128000, 16384}
	case strings.Contains(m, "solar-pro") || strings.Contains(m, "longcat"):
		return [2]int{200000, 32000}
	case strings.Contains(m, "o1") || strings.Contains(m, "o3"):
		return [2]int{200000, 100000}
	case strings.Contains(m, "deepseek") || strings.Contains(m, "qwen") || strings.Contains(m, "glm") || strings.Contains(m, "kimi"):
		return [2]int{131072, 8192}
	// MiMo V2.5/V2.6 declare 1M/128k upstream (*mimo*v2.5*, *mimo*v2.6*); the
	// generic `*mimo*` row stays at 262144/131072, so only the numbered
	// families belong here.
	case strings.Contains(m, "mimo-v2.6") || strings.Contains(m, "mimo-v2.5"):
		return [2]int{1048576, 131072}
	default:
		return [2]int{128000, 4096}
	}
}

// GetCapabilitiesForModel resolves capabilities using the fallback chain.
func GetCapabilitiesForModel(provider, model string) Capabilities {
	if model == "" {
		return DefaultCapabilities
	}

	key := unique.Make(provider + "||" + model)
	capsCacheMu.RLock()
	if cached, ok := capsCache[key]; ok {
		capsCacheMu.RUnlock()
		return cached
	}
	capsCacheMu.RUnlock()

	baseModel := model
	if _, after, ok := strings.CutLast(model, "/"); ok {
		baseModel = after
	}

	// CommandCode routes every model through one /alpha/generate wire, so the
	// per-family patterns below (deepseek-v4 → vision:false, thinkingFormat
	// deepseek, …) must not win here. Upstream short-circuits the same way
	// (open-sse/providers/capabilities.js:570).
	// The goat entry is the documented Provider API: three lanes, per-model
	// windows from the live feed, and a thinking shape that follows the lane the
	// model is declared on. It must not inherit the /alpha/generate block below,
	// whose single-lane assumptions (1M window for everything, the CLI thinking
	// writer) are exactly what this provider exists to correct.
	if isCommandCodeGoatProvider(provider) {
		res := commandCodeGoatCapabilities(model)
		capsCacheMu.Lock()
		capsCache[key] = res
		capsCacheMu.Unlock()
		return res
	}

	if isCommandCodeProvider(provider) {
		res := commandCodeCapabilities(model)
		capsCacheMu.Lock()
		capsCache[key] = res
		capsCacheMu.Unlock()
		return res
	}

	// 1. Provider-specific override
	var res Capabilities
	resolved := false

	if provider != "" {
		if pCaps, ok := providerCapabilities[provider]; ok {
			if caps, ok := pCaps[model]; ok {
				res = mergeCapabilities(DefaultCapabilities, caps)
				resolved = true
			} else if caps, ok := pCaps[baseModel]; ok {
				res = mergeCapabilities(DefaultCapabilities, caps)
				resolved = true
			}
		}
	}

	// 2. Canonical exact
	if !resolved {
		if caps, ok := modelCapabilities[baseModel]; ok {
			res = mergeCapabilities(DefaultCapabilities, caps)
			resolved = true
		} else if caps, ok := modelCapabilities[model]; ok {
			res = mergeCapabilities(DefaultCapabilities, caps)
			resolved = true
		}
	}

	if !resolved {
		lowerBase, lowerFull := strings.ToLower(baseModel), strings.ToLower(model)
		for i := range patternCapabilities {
			p := &patternCapabilities[i]
			if !fragsMatch(p, lowerBase) && !fragsMatch(p, lowerFull) {
				continue
			}
			if matchPattern(p.pattern, baseModel) || matchPattern(p.pattern, model) {
				res = mergeCapabilities(DefaultCapabilities, p.caps)
				resolved = true
				break
			}
		}
	}
	if !resolved {
		res = DefaultCapabilities
	}

	// 5. Dynamic synced catalog overlay (only ever turns capabilities ON)
	if dynamic := GetCatalogModalities(provider, model); dynamic != nil {
		if dynamic.Vision {
			res.Vision = true
		}
		if dynamic.PDF {
			res.PDF = true
		}
		if dynamic.AudioInput {
			res.AudioInput = true
		}
		if dynamic.VideoInput {
			res.VideoInput = true
		}
	}

	// 6. Custom model caps (from kv customModels) — additive, like dynamic,
	// except the token limits: those are a declaration, so a declared number
	// replaces the table's guess instead of only filling a gap.
	if custom, ok := lookupCustomModelCaps(provider, model); ok {
		applyCustomCaps(&res, custom)
	}

	// Last resort: a model id that names its modality ("qwen3-vl-plus",
	// "glm-4.6v") is a vision model even when no table knows it yet. Only
	// ever turns vision ON.
	if !res.Vision && looksLikeVisionModel(baseModel) {
		res.Vision = true
	}

	// A speech-synthesis model emits audio, and models_list's buildModalities
	// derives output_modalities from this flag alone — so a TTS model without
	// it is published as text-only and never reaches the audio lane, while its
	// endpoint happily answers audio/wav.
	//
	// The name signal does the work. The registry's per-provider service kind
	// is the more authoritative source but cannot stand alone: it needs an
	// exact ProviderModelKinds[provider][model] hit, which a custom provider
	// node never has (no registry entry, no alias), so the reported case —
	// studio/gemini-3.8-flash-tts on an operator-defined node — is reachable
	// by name only. The same gap hits a builtin that carries a TTS id without
	// declaring its kind (vertex, ag) and the eight mimo pairs on
	// xiaomi-tokenplan / xmtp.
	//
	// The kind check is kept as a fallback for a classified id whose name
	// carries no signal at all. Removing it breaks no test today, so it is
	// deliberate slack, not redundancy — ttsName would have nothing to match
	// in a future id like "sonic-2".
	//
	// Only ever turns audio output ON, like the vision rule above. It must stay
	// that narrow: upstream advertises thinking levels for its TTS models
	// (13 pairs in testdata/thinking_levels.json), so clearing Reasoning or the
	// thinking format here would break that parity.
	if !res.AudioOutput &&
		(GetProviderModelKind(provider, model) == serviceKindTTS || looksLikeTTSModel(baseModel)) {
		res.AudioOutput = true
	}

	capsCacheMu.Lock()
	capsCache[key] = res
	capsCacheMu.Unlock()

	return res
}

// ExplicitToolSupport reports whether the static registry explicitly declares
// Tools:true for provider/model. Unlike GetCapabilitiesForModel it does not
// inherit DefaultCapabilities.Tools — which is true for every unknown model —
// so callers can distinguish "declared tool-capable" from "not declared".
// Custom overrides and dynamic catalog modalities are intentionally ignored:
// neither can turn tool support on, and a provider-specific request filter
// must not be widened by an unrelated modalities refresh.
func ExplicitToolSupport(provider, model string) bool {
	if provider == "" || model == "" {
		return false
	}
	caps, ok := providerCapabilities[provider]
	if !ok {
		return false
	}
	if c, ok := caps[model]; ok {
		return c.Tools
	}
	if _, after, ok := strings.CutLast(model, "/"); ok {
		if c, ok := caps[after]; ok {
			return c.Tools
		}
	}
	return false
}

func mergeCapabilities(base, overlay Capabilities) Capabilities {
	if overlay.Vision {
		base.Vision = true
	}
	if overlay.PDF {
		base.PDF = true
	}
	if overlay.AudioInput {
		base.AudioInput = true
	}
	if overlay.VideoInput {
		base.VideoInput = true
	}
	if overlay.ImageOutput {
		base.ImageOutput = true
	}
	if overlay.AudioOutput {
		base.AudioOutput = true
	}
	if overlay.Search {
		base.Search = true
	}
	if !overlay.Tools {
		// if specifically disabled (Tools is true by default usually, but we check if we need to turn it off)
		// Wait, the merge logic in JS is { ...DEFAULT, ...caps }.
		// So if overlay sets tools: false, it should be false.
		// In Go, bool zero value is false. So we can't tell if overlay didn't set it, or set it to false.
		// However, in our hardcoded maps above, I explicitly included Tools: true for all that have it,
		// and we can assume any overlay boolean that is `false` is meant to be false if it differs from default.
		// Actually, to make it simple, let's just use the overlay if it has truthy values, except Tools which we default to true.
		// Let's just do a naive merge.
	}
	// For Go, since we define complete Capabilities structs in the maps with Tools: true where needed:
	res := Capabilities{
		Vision:      base.Vision || overlay.Vision,
		PDF:         base.PDF || overlay.PDF,
		AudioInput:  base.AudioInput || overlay.AudioInput,
		VideoInput:  base.VideoInput || overlay.VideoInput,
		ImageOutput: base.ImageOutput || overlay.ImageOutput,
		AudioOutput: base.AudioOutput || overlay.AudioOutput,
		Search:      base.Search || overlay.Search,
		Tools:       overlay.Tools, // We made sure to set Tools:true in all overlays where it applies. If it's omitted, it becomes false. Wait, DefaultCapabilities has Tools=true. Let's make sure our maps above have Tools:true for everything except gpt-image-1.
		Reasoning:   base.Reasoning || overlay.Reasoning,
		// Thinking is a declaration, not a flag: an overlay that names no
		// thinking format says nothing about thinking and must not clear the
		// base ThinkingCanDisable=true. The whole block is taken from the
		// overlay as soon as it declares a format, mirroring the JS spread
		// where an absent key keeps the default.
		ThinkingCanDisable: base.ThinkingCanDisable,
		ThinkingOffType:    base.ThinkingOffType,
		ForcedToolChoice:   base.ForcedToolChoice,
		// Limits are a declaration, not a flag: an overlay naming no
		// contextWindow/maxOutput says nothing about them, so the base's
		// numbers survive. Without this, every provider and exact-model row
		// blanked the pattern table's limits and the substring table below
		// republished the family default instead.
		ContextWindow: base.ContextWindow,
		MaxOutput:     base.MaxOutput,
	}
	if overlay.ThinkingFormat != "" {
		res.ThinkingFormat = overlay.ThinkingFormat
		res.ThinkingCanDisable = overlay.ThinkingCanDisable
		res.ThinkingOffType = overlay.ThinkingOffType
		res.ThinkingRange = overlay.ThinkingRange
		res.ThinkingEffortSupported = overlay.ThinkingEffortSupported
		res.ForcedToolChoice = overlay.ForcedToolChoice
	}
	if overlay.ContextWindow != 0 {
		res.ContextWindow = overlay.ContextWindow
	}
	if overlay.MaxOutput != 0 {
		res.MaxOutput = overlay.MaxOutput
	}
	return res
}

// CapabilitiesDetail matches the serializable capabilities object expected by
// clients in /v1/models (upstream getCapabilitiesForModel: vision, pdf,
// audioInput, videoInput, imageOutput, audioOutput, search, tools, reasoning,
// thinkingFormat, thinkingCanDisable, thinkingRange, contextWindow, maxOutput).
type CapabilitiesDetail struct {
	Vision                  bool    `json:"vision"`
	PDF                     bool    `json:"pdf"`
	AudioInput              bool    `json:"audioInput"`
	VideoInput              bool    `json:"videoInput"`
	ImageOutput             bool    `json:"imageOutput"`
	AudioOutput             bool    `json:"audioOutput"`
	Search                  bool    `json:"search"`
	Tools                   bool    `json:"tools"`
	Reasoning               bool    `json:"reasoning"`
	Jailbreak               bool    `json:"jailbreak"`
	ThinkingFormat          *string `json:"thinkingFormat"`
	ThinkingCanDisable      bool    `json:"thinkingCanDisable"`
	ThinkingRange           any     `json:"thinkingRange"`
	ThinkingEffortSupported bool    `json:"thinkingEffortSupported"`
	ThinkingOffType         string  `json:"thinkingOffType,omitempty"`
	ForcedToolChoice        *bool   `json:"forcedToolChoice,omitempty"`
	ContextWindow           int     `json:"contextWindow,omitempty"`
	MaxOutput               int     `json:"maxOutput,omitempty"`
}

// ComboCapabilities is the aggregated block upstream publishes for a combo
// (aggregateComboCapabilities). It is deliberately a different type: the union
// drops thinkingEffortSupported, booleans fold with `some` — except tools, which
// requires *every* leaf to support it — the thinking fields come from the first
// leaf, the context window is the narrowest leaf and maxOutput the narrowest
// positive leaf (zero/unbounded never wins unless every leaf is unbounded).
type ComboCapabilities struct {
	Vision             bool    `json:"vision"`
	PDF                bool    `json:"pdf"`
	AudioInput         bool    `json:"audioInput"`
	VideoInput         bool    `json:"videoInput"`
	ImageOutput        bool    `json:"imageOutput"`
	AudioOutput        bool    `json:"audioOutput"`
	Search             bool    `json:"search"`
	Tools              bool    `json:"tools"`
	Reasoning          bool    `json:"reasoning"`
	ThinkingFormat     *string `json:"thinkingFormat"`
	ThinkingCanDisable bool    `json:"thinkingCanDisable"`
	ThinkingRange      any     `json:"thinkingRange"`
	ContextWindow      int     `json:"contextWindow"`
	MaxOutput          int     `json:"maxOutput"`
}

// AggregateComboCapabilities folds leaf capability blocks with upstream's exact
// rules. An empty list yields no aggregate.
func AggregateComboCapabilities(leaves []CapabilitiesDetail) (ComboCapabilities, bool) {
	if len(leaves) == 0 {
		return ComboCapabilities{}, false
	}
	first := leaves[0]
	merged := ComboCapabilities{
		Reasoning:          first.Reasoning,
		ThinkingFormat:     first.ThinkingFormat,
		ThinkingCanDisable: first.ThinkingCanDisable,
		ThinkingRange:      first.ThinkingRange,
		ContextWindow:      first.ContextWindow,
		MaxOutput:          first.MaxOutput,
		Tools:              true,
	}
	for _, leaf := range leaves {
		merged.Vision = merged.Vision || leaf.Vision
		merged.PDF = merged.PDF || leaf.PDF
		merged.AudioInput = merged.AudioInput || leaf.AudioInput
		merged.VideoInput = merged.VideoInput || leaf.VideoInput
		merged.ImageOutput = merged.ImageOutput || leaf.ImageOutput
		merged.AudioOutput = merged.AudioOutput || leaf.AudioOutput
		merged.Search = merged.Search || leaf.Search
		merged.Tools = merged.Tools && leaf.Tools
		if leaf.ContextWindow > 0 && (merged.ContextWindow == 0 || leaf.ContextWindow < merged.ContextWindow) {
			merged.ContextWindow = leaf.ContextWindow
		}
		if leaf.MaxOutput > 0 && (merged.MaxOutput == 0 || leaf.MaxOutput < merged.MaxOutput) {
			merged.MaxOutput = leaf.MaxOutput
		}
	}
	return merged, true
}

// GetCapabilitiesDetailForModel returns the full JSON-serializable capabilities
// map for /v1/models, matching upstream's key set and its
// context_length / max_completion_tokens mirrors.
func GetCapabilitiesDetailForModel(provider, model string) CapabilitiesDetail {
	caps := GetCapabilitiesForModel(provider, model)
	// Each level only fills what is still unset, so a source that declares
	// just one of the two numbers does not block the next level from
	// answering the other. Order is most specific first:
	//
	//   1. a custom row that declares contextWindow/maxOutput — the operator's
	//      own declaration for this exact endpoint,
	//   2. a provider-qualified limit declaration (providerTokenLimits),
	//   3. a provider entry that declares its own limits (CommandCode, the
	//      codebuddy catalogs) — the gateway's own number,
	//   4. the models.dev-synced catalog keyed by provider + model — the
	//      authoritative per-gateway figure,
	//   5. whatever GetCapabilitiesForModel already resolved, which past this
	//      point can only be a pattern-table guess,
	//   6. the substring table,
	//   7. the 128k floor.
	//
	// The catalog has to outrank the pattern table even when the pattern
	// already filled ContextWindow: `*glm-5.3*` hardcodes 1000000/128000, but
	// OpenCode's free tier serves the same weights with a 200000/32000 window.
	// Letting the pattern win published 1M for a 200k endpoint, so clients
	// budgeted five times the context the gateway would actually accept.
	var cw, maxOut int
	fill := func(c, m int) {
		if cw == 0 && c > 0 {
			cw = c
		}
		if maxOut == 0 && m > 0 {
			maxOut = m
		}
	}

	if custom, ok := lookupCustomModelCaps(provider, model); ok {
		fill(custom.ContextWindow, custom.MaxOutput)
	}
	if c, m := lookupProviderTokenLimits(provider, model); c > 0 || m > 0 {
		fill(c, m)
	}
	if pCaps, ok := providerCapabilities[provider]; ok {
		baseModel := model
		if _, after, found := strings.CutLast(model, "/"); found {
			baseModel = after
		}
		if c, found := pCaps[model]; found {
			fill(c.ContextWindow, c.MaxOutput)
		} else if c, found := pCaps[baseModel]; found {
			fill(c.ContextWindow, c.MaxOutput)
		}
	}
	if c, m := lookupCanonicalStaticLimits(provider, model); c > 0 || m > 0 {
		fill(c, m)
	}
	if c, m := GetCatalogLimits(provider, model); c > 0 || m > 0 {
		fill(c, m)
	}
	fill(caps.ContextWindow, caps.MaxOutput)
	if c, m := GetModelTokenLimits(model); c > 0 || m > 0 {
		fill(c, m)
	}
	if c, m := GetModelTokenLimits(provider + "/" + model); c > 0 || m > 0 {
		fill(c, m)
	}
	if cw == 0 {
		cw = 128000
	}
	var thinkingFormat *string
	if caps.ThinkingFormat != "" {
		f := caps.ThinkingFormat
		thinkingFormat = &f
	}
	var thinkingRange any
	if caps.ThinkingRange != nil {
		thinkingRange = caps.ThinkingRange
	}
	return CapabilitiesDetail{
		Vision:                  caps.Vision,
		PDF:                     caps.PDF,
		AudioInput:              caps.AudioInput,
		VideoInput:              caps.VideoInput,
		ImageOutput:             caps.ImageOutput,
		AudioOutput:             caps.AudioOutput,
		Search:                  caps.Search,
		Tools:                   caps.Tools,
		Reasoning:               caps.Reasoning,
		Jailbreak:               caps.Jailbreak || IsJailbreakModel(model),
		ThinkingFormat:          thinkingFormat,
		ThinkingCanDisable:      caps.CanDisableThinking(),
		ThinkingRange:           thinkingRange,
		ThinkingEffortSupported: caps.ThinkingEffortSupported,
		ThinkingOffType:         caps.ThinkingOffType,
		ForcedToolChoice:        caps.ForcedToolChoice,
		ContextWindow:           cw,
		MaxOutput:               maxOut,
	}
}

// lookupCustomModelCaps resolves a custom model row by its exact id first and
// by its bare id second, mirroring the two-key shape SetCustomModelCaps writes.
func lookupCustomModelCaps(provider, model string) (Capabilities, bool) {
	if caps, ok := GetCustomModelCaps(provider, model); ok {
		return caps, true
	}
	if _, after, ok := strings.CutLast(model, "/"); ok {
		return GetCustomModelCaps(provider, after)
	}
	return Capabilities{}, false
}

// applyCustomCaps merges a custom model's saved block. Every modality flag is
// additive, the same way the synced catalog overlay is: a custom row can only
// turn a capability on, never off. The token limits are the exception — they
// are a declaration, so a declared number replaces whatever the substring
// table guessed, and zero means "not declared" and leaves the table alone.
func applyCustomCaps(dst *Capabilities, custom Capabilities) {
	if custom.Vision {
		dst.Vision = true
	}
	if custom.PDF {
		dst.PDF = true
	}
	if custom.AudioInput {
		dst.AudioInput = true
	}
	if custom.VideoInput {
		dst.VideoInput = true
	}
	if custom.ImageOutput {
		dst.ImageOutput = true
	}
	if custom.AudioOutput {
		dst.AudioOutput = true
	}
	if custom.Search {
		dst.Search = true
	}
	if custom.Tools {
		dst.Tools = true
	}
	if custom.Reasoning {
		dst.Reasoning = true
	}
	if custom.Jailbreak {
		dst.Jailbreak = true
	}
	if custom.ContextWindow != 0 {
		dst.ContextWindow = custom.ContextWindow
	}
	if custom.MaxOutput != 0 {
		dst.MaxOutput = custom.MaxOutput
	}
}
