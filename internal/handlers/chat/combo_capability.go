package chat

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"slices"
	"sort"
	"strings"

	"9router/proxy/internal/providers"
)

func detectNewTurn(body []byte) bool {
	var req struct {
		Messages []struct {
			Role    string         `json:"role"`
			Content jsontext.Value `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return true
	}
	for i := len(req.Messages) - 1; i >= 0; i-- {
		msg := req.Messages[i]
		if msg.Role == "tool" {
			return false
		}
		if msg.Role != "user" {
			continue
		}
		return contentHasText(msg.Content)
	}
	return true
}

// contentHasText reports whether OpenAI message content contains plain text.
func contentHasText(content jsontext.Value) bool {
	var s string
	if err := json.Unmarshal(content, &s); err == nil {
		return s != ""
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &parts); err == nil {
		for _, p := range parts {
			if p.Text != "" {
				return true
			}
		}
	}
	return false
}

// DetectRequiredCapabilities extracts the capabilities a request needs.
//
// Modalities (vision/pdf/audioInput/videoInput) are scanned only on the current
// user turn — the trailing run after the last assistant/model message, which
// may span several messages. Media in older turns must not pin the combo to a
// vision model; that history is stripped or placeholdered downstream.
// "tools" is request-wide because it lives in the top-level tools array.
//
// Port of upstream detectRequiredCapabilities (open-sse/services/combo.js).
func DetectRequiredCapabilities(body []byte) map[string]bool {
	required := make(map[string]bool)

	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return required
	}

	// OpenAI / Claude / Hermes / Ollama.
	for _, msg := range trailingUserItems(m["messages"]) {
		scanMessage(msg, required)
	}
	// Responses API.
	for _, item := range trailingUserItems(m["input"]) {
		scanContentBlocks(itemContent(item), required)
	}
	// Gemini / Antigravity.
	contents, _ := m["contents"].([]any)
	if len(contents) == 0 {
		if req, ok := m["request"].(map[string]any); ok {
			contents, _ = req["contents"].([]any)
		}
	}
	for _, c := range trailingUserItems(contents) {
		scanContentBlocks(itemParts(c), required)
	}

	if tools, ok := m["tools"].([]any); ok {
		for _, t := range tools {
			tm, ok := t.(map[string]any)
			if !ok {
				continue
			}
			if tm["type"] == "function" || tm["function"] != nil || tm["functionDeclarations"] != nil {
				required["tools"] = true
				break
			}
		}
	}

	return required
}

// isAssistantRole reports whether a message is a model turn, which ends the
// current user run.
func isAssistantRole(role string) bool {
	return role == "assistant" || role == "model"
}

// trailingUserItems returns the trailing run of items after the last
// assistant/model entry — the current user turn. It may span several messages
// (text + image split across blocks).
func trailingUserItems(arr any) []any {
	list, ok := arr.([]any)
	if !ok || len(list) == 0 {
		return nil
	}
	i := len(list) - 1
	for i >= 0 {
		role := ""
		if m, ok := list[i].(map[string]any); ok {
			role, _ = m["role"].(string)
		}
		if isAssistantRole(role) {
			break
		}
		i--
	}
	return list[i+1:]
}

// itemParts reads a Gemini content entry's parts field.
func itemParts(item any) any {
	if m, ok := item.(map[string]any); ok {
		return m["parts"]
	}
	return nil
}

// scanMessage checks one message for capability requirements, covering the
// shapes that carry media outside the standard content array.
func scanMessage(msg any, required map[string]bool) {
	m, ok := msg.(map[string]any)
	if !ok {
		return
	}

	// Ollama / Hermes images array.
	if images, ok := m["images"].([]any); ok && len(images) > 0 {
		required["vision"] = true
	}

	// Vercel AI SDK / Hermes attachments / experimental_attachments.
	attachments, _ := m["experimental_attachments"].([]any)
	if len(attachments) == 0 {
		attachments, _ = m["attachments"].([]any)
	}
	for _, raw := range attachments {
		att, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		mime := firstString(att, "contentType", "mediaType")
		if mime == "" {
			if url, ok := att["url"].(string); ok {
				mime = dataURIMime(url)
			}
		}
		if mime != "" {
			addByMime(required, mime)
		} else if att["url"] != nil || att["data"] != nil {
			required["vision"] = true
		}
	}

	// Direct message-level modality properties.
	if m["image_url"] != nil || m["image"] != nil {
		required["vision"] = true
	}
	if m["audio_url"] != nil || m["audio"] != nil {
		required["audioInput"] = true
	}

	scanContentBlocks(m["content"], required)

	// Data URIs embedded in plain string content.
	if s, ok := m["content"].(string); ok {
		switch {
		case strings.Contains(s, "data:image/"):
			required["vision"] = true
		case strings.Contains(s, "data:audio/"):
			required["audioInput"] = true
		case strings.Contains(s, "data:application/pdf"):
			required["pdf"] = true
		}
	}
}

// scanContentBlocks walks a content array.
func scanContentBlocks(content any, required map[string]bool) {
	blocks, ok := content.([]any)
	if !ok {
		return
	}
	for _, b := range blocks {
		scanContentBlock(b, required)
	}
}

// scanContentBlock checks a single content block for capability requirements.
func scanContentBlock(block any, required map[string]bool) {
	b, ok := block.(map[string]any)
	if !ok {
		return
	}
	typ, _ := b["type"].(string)
	switch typ {
	case "image_url", "image", "input_image":
		required["vision"] = true
	case "input_audio", "audio_url", "audio":
		required["audioInput"] = true
	case "input_video", "video_url", "video":
		required["videoInput"] = true
	case "file", "document", "input_file":
		// Infer the modality from an embedded mime when available; fall back
		// to pdf for a generic file.
		if mime := fileBlockMime(b); mime != "" {
			addByMime(required, mime)
		} else {
			required["pdf"] = true
		}
	}

	// Gemini parts carry a mime on inlineData / fileData.
	if inner, ok := b["inlineData"].(map[string]any); ok {
		addByMime(required, stringOf(inner, "mimeType"))
	}
	if inner, ok := b["fileData"].(map[string]any); ok {
		addByMime(required, stringOf(inner, "mimeType"))
	}
}

// fileBlockMime digs a mime type out of the several shapes a file block uses
// to carry its payload.
func fileBlockMime(b map[string]any) string {
	if ia, ok := b["input_audio"].(map[string]any); ok {
		if format := stringOf(ia, "format"); format != "" {
			return "audio/" + format
		}
	}
	if file, ok := b["file"].(map[string]any); ok {
		if data := stringOf(file, "file_data"); data != "" {
			return dataURIMime(data)
		}
	}
	if source, ok := b["source"].(map[string]any); ok {
		if mime := stringOf(source, "media_type"); mime != "" {
			return mime
		}
		if data := stringOf(source, "data"); data != "" {
			return dataURIMime(data)
		}
	}
	return ""
}

// addByMime maps a mime type to its capability. An empty or unknown mime is
// ignored so an unrelated content type does not pin the combo.
func addByMime(required map[string]bool, mime string) {
	switch {
	case mime == "":
		return
	case strings.HasPrefix(mime, "image/"):
		required["vision"] = true
	case mime == "application/pdf":
		required["pdf"] = true
	case strings.HasPrefix(mime, "audio/"):
		required["audioInput"] = true
	case strings.HasPrefix(mime, "video/"):
		required["videoInput"] = true
	}
}

// dataURIMime extracts the mime from a "data:<mime>;base64,..." URL.
func dataURIMime(url string) string {
	rest, ok := strings.CutPrefix(url, "data:")
	if !ok {
		return ""
	}
	mime, _, ok := strings.Cut(rest, ",")
	if !ok {
		return ""
	}
	mime, _, _ = strings.Cut(mime, ";")
	return mime
}

// firstString returns the first key that holds a string value.
func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v := stringOf(m, k); v != "" {
			return v
		}
	}
	return ""
}

// stringOf reads a string field, returning "" when absent or another type.
func stringOf(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

// ReorderByCapabilities stably floats models satisfying constraints to the
// front. Never drops a model, so the fallback chain stays intact.
//
// Tier 0: satisfies all hard AND all soft capabilities.
// Tier 1: satisfies every hard capability.
// Tier 2: the rest.
//
// Port of upstream reorderByCapabilities (open-sse/services/combo.js).
func ReorderByCapabilities(comboModels []string, required map[string]bool) []string {
	if len(required) == 0 || len(comboModels) <= 1 {
		return comboModels
	}

	hard := requiredHardCaps(required)
	soft := make([]string, 0, len(required))
	for cap := range required {
		if !slices.Contains(adapterCapabilityKeys, cap) {
			soft = append(soft, cap)
		}
	}

	tier := make(map[string]int, len(comboModels))
	order := make([]string, len(comboModels))
	for i, m := range comboModels {
		tier[m] = capabilityTier(m, hard, soft)
		order[i] = m
	}

	sort.SliceStable(order, func(i, j int) bool { return tier[order[i]] < tier[order[j]] })
	return order
}

// capabilityTier scores one model entry: 0 covers hard+soft, 1 covers hard
// only, 2 covers neither.
func capabilityTier(modelEntry string, hard, soft []string) int {
	caps := modelEntryCaps(modelEntry)
	for _, c := range hard {
		if !capabilityFlag(caps, c) {
			return 2
		}
	}
	for _, c := range soft {
		if !capabilityFlag(caps, c) {
			return 1
		}
	}
	return 0
}

// modelEntryCaps resolves the capability block for a "provider/model" entry.
func modelEntryCaps(modelEntry string) providers.Capabilities {
	provider, model, _ := strings.Cut(modelEntry, "/")
	return providers.GetCapabilitiesForModel(provider, model)
}

// comboRespectsStoredOrder reports whether this combo asked to skip the
// rotation strategy and walk its stored model list in order. The flag lives in
// the combo's own strategy row (settings.comboStrategies[name]) so it can
// differ per combo, the same place smartPolicy and sessionSticky live.
//
// A settings read that fails is treated as "not set" rather than as an error:
// the caller is mid-request with a combo to route, and the rotation strategy
// is the behaviour every combo had before the flag existed.
func (h *ChatHandler) comboRespectsStoredOrder(comboName string) bool {
	if h.Repo == nil || comboName == "" {
		return false
	}
	settings, err := h.Repo.GetSettings()
	if err != nil || settings == nil {
		return false
	}
	cfg, ok := settings.ComboStrategies[comboName]
	if !ok {
		return false
	}
	return cfg.RespectStoredOrder
}

// NormalizeComboWeights validates and normalizes weights for the given model list.
// Missing models default to weight 1.
// Weights are clamped to [0, 1000].
// Returns nil if rawWeights is empty, all models have weight 1, or no model has weight > 0.
