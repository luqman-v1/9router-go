package providers

import (
	"regexp"
	"slices"
	"strings"
)

// Thinking levels are the selectable values of the dashboard's
// "Thinking: <level>" picker and the `(level)` suffix 9router accepts on a
// model id. This is the Go port of open-sse/providers/thinkingLevels.js:
// the level set is a function of the model's capabilities, refined by a
// model-name pattern table and by whether thinking can be switched off.

// Shared level sets (deduped), verified against provider docs and the wire in
// thinkingUnified.applyFormat.
var (
	levelBase    = []string{"none", "low", "medium", "high"}                     // qwen, step, hunyuan, gemini-budget
	levelOnOff   = []string{"none", "thinking"}                                  // zai (binary), minimax (adaptive)
	levelOpenAI  = []string{"none", "minimal", "low", "medium", "high", "xhigh"} // GPT-5.x / o-series (no "max")
	levelMax     = []string{"none", "low", "medium", "high", "max"}              // kimi
	levelBudgetX = []string{"none", "low", "medium", "high", "xhigh", "max"}     // claude-budget, claude-adaptive
	levelGemini  = []string{"minimal", "low", "medium", "high"}                  // gemini-3 thinkingLevel (no disable)
	levelHiMax   = []string{"none", "high", "max"}                               // deepseek (low/med→high, xhigh→max)
	levelCMDCode = []string{"none", "low", "medium", "high", "xhigh", "max"}     // commandcode

	// Opus/Sonnet 4.6 lack xhigh (Anthropic + Kiro docs) — keep the 4-level+max
	// set for them. This has to stay paired with the claude-adaptive bump to
	// levelBudgetX: without the exclusions the picker would offer xhigh on
	// models that answer 400 for it (upstream decolua/9router 7894f3d3).
	levelClaudeNoXHigh = []string{"none", "low", "medium", "high", "max"}
)

// codexGPT56Levels is the shared set for the GPT-5.6 Codex family.
func codexGPT56Levels() []string {
	return []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}
}

// codexGPT56UltraLevels is the GPT-5.6 sol/terra set, which adds "ultra".
func codexGPT56UltraLevels() []string {
	return []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"}
}

// formatLevels maps the wire thinking format to its selectable levels and is the
// source of truth for the picker's options (thinkingLevels.js:19).
var formatLevels = map[string][]string{
	"openai":          levelOpenAI,
	"claude-adaptive": levelBudgetX,
	"claude-budget":   levelBudgetX,
	"gemini-level":    levelGemini,
	"gemini-budget":   levelBase,
	"zai":             levelOnOff,
	"qwen":            levelBase,
	"kimi":            levelMax,
	"deepseek":        levelHiMax,
	"commandcode":     levelCMDCode,
	"minimax":         levelOnOff,
	"hunyuan":         levelBase,
	"step":            levelBase,
}

// thinkingPattern is a model-name override, more precise than the format
// default. An empty Provider applies to every provider.
type thinkingPattern struct {
	Provider string
	Pattern  string
	Levels   []string
}

