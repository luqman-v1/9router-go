package guardrails

import (
	"bytes"
	json "encoding/json/v2"
	"strings"
)

// The outbound tap judges the text a model produced, not the bytes the wire
// carried. Those are not the same thing: an address split across two deltas is
// `"billing@acme-"` and `"corp.com"` in JSON but `billing@acme-corp.com` in
// text, and only the second form matches anything. So each event is decoded,
// its model-visible text is extracted, and the event is re-encoded from the
// masked result.
//
// Every other line of an event — its name, its id, any comment — is carried over
// untouched, so filtering never rewrites framing the client depends on.

// sseEventSeparator ends an event, per the SSE spec.
const sseEventSeparator = "\n\n"

// nextSSEEvent splits the first complete event off an accumulated byte buffer.
// It reports false when the buffer holds no complete event yet, leaving the
// caller holding the partial one until the rest of it arrives.
func nextSSEEvent(buf []byte) (event, rest []byte, ok bool) {
	normalized := bytes.ReplaceAll(buf, []byte("\r\n"), []byte("\n"))
	idx := bytes.Index(normalized, []byte(sseEventSeparator))
	if idx < 0 {
		return nil, buf, false
	}
	end := idx + len(sseEventSeparator)
	return normalized[:end], normalized[end:], true
}

// sseDataFields returns an event's data fields and whether it carried any. An
// event with only comments or an event name carries nothing a policy can judge
// and is relayed untouched.
func sseDataFields(event []byte) (fields [][]byte, ok bool) {
	for _, line := range bytes.Split(bytes.TrimRight(event, "\n"), []byte("\n")) {
		if bytes.HasPrefix(line, []byte(":")) {
			continue // comment / keep-alive
		}
		field, value, found := bytes.Cut(line, []byte(":"))
		if !found || string(field) != "data" {
			continue
		}
		// Per the SSE spec, strip a single optional leading space.
		fields = append(fields, bytes.TrimPrefix(value, []byte(" ")))
	}
	return fields, len(fields) > 0
}

// ssePayload is the joined data fields of an event, which is what a scanner
// judges.
func ssePayload(event []byte) ([]byte, bool) {
	fields, ok := sseDataFields(event)
	if !ok {
		return nil, false
	}
	return bytes.Join(fields, []byte("\n")), true
}

// splitEvent separates an event's non-data lines from its data fields, so the
// envelope can be rebuilt around a masked payload.
func splitEvent(event []byte) (prefix []byte, fields [][]byte) {
	for _, line := range bytes.Split(bytes.TrimRight(event, "\n"), []byte("\n")) {
		if bytes.HasPrefix(line, []byte(":")) {
			prefix = append(prefix, line...)
			prefix = append(prefix, '\n')
			continue
		}
		field, _, found := bytes.Cut(line, []byte(":"))
		if found && string(field) == "data" {
			fields = append(fields, bytes.TrimPrefix(bytes.TrimPrefix(line, []byte("data:")), []byte(" ")))
			continue
		}
		prefix = append(prefix, line...)
		prefix = append(prefix, '\n')
	}
	return prefix, fields
}

// rebuildEvent writes a masked payload back into an event's envelope.
func rebuildEvent(prefix []byte, payload []byte) []byte {
	out := bytes.NewBuffer(make([]byte, 0, len(prefix)+len(payload)+16))
	out.Write(prefix)
	out.WriteString("data: ")
	out.Write(payload)
	out.WriteString("\n\n")
	return out.Bytes()
}

// decodePayload decodes one event's JSON payload, reporting whether it is JSON
// at all.
//
// It is not for a sentinel ("[DONE]"), a bare usage chunk, or a provider's own
// error frame — none of which carry model text a policy would judge, and all of
// which are relayed exactly as they arrived.
func decodePayload(payload []byte) (map[string]any, bool) {
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil, false
	}
	return decoded, true
}

