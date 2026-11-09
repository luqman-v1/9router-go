package executor

import "strings"

// claudeImageBlock converts one OpenAI `image_url` content part into the block
// the Claude Messages API defines for an image:
//
//	{"type":"image_url","image_url":{"url":"data:image/png;base64,..."}}
//	  -> {"type":"image","source":{"type":"base64","media_type":"image/png","data":"..."}}
//	{"type":"image_url","image_url":{"url":"https://..."}}
//	  -> {"type":"image","source":{"type":"url","url":"https://..."}}
//
// `image_url` may be the object form or a bare string (some clients send it).
// ok is false when the URL is neither a base64 data URI nor http(s), which
// Claude has no way to represent. Mirrors upstream
// open-sse/translator/request/openai-to-claude.js (IMAGE_URL branch).
func claudeImageBlock(part map[string]any) (block map[string]any, ok bool) {
	var url string
	switch u := part["image_url"].(type) {
	case string:
		url = u
	case map[string]any:
		url, _ = u["url"].(string)
	}
	url = strings.TrimSpace(url)

	if rest, found := strings.CutPrefix(url, "data:"); found {
		// The media type runs up to the FIRST ';' and what follows must be exactly
		// "base64", as in upstream's /^data:([^;]+);base64,([\s\S]+)$/. A
		// parameter such as ";charset=utf-8" would otherwise end up inside
		// media_type, which Anthropic rejects: the 400 this conversion removes,
		// reached through the other door.
		meta, data, found := strings.Cut(rest, ",")
		mediaType, params, _ := strings.Cut(meta, ";")
		if !found || params != "base64" || mediaType == "" || data == "" {
			return nil, false
		}
		return map[string]any{
			"type": "image",
			"source": map[string]any{
				"type":       "base64",
				"media_type": mediaType,
				"data":       data,
			},
		}, true
	}
	if strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "http://") {
		return map[string]any{
			"type":   "image",
			"source": map[string]any{"type": "url", "url": url},
		}, true
	}
	return nil, false
}

// convertClaudeImageParts rewrites every OpenAI `image_url` part of a content
// array into a Claude `image` block, and returns the content unchanged when it
// is not an array or holds none. It never mutates its input.
//
// Anthropic rejects the OpenAI shape with
// "messages.<n>.content.<i>: Input tag 'image_url' found using 'type' does not
// match any of the expected tags", and because the message stays in the
// client's history, every later request of the conversation failed too. A part
// Claude cannot represent (see claudeImageBlock) is dropped rather than
// forwarded: forwarding it would only reproduce that 400 for the whole session.
// Used for user turns, replayed assistant turns, and tool_result content, which
// all accept image blocks.
func convertClaudeImageParts(content any) any {
	parts, ok := content.([]any)
	if !ok {
		return content
	}
	changed := false
	for _, p := range parts {
		if m, isMap := p.(map[string]any); isMap && m["type"] == "image_url" {
			changed = true
			break
		}
	}
	if !changed {
		return content
	}
	out := make([]any, 0, len(parts))
	for _, p := range parts {
		m, isMap := p.(map[string]any)
		if !isMap || m["type"] != "image_url" {
			out = append(out, p)
			continue
		}
		if block, ok := claudeImageBlock(m); ok {
			out = append(out, block)
		}
	}
	return out
}

// imageEmptiedMarker tags an intermediate message whose content array held
// only image parts that Claude cannot represent, so convertClaudeImageParts left
// it with nothing. It never reaches the wire: dropImageEmptiedMessages removes
// the message, or the marker, before the body is built.
const imageEmptiedMarker = "\x00imageEmptied"

// emptiedByImageDrop reports whether converting `before` into `after` removed
// every part: a non-empty array of image parts that all turned out to be
// unrepresentable. A message the client itself sent with an empty array is not
// ours to repair and is not reported.
func emptiedByImageDrop(before, after any) bool {
	in, ok := before.([]any)
	if !ok || len(in) == 0 {
		return false
	}
	out, ok := after.([]any)
	return ok && len(out) == 0
}

// dropImageEmptiedMessages removes the messages convertClaudeImageParts emptied.
// Anthropic rejects `content: []` ("messages.<n>: content must not be empty"),
// and the bad turn stays in the client's history, so every later request of the
// conversation fails the same way. Only messages emptied by this conversion are
// touched; the same-role merge that follows joins the neighbours the removal
// leaves adjacent.
//
// A conversation this would empty completely keeps its turns (without the
// marker): an empty `messages` array is its own rejection, and the caller that
// sent only unrepresentable images should get the provider's reason rather than
// a silently rewritten request.
func dropImageEmptiedMessages(messages []map[string]any) []map[string]any {
	kept := make([]map[string]any, 0, len(messages))
	for _, m := range messages {
		if m[imageEmptiedMarker] != nil {
			continue
		}
		kept = append(kept, m)
	}
	if len(kept) == 0 {
		for _, m := range messages {
			delete(m, imageEmptiedMarker)
		}
		return messages
	}
	for _, m := range kept {
		delete(m, imageEmptiedMarker)
	}
	return kept
}