// patternThinking is ordered; the first match wins
// (thinkingLevels.js:38).
var patternThinking = []thinkingPattern{
	{Pattern: "*claude*4.6*", Levels: levelClaudeNoXHigh},
	{Pattern: "*claude*4-6*", Levels: levelClaudeNoXHigh},
	{Provider: "codex", Pattern: "*gpt-6*", Levels: codexGPT56Levels()},
	{Provider: "codex", Pattern: "*gpt-5.6-sol*", Levels: codexGPT56UltraLevels()},
	{Provider: "codex", Pattern: "*gpt-5.6-terra*", Levels: codexGPT56UltraLevels()},
	{Provider: "codex", Pattern: "*gpt-5.6-luna*", Levels: codexGPT56Levels()},
	{Pattern: "*codex*", Levels: []string{"low", "medium", "high", "xhigh"}}, // codex cannot disable thinking
	{Pattern: "*mimo*v2.6*", Levels: []string{"none", "low", "medium", "high", "xhigh"}},
	// mimo-v2.5-pro on opencode-go rejects reasoning_effort "max" (probed live);
	// v2.5 accepts it.
	{Pattern: "*mimo*v2.5-pro*", Levels: []string{"none", "low", "medium", "high", "xhigh"}},
	// DeepSeek v4.* (Alibaba MaaS, probed live): effort low|medium|high|xhigh|max
	// all 200 via output_config.effort; "none" is a 400 on the anthropic route
	// (disable thinking instead). none kept for the picker = disable.
	//
	// ⚠️ The codebuddy-cn exact ids below MUST stay above this unqualified
	// glob — patternThinking is first-match-wins, so a provider-qualified
	// entry after it would never fire for a dotted id like
	// deepseek-v4.1-flash (upstream decolua/9router#4614).
	{Provider: "codebuddy-cn", Pattern: "deepseek-v4-pro", Levels: []string{"low", "high", "xhigh"}},
	{Provider: "codebuddy-cn", Pattern: "deepseek-v4.1-flash", Levels: []string{"low", "high", "max"}},
	{Pattern: "*deepseek-v4.*", Levels: []string{"none", "low", "medium", "high", "xhigh", "max"}},
	// codebuddy-cn per-model effort sets — the server's product-config payload
	// publishes `reasoning.supportedEfforts` per model. NOTE: the chat endpoint
	// accepts any level you send (probed none/minimal/low/medium/high/xhigh/max
	// → all 200), but values outside a model's supportedEfforts are silently
	// clamped, so the declared set stays authoritative for the picker. Models
	// that publish no supportedEfforts (glm-5.1 / kimi-k3-1 / minimax-m3)
	// fall through to the openai format default.
	{Provider: "codebuddy-cn", Pattern: "glm-5.3", Levels: []string{"low", "high", "max"}},
	{Provider: "codebuddy-cn", Pattern: "glm-5.3-flash", Levels: []string{"low", "high", "max"}},
	{Provider: "codebuddy-cn", Pattern: "glm-5.2", Levels: []string{"high", "xhigh"}},
	{Provider: "codebuddy-cn", Pattern: "kimi-k2.8-preview", Levels: []string{"low", "medium", "high"}},
	{Provider: "codebuddy-cn", Pattern: "hy3", Levels: []string{"low", "high"}},
	{Provider: "codebuddy-cn", Pattern: "hy4-preview", Levels: []string{"high"}},
	// codebuddy-intl rides the same gateway catalog, so its deepseek levels match.
	{Provider: "codebuddy-intl", Pattern: "deepseek-v4*", Levels: []string{"low", "high", "xhigh"}},
	// MiniMax Code (mcode) — adaptive effort via output_config.effort. M3.1
	// always thinks (no none) and offers the five efforts; M3 is switchable
	// with none/high only. The M2.7 pair takes no pattern and falls through to
	// the claude-adaptive set minus none, because canDisable is false.
	// Provider-qualified and case-insensitive glob matching, so both the
	// canonical id and the published alias resolve to the same set.
	{Provider: "minimax-code", Pattern: "MiniMax-M3.1*", Levels: []string{"low", "medium", "high", "xhigh", "max"}},
	{Provider: "minimax-code", Pattern: "MiniMax-M3", Levels: []string{"none", "high"}},
	{Provider: "minimax-code-global", Pattern: "MiniMax-M3.1*", Levels: []string{"low", "medium", "high", "xhigh", "max"}},
	{Provider: "minimax-code-global", Pattern: "MiniMax-M3", Levels: []string{"none", "high"}},
	{Provider: "mmc", Pattern: "MiniMax-M3.1*", Levels: []string{"low", "medium", "high", "xhigh", "max"}},
	{Provider: "mmc", Pattern: "MiniMax-M3", Levels: []string{"none", "high"}},
	{Provider: "mmg", Pattern: "MiniMax-M3.1*", Levels: []string{"low", "medium", "high", "xhigh", "max"}},
	{Provider: "mmg", Pattern: "MiniMax-M3", Levels: []string{"none", "high"}},
}

// codexModelThinkingLevels mirrors the per-model `thinkingLevels` field of the
// Codex registry entries (open-sse/providers/registry/codex.js). Upstream reads
// it off the catalog record; the Go catalog is a flat id list, so the field
// lives here. GPT-6 Sol/Luna are "responses-lite": they take an effort but
// neither "none" nor "minimal".
var codexModelThinkingLevels = map[string][]string{
	"gpt-6-sol":   {"low", "medium", "high", "xhigh", "max"},
	"gpt-6-luna":  {"low", "medium", "high", "xhigh", "max"},
	"gpt-6.1-sol": {"low", "medium", "high", "xhigh", "max"},
	// The `[1m]` ids resolve to their base model upstream (upstreamModelId),
	// so they keep the base model's level set.
	"gpt-6-sol[1m]":  {"low", "medium", "high", "xhigh", "max"},
	"gpt-6-luna[1m]": {"low", "medium", "high", "xhigh", "max"},
}

// codexModelLevels returns the registry-declared levels for a Codex model, with
// any trailing "(level)" override stripped first — the suffix is a 9router
// request override, not part of the model id.
func codexModelLevels(provider, model string) []string {
	if provider != "codex" {
		return nil
	}
	base := model
	if open := strings.LastIndex(base, "("); open != -1 && strings.HasSuffix(base, ")") {
		base = strings.TrimSpace(base[:open])
	}
	return codexModelThinkingLevels[base]
}

