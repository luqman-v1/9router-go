package tokensaver

import (
	"regexp"
)

// Injection patterns: heuristics for classic prompt-injection attempts where a
// user message tries to override system instructions. This is a tag/flag only,
// never a hard block — false positives would break legitimate requests.
// ponytail: heuristic keyword scan; upgrade to a model-based classifier when a
// real false-positive budget is measured.
var injectionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)ignore (all |any |the )?(previous|prior|above|earlier).{0,40}(instructions|prompt|directives)`),
	regexp.MustCompile(`(?i)(disregard|forget|forget about) (the )?(above|previous|prior|system).{0,30}(instructions|prompt|rules)`),
	regexp.MustCompile(`(?i)you are now .{0,60}(jailbreak|jail broken|without (any |the )?(restrictions|limitations|filters))`),
	regexp.MustCompile(`(?i)(reveal|show|print|output).{0,30}(your (system )?(prompt|instructions)|the system prompt|system instructions)`),
	regexp.MustCompile(`(?i)act as (if )?(an? |the )?(unfiltered|unrestricted|uncensored|developer mode|do anything now|DAN)`),
	regexp.MustCompile(`(?i)` + "`" + `\s*(system|developer)\s*:`), // inline role-injection like `system:`
	regexp.MustCompile(`(?i)(<\|im_start\|>system|<<SYS>>|\[SYSTEM PROMPT\]|\[INST\]\s*<<SYS>>)`),
	regexp.MustCompile(`(?i)(repeat|print|output|write).{0,30}(the text (above|before)|everything (above|before)).{0,30}(verbatim|word for word|exact)`),
	regexp.MustCompile(`(?i)(developer override|admin mode|maintenance mode).{0,30}(enabled|activated|on)`),
}

// InjectionResult reports whether a request body contains a flagged message.
type InjectionResult struct {
	Flagged   bool     `json:"flagged"`
	Reasons   []string `json:"reasons,omitempty"`
	MessageID int      `json:"messageId,omitempty"` // index of first flagged message
}

// DetectInjection scans user/developer/tool content in an LLM request body for
// prompt-injection patterns. It parses messages[] (OpenAI chat), input[]
// (Responses), and text[] (Claude-style) uniformly. Returns empty result when
// the body isn't a parseable messages-style JSON.
func DetectInjection(body []byte) InjectionResult {
	var m map[string]any
	if err := unmarshalAny(body, &m); err != nil {
		return InjectionResult{}
	}

	var items []any
	switch v := m["messages"].(type) {
	case []any:
		items = v
	case nil:
		if in, ok := m["input"].([]any); ok {
			items = in
		}
	}

	var out InjectionResult
	for i, item := range items {
		msg, ok := item.(map[string]any)
		if !ok {
			continue
		}
		// Skip assistant messages — injection lives in user content.
		if role, _ := msg["role"].(string); role == "assistant" {
			continue
		}
		for _, text := range extractTexts(msg) {
			if reason := matchInjection(text); reason != "" {
				if !out.Flagged {
					out.MessageID = i
				}
				out.Flagged = true
				out.Reasons = append(out.Reasons, reason)
			}
		}
	}
	return out
}

// extractTexts pulls every user-visible string out of a message: string
// content, Claude content[] blocks, and tool output.
func extractTexts(msg map[string]any) []string {
	var texts []string
	if s, ok := msg["content"].(string); ok {
		texts = append(texts, s)
	}
	if arr, ok := msg["content"].([]any); ok {
		for _, part := range arr {
			if blk, ok := part.(map[string]any); ok {
				if text, ok := blk["text"].(string); ok {
					texts = append(texts, text)
				}
			}
		}
	}
	if s, ok := msg["output"].(string); ok {
		texts = append(texts, s)
	}
	return texts
}

// matchInjection returns the matched pattern description, or "" if clean.
func matchInjection(text string) string {
	for _, re := range injectionPatterns {
		if re.MatchString(text) {
			return re.String()
		}
	}
	return ""
}
