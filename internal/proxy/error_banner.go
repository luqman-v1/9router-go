package proxy

import (
	"bytes"
	"regexp"
	"strings"

	"encoding/json/jsontext"
	json "encoding/json/v2"

	"9router/proxy/internal/handlerutil"
)

// An upstream fault can arrive as HTTP 200 whose ONLY content is a status
// banner: Antigravity's agent path reports "Agent execution terminated due to
// error ..." as ordinary assistant text with a normal finish reason. Every
// "does this response carry an answer" check sees text and commits the turn, so
// combo fallback never advances and the client is shown an error message as if
// it were the model's reply.
//
// The bar is deliberately narrow: the WHOLE accumulated text must be
// banner-shaped — short, and one of the exact banner shapes below. Each pattern
// is anchored at BOTH ends: a real answer that merely STARTS with a fault
// phrase ("Quota exceeded is the phrase you asked about; ...") must never
// classify as a banner, because that turns a legitimate short answer into a
// retry — the exact false positive this layer exists to avoid. A banner split
// across frames is held open while the text is still a prefix of a known
// lead-in, so the stream is not committed on the first fragment.

// errorBannerMaxChars bounds "short". A fault banner is a sentence or two; an
// answer is longer. Keeping the cap small is what stops the matcher from ever
// classifying a real reply as a banner.
const errorBannerMaxChars = 2000

// bannerTail is what a banner may continue with after its opening phrase: a
// status code, a short reason, punctuation, a model id, a retry hint. The
// continuation may not introduce a new sentence (". A" / "? The" / "! We" /
// "; This") — that is the shape of a real answer that merely OPENS with the
// fault phrase, and it must not classify.
//
// The first character may be punctuation (": upstream 502", "- retry after
// 60s", "(model gpt-6)"), which the rest-of-run class excludes, so the tail is
// split into one optional leading separator plus a run of sentence-free text.
const bannerTail = `(?:\s*[:;,(\-]\s*|\s+)?[^.!?;]*$`

