package media

import (
	"bytes"
	jsontext "encoding/json/jsontext"
	json "encoding/json/v2"
	"strings"
)

// measureJSONInput counts inline media in a JSON body without materializing it.
//
// jsontext tokenizes the body in place: handed a *bytes.Buffer it borrows the
// caller's slice rather than copying, and a string value is measured from its
// token span instead of being decoded into a Go string. Unmarshaling the body
// into a generic tree instead costs a second full copy of it — an 8MB body
// measured +8MB of heap, a 32MB body +32MB — at the moment the cap is checked,
// on the box AGENTS.md §4.G expects to run this.
func measureJSONInput(body []byte) (mediaInputUse, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return mediaInputUse{}, nil
	}
	dec := jsontext.NewDecoder(bytes.NewBuffer(body))
	var use mediaInputUse
	// member[d] is the member name in effect at stack depth d: the key just
	// read for a scalar, or inherited from the enclosing member for a value
	// inside a container, so {"images": ["…"]} measures its elements as
	// "images". Depth comes from the decoder, so the two cannot drift.
	member := []string{""}
	for {
		kind := dec.PeekKind()
		if kind == 0 {
			return use, nil
		}
		start := dec.InputOffset()
		switch kind {
		case '{', '[':
			if _, err := dec.ReadToken(); err != nil {
				return use, err
			}
			member = grow(member, dec.StackDepth())
		case '}', ']':
			if _, err := dec.ReadToken(); err != nil {
				return use, err
			}
		case '"':
			isKey := isMemberName(dec)
			tok, err := dec.ReadToken()
			if err != nil {
				return use, err
			}
			depth := dec.StackDepth()
			if isKey {
				// Keys are short, so materializing one costs nothing; a value
				// never is, and is never materialized.
				member = grow(member, depth)
				member[depth] = strings.ToLower(tok.String())
				continue
			}
			member = grow(member, depth)
			use.recordJSONString(body, start, dec.InputOffset()-start, member[depth])
		default:
			if err := dec.SkipValue(); err != nil {
				return use, err
			}
		}
	}
}

// grow extends member until it has an entry for the given stack depth, and
// copies the enclosing member name into the new level.
func grow(member []string, depth int) []string {
	for len(member) <= depth {
		member = append(member, member[len(member)-1])
	}
	return member
}

// isMemberName reports whether the string token about to be read is an object
// member name rather than a value. jsontext tracks, per open container, how
// many names and values it has seen; an object at an even count is waiting for
// a name.
func isMemberName(dec *jsontext.Decoder) bool {
	depth := dec.StackDepth()
	if depth == 0 {
		return false
	}
	kind, seen := dec.StackIndex(depth)
	return kind == jsontext.KindBeginObject && seen%2 == 0
}

// recordJSONString measures one string value. start is the decoder's offset
// before the token was read — it points at whatever separates the token from
// its predecessor — and size spans from there to the token's end, so the
// opening quote is located within the span rather than assumed.
func (use *mediaInputUse) recordJSONString(body []byte, start, size int64, member string) {
	if size < 2 || start < 0 || start+size > int64(len(body)) {
		return
	}
	span := body[start : start+size]
	quote := bytes.IndexByte(span, '"')
	if quote < 0 || quote+1 >= len(span) {
		return
	}
	// Measure the DECODED value, not the raw token text. A string token keeps
	// its escapes until something unquotes it, and the lanes hand the decoded
	// string to the provider — so measuring the raw text let a legal escape
	// like A (an 'A') or \/ (a base64 '/') at the head of a payload make
	// the sniff classify it as prose while the full payload went upstream.
	raw := unquoteJSONString(span[quote+1 : len(span)-1])
	head := raw
	if len(head) > mediaProbeLen {
		head = head[:mediaProbeLen]
	}
	if hasPrefixFold(raw, "data:") {
		if n, audioVideo, ok := dataURIBytes(raw); ok {
			use.recordItem(n, audioVideo)
		}
		return
	}
	// The image lanes accept bare base64 in these fields and wrap it into a
	// data URL themselves (codex_image.go, antigravity_image.go).
	if isBareMediaField(member) && looksLikeBase64Payload(raw, head) {
		use.recordItem(base64DecodedLen(raw), false)
	}
}

// unquoteJSONString decodes a JSON string token's contents (the bytes between
// its quotes) into the value the lanes will forward. A string that will not
// unquote is returned as-is, so a malformed token still measures rather than
// measuring zero.
//
// The returned slice may alias raw when the token carried no escapes, which is
// the common case and keeps the allocation-free property the streaming walk
// exists for.
func unquoteJSONString(raw []byte) []byte {
	if bytes.IndexByte(raw, '\\') < 0 {
		return raw
	}
	var out string
	if err := json.Unmarshal([]byte(`"`+string(raw)+`"`), &out); err != nil {
		return raw
	}
	return []byte(out)
}

