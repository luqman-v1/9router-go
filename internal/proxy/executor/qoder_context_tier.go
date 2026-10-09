package executor

import (
	json "encoding/json/v2"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Qoder context-window tiers — port of
// open-sse/shared/qoder/contextTier.js (parseTierTokenCount, tierName,
// getQoderContextTiers, estimateQoderPromptTokens, normalizeMode,
// findNamedTier, resolveQoderContextTier, applyQoderContextTier).
//
// Each Qoder model_config ships a `context_config` list (e.g. 200K / 400K / 1M
// for qmodel_38max) while `max_input_tokens` only carries the tier the IDE
// currently has selected (~180K by default). The Qoder IDE lets a user switch
// tiers from the model picker; a qodercli-style client — which is what this
// gateway impersonates — has no picker, so a long session that grew past the
// default tier is rejected upstream even though the model itself supports 1M.
//
// This emulates the IDE: estimate the prompt size, pick the smallest advertised
// tier that fits (never below the model's current default), and mirror the
// choice into the same three places the IDE writes. Pure functions, no I/O.

const (
	qoderTierKilo = 1_000
	qoderTierMega = 1_000_000

	// qoderTierHeadroom absorbs tokenizer variance on top of the estimate.
	qoderTierHeadroom = 0.15

	// QoderContextTierEnv overrides tier selection: auto (default) | max |
	// default | <tier name, e.g. 1M>.
	QoderContextTierEnv = "QODER_CONTEXT_TIER"
)

const (
	qoderTierModeAuto    = "auto"
	qoderTierModeMax     = "max"
	qoderTierModeDefault = "default"
)

// qoderTier is one advertised context window for a model.
type qoderTier struct {
	Name       string
	TokenCount int
	IsDefault  bool
}

// parseQoderTierTokenCount normalizes "200K" | "1M" | "204800" | 204800 into
// an integer token count, or 0 when unparseable.
func parseQoderTierTokenCount(v any) int {
	switch value := v.(type) {
	case float64:
		if value > 0 && !math.IsInf(value, 0) {
			return int(value)
		}
		return 0
	case string:
		return parseQoderTierTokenString(value)
	default:
		return 0
	}
}

// parseQoderTierTokenString handles the string form: an optional decimal
// followed by an optional K/M unit, case-insensitive.
func parseQoderTierTokenString(s string) int {
	trimmed := strings.ToUpper(strings.TrimSpace(s))
	if trimmed == "" {
		return 0
	}
	digits, unit := trimmed, ""
	if last := trimmed[len(trimmed)-1]; last == 'K' || last == 'M' {
		digits, unit = trimmed[:len(trimmed)-1], string(last)
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(digits), 64)
	if err != nil || value <= 0 {
		return 0
	}
	switch unit {
	case "K":
		value *= qoderTierKilo
	case "M":
		value *= qoderTierMega
	}
	if math.IsInf(value, 0) || value <= 0 {
		return 0
	}
	return int(value)
}

// qoderTierName names a tier after its own label when it has one, else derives
// one from the token count ("1M", "200K", or the bare number).
func qoderTierName(entry map[string]any, tokenCount int) string {
	for _, key := range []string{"name", "label", "display_name", "displayName", "key", "id"} {
		if raw, ok := entry[key].(string); ok && strings.TrimSpace(raw) != "" {
			return strings.TrimSpace(raw)
		}
	}
	if tokenCount >= qoderTierMega && tokenCount%qoderTierMega == 0 {
		return strconv.Itoa(tokenCount/qoderTierMega) + "M"
	}
	if tokenCount >= qoderTierKilo && tokenCount%qoderTierKilo == 0 {
		return strconv.Itoa(tokenCount/qoderTierKilo) + "K"
	}
	return strconv.Itoa(tokenCount)
}

// qoderContextTiers normalizes a model_config's context_config into tiers
// sorted ascending by token count. It accepts snake_case and camelCase shapes
// and returns nil when the model advertises no tiers at all.
func qoderContextTiers(modelConfig map[string]any) []qoderTier {
	var list []any
	for _, key := range []string{"context_config", "contextConfig"} {
		if raw, ok := modelConfig[key].([]any); ok {
			list = raw
			break
		}
	}
	if list == nil {
		return nil
	}

	byCount := map[int]qoderTier{}
	for _, item := range list {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		var tokenCount int
		for _, key := range []string{"tokenCount", "token_count", "max_input_tokens", "maxInputTokens", "contextLength", "context_length"} {
			if tokenCount = parseQoderTierTokenCount(entry[key]); tokenCount > 0 {
				break
			}
		}
		if tokenCount == 0 {
			continue
		}
		isDefault := entry["isDefault"] == true || entry["is_default"] == true || entry["default"] == true
		prev := byCount[tokenCount]
		byCount[tokenCount] = qoderTier{
			Name:       qoderTierName(entry, tokenCount),
			TokenCount: tokenCount,
			IsDefault:  prev.IsDefault || isDefault,
		}
	}

	tiers := make([]qoderTier, 0, len(byCount))
	for _, tier := range byCount {
		tiers = append(tiers, tier)
	}
	sort.Slice(tiers, func(i, j int) bool { return tiers[i].TokenCount < tiers[j].TokenCount })
	return tiers
}

// estimateQoderPromptTokens is a rough prompt-size estimate. CJK characters
// count ~1 token each and everything else ~4 chars/token: the plain chars/4
// rule underestimates Chinese and Japanese by up to 4x, which is exactly when a
// tier decision matters.
func estimateQoderPromptTokens(system string, messages, tools []any) int {
	raw, err := json.Marshal(map[string]any{
		"system":   system,
		"messages": messages,
		"tools":    tools,
	})
	if err != nil {
		return 0
	}
	cjk := 0
	for _, r := range string(raw) {
		if isQoderCJK(r) {
			cjk++
		}
	}
	total := len(string(raw))
	return int(math.Ceil(float64(cjk) + float64(total-cjk)/4))
}

// isQoderCJK matches the ranges upstream's CJK_RE covers: Hangul Jamo, the
// CJK block and radicals, Hangul Syllables, CJK Compatibility Ideographs, and
// the halfwidth/fullwidth forms.
func isQoderCJK(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x11ff, // Hangul Jamo
		r >= 0x2e80 && r <= 0x9fff, // CJK radicals through CJK ideographs
		r >= 0xac00 && r <= 0xd7af, // Hangul Syllables
		r >= 0xf900 && r <= 0xfaff, // CJK Compatibility Ideographs
		r >= 0xff00 && r <= 0xffef: // Halfwidth and Fullwidth Forms
		return true
	}
	return false
}

