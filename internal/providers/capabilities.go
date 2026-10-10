package providers

import (
	"path"
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
func SetCustomModelCaps(provider, model string, caps Capabilities) {
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
	// ThinkingFormat is the wire shape of the thinking control
	// (openai, claude-adaptive, claude-budget, gemini-level, gemini-budget,
	// zai, qwen, kimi, deepseek, commandcode, minimax, hunyuan, step).
	ThinkingFormat string
	// ThinkingCanDisable reports whether thinking can be turned off. It is a
	// pointer because a plain bool cannot say "not specified": upstream's
	// default is true, and a table entry that names a thinking format without
	// this flag must still resolve to true. See canDisableThinking.
	ThinkingCanDisable *bool
	// ThinkingRange is the {min, max} budget clamp for budget-based formats.
	ThinkingRange *ThinkingRange
	// ThinkingEffortSupported reports that the model accepts a reasoning_effort
	// level on the wire.
	ThinkingEffortSupported bool
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

// canDisableThinking resolves the tri-state ThinkingCanDisable flag: an unset
// entry means "not specified", which is upstream's default — thinking can be
// switched off.
func canDisableThinking(c Capabilities) bool {
	return c.ThinkingCanDisable == nil || *c.ThinkingCanDisable
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
	"claude-opus-5":                    {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive"},
	"claude-opus-5-thinking":           {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive"},
	"claude-opus-5-agentic":            {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive"},
	"claude-opus-5-thinking-agentic":   {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive"},
	// Opus 5.5 — experimental preview on Kiro (1M context, 2x credits).
	"claude-opus-5.5":                  {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive"},
	"claude-opus-5.5-thinking":         {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive"},
	"claude-opus-5.5-agentic":          {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive"},
	"claude-opus-5.5-thinking-agentic": {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive"},
	// Opus 5.5 on Antigravity (upstream v0.5.99, a07ed95b): all five
	// effort spellings, 1M window / 128K output. thinkingCanDisable:false is
	// representable here — without it canDisableThinking defaults to true and
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
	"claude-opus-5-5-thinking": {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ThinkingCanDisable: new(false), ContextWindow: 1000000, MaxOutput: 128000},
	"claude-opus-5-5-high":     {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ThinkingCanDisable: new(false), ContextWindow: 1000000, MaxOutput: 128000},
	"claude-opus-5-5-medium":   {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ThinkingCanDisable: new(false), ContextWindow: 1000000, MaxOutput: 128000},
	"claude-opus-5-5-low":      {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ThinkingCanDisable: new(false), ContextWindow: 1000000, MaxOutput: 128000},
	"claude-opus-4.6":                  {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive"},
	"claude-opus-4.7":                  {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive"},
	"claude-opus-4-7":                  {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive"},
	"claude-opus-4.8":                  {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive"},
	"claude-opus-4-6":                  {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive"},
	"claude-opus-4-8":                  {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive"},
	"claude-opus-4.8-thinking":         {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive"},
	"claude-opus-4-8-thinking":         {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive"},
	"claude-sonnet-4.6":                {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive"},
	"claude-sonnet-4-6":                {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive"},
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
	"claude-sonnet-5":                  {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive"},
	"claude-sonnet-5-thinking":         {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive"},
	"claude-sonnet-5-agentic":          {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive"},
	"claude-sonnet-5-thinking-agentic": {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive"},
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
	"agnes-3.0-pro":                    {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 512000, MaxOutput: 65536},
	"agnes-3.0-flash":                  {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 512000, MaxOutput: 65536},
	// GLM-5.3-FLASH is the multimodal z.ai line and it carries the full 1M
	// window. Upstream decolua/9router#4656: z.ai's docs state that "GLM-5.3
	// and GLM-5.3-FLASH no longer support disabling thinking (an error will
	// occur if the thinking.type parameter is set to disabled)". Left unset,
	// canDisableThinking reports true and the translator emits
	// enable_thinking:false on any turn that asked for no reasoning — z.ai
	// answers 400 code 1210 "Invalid API parameter". It looked intermittent
	// because only some turns ask.
	"glm-5.3-flash":                {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "zai", ThinkingCanDisable: new(false)},
	"claude-fable-5-1":                 {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive", ThinkingCanDisable: new(false)},
	"glm-5.2":                          {Reasoning: true, Tools: true, ThinkingFormat: "zai", ThinkingCanDisable: new(false)},
	"glm-4.6v":                         {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "zai"},
	"glm-4.5v":                         {Vision: true, VideoInput: true, Reasoning: true, Tools: true, ThinkingFormat: "zai"},
	"deepseek-v4-flash-vision-exp":     {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "deepseek"},
	"deepseek-v4-vision":               {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "deepseek", ThinkingEffortSupported: true},
	"deepseek-v4.1-flash":              {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "deepseek"},
	// Some gateways publish this model under the hyphenated id (dash instead
	// of dot); without the row it fell through to the text-only `*deepseek-v4*`
	// pattern and lost its vision and 1M window.
	"deepseek-v4-1-flash":              {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "deepseek"},
	"deepseek-flash":                   {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "deepseek"},
	"muse-spark-1.2-contributor-free":  {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai"},
	"muse-spark-1.3-contributor-free":  {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai"},
	"vision-model":                     {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "qwen"},
	"coder-model":                      {Reasoning: true, Tools: true, ThinkingFormat: "qwen"},
	"kimi-k3":                          {Vision: true, VideoInput: true, Reasoning: true, Tools: true, ThinkingFormat: "kimi", ThinkingCanDisable: new(false)},
	"k3":                               {Vision: true, VideoInput: true, Reasoning: true, Tools: true, ThinkingFormat: "kimi", ThinkingCanDisable: new(false)},
	"kimi-for-coding":                  {Vision: true, VideoInput: true, Reasoning: true, Tools: true, ThinkingFormat: "kimi", ThinkingCanDisable: new(false)},
	"kimi-for-coding-highspeed":        {Vision: true, VideoInput: true, Reasoning: true, Tools: true, ThinkingFormat: "kimi", ThinkingCanDisable: new(false)},
	"kimi-k2.7-code":                   {Vision: true, VideoInput: true, Reasoning: true, Tools: true, ThinkingFormat: "kimi", ThinkingCanDisable: new(false)},
	"kimi-k2.7-code-highspeed":         {Vision: true, VideoInput: true, Reasoning: true, Tools: true, ThinkingFormat: "kimi", ThinkingCanDisable: new(false)},
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
var providerCapabilities = map[string]map[string]Capabilities{
	"nvidia": {
		"minimaxai/minimax-m2.7":        {Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(false)},
		"minimaxai/minimax-m3":          {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(false)},
		"z-ai/glm-5.2":                  {Reasoning: true, Tools: true, ThinkingFormat: "openai"},
		"deepseek-ai/deepseek-v4-pro":   {Reasoning: true, Tools: true, ThinkingFormat: "openai"},
		"deepseek-ai/deepseek-v4-flash": {Reasoning: true, Tools: true, ThinkingFormat: "openai"},
	},
	"codex": {
		// Codex OAuth reports its own context windows (open-sse/providers/
		// capabilities.js), which differ from both the OpenAI API's and the
		// sibling GPT-5.6 models' — Sol is wider than Terra/Luna, and the [1m]
		// variants are wider again.
		"gpt-6-astra":            codexGpt56Caps(272000),
		"gpt-6-sol":              codexGpt56Caps(272000),
		"gpt-6-luna":             codexGpt56Caps(272000),
		"gpt-6.1-sol":            codexGpt56Caps(272000),
		"gpt-6-astra[1m]":        codexGpt56Caps(codexExtendedContextWindow),
		"gpt-6-sol[1m]":          codexGpt56Caps(codexExtendedContextWindow),
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
		"glm-5.2":             {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(true), ContextWindow: 1000000, MaxOutput: 131072},
		"glm-5.1":             {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(false), ContextWindow: 200000, MaxOutput: 48000},
		"glm-5.0-turbo":       {Reasoning: true, Tools: true},
		"minimax-m3":          {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(false), ContextWindow: 512000, MaxOutput: 524288},
		"minimax-m2.7":        {Reasoning: true, Tools: true},
		"kimi-k2.5":           {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(false), ContextWindow: 164000, MaxOutput: 32000},
		// Upstream #4614 drops glm-5v-turbo / kimi-k2.7 / kimi-k2.6 from the
		// published server list, and v0.5.99 keeps them dropped. They stay here
		// on purpose: a saved combo or connection naming one of them is still
		// routable here, and dropping the row would make a shipped model
		// unroutable for no user-visible gain. The level divergence this
		// causes is recorded in thinking_levels_fixture_test.go. The limits
		// are the server's own figures.
		"glm-5v-turbo":        {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(false), ContextWindow: 200000, MaxOutput: 64000},
		"kimi-k2.7":           {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(false), ContextWindow: 256000, MaxOutput: 32000},
		"kimi-k2.6":           {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(false), ContextWindow: 256000, MaxOutput: 32000},
		"hy3-preview":         {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(false), ContextWindow: 192000, MaxOutput: 64000},
		"hy3":                 {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(false), ContextWindow: 192000, MaxOutput: 64000},
		"hy3-x":               {Vision: true, Reasoning: true, Tools: true},
		"hy4-preview":         {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(false), ContextWindow: 1000000, MaxOutput: 64000},
		"hy4-preview-x":       {Vision: true, Reasoning: true, Tools: true},
		"glm-5.3":             {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(true), ContextWindow: 1000000, MaxOutput: 131072},
		"glm-5.3-flash":       {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(true), ContextWindow: 1000000, MaxOutput: 131072},
		"kimi-k3-1":           {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(false), ContextWindow: 1000000, MaxOutput: 1048576},
		// contextWindow is the server's contextWindow.defaultLength (300000),
		// NOT maxInputTokens: k2.8 publishes supportedLengths [300000, 1000000]
		// and the gateway only serves 1M to callers who opt in, which this
		// executor never does. Budgeting 1M here would let the capacity
		// adapter plan against a window the model does not actually have.
		"kimi-k2.8-preview":   {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(true), ContextWindow: 300000, MaxOutput: 131072},
		"deepseek-v4-pro":     {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(true), ContextWindow: 1000000, MaxOutput: 393216},
		"deepseek-v4.1-flash": {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(true), ContextWindow: 1000000, MaxOutput: 393216},
		"deepseek-v4-flash":   {Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(false), ContextWindow: 1000000, MaxOutput: 50000},
		"deepseek-v3-2-volc":  {Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(false)},
	},
	"poolside": {
		"laguna-s-2.1":  {Reasoning: true, Tools: true, ThinkingFormat: "openai"},
		"laguna-xs-2.1": {Reasoning: true, Tools: true, ThinkingFormat: "openai"},
	},
	// Devin CLI's registry declares 200k for these GPT variants; the generic
	// gpt-5.4/5.5 rows now publish the 1.05M API window, so the gateway's own
	// number has to be recorded here (upstream decolua/9router 89ffac5a).
	"devin-cli": {
		"gpt-5.4-high":    devinCLIGPTCaps,
		"gpt-5.4-medium":  devinCLIGPTCaps,
		"gpt-5.4-low":     devinCLIGPTCaps,
		"gpt-5.5-xhigh":   devinCLIGPTCaps,
		"gpt-5.5-high":    devinCLIGPTCaps,
		"gpt-5.5-medium":  devinCLIGPTCaps,
		"gpt-5.5-low":     devinCLIGPTCaps,
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
}

func init() {
	kiroGpt56 := Capabilities{
		Vision: true, Reasoning: true, Search: true, Tools: true,
		ThinkingFormat: "openai", ThinkingCanDisable: new(true),
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
		"deepseek-v4.1-flash:cloud": {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "deepseek"},
	}
	providerCapabilities["opencode-go"] = map[string]Capabilities{
		"glm-5.3-flash": {
			Vision: true, VideoInput: true, PDF: true, Reasoning: true, Tools: true,
			ThinkingFormat: "openai", ThinkingCanDisable: new(false),
		},
	}

	// `cx` is the alias codex publishes under, so it serves the same catalog
	// upstream does (open-sse/providers/capabilities.js:
	// PROVIDER_CAPABILITIES.cx = PROVIDER_CAPABILITIES.codex).
	providerCapabilities["cx"] = providerCapabilities["codex"]

	// The global mcode site serves the identical catalog, so it shares the
	// table rather than restating it (upstream
	// PROVIDER_CAPABILITIES["minimax-code-global"] = PROVIDER_CAPABILITIES["minimax-code"]).
	providerCapabilities["minimax-code-global"] = providerCapabilities["minimax-code"]

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
	{pattern: "*claude*haiku*", caps: Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-budget"}},
	{pattern: "*claude*opus*", caps: Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-budget"}},
	{pattern: "*claude*sonnet*", caps: Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-budget"}},
	{pattern: "*claude*fable*", caps: Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-budget"}},
	{pattern: "*claude*mythos*", caps: Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-budget"}},
	{pattern: "*claude-3*", caps: Capabilities{Vision: true, Tools: true}},
	{pattern: "*claude*", caps: Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-budget"}},

	{pattern: "*gemini*image*", caps: Capabilities{Vision: true, ImageOutput: true, Tools: true}},
	{pattern: "*gemini-3.8*", caps: Capabilities{Vision: true, AudioInput: true, VideoInput: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "gemini-level", ThinkingCanDisable: new(false)}},
	{pattern: "*gemini-3.7*", caps: Capabilities{Vision: true, AudioInput: true, VideoInput: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "gemini-level", ThinkingCanDisable: new(false)}},
	{pattern: "*gemini-3*pro*", caps: Capabilities{Vision: true, AudioInput: true, VideoInput: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "gemini-level", ThinkingCanDisable: new(false)}},
	{pattern: "*gemini-3*", caps: Capabilities{Vision: true, AudioInput: true, VideoInput: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "gemini-level", ThinkingCanDisable: new(false)}},
	{pattern: "*gemini-2.5*", caps: Capabilities{Vision: true, AudioInput: true, VideoInput: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "gemini-budget", ThinkingRange: &ThinkingRange{Min: 0, Max: 24576}}},
	{pattern: "*gemini-2*", caps: Capabilities{Vision: true, AudioInput: true, VideoInput: true, Search: true, Tools: true}},
	{pattern: "*gemini*", caps: Capabilities{Vision: true, Search: true, Tools: true}},
	{pattern: "*gemma*", caps: Capabilities{Vision: true, Tools: true}},
	{pattern: "*nanobanana*", caps: Capabilities{Vision: true, ImageOutput: true, Tools: true}},

	// 1.05M is the API window for the whole gpt-6 family (astra, luna, sol alike).
	// It used to carry one gateway's 272k truncation, so every other provider's
	// gpt-6 models were published at 3.9x under their real window. A gateway
	// that really truncates lower says so in its own row below.
	{pattern: "*gpt-6*", caps: Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 1050000, MaxOutput: 128000}},
	{pattern: "*gpt-5*image*", caps: Capabilities{ImageOutput: true, Tools: true}},
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
	{pattern: "*gpt-5*codex*", caps: Capabilities{Reasoning: true, Search: true, Tools: true, ThinkingFormat: "openai"}},
	{pattern: "*gpt-5*", caps: Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "openai"}},
	{pattern: "*gpt-4o*", caps: Capabilities{Vision: true, Search: true, Tools: true}},
	{pattern: "*gpt-4.1*", caps: Capabilities{Vision: true, Tools: true}},
	{pattern: "*gpt-4-turbo*", caps: Capabilities{Vision: true, Tools: true}},
	{pattern: "*gpt-4*", caps: Capabilities{Tools: true}},
	{pattern: "*gpt-3.5*", caps: Capabilities{Tools: true}},
	{pattern: "*gpt-oss*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "openai"}},
	{pattern: "*solar-pro*", caps: Capabilities{Reasoning: true, Tools: true}},

	{pattern: "*o1-mini*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "openai"}},
	{pattern: "*o1*", caps: Capabilities{Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai"}},
	{pattern: "*o3*", caps: Capabilities{Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai"}},
	{pattern: "*o4*", caps: Capabilities{Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai"}},

	{pattern: "*grok*image*", caps: Capabilities{ImageOutput: true, Tools: true}},
	{pattern: "*grok-code*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "openai"}},
	{pattern: "*grok-4.6*", caps: Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "openai"}},
	{pattern: "*grok-4.5*", caps: Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "openai"}},
	{pattern: "*grok-4*", caps: Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "openai"}},
	{pattern: "*grok-3*", caps: Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "openai"}},
	{pattern: "*grok*", caps: Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "openai"}},

	{pattern: "*qwen*vl*", caps: Capabilities{Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "qwen"}},
	{pattern: "*qwen*omni*", caps: Capabilities{Vision: true, AudioInput: true, VideoInput: true, Reasoning: true, Tools: true, ThinkingFormat: "qwen"}},
	{pattern: "*qwen*coder*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "qwen"}},
	{pattern: "*qwen*max*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "qwen"}},
	{pattern: "*qwen3.5*", caps: Capabilities{Vision: true, VideoInput: true, Reasoning: true, Tools: true, ThinkingFormat: "qwen"}},
	{pattern: "*qwen3.6*", caps: Capabilities{Vision: true, VideoInput: true, Reasoning: true, Tools: true, ThinkingFormat: "qwen"}},
	{pattern: "*qwen3.7*", caps: Capabilities{Vision: true, VideoInput: true, Reasoning: true, Tools: true, ThinkingFormat: "qwen"}},
	{pattern: "*qwen3.8*", caps: Capabilities{Vision: true, VideoInput: true, Reasoning: true, Tools: true}},
	{pattern: "*qwen*plus*", caps: Capabilities{Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "qwen"}},
	{pattern: "*qwen*235b*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "qwen"}},
	{pattern: "*qwq*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "qwen", ThinkingCanDisable: new(false)}},
	{pattern: "*qwen*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "qwen"}},

	{pattern: "*kimi*k3*", caps: Capabilities{Vision: true, VideoInput: true, Reasoning: true, Tools: true, ThinkingFormat: "kimi", ThinkingCanDisable: new(false)}},
	{pattern: "*kimi*for-coding*", caps: Capabilities{Vision: true, VideoInput: true, Reasoning: true, Tools: true, ThinkingFormat: "kimi", ThinkingCanDisable: new(false)}},
	{pattern: "*kimi*k2.7*code*", caps: Capabilities{Vision: true, VideoInput: true, Reasoning: true, Tools: true, ThinkingFormat: "kimi", ThinkingCanDisable: new(false)}},
	{pattern: "*kimi*k2*", caps: Capabilities{Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "kimi"}},
	{pattern: "*kimi*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "kimi"}},

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
	{pattern: "*glm-5*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "zai"}},
	{pattern: "*glm-4.7*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "zai"}},
	{pattern: "*glm-4*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "zai"}},
	{pattern: "*glm*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "zai"}},
	{pattern: "*z-ai*", caps: Capabilities{Reasoning: true, Tools: true}},
	{pattern: "*zai*", caps: Capabilities{Reasoning: true, Tools: true}},

	{pattern: "*deepseek-v4.*", caps: Capabilities{Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "deepseek", ThinkingEffortSupported: true}},
	{pattern: "*deepseek-v4*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "deepseek", ThinkingEffortSupported: true}},
	{pattern: "*deepseek*flash*", caps: Capabilities{Vision: true, Reasoning: true, Tools: true}},
	{pattern: "*reasoner*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "deepseek", ThinkingCanDisable: new(false)}},
	{pattern: "*deepseek-r*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "deepseek", ThinkingCanDisable: new(false)}},
	{pattern: "*deepseek-chat*", caps: Capabilities{Tools: true}},
	{pattern: "*deepseek*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "deepseek"}},

	{pattern: "*minimax*image*", caps: Capabilities{ImageOutput: true, Tools: true}},
	{pattern: "*minimax-m3*", caps: Capabilities{Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "minimax"}},
	{pattern: "*minimax-m2.7*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "minimax", ThinkingCanDisable: new(false)}},
	{pattern: "*minimax-m2.5*", caps: Capabilities{Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "minimax", ThinkingCanDisable: new(false)}},
	{pattern: "*minimax*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "minimax", ThinkingCanDisable: new(false)}},

	{pattern: "*mimo*v2.6*", caps: Capabilities{Vision: true, AudioInput: true, VideoInput: true, Reasoning: true, Tools: true, ThinkingFormat: "deepseek", ThinkingCanDisable: new(false)}},
	{pattern: "*mimo*v2.5*", caps: Capabilities{Vision: true, AudioInput: true, VideoInput: true, Reasoning: true, Tools: true, ThinkingFormat: "deepseek", ThinkingCanDisable: new(false)}},
	{pattern: "*mimo*omni*", caps: Capabilities{Vision: true, AudioInput: true, Reasoning: true, Tools: true, ThinkingFormat: "deepseek", ThinkingCanDisable: new(false)}},
	{pattern: "*mimo*", caps: Capabilities{Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "deepseek", ThinkingCanDisable: new(false)}},

	{pattern: "*llama-4*", caps: Capabilities{Vision: true, Tools: true}},
	{pattern: "*llama*", caps: Capabilities{Tools: true}},

	{pattern: "*codestral*", caps: Capabilities{Tools: true}},
	{pattern: "*mistral-large*", caps: Capabilities{Vision: true, Tools: true}},
	{pattern: "*mistral*", caps: Capabilities{Tools: true}},

	{pattern: "*command-a-vision*", caps: Capabilities{Vision: true, Tools: true}},
	{pattern: "*command*", caps: Capabilities{Tools: true}},

	{pattern: "*sonar*", caps: Capabilities{Search: true, Tools: true}},
	{pattern: "*pplx*", caps: Capabilities{Search: true, Tools: true}},
	{pattern: "*perplexity*", caps: Capabilities{Search: true, Tools: true}},

	{pattern: "*laguna-s-2.1*free*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "openai"}},
	{pattern: "*laguna-s-2.1*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "openai"}},
	{pattern: "*laguna*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "openai"}},

	{pattern: "*muse*spark*", caps: Capabilities{Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai"}},
	{pattern: "*hunyuan*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "hunyuan"}},
	{pattern: "hy3*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "hunyuan"}},
	{pattern: "*step-*", caps: Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "step"}},
	{pattern: "*nemotron*", caps: Capabilities{Reasoning: true, Tools: true}},
	{pattern: "*ling-*", caps: Capabilities{Reasoning: true, Tools: true}},
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

func lookupModelTokenLimits(model string) [2]int {
	m := strings.ToLower(model)

	switch {
	case strings.Contains(m, "gpt-5.4-mini") || strings.Contains(m, "gpt-5.4-nano"):
		return [2]int{400000, 128000}
	case strings.Contains(m, "gpt-5.4") || strings.Contains(m, "gpt-5.5") || strings.Contains(m, "gpt-5.6") || strings.Contains(m, "gpt-6"):
		return [2]int{1050000, 128000}
	case strings.Contains(m, "deepseek-v4.1-flash") || strings.Contains(m, "deepseek-v4-flash"):
		return [2]int{1000000, 128000}
	case strings.Contains(m, "gemini-1.5") || strings.Contains(m, "gemini-2.0") || strings.Contains(m, "gemini-2.5") || strings.Contains(m, "gemini-3") || strings.Contains(m, "glm-5.3-flash"):
		return [2]int{1048576, 65536}
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

	capsCacheMu.Lock()
	capsCache[key] = res
	capsCacheMu.Unlock()

	return res
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
		res.ThinkingRange = overlay.ThinkingRange
		res.ThinkingEffortSupported = overlay.ThinkingEffortSupported
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
	ThinkingFormat          *string `json:"thinkingFormat"`
	ThinkingCanDisable      bool    `json:"thinkingCanDisable"`
	ThinkingRange           any     `json:"thinkingRange"`
	ThinkingEffortSupported bool    `json:"thinkingEffortSupported"`
	ContextWindow           int     `json:"contextWindow,omitempty"`
	MaxOutput               int     `json:"maxOutput,omitempty"`
}

// ComboCapabilities is the aggregated block upstream publishes for a combo
// (aggregateComboCapabilities). It is deliberately a different type: the union
// drops thinkingEffortSupported, booleans fold with `some` — except tools, which
// requires *every* leaf to support it — the thinking fields come from the first
// leaf, the context window is the narrowest leaf and maxOutput the widest.
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
		if leaf.ContextWindow < merged.ContextWindow {
			merged.ContextWindow = leaf.ContextWindow
		}
		if leaf.MaxOutput > merged.MaxOutput {
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
	// A provider entry that declares its own limits (CommandCode) is more
	// specific than the catalog, so it wins. Everything else keeps the
	// catalog-then-table-then-floor chain.
	cw, maxOut := caps.ContextWindow, caps.MaxOutput
	if cw == 0 && maxOut == 0 {
		// Upstream resolves limits from the models.dev-synced catalog keyed by
		// provider + model before falling back to its pattern table and the
		// DEFAULT_CAPABILITIES floor; the catalog is the authoritative source, so
		// it wins here too and the substring table only fills the gaps.
		cw, maxOut = GetCatalogLimits(provider, model)
	}
	if cw == 0 && maxOut == 0 {
		cw, maxOut = GetModelTokenLimits(model)
	}
	if cw == 0 && maxOut == 0 {
		cw, maxOut = GetModelTokenLimits(provider + "/" + model)
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
		ThinkingFormat:          thinkingFormat,
		ThinkingCanDisable:      canDisableThinking(caps),
		ThinkingRange:           thinkingRange,
		ThinkingEffortSupported: caps.ThinkingEffortSupported,
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
	if custom.ContextWindow != 0 {
		dst.ContextWindow = custom.ContextWindow
	}
	if custom.MaxOutput != 0 {
		dst.MaxOutput = custom.MaxOutput
	}
}
