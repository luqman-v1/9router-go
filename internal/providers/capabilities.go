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
	"claude-sonnet-5-5":                {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive"},
	"claude-sonnet-5":                  {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive"},
	"claude-sonnet-5-thinking":         {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive"},
	"claude-sonnet-5-agentic":          {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive"},
	"claude-sonnet-5-thinking-agentic": {Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive"},
	"gpt-image-1":                      {ImageOutput: true},
	"glm-5.3-flash":                    {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "zai"},
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
	"codebuddy-cn": {
		"glm-5.2":             {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(true)},
		"glm-5.1":             {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(false)},
		"glm-5.0-turbo":       {Reasoning: true, Tools: true},
		"glm-5v-turbo":        {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(false)},
		"minimax-m3":          {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(false)},
		"minimax-m2.7":        {Vision: true, Reasoning: true, Tools: true},
		"kimi-k2.7":           {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(false)},
		"kimi-k2.6":           {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(false)},
		"kimi-k2.5":           {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(false)},
		"hy3-preview":         {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(false)},
		"hy3":                 {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(false)},
		"hy3-x":               {Vision: true, Reasoning: true, Tools: true},
		"hy4-preview":         {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(false)},
		"hy4-preview-x":       {Vision: true, Reasoning: true, Tools: true},
		"glm-5.3":             {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(true)},
		"glm-5.3-flash":       {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(true)},
		"kimi-k3-1":           {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(false)},
		"deepseek-v4-pro":     {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(true)},
		"deepseek-v4.1-flash": {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(true)},
		"deepseek-v4-flash":   {Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai", ThinkingCanDisable: new(false)},
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
}

type patternCapability struct {
	pattern string
	caps    Capabilities
}

var patternCapabilities = []patternCapability{
	// Ahead of the generic sonnet row: vendor-prefixed 5.x ids
	// ("anthropic/claude-sonnet-5", "openrouter/claude-sonnet-5") have no exact
	// entry, and budget thinking on them sends a token budget Anthropic no
	// longer accepts.
	{"*claude*sonnet-5*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive"}},
	{"*claude*opus-5*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive"}},
	{"*claude*opus-4.6*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive"}},
	{"*claude*opus-4.7*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive"}},
	{"*claude*opus-4.8*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive"}},
	{"*claude*sonnet-4.6*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive"}},
	{"*claude*sonnet-4.7*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-adaptive"}},
	{"*claude*haiku*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-budget"}},
	{"*claude*opus*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-budget"}},
	{"*claude*sonnet*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-budget"}},
	{"*claude*fable*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-budget"}},
	{"*claude*mythos*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-budget"}},
	{"*claude-3*", Capabilities{Vision: true, Tools: true}},
	{"*claude*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "claude-budget"}},

	{"*gemini*image*", Capabilities{Vision: true, ImageOutput: true, Tools: true}},
	{"*gemini-3.8*", Capabilities{Vision: true, AudioInput: true, VideoInput: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "gemini-level", ThinkingCanDisable: new(false)}},
	{"*gemini-3.7*", Capabilities{Vision: true, AudioInput: true, VideoInput: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "gemini-level", ThinkingCanDisable: new(false)}},
	{"*gemini-3*pro*", Capabilities{Vision: true, AudioInput: true, VideoInput: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "gemini-level", ThinkingCanDisable: new(false)}},
	{"*gemini-3*", Capabilities{Vision: true, AudioInput: true, VideoInput: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "gemini-level", ThinkingCanDisable: new(false)}},
	{"*gemini-2.5*", Capabilities{Vision: true, AudioInput: true, VideoInput: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "gemini-budget", ThinkingRange: &ThinkingRange{Min: 0, Max: 24576}}},
	{"*gemini-2*", Capabilities{Vision: true, AudioInput: true, VideoInput: true, Search: true, Tools: true}},
	{"*gemini*", Capabilities{Vision: true, Search: true, Tools: true}},
	{"*gemma*", Capabilities{Vision: true, Tools: true}},
	{"*nanobanana*", Capabilities{Vision: true, ImageOutput: true, Tools: true}},

	// 1.05M is the API window for the whole gpt-6 family (astra, luna, sol alike).
	// It used to carry one gateway's 272k truncation, so every other provider's
	// gpt-6 models were published at 3.9x under their real window. A gateway
	// that really truncates lower says so in its own row below.
	{"*gpt-6*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 1050000, MaxOutput: 128000}},
	{"*gpt-5*image*", Capabilities{ImageOutput: true, Tools: true}},
	// gpt-5.4 is where the 1.05M window starts, but the mini and nano tiers
	// stayed at 400k — first match wins, so those two have to be listed ahead of
	// it. The image row has to be ahead of all of them: gpt-5.6-sol-image would
	// otherwise match *gpt-5.6* and be published as a reasoning model, which is
	// how seven image entries ended up offering thinking levels that upstream
	// declares none for.
	{"*gpt-5.4-mini*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 400000, MaxOutput: 128000}},
	{"*gpt-5.4-nano*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 400000, MaxOutput: 128000}},
	{"*gpt-5.4*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 1050000, MaxOutput: 128000}},
	{"*gpt-5.5*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 1050000, MaxOutput: 128000}},
	{"*gpt-5.6*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "openai", ContextWindow: 1050000, MaxOutput: 128000}},

	{"*gpt-image*", Capabilities{ImageOutput: true, Tools: true}},
	{"*gpt-5*codex*", Capabilities{Reasoning: true, Search: true, Tools: true, ThinkingFormat: "openai"}},
	{"*gpt-5*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "openai"}},
	{"*gpt-4o*", Capabilities{Vision: true, Search: true, Tools: true}},
	{"*gpt-4.1*", Capabilities{Vision: true, Tools: true}},
	{"*gpt-4-turbo*", Capabilities{Vision: true, Tools: true}},
	{"*gpt-4*", Capabilities{Tools: true}},
	{"*gpt-3.5*", Capabilities{Tools: true}},
	{"*gpt-oss*", Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "openai"}},
	{"*solar-pro*", Capabilities{Reasoning: true, Tools: true}},

	{"*o1-mini*", Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "openai"}},
	{"*o1*", Capabilities{Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai"}},
	{"*o3*", Capabilities{Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai"}},
	{"*o4*", Capabilities{Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai"}},

	{"*grok*image*", Capabilities{ImageOutput: true, Tools: true}},
	{"*grok-code*", Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "openai"}},
	{"*grok-4.6*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "openai"}},
	{"*grok-4.5*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "openai"}},
	{"*grok-4*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "openai"}},
	{"*grok-3*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "openai"}},
	{"*grok*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true, ThinkingFormat: "openai"}},

	{"*qwen*vl*", Capabilities{Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "qwen"}},
	{"*qwen*omni*", Capabilities{Vision: true, AudioInput: true, VideoInput: true, Reasoning: true, Tools: true, ThinkingFormat: "qwen"}},
	{"*qwen*coder*", Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "qwen"}},
	{"*qwen*max*", Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "qwen"}},
	{"*qwen3.5*", Capabilities{Vision: true, VideoInput: true, Reasoning: true, Tools: true, ThinkingFormat: "qwen"}},
	{"*qwen3.6*", Capabilities{Vision: true, VideoInput: true, Reasoning: true, Tools: true, ThinkingFormat: "qwen"}},
	{"*qwen3.7*", Capabilities{Vision: true, VideoInput: true, Reasoning: true, Tools: true, ThinkingFormat: "qwen"}},
	{"*qwen3.8*", Capabilities{Vision: true, VideoInput: true, Reasoning: true, Tools: true}},
	{"*qwen*plus*", Capabilities{Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "qwen"}},
	{"*qwen*235b*", Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "qwen"}},
	{"*qwq*", Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "qwen", ThinkingCanDisable: new(false)}},
	{"*qwen*", Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "qwen"}},

	{"*kimi*k3*", Capabilities{Vision: true, VideoInput: true, Reasoning: true, Tools: true, ThinkingFormat: "kimi", ThinkingCanDisable: new(false)}},
	{"*kimi*for-coding*", Capabilities{Vision: true, VideoInput: true, Reasoning: true, Tools: true, ThinkingFormat: "kimi", ThinkingCanDisable: new(false)}},
	{"*kimi*k2.7*code*", Capabilities{Vision: true, VideoInput: true, Reasoning: true, Tools: true, ThinkingFormat: "kimi", ThinkingCanDisable: new(false)}},
	{"*kimi*k2*", Capabilities{Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "kimi"}},
	{"*kimi*", Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "kimi"}},

	{"*glm-5.3*", Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "zai", ThinkingEffortSupported: true}},
	{"*glm-5.2*", Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "zai", ThinkingEffortSupported: true}},
	{"*glm-5*", Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "zai"}},
	{"*glm-4.7*", Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "zai"}},
	{"*glm-4*", Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "zai"}},
	{"*glm*", Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "zai"}},
	{"*z-ai*", Capabilities{Reasoning: true, Tools: true}},
	{"*zai*", Capabilities{Reasoning: true, Tools: true}},

	{"*deepseek-v4.*", Capabilities{Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "deepseek", ThinkingEffortSupported: true}},
	{"*deepseek-v4*", Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "deepseek", ThinkingEffortSupported: true}},
	{"*deepseek*flash*", Capabilities{Vision: true, Reasoning: true, Tools: true}},
	{"*reasoner*", Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "deepseek", ThinkingCanDisable: new(false)}},
	{"*deepseek-r*", Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "deepseek", ThinkingCanDisable: new(false)}},
	{"*deepseek-chat*", Capabilities{Tools: true}},
	{"*deepseek*", Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "deepseek"}},

	{"*minimax*image*", Capabilities{ImageOutput: true, Tools: true}},
	{"*minimax-m3*", Capabilities{Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "minimax"}},
	{"*minimax-m2.7*", Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "minimax", ThinkingCanDisable: new(false)}},
	{"*minimax-m2.5*", Capabilities{Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "minimax", ThinkingCanDisable: new(false)}},
	{"*minimax*", Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "minimax", ThinkingCanDisable: new(false)}},

	{"*mimo*v2.6*", Capabilities{Vision: true, AudioInput: true, VideoInput: true, Reasoning: true, Tools: true, ThinkingFormat: "deepseek", ThinkingCanDisable: new(false)}},
	{"*mimo*v2.5*", Capabilities{Vision: true, AudioInput: true, VideoInput: true, Reasoning: true, Tools: true, ThinkingFormat: "deepseek", ThinkingCanDisable: new(false)}},
	{"*mimo*omni*", Capabilities{Vision: true, AudioInput: true, Reasoning: true, Tools: true, ThinkingFormat: "deepseek", ThinkingCanDisable: new(false)}},
	{"*mimo*", Capabilities{Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "deepseek", ThinkingCanDisable: new(false)}},

	{"*llama-4*", Capabilities{Vision: true, Tools: true}},
	{"*llama*", Capabilities{Tools: true}},

	{"*codestral*", Capabilities{Tools: true}},
	{"*mistral-large*", Capabilities{Vision: true, Tools: true}},
	{"*mistral*", Capabilities{Tools: true}},

	{"*command-a-vision*", Capabilities{Vision: true, Tools: true}},
	{"*command*", Capabilities{Tools: true}},

	{"*sonar*", Capabilities{Search: true, Tools: true}},
	{"*pplx*", Capabilities{Search: true, Tools: true}},
	{"*perplexity*", Capabilities{Search: true, Tools: true}},

	{"*laguna-s-2.1*free*", Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "openai"}},
	{"*laguna-s-2.1*", Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "openai"}},
	{"*laguna*", Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "openai"}},

	{"*muse*spark*", Capabilities{Vision: true, Reasoning: true, Tools: true, ThinkingFormat: "openai"}},
	{"*hunyuan*", Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "hunyuan"}},
	{"hy3*", Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "hunyuan"}},
	{"*step-*", Capabilities{Reasoning: true, Tools: true, ThinkingFormat: "step"}},
	{"*nemotron*", Capabilities{Reasoning: true, Tools: true}},
	{"*ling-*", Capabilities{Reasoning: true, Tools: true}},
	{"*muse-spark*", Capabilities{Vision: true, Reasoning: true, Tools: true}},
}

