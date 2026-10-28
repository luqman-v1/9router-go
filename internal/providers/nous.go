package providers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
)

const (
	NousInferenceBaseURL               = "https://inference-api.nousresearch.com/v1"
	NousChatCompletionsURL             = "https://inference-api.nousresearch.com/v1/chat/completions"
	NousModelsURL                      = "https://inference-api.nousresearch.com/v1/models"
	NousPortalBaseURL                  = "https://portal.nousresearch.com"
	NousDeviceCodeURL                  = "https://portal.nousresearch.com/api/oauth/device/code"
	NousTokenURL                       = "https://portal.nousresearch.com/api/oauth/token"
	NousAgentKeyURL                    = "https://portal.nousresearch.com/api/oauth/agent-key"
	NousOAuthClientID                  = "hermes-cli"
	NousOAuthScope                     = "inference:invoke inference:mint_agent_key"
	NousValidationModel                = "poolside/laguna-xs-2.1:free"
	NousAttributionHostname            = "inference-api.nousresearch.com"
	NousAttributionChatCompletionsPath = "/v1/chat/completions"
)

var NousAttributionTags = []string{
	"product=9router",
	"client=9router",
	"user=9router",
}

// NousCatalogModel matches the normalized catalogue shape expected by 9router.
type NousCatalogModel struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	ContextLength   *int64  `json:"contextLength,omitempty"`
	MaxOutputTokens *int64  `json:"maxOutputTokens,omitempty"`
	Description     *string `json:"description,omitempty"`
	UpstreamModelID string  `json:"upstreamModelId,omitempty"`
}

func asPositiveInt64(val any) *int64 {
	switch v := val.(type) {
	case float64:
		if v > 0 {
			n := int64(v)
			return &n
		}
	case int64:
		if v > 0 {
			return &v
		}
	case int:
		if v > 0 {
			n := int64(v)
			return &n
		}
	case string:
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil && n > 0 {
			return &n
		}
	}
	return nil
}

func isTextCapable(m map[string]any) bool {
	arch, ok := m["architecture"].(map[string]any)
	if !ok || arch == nil {
		return true
	}
	rawMods, ok := arch["output_modalities"]
	if !ok || rawMods == nil {
		return true
	}
	mods, ok := rawMods.([]any)
	if !ok || len(mods) == 0 {
		return true
	}
	for _, mod := range mods {
		if s, ok := mod.(string); ok && s == "text" {
			return true
		}
	}
	return false
}

func getRawModelList(payload any) []map[string]any {
	switch p := payload.(type) {
	case []map[string]any:
		return p
	case []any:
		out := make([]map[string]any, 0, len(p))
		for _, item := range p {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	case map[string]any:
		for _, key := range []string{"data", "models", "results"} {
			if arr, ok := p[key].([]any); ok {
				return getRawModelList(arr)
			}
			if arr, ok := p[key].([]map[string]any); ok {
				return arr
			}
		}
	}
	return nil
}

func isZeroPrice(val any) bool {
	if val == nil {
		return false
	}
	switch v := val.(type) {
	case float64:
		return v == 0
	case int:
		return v == 0
	case int64:
		return v == 0
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return false
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f == 0
		}
	}
	return false
}

// NormalizeNousModels filters and normalizes live /v1/models entries from Nous Research.
func NormalizeNousModels(payload any) []NousCatalogModel {
	raw := getRawModelList(payload)
	var out []NousCatalogModel

	for _, m := range raw {
		if !isTextCapable(m) {
			continue
		}
		id, _ := m["id"].(string)
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		name, _ := m["name"].(string)
		if name == "" {
			name = id
		}

		topProvider, _ := m["top_provider"].(map[string]any)

		ctxVal := m["context_length"]
		if ctxVal == nil && topProvider != nil {
			ctxVal = topProvider["context_length"]
		}
		contextLength := asPositiveInt64(ctxVal)

		var maxOutputTokens *int64
		if topProvider != nil {
			maxOutputTokens = asPositiveInt64(topProvider["max_completion_tokens"])
		}

		var descPtr *string
		if desc, ok := m["description"].(string); ok && desc != "" {
			descPtr = &desc
		}

		out = append(out, NousCatalogModel{
			ID:              id,
			Name:            name,
			ContextLength:   contextLength,
			MaxOutputTokens: maxOutputTokens,
			Description:     descPtr,
		})
	}

	// Keep first-party Nous models prominent while preserving upstream order
	// within both first-party and third-party groups (stable sort).
	sort.SliceStable(out, func(i, j int) bool {
		iFirstParty := strings.HasPrefix(out[i].ID, "nousresearch/")
		jFirstParty := strings.HasPrefix(out[j].ID, "nousresearch/")
		if iFirstParty != jFirstParty {
			return iFirstParty
		}
		return false
	})

	return out
}