// qoderTierSelection is the tier a request should run under. The rationale
// upstream logs is not carried here: nothing in the gateway reports it.
type qoderTierSelection struct {
	Tier qoderTier
}

// resolveQoderContextTier picks the tier for a request. It returns nil to leave
// the payload untouched — either the model advertises no tiers, or the default
// one already fits.
func resolveQoderContextTier(modelConfig map[string]any, system string, messages, tools []any, preference string) *qoderTierSelection {
	tiers := qoderContextTiers(modelConfig)
	if len(tiers) == 0 {
		return nil
	}

	mode := qoderTierMode(preference)
	largest := tiers[len(tiers)-1]
	defaultTier := tiers[0]
	for _, tier := range tiers {
		if tier.IsDefault {
			defaultTier = tier
			break
		}
	}
	estimated := estimateQoderPromptTokens(system, messages, tools)
	need := int(math.Ceil(float64(estimated) * (1 + qoderTierHeadroom)))

	switch strings.ToLower(mode) {
	case qoderTierModeMax:
		return &qoderTierSelection{Tier: largest}
	case qoderTierModeDefault:
		return &qoderTierSelection{Tier: defaultTier}
	}

	// auto: keep the upstream default while the prompt still fits it.
	currentLimit := parseQoderTierTokenCount(modelConfig["max_input_tokens"])
	if currentLimit == 0 {
		currentLimit = parseQoderTierTokenCount(modelConfig["maxInputTokens"])
	}
	if currentLimit == 0 {
		currentLimit = defaultTier.TokenCount
	}
	if need <= currentLimit {
		return nil
	}

	for _, tier := range tiers {
		if tier.TokenCount >= need && tier.TokenCount > currentLimit {
			return &qoderTierSelection{Tier: tier}
		}
	}
	if largest.TokenCount <= currentLimit {
		return nil // nothing bigger to escalate to
	}
	return &qoderTierSelection{Tier: largest}
}

// qoderTierMode normalizes the configured preference, defaulting to auto.
func qoderTierMode(preference string) string {
	if trimmed := strings.TrimSpace(preference); trimmed != "" {
		return trimmed
	}
	return qoderTierModeAuto
}

// applyQoderContextTier writes the chosen tier into a Qoder chat payload,
// mirroring the three places the IDE records it.
func applyQoderContextTier(payload map[string]any, tier qoderTier) {
	if payload == nil || tier.TokenCount == 0 {
		return
	}
	if params, ok := payload["parameters"].(map[string]any); ok {
		params["context_length"] = tier.TokenCount
	}
	chatContext, ok := payload["chat_context"].(map[string]any)
	if !ok {
		return
	}
	extra, ok := chatContext["extra"].(map[string]any)
	if !ok {
		return
	}
	override, ok := extra["ideModelConfigOverride"].(map[string]any)
	if !ok {
		override = map[string]any{}
		extra["ideModelConfigOverride"] = override
	}
	override["max_input_tokens"] = tier.TokenCount
	if modelConfig, ok := payload["model_config"].(map[string]any); ok {
		modelConfig["max_input_tokens"] = tier.TokenCount
	}
}