// errorBannerPatterns are the exact banner shapes seen in a 200 body. Each is
// anchored at both ends: the text must BE the banner, not merely open with one.
var errorBannerPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^agent\s+(execution\s+)?terminated\s+due\s+to\s+error` + bannerTail),
	regexp.MustCompile(`(?i)^agent\s+(execution\s+)?terminated$`),
	regexp.MustCompile(`(?i)^(request\s+)?(failed|refused)\s*(due\s+to|because|:)` + bannerTail),
	regexp.MustCompile(`(?i)^(authentication|authorization|permission)\s+(failed|denied|required)` + bannerTail),
	regexp.MustCompile(`(?i)^(quota|rate\s*limit|capacity)\s+(exceeded|exhausted|reached|limit)` + bannerTail),
	regexp.MustCompile(`(?i)^(policy|content\s+policy|safety)\s+(violation|blocked|denied)` + bannerTail),
	regexp.MustCompile(`(?i)^(502|503|504|429)\b` + bannerTail),
	regexp.MustCompile(`(?i)^(service|upstream|gateway)\s+(unavailable|error|failed)` + bannerTail),
	regexp.MustCompile(`(?i)^(internal\s+)?server\s+error$`),
	regexp.MustCompile(`(?i)^(bad\s+gateway|gateway\s+timeout)$`),
	regexp.MustCompile(`(?i)^(an?\s+)?(unexpected\s+)?error\s+occurred` + bannerTail),
	regexp.MustCompile(`(?i)^(the\s+)?(model|upstream)\s+(is\s+)?(currently\s+)?(overloaded|unavailable)` + bannerTail),
}

// bannerLeadIns are the prefixes a split banner may arrive in. A frame whose
// text is a strict prefix of one of these is held open: the next frame may
// complete the banner, and committing now would show the client half a fault
// message as an answer.
var bannerLeadIns = []string{
	"agent",
	"agent execution",
	"agent execution terminated",
	"agent execution terminated due to error",
	"request failed",
	"error",
	"error occurred",
	"an error occurred",
	"authentication failed",
	"authorization failed",
	"permission denied",
	"quota exceeded",
	"rate limit",
	"rate limit exceeded",
	"policy violation",
	"content policy",
	"safety blocked",
	"service unavailable",
	"upstream unavailable",
	"upstream error",
	"gateway timeout",
	"bad gateway",
	"internal server error",
	"server error",
}

// normalizeBannerText collapses whitespace so a banner split across frames (or
// wrapped) still matches, and bounds the length so a long text is cheap to
// reject.
func normalizeBannerText(text string) string {
	text = strings.TrimSpace(text)
	if len(text) > errorBannerMaxChars*2 {
		return text[:errorBannerMaxChars*2]
	}
	return strings.Join(strings.Fields(text), " ")
}

// IsErrorBannerText reports whether the whole text is an upstream fault banner
// rather than an answer. Empty text is not a banner: the caller has separate
// handling for "no content at all".
func IsErrorBannerText(text string) bool {
	text = normalizeBannerText(text)
	if text == "" || len(text) > errorBannerMaxChars {
		return false
	}
	for _, re := range errorBannerPatterns {
		if re.MatchString(text) {
			return true
		}
	}
	return false
}

// fastBannerText pulls the first assistant-text string out of a raw SSE chunk
// without decoding JSON. It returns ok=false when the chunk needs the full
// parser (an escaped string, or no recognisable text key), so the caller falls
// back rather than guessing.
//
// The banner decision runs on every content write of every streaming turn, and
// the JSON decode measured ~1.7 µs against ~0.18 µs for this scan. The scan is
// only a PREFILTER: a hit is not a verdict, it only means "worth classifying".
func fastBannerText(chunk []byte) ([]byte, bool) {
	for _, key := range bannerTextKeys {
		j := bytes.Index(chunk, key)
		if j < 0 {
			continue
		}
		rest := chunk[j+len(key):]
		end := bytes.IndexByte(rest, '"')
		if end < 0 {
			return nil, false
		}
		val := rest[:end]
		if bytes.IndexByte(val, '\\') >= 0 {
			return nil, false
		}
		return val, true
	}
	return nil, false
}

// bannerTextKeys are the opening delimiters of every text-bearing field the
// banner shapes use: OpenAI content/reasoning, Anthropic text, the Responses
// bare delta, and bare text.
var bannerTextKeys = [][]byte{
	[]byte(`"content":"`),
	[]byte(`"text":"`),
	[]byte(`"delta":"`),
	[]byte(`"reasoning":"`),
	[]byte(`"reasoning_content":"`),
	[]byte(`"thinking":"`),
}

// chunkNeedsBannerCheck reports whether a complete SSE frame might carry a
// banner, and the text to classify when it does. It is the peeker's fast path:
// a frame whose text cannot be a banner returns false without any JSON decode.
func chunkNeedsBannerCheck(chunk []byte) (string, bool) {
	raw, ok := fastBannerText(chunk)
	if !ok {
		// Escaped or unrecognised: fall back to the full extractor.
		text := ExtractSSEText(chunk)
		if text == "" {
			return "", false
		}
		return text, IsBannerSuspect(text)
	}
	// The verdict only needs the leading bytes: a banner is short, and
	// IsBannerSuspect rejects on the first letter before normalizing.
	if !startsWithBannerLetter(string(raw)) {
		return "", false
	}
	text := string(raw)
	return text, IsBannerSuspect(text)
}