// jsonIsWhitespaceEscape reports whether b[i] starts one of the escapes a
// line-wrapped payload needs inside a JSON string: a newline, a carriage
// return, or a tab. Any other escape means the value is text.
func jsonIsWhitespaceEscape(b []byte, i int) (skip int, ok bool) {
	if b[i] != '\\' || i+1 >= len(b) {
		return 0, false
	}
	switch b[i+1] {
	case 'n', 'r', 't':
		return 2, true
	}
	return 0, false
}

// dataURIBytes reports how many content bytes a data: URI carries, and whether
// it is audio or video. ok is false for a string that merely starts with
// "data:" — a prompt can, and it is not media.
func dataURIBytes(raw []byte) (n int64, audioVideo, ok bool) {
	rest := raw[len("data:"):]
	comma := bytes.IndexByte(rest, ',')
	if comma <= 0 {
		return 0, false, false
	}
	meta, payload := rest[:comma], rest[comma+1:]
	mediaType := meta
	if semi := bytes.IndexByte(meta, ';'); semi >= 0 {
		mediaType = meta[:semi]
	}
	// A real data URI names a media type ("image/png", "application/pdf").
	// Anything without a '/' after "data:" is prose, not a payload.
	if len(mediaType) > 100 || bytes.IndexByte(mediaType, '/') < 0 {
		return 0, false, false
	}
	audioVideo = isAudioVideoMIME(string(mediaType))
	if bytes.Contains(bytes.ToLower(meta), []byte(";base64")) {
		return base64DecodedLen(payload), audioVideo, true
	}
	// A percent-encoded payload is never larger than its text, so its length
	// is the conservative reading.
	return int64(len(payload)), audioVideo, true
}

// bareMediaFields are the members the image lanes read base64 out of directly.
var bareMediaFields = map[string]bool{
	"image":     true,
	"images":    true,
	"mask":      true,
	"reference": true,
}

func isBareMediaField(name string) bool {
	return bareMediaFields[strings.ToLower(name)]
}

// looksLikeBase64Payload reports whether a string is plausibly raw base64
// rather than ordinary text. It has to be long enough to matter, and its head
// must use only a base64 alphabet plus the whitespace line-wrapping inserts —
// a prompt mentioning an image must not be counted as one.
//
// The URL-safe alphabet (- and _) is accepted, and so is whitespace in either
// form a caller can send it: literal, or escaped as \n \r \t, which is the only
// way a line-wrapped payload fits inside a JSON string. Both are legal base64
// the lanes forward unchanged — codexImageDataURL wraps a bare payload into a
// data: URL without re-encoding it — so a URL-safe or wrapped payload is
// exactly as large and used to measure as zero media.
func looksLikeBase64Payload(raw, head []byte) bool {
	if len(raw) < 1024 {
		return false
	}
	if len(head) > 256 {
		head = head[:256]
	}
	for i := 0; i < len(head); i++ {
		c := head[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '+', c == '/', c == '=', c == '-', c == '_':
		case c == '\r', c == '\n', c == ' ', c == '\t':
		case c == '\\':
			skip, ok := jsonIsWhitespaceEscape(head, i)
			if !ok {
				return false
			}
			i += skip - 1
		default:
			return false
		}
	}
	return true
}

// base64DecodedLen is the size raw base64 decodes to, without decoding it.
// Whitespace is skipped (wrapped base64 inserts it) and '=' is padding.
func base64DecodedLen(raw []byte) int64 {
	symbols, pad := 0, 0
	for i := 0; i < len(raw); i++ {
		switch c := raw[i]; c {
		case '=':
			pad++
		case '\r', '\n', ' ', '\t', '\f', '\v':
		case '\\':
			skip, ok := jsonIsWhitespaceEscape(raw, i)
			if !ok {
				// Not a wrapped payload after all; count it so the caller's
				// size is not silently understated.
				symbols++
				continue
			}
			i += skip - 1
		default:
			symbols++
		}
	}
	total := symbols + pad
	if total%4 == 0 {
		return int64(total/4*3 - pad)
	}
	return int64(total) * 3 / 4
}

func hasPrefixFold(b []byte, prefix string) bool {
	return len(b) >= len(prefix) && strings.EqualFold(string(b[:len(prefix)]), prefix)
}