// matchPattern checks if a string matches a glob pattern (only supports * as wildcard)
func matchPattern(pattern, s string) bool {
	matched, err := path.Match(strings.ToLower(pattern), strings.ToLower(s))
	if err != nil {
		return false
	}
	return matched
}

// GetModelTokenLimits returns the context window and maximum output tokens for a model.
func GetModelTokenLimits(model string) (contextWindow int, maxOutput int) {
	m := strings.ToLower(model)

	switch {
	case strings.Contains(m, "gpt-5.4-mini") || strings.Contains(m, "gpt-5.4-nano"):
		return 400000, 128000
	case strings.Contains(m, "gpt-5.4") || strings.Contains(m, "gpt-5.5") || strings.Contains(m, "gpt-5.6") || strings.Contains(m, "gpt-6"):
		return 1050000, 128000
	case strings.Contains(m, "deepseek-v4.1-flash") || strings.Contains(m, "deepseek-v4-flash"):
		return 1000000, 128000
	case strings.Contains(m, "gemini-1.5") || strings.Contains(m, "gemini-2.0") || strings.Contains(m, "gemini-2.5") || strings.Contains(m, "gemini-3") || strings.Contains(m, "glm-5.3-flash"):
		return 1048576, 65536
	case strings.Contains(m, "grok-4.5") || strings.Contains(m, "grok-4.6"):
		return 524288, 32768
	case strings.Contains(m, "claude-3") || strings.Contains(m, "claude-sonnet") || strings.Contains(m, "claude-opus") || strings.Contains(m, "claude-haiku"):
		return 200000, 8192
	case strings.Contains(m, "gpt-4o") || strings.Contains(m, "gpt-4-turbo") || strings.Contains(m, "gpt-4.1") || strings.Contains(m, "gpt-5"):
		return 128000, 16384
	case strings.Contains(m, "solar-pro") || strings.Contains(m, "longcat"):
		return 200000, 32000
	case strings.Contains(m, "o1") || strings.Contains(m, "o3"):
		return 200000, 100000
	case strings.Contains(m, "deepseek") || strings.Contains(m, "qwen") || strings.Contains(m, "glm") || strings.Contains(m, "kimi"):
		return 131072, 8192
	default:
		return 128000, 4096
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

	// 3. Pattern match
	if !resolved {
		for _, p := range patternCapabilities {
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