// GetThinkingLevels returns the valid thinking levels for a model, or nil when
// the model has no reasoning. The returned slice is a fresh copy, safe for the
// caller to keep or filter.
func GetThinkingLevels(provider, model string) []string {
	if provider == "kiro" && ResolveKiroEffortPath(model) == "" {
		return nil
	}
	caps := GetCapabilitiesForModel(provider, model)
	if !caps.Reasoning {
		return nil
	}

	// A registry-declared per-model set wins over everything, then the pattern
	// table, then the wire format's default — the order upstream resolves in.
	levels := codexModelLevels(provider, model)
	if levels == nil {
		levels = formatLevels[caps.ThinkingFormat]
		if levels == nil {
			levels = levelBase
		}
		for _, entry := range patternThinking {
			if entry.Provider != "" && entry.Provider != provider {
				continue
			}
			if matchThinkingGlob(entry.Pattern, model) {
				levels = entry.Levels
				break
			}
		}
	}
	if !canDisableThinking(caps) {
		levels = withoutLevel(levels, "none")
	}
	return append([]string(nil), levels...)
}

// withoutLevel returns levels without the given level.
func withoutLevel(levels []string, drop string) []string {
	out := make([]string, 0, len(levels))
	for _, l := range levels {
		if l != drop {
			out = append(out, l)
		}
	}
	return out
}

// matchThinkingGlob matches upstream's glob semantics: `*` stands for any run of
// characters *including* `/`, the whole string must match, and the comparison is
// case-insensitive. path.Match is not usable here because its `*` never crosses
// a separator, which would break patterns like `*deepseek-v4.*` against the
// vendor-prefixed ids CommandCode and friends publish.
func matchThinkingGlob(pattern, s string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return strings.EqualFold(parts[0], s)
	}

	rest := s
	if !strings.HasPrefix(strings.ToLower(rest), strings.ToLower(parts[0])) {
		return false
	}
	rest = rest[len(parts[0]):]

	for i := 1; i < len(parts)-1; i++ {
		idx := strings.Index(strings.ToLower(rest), strings.ToLower(parts[i]))
		if idx < 0 {
			return false
		}
		rest = rest[idx+len(parts[i]):]
	}

	return strings.HasSuffix(strings.ToLower(rest), strings.ToLower(parts[len(parts)-1]))
}

var (
	// (^|[/.])gpt[/.]5[/.]6([/.]|$) — GPT-5.6 on Kiro uses reasoning.effort.
	kiroGPT56Re = regexp.MustCompile(`(^|[/.])gpt[/.]5[/.]6([/.]|$)`)
	// claude, then any number of .segment words, then the version numbers.
	kiroClaudeRe = regexp.MustCompile(`(^|[/.])claude(([/.][a-z]+)*)([/.])(\d+)([/.](\d+))?([/.]|$)`)
)

// ResolveKiroEffortPath reports where Kiro expects the effort field for a model
// ("reasoning" or "output_config"), or "" when the model does not support the
// additional request fields at all. Port of
// open-sse/config/kiroConstants.js:218.
func ResolveKiroEffortPath(model string) string {
	if model == "" {
		return ""
	}
	normalized := strings.ToLower(strings.ReplaceAll(model, "-", "."))
	if kiroGPT56Re.MatchString(normalized) {
		return "reasoning"
	}
	if !strings.Contains(normalized, "claude") {
		return ""
	}
	sub := kiroClaudeRe.FindStringSubmatch(normalized)
	if sub == nil {
		return ""
	}
	// sub[2] = version words, sub[5] = major, sub[7] = minor
	major, ok := parseDigits(sub[5])
	if !ok {
		return ""
	}
	minor := 0
	hasMinor := false
	if sub[7] != "" {
		if m, ok := parseDigits(sub[7]); ok {
			minor, hasMinor = m, true
		}
	}
	// Kiro rejected additionalModelRequestFields on legacy 4.5 models in live
	// smoke. Default future Claude/Kiro models to supported so new model
	// releases do not need a code allowlist update.
	dateSuffixMinor := hasMinor && minor >= 1000
	if major < 4 || (major == 4 && (!hasMinor || minor <= 5 || dateSuffixMinor)) {
		return ""
	}
	return "output_config"
}

func parseDigits(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, true
}

// ClampDeepseekEffort maps a requested thinking level onto the effort vocabulary
// that DeepSeek-shaped upstreams accept, downgrading "max" to "high" when the
// model's declared levels do not include it.
//
// Port of the deepseek case in open-sse/translator/thinkingUnified.js applyFormat
// (upstream 1b72f02e). Without it, mimo-v2.5-pro and v2.6 on the Go lane answer
// 400 to reasoning_effort "max" — probed live upstream, while mimo-v2.5 accepts
// the same value, so the clamp keys off the declared levels rather than the name.
//
// The rule is deliberately general: any level set omitting "max" would hit the
// same rejection, and levelOpenAI (GPT-5.x, o-series) has no "max" either.
func ClampDeepseekEffort(provider, model, level string) string {
	want := "high"
	if level == "xhigh" || level == "max" {
		want = "max"
	}
	if want == "max" && !slices.Contains(GetThinkingLevels(provider, model), "max") {
		return "high"
	}
	return want
}