// IsBannerSuspect reports whether text might still turn out to be a banner and
// the stream should not be committed yet: it either already matches a banner or
// is a strict prefix of one. The second half is what a split frame needs — the
// first frame reads "Agent execution" and must not be shown as the answer.
func IsBannerSuspect(text string) bool {
	// Cheap reject before any allocation: every banner shape and every lead-in
	// begins with one of a handful of letters, and a real answer almost never
	// does. The normalized pass below costs two allocations per call, and this
	// runs once per content frame of every streaming turn.
	if !startsWithBannerLetter(text) {
		return false
	}
	text = normalizeBannerText(text)
	if text == "" {
		return false
	}
	if IsErrorBannerText(text) {
		return true
	}
	lower := strings.ToLower(text)
	for _, lead := range bannerLeadIns {
		if strings.HasPrefix(lead, lower) {
			return true
		}
	}
	return false
}

// startsWithBannerLetter reports whether the first letter of text (skipping
// whitespace and an opening quote/bracket) can begin a banner shape or a
// banner lead-in. The set is the union of the first letters in
// errorBannerPatterns and bannerLeadIns: a, r, f, q, c, p, s, i, b, g, u, t,
// e, m, d, 5, 4, 3.
func startsWithBannerLetter(text string) bool {
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '"' || c == '[' || c == '{' {
			continue
		}
		switch c | 0x20 { // fold ASCII case
		case 'a', 'b', 'c', 'd', 'e', 'f', 'g', 'i', 'm', 'p', 'q', 'r', 's', 't', 'u':
			return true
		}
		return c == '3' || c == '4' || c == '5'
	}
	return false
}

// ExtractSSEText returns the assistant text carried by a chunk of SSE bytes.
// It reads the same places the client would: an OpenAI delta's `content`
// (string or text parts), a Claude `content_block_delta`'s `text`, and a
// Responses `delta`/`text`. Bytes that are not a data frame contribute nothing,
// so a comment or a `[DONE]` line is silently skipped rather than treated as an
// empty answer.
func ExtractSSEText(chunk []byte) string {
	var sb strings.Builder
	sawDataLine := false
	for _, line := range bytes.Split(chunk, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		if !bytes.HasPrefix(trimmed, []byte("data:")) {
			continue
		}
		sawDataLine = true
		payload := bytes.TrimSpace(trimmed[len("data:"):])
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		appendJSONText(&sb, payload)
	}
	// The event scanner hands over the bare payload, without its `data:` line;
	// a raw chunk from the writer still carries it. Both shapes reach here, so
	// fall back to treating the whole chunk as one JSON frame.
	if !sawDataLine {
		trimmed := bytes.TrimSpace(chunk)
		if len(trimmed) > 0 && trimmed[0] == '{' {
			appendJSONText(&sb, trimmed)
		}
	}
	return sb.String()
}

// FrameParses reports whether chunk holds at least one complete JSON `data:`
// frame. The peeker uses it to tell a frame that is still arriving (hold it —
// its first half looks like content to the substring heuristics) from a frame
// that finished without text (a structural Anthropic frame such as a thinking
// or signature_delta block — relay it; holding it would starve the
// first-output gate and time out a healthy stream).
//
// It runs on every write of every streaming turn, so it is a brace scan, not a
// decode: the scan measured ~0.13 µs per frame against ~4.7 µs for unmarshal
// into any. The scan counts braces outside strings; a string that opens and
// never closes means the frame is still arriving, which is the case the peeker
// holds on.
func FrameParses(chunk []byte) bool {
	// Line scanning without bytes.Split: it allocates a slice-of-slices per
	// call, and this runs on every write of every streaming turn.
	for len(chunk) > 0 {
		var line []byte
		if i := bytes.IndexByte(chunk, '\n'); i >= 0 {
			line, chunk = chunk[:i], chunk[i+1:]
		} else {
			line, chunk = chunk, nil
		}
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) < 5 || trimmed[0] != 'd' || !bytes.HasPrefix(trimmed, dataPrefix) {
			continue
		}
		payload := bytes.TrimSpace(trimmed[len(dataPrefix):])
		if len(payload) == 0 || bytes.Equal(payload, doneMarker) {
			continue
		}
		if frameComplete(payload) {
			return true
		}
	}
	return false
}

