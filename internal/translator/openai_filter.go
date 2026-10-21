package translator

import (
	json "encoding/json/v2"
)

// OpenAI content/message block types missing from the package's existing
// sets (claude_blocks.go, responses_request.go), mirroring
// open-sse/translator/schema/blocks.js.
const (
	openaiBlockImage      = "image"
	openaiBlockInputAudio = "input_audio"
	openaiBlockAudioURL   = "audio_url"
	openaiBlockFile       = "file"

	claudeBlockRedactedThinking = "redacted_thinking"
	claudeBlockToolUse          = "tool_use"
)

// validOpenAIContentTypes mirrors VALID_OPENAI_CONTENT_TYPES.
var validOpenAIContentTypes = map[string]bool{
	openaiBlockText:       true,
	openaiBlockImageURL:   true,
	openaiBlockImage:      true,
	openaiBlockInputAudio: true,
	openaiBlockAudioURL:   true,
	openaiBlockFile:       true,
}

// FilterOptions carries the per-provider quirks the filter honours. The one
// upstream registers is preserveCacheControl: a provider registered with it
// keeps the cache_control markers on its content blocks (the alicode family
// on DashScope), so the filter must not strip them.
type FilterOptions struct {
	PreserveCacheControl bool
}

// FilterToOpenAIFormat normalises a body to clean OpenAI shape — port of
// filterToOpenAIFormat in open-sse/translator/formats/openai.js:10-133.
func FilterToOpenAIFormat(body map[string]any, opts FilterOptions) map[string]any {
	msgs, ok := body["messages"].([]any)
	if !ok {
		return body
	}
	body["messages"] = filterMessages(msgs, opts)
	normalizeTools(body)
	normalizeToolChoice(body)
	return body
}

// FilterToOpenAIFormatJSON is FilterToOpenAIFormat over raw bytes;
// decode errors return the input unchanged.
func FilterToOpenAIFormatJSON(body []byte, opts FilterOptions) []byte {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil || m == nil {
		return body
	}
	FilterToOpenAIFormat(m, opts)
	out, err := json.Marshal(m, json.Deterministic(true))
	if err != nil {
		return body
	}
	return out
}

// stripBlock drops the signature and, unless the provider keeps them, the
// cache_control markers from a kept block.
func stripBlock(block map[string]any, opts FilterOptions) map[string]any {
	out := shallowCopy(block)
	delete(out, "signature")
	if !opts.PreserveCacheControl {
		delete(out, "cache_control")
	}
	return out
}

func shallowCopy(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func stringField(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func parametersField(m map[string]any, key string) any {
	p, ok := m[key]
	if ok && p != nil {
		return p
	}
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
