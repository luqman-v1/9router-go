package translator

import (
	json "encoding/json/v2"
	"errors"
)

// ErrNoMessages reports that the request carries no messages. Both OpenAI
// and Anthropic answer an empty messages array with a 400, so the caller
// fails the request locally instead of spending the call.
var ErrNoMessages = errors.New("the request carries no messages")

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
	filterToOpenAIFormat(body, opts)
	return body
}

// filterToOpenAIFormat normalises the body and reports whether it changed
// anything. The JSON entry point needs the flag: a body the filter does not
// touch is returned byte for byte, so a plain request pays no
// re-serialisation on the way out.
func filterToOpenAIFormat(body map[string]any, opts FilterOptions) bool {
	changed := false
	if msgs, ok := body["messages"].([]any); ok {
		if filtered, messagesChanged := filterMessages(msgs, opts); messagesChanged {
			body["messages"] = filtered
			changed = true
		}
	}
	if normalizeTools(body) {
		changed = true
	}
	if normalizeToolChoice(body) {
		changed = true
	}
	return changed
}

// FilterToOpenAIFormatJSON is FilterToOpenAIFormat over raw bytes;
// decode errors return the input unchanged. A body the filter does not change
// is returned unchanged as well, so the bytes the client sent are the bytes
// the upstream sees.
//
// A body whose message list ends up empty is reported as ErrNoMessages: the
// upstream would answer that with a 400, so the caller answers it here.
func FilterToOpenAIFormatJSON(body []byte, opts FilterOptions) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil || m == nil {
		return body, nil
	}
	changed := filterToOpenAIFormat(m, opts)
	if msgs, ok := m["messages"].([]any); ok && len(msgs) == 0 {
		return body, ErrNoMessages
	}
	if !changed {
		return body, nil
	}
	out, err := json.Marshal(m, json.Deterministic(true))
	if err != nil {
		return body, nil
	}
	return out, nil
}

// stripBlock drops the signature and, unless the provider keeps them, the
// cache_control markers from a kept block. The bool reports whether anything
// was dropped.
func stripBlock(block map[string]any, opts FilterOptions) (map[string]any, bool) {
	out := shallowCopy(block)
	changed := false
	if _, has := out["signature"]; has {
		delete(out, "signature")
		changed = true
	}
	if !opts.PreserveCacheControl {
		if _, has := out["cache_control"]; has {
			delete(out, "cache_control")
			changed = true
		}
	}
	return out, changed
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
