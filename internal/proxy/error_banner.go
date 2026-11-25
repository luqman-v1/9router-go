package proxy

import (
	"bytes"
	"regexp"
	"strings"

	json "encoding/json/v2"
)

// An upstream fault can arrive as HTTP 200 whose ONLY content is a status
// banner: Antigravity's agent path reports "Agent execution terminated due to
// error ..." as ordinary assistant text with a normal finish reason. Every
// "does this response carry an answer" check sees text and commits the turn, so
// combo fallback never advances and the client is shown an error message as if
// it were the model's reply.
//
// The bar is deliberately narrow: the WHOLE accumulated text must be
// banner-shaped — short, and matching a fault phrase. A real answer that
// mentions "capacity" or "timeout" mid-text never matches, because the match
// must cover the entire text. A banner split across frames is held open while
// the text is still a prefix of a known lead-in, so the stream is not committed
// on the first fragment.

// errorBannerMaxChars bounds "short". A fault banner is a sentence or two; an
// answer is longer. Keeping the cap small is what stops the matcher from ever
// classifying a real reply as a banner.
const errorBannerMaxChars = 2000

// errorBannerPatterns are the upstream fault phrases seen in a 200 body.
var errorBannerPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^agent\s+(execution\s+)?terminated\s+due\s+to\s+error`),
	regexp.MustCompile(`(?i)^agent\s+(execution\s+)?terminated`),
	regexp.MustCompile(`(?i)^(request\s+)?(failed|refused)\s*(due\s+to|because|:)`),
	regexp.MustCompile(`(?i)^(authentication|authorization|permission)\s+(failed|denied|required)`),
	regexp.MustCompile(`(?i)^(quota|rate\s*limit|capacity)\s+(exceeded|exhausted|reached|limit)`),
	regexp.MustCompile(`(?i)^(policy|content\s+policy|safety)\s+(violation|blocked|denied)`),
	regexp.MustCompile(`(?i)^(502|503|504|429)\b`),
	regexp.MustCompile(`(?i)^(service|upstream|gateway)\s+(unavailable|error|failed)`),
	regexp.MustCompile(`(?i)^(internal\s+)?server\s+error`),
	regexp.MustCompile(`(?i)^(bad\s+gateway|gateway\s+timeout)`),
	regexp.MustCompile(`(?i)^(an?\s+)?(unexpected\s+)?error\s+occurred`),
	regexp.MustCompile(`(?i)^(the\s+)?(model|upstream)\s+(is\s+)?(currently\s+)?(overloaded|unavailable)`),
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

// IsBannerSuspect reports whether text might still turn out to be a banner and
// the stream should not be committed yet: it either already matches a banner or
// is a strict prefix of one. The second half is what a split frame needs — the
// first frame reads "Agent execution" and must not be shown as the answer.
func IsBannerSuspect(text string) bool {
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

// appendJSONText walks the text-bearing fields of one JSON frame.
func appendJSONText(sb *strings.Builder, payload []byte) {
	var frame map[string]any
	if err := json.Unmarshal(payload, &frame); err != nil {
		return
	}
	// OpenAI chat completions: choices[].delta.content / message.content.
	if choices, ok := frame["choices"].([]any); ok {
		for _, raw := range choices {
			choice, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			for _, holder := range []string{"delta", "message"} {
				if m, ok := choice[holder].(map[string]any); ok {
					appendContentText(sb, m["content"])
				}
			}
		}
	}
	// Anthropic: content_block_delta.delta.text (and content_block_start.text).
	if m, ok := frame["delta"].(map[string]any); ok {
		if s, ok := m["text"].(string); ok {
			sb.WriteString(s)
		}
	}
	if m, ok := frame["content_block"].(map[string]any); ok {
		if s, ok := m["text"].(string); ok {
			sb.WriteString(s)
		}
	}
	// Responses API: output_text.delta carries a bare string `delta`, and the
	// done variant carries `text`.
	if s, ok := frame["delta"].(string); ok {
		sb.WriteString(s)
	}
	if s, ok := frame["text"].(string); ok {
		sb.WriteString(s)
	}
}

// appendContentText handles a content value that is either a string or an array
// of `{type:"text", text:"..."}` parts. Tool-call content contributes nothing:
// a tool call is an answer even when it carries no prose.
func appendContentText(sb *strings.Builder, content any) {
	switch v := content.(type) {
	case string:
		sb.WriteString(v)
	case []any:
		for _, raw := range v {
			part, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if part["type"] == "text" {
				if s, ok := part["text"].(string); ok {
					sb.WriteString(s)
				}
			}
		}
	}
}