// NormalizeRoutedNousModels prefixes models with "nous/" for routed model selectors.
func NormalizeRoutedNousModels(payload any) []NousCatalogModel {
	models := NormalizeNousModels(payload)
	for i := range models {
		models[i].UpstreamModelID = models[i].ID
		models[i].ID = "nous/" + models[i].ID
	}
	return models
}

// NormalizeNousFreeModels filters for zero-priced models with >= 200k context.
func NormalizeNousFreeModels(payload any) []NousCatalogModel {
	raw := getRawModelList(payload)
	var freeModels []map[string]any

	for _, m := range raw {
		if !isTextCapable(m) {
			continue
		}
		topProvider, _ := m["top_provider"].(map[string]any)
		ctxVal := m["context_length"]
		if ctxVal == nil && topProvider != nil {
			ctxVal = topProvider["context_length"]
		}
		clen := asPositiveInt64(ctxVal)
		if clen == nil || *clen < 200000 {
			continue
		}

		pricing, _ := m["pricing"].(map[string]any)
		if pricing == nil || !isZeroPrice(pricing["prompt"]) || !isZeroPrice(pricing["completion"]) {
			continue
		}

		freeModels = append(freeModels, m)
	}

	normalized := NormalizeNousModels(freeModels)
	sort.SliceStable(normalized, func(i, j int) bool {
		var a, b int64
		if normalized[i].ContextLength != nil {
			a = *normalized[i].ContextLength
		}
		if normalized[j].ContextLength != nil {
			b = *normalized[j].ContextLength
		}
		return a > b
	})

	return normalized
}