var (
	dataPrefix = []byte("data:")
	doneMarker = []byte("[DONE]")
)

// frameComplete reports whether payload holds one balanced JSON object. A
// string still open, or braces still open, means the frame is mid-arrival.
func frameComplete(payload []byte) bool {
	depth := 0
	inStr, esc := false, false
	started := false
	for i := 0; i < len(payload); i++ {
		c := payload[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			if depth > 0 {
				inStr = true
			}
		case '{':
			depth++
			started = true
		case '}':
			depth--
			if started && depth == 0 {
				return true
			}
		}
	}
	return false
}

// bannerFrame is the typed view of one SSE frame the banner decision needs.
// A struct instead of map[string]any: the decision runs on every content write
// of every streaming request, and the map shape measured 1.2 us / 16 allocs per
// frame against 0.4 us / 5 for the struct (the reviewer's finding), for exactly
// the same answer. Fields the three wire shapes use: OpenAI choices and their
// delta/message, Anthropic's delta/content_block text, and the Responses API's
// bare-string `delta` / `text`.
type bannerFrame struct {
	Choices []struct {
		Delta struct {
			Content          jsontext.Value `json:"content"`
			ReasoningContent jsontext.Value `json:"reasoning_content"`
		} `json:"delta"`
		Message struct {
			Content jsontext.Value `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	// Delta is read as raw bytes and reinterpreted: the Responses API sends a
	// bare string, Anthropic an object with `text`. One field, because two
	// struct fields cannot both bind `delta`.
	Delta        jsontext.Value `json:"delta"`
	ContentBlock struct {
		Text jsontext.Value `json:"text"`
	} `json:"content_block"`
	Text *string `json:"text"`
}

// appendJSONText walks the text-bearing fields of one JSON frame.
func appendJSONText(sb *strings.Builder, payload []byte) {
	var frame bannerFrame
	if err := json.Unmarshal(payload, &frame, handlerutil.UpstreamBody); err != nil {
		return
	}
	for _, choice := range frame.Choices {
		appendContentText(sb, choice.Delta.Content)
		appendContentText(sb, choice.Delta.ReasoningContent)
		appendContentText(sb, choice.Message.Content)
	}
	appendContentText(sb, frame.ContentBlock.Text)
	// `delta` is a string on the Responses API and an object with `text` on
	// Anthropic; the bytes decide which.
	trimmedDelta := bytes.TrimSpace(frame.Delta)
	switch {
	case len(trimmedDelta) > 0 && trimmedDelta[0] == '"':
		var s string
		if err := json.Unmarshal(trimmedDelta, &s, handlerutil.UpstreamBody); err == nil {
			sb.WriteString(s)
		}
	case len(trimmedDelta) > 0 && trimmedDelta[0] == '{':
		var delta struct {
			Text jsontext.Value `json:"text"`
		}
		if err := json.Unmarshal(trimmedDelta, &delta, handlerutil.UpstreamBody); err == nil {
			appendContentText(sb, delta.Text)
		}
	}
	if frame.Text != nil {
		sb.WriteString(*frame.Text)
	}
}

// appendContentText handles a content value that is either a string or an array
// of `{type:"text", text:"..."}` parts. Tool-call content contributes nothing:
// a tool call is an answer even when it carries no prose.
func appendContentText(sb *strings.Builder, content jsontext.Value) {
	trimmed := bytes.TrimSpace(content)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return
	}
	switch trimmed[0] {
	case '"':
		var s string
		if err := json.Unmarshal(trimmed, &s, handlerutil.UpstreamBody); err == nil {
			sb.WriteString(s)
		}
	case '[':
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(trimmed, &parts, handlerutil.UpstreamBody); err == nil {
			for _, part := range parts {
				if part.Type == "text" {
					sb.WriteString(part.Text)
				}
			}
		}
	}
}