// payloadText returns, in document order, the strings a policy judges: the
// delta content on an OpenAI or Responses chunk, or the text delta on an
// Anthropic one.
//
// Only those are judged. A whole-payload scan would also read the fields a
// provider put there — ids, model names, finish reasons — and a detector that
// fired on those would redact metadata the client needs to make sense of the
// frame.
func payloadText(payload map[string]any) []string {
	var out []string
	for _, choice := range sliceOf(payload["choices"]) {
		choiceMap, ok := choice.(map[string]any)
		if !ok {
			continue
		}
		delta, ok := choiceMap["delta"].(map[string]any)
		if !ok {
			continue
		}
		if text, ok := delta["content"].(string); ok {
			out = append(out, text)
		}
	}
	if block, ok := payload["content_block"].(map[string]any); ok {
		if text, ok := block["text"].(string); ok {
			out = append(out, text)
		}
	}
	if text, ok := payload["delta"].(string); ok {
		out = append(out, text)
	}
	return out
}

// sliceOf normalises a decoded JSON array.
func sliceOf(v any) []any {
	s, _ := v.([]any)
	return s
}

// setPayloadText writes replacement strings back into a decoded payload, in the
// same order payloadText returned them.
func setPayloadText(payload map[string]any, values []string) {
	i := 0
	next := func() string {
		v := values[i]
		i++
		return v
	}
	for _, choice := range sliceOf(payload["choices"]) {
		choiceMap, ok := choice.(map[string]any)
		if !ok {
			continue
		}
		delta, ok := choiceMap["delta"].(map[string]any)
		if !ok {
			continue
		}
		if _, ok := delta["content"].(string); ok {
			delta["content"] = next()
		}
	}
	if block, ok := payload["content_block"].(map[string]any); ok {
		if _, ok := block["text"].(string); ok {
			block["text"] = next()
		}
	}
	if _, ok := payload["delta"].(string); ok {
		payload["delta"] = next()
	}
}

// joinText concatenates a frame's judged strings.
//
// The join is deliberately empty: it makes every string's offset in the joined
// text the same in the original and in the masked text, which is what lets a
// replacement be written back into the exact event that carried it.
func joinText(parts []string) string { return strings.Join(parts, "") }

// decodeOrNil decodes a payload, reporting false for anything that is not a
// JSON object.
func decodeOrNil(payload []byte) (map[string]any, bool) {
	decoded, ok := decodePayload(payload)
	return decoded, ok
}

// remap distributes a masked window back across the frames that produced it.
//
// Each frame contributed a known span of the original text, and only the frames
// the redaction actually touched may change. So the change is located once —
// the common prefix, the replacement, the common suffix — and the replacement
// is given whole to the one frame the change begins in. Splitting the masked
// text back by the original span lengths would chop a replacement in half
// across two frames ("[REDACT" then "ED]"), which is exactly the artefact the
// redaction was supposed to prevent.
func remap(original, masked string, spans []int) []string {
	prefix := commonPrefixLen(original, masked)
	suffix := commonSuffixLen(original, masked, prefix)
	replacement := masked[prefix : len(masked)-suffix]
	// Everything after the change is identical in both strings, so it maps one
	// to one; only the replaced region has to be redistributed.
	tail := original[len(original)-suffix:]

	out := make([]string, len(spans))
	pos := 0
	for i, size := range spans {
		// Clamp: a frame whose text is empty must not advance the cursor past
		// the text, or the slice below runs off the end of the string.
		end := min(pos+max(size, 0), len(original))
		if end < pos {
			end = pos
		}
		var b strings.Builder
		// Text before the change keeps its original place.
		b.WriteString(original[pos:clamp(prefix, pos, end)])
		// The replacement lands whole in the frame the change begins in, so a
		// redacted value is never split across two deltas.
		if pos <= prefix && prefix <= end {
			b.WriteString(replacement)
		}
		// Text after the change is unchanged in both strings, so it maps one
		// to one.
		tailStart := len(original) - suffix
		if end > tailStart {
			from := clamp(pos-tailStart, 0, len(tail))
			b.WriteString(tail[from:clamp(end-tailStart, 0, len(tail))])
		}
		out[i] = b.String()
		pos = end
	}
	return out
}

// clamp bounds n to [lo, hi].
func clamp(n, lo, hi int) int {
	return min(max(n, lo), hi)
}

// commonPrefixLen is how far original and masked agree from the start.
func commonPrefixLen(a, b string) int {
	n := min(len(a), len(b))
	for i := range n {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

// commonSuffixLen is how far original and masked agree from the end, never
// counting bytes commonPrefixLen already claimed.
func commonSuffixLen(a, b string, prefix int) int {
	n := 0
	for prefix+n < min(len(a), len(b)) && a[len(a)-1-n] == b[len(b)-1-n] {
		n++
	}
	return n
}