// ShouldApplyNousAttribution checks whether a target URL qualifies for Nous
// attribution tags: scheme must be "https", hostname must match
// NousAttributionHostname, and path must match NousAttributionChatCompletionsPath
// (or the /v1 base path that normalizes to it).
func ShouldApplyNousAttribution(rawURL string) bool {
	if rawURL == "" {
		return false
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	if u.Scheme != "https" {
		return false
	}
	if u.Hostname() != NousAttributionHostname {
		return false
	}
	p := strings.TrimRight(u.Path, "/")
	if p == "/v1" {
		p = "/v1/chat/completions"
	}
	return p == NousAttributionChatCompletionsPath
}

// ApplyNousThinking translates thinking/reasoning parameters into Nous's wire
// dialect for models declaring ThinkingFormat: "nous":
//   - When reasoning is enabled, emits nested {"reasoning": {"enabled": true, "effort": ...}}
//     where effort is preserved, or "medium" for auto mode.
//   - When thinking is disabled ("none" or "off"), omits the reasoning object
//     if the model can disable thinking.
//   - Removes top-level reasoning_effort and other thinking control fields.
//   - If no thinking was requested, leaves the body untouched.
//   - Only applies when provider is "nous" (or its alias "ns") to prevent
//     cross-provider mutation (AGENTS.md §3.A).
func ApplyNousThinking(body []byte, provider string) []byte {
	// Strictly scope to the nous provider (and its alias "ns") to prevent
	// cross-provider leakage where a non-Nous provider serving a model with
	// a matching ID would incorrectly get the Nous dialect applied.
	if provider != "nous" && provider != "ns" {
		return body
	}

	if !bytes.Contains(body, []byte("reasoning")) &&
		!bytes.Contains(body, []byte("thinking")) &&
		!bytes.Contains(body, []byte("output_config")) &&
		!bytes.Contains(body, []byte("(")) {
		return body
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil || raw == nil {
		return body
	}

	rawModel, _ := raw["model"].(string)
	cleanModel, suffix := splitThinkingSuffix(rawModel)
	modelForCaps := strings.TrimPrefix(cleanModel, "nous/")
	caps := GetCapabilitiesForModel(provider, modelForCaps)
	if caps.ThinkingFormat != "nous" {
		return body
	}

	var intent string
	if suffix != "" {
		intent = strings.Trim(suffix, "()")
		raw["model"] = cleanModel
	} else if oc, ok := raw["output_config"].(map[string]any); ok && oc["effort"] != nil {
		intent, _ = oc["effort"].(string)
	} else if re, ok := raw["reasoning_effort"].(string); ok && re != "" {
		intent = re
	} else if r, ok := raw["reasoning"].(map[string]any); ok {
		if eff, ok := r["effort"].(string); ok && eff != "" {
			intent = eff
		} else if enabled, ok := r["enabled"].(bool); ok {
			if !enabled {
				intent = "none"
			} else {
				intent = "enabled"
			}
		}
	} else if t, ok := raw["thinking"].(map[string]any); ok {
		if typ, ok := t["type"].(string); ok {
			if strings.EqualFold(typ, "disabled") {
				intent = "none"
			} else if strings.EqualFold(typ, "adaptive") || strings.EqualFold(typ, "enabled") {
				intent = "auto"
			}
		}
	} else if et, ok := raw["enable_thinking"].(bool); ok {
		if !et {
			intent = "none"
		} else {
			intent = "auto"
		}
	}

	if intent == "" {
		return body
	}

	delete(raw, "reasoning_effort")
	delete(raw, "thinking")
	delete(raw, "output_config")
	delete(raw, "enable_thinking")
	delete(raw, "thinking_budget")
	delete(raw, "reasoning")

	intentLower := strings.ToLower(strings.TrimSpace(intent))
	none := intentLower == "none" || intentLower == "off"
	canDisable := caps.ThinkingCanDisable == nil || *caps.ThinkingCanDisable

	if none && canDisable {
		// Omit reasoning
	} else {
		var level string
		switch {
		case intentLower == "auto":
			level = "medium"
		case none && !canDisable:
			level = "minimal"
		case intentLower != "enabled":
			level = intentLower
		}

		reasoning := map[string]any{"enabled": true}
		if level != "" {
			reasoning["effort"] = level
		}
		raw["reasoning"] = reasoning
	}

	updated, err := json.Marshal(raw)
	if err != nil {
		return body
	}
	return updated
}

// ApplyNousAttribution tags API-key requests with 9router attribution tags,
// preserving any caller-provided tags and removing empty tags.
func ApplyNousAttribution(body []byte) []byte {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return body
	}

	if rawTags, exists := req["tags"]; exists && rawTags != nil {
		if _, ok := rawTags.([]any); !ok {
			return body
		}
	}

	var existingTags []string
	if rawTags, ok := req["tags"].([]any); ok {
		for _, item := range rawTags {
			if s, ok := item.(string); ok {
				existingTags = append(existingTags, s)
			}
		}
	} else if rawTagsStr, ok := req["tags"].([]string); ok {
		existingTags = append(existingTags, rawTagsStr...)
	}

	var cleanedTags []string
	for _, fallback := range NousAttributionTags {
		idx := strings.Index(fallback, "=")
		if idx == -1 {
			continue
		}
		prefix := fallback[:idx+1]

		// Filter out empty values for this prefix (e.g. "product=", "user=  ")
		for _, tag := range existingTags {
			if strings.HasPrefix(tag, prefix) {
				val := strings.TrimSpace(tag[len(prefix):])
				if val != "" && !slices.Contains(cleanedTags, tag) {
					cleanedTags = append(cleanedTags, tag)
				}
			}
		}

		// If no valid tag exists with this prefix, add the fallback
		hasPrefix := false
		for _, tag := range cleanedTags {
			if strings.HasPrefix(tag, prefix) {
				hasPrefix = true
				break
			}
		}
		if !hasPrefix {
			cleanedTags = append(cleanedTags, fallback)
		}
	}

	// Keep any custom caller tags (tags not matching product=, client=, user=)
	for _, tag := range existingTags {
		isAttribution := false
		for _, fallback := range NousAttributionTags {
			idx := strings.Index(fallback, "=")
			if idx != -1 && strings.HasPrefix(tag, fallback[:idx+1]) {
				isAttribution = true
				break
			}
		}
		if !isAttribution && !slices.Contains(cleanedTags, tag) {
			cleanedTags = append(cleanedTags, tag)
		}
	}

	req["tags"] = cleanedTags
	updated, err := json.Marshal(req)
	if err != nil {
		return body
	}
	return updated
}

// IsNousApiKeyAccepted returns true if the status code indicates accepted credentials.
func IsNousApiKeyAccepted(status int) bool {
	return status >= 200 && status < 300
}

// GetNousApiKeyValidationError maps rejected status codes to user-facing messages.
func GetNousApiKeyValidationError(status int) string {
	if status == 401 || status == 403 {
		return "Invalid or inactive API key"
	}
	return fmt.Sprintf("Unable to verify API key (Nous returned %d)", status)
}
