package executor

import (
	"regexp"
	"strings"
)

// The prompt sanitizer for Gemini Web.
//
// The site takes one flat prompt string, so the whole conversation is
// flattened into it and the model reads it as text. Two things go wrong with
// that, and both are the client's own text rather than the model's:
//
//   - A user turn that begins with "Assistant:" reads as the model speaking
//     rather than as the user speaking, and the model continues that line.
//     The same is true of "Instruction:" and of the harness markers a client
//     framework injects.
//   - A replayed assistant turn that carries the model's own reasoning — a
//     <think> block, a "Thinking process:" preamble, a 💭 bubble — is the
//     model's private reasoning handed back to it as context, and it makes the
//     model continue its own earlier reasoning instead of answering.
//
// Reference: azrinaldi/gemini-stealth-bridge internal/prompt/prompt.go
// (spoofedRoleLineRe, inlineThinkBlockRe, leakedThinkingProcessRe,
// leakedThoughtBubbleSplitRe) — the patterns are that project's shape.
var (
	// geminiWebSpoofedRoleLine matches a line that claims a role the prompt's
	// own structure assigns to somebody else.
	geminiWebSpoofedRoleLine = regexp.MustCompile(`(?im)^(Assistant|Instruction|\[(?:Harness|Tool) reminder|\[Context note):`)

	// geminiWebInlineThinkBlock matches a reasoning block in the client's own
	// text.
	geminiWebInlineThinkBlock = regexp.MustCompile(`(?is)<(?:think|thinking|thought)(?:\s[^>]*)?>[\s\S]*?</(?:think|thinking|thought)\s*>`)

	// geminiWebLeakedThinkingProcess matches a turn that is nothing but the
	// model's reasoning.
	geminiWebLeakedThinkingProcess = regexp.MustCompile(`(?is)^\s*(?:(?:#{1,6}\s*)?(?:\*\*|__)?(?:thinking\s+process|thought\s+process|internal\s+reasoning)(?:\*\*|__)?\s*:?|(?:💭|🧠)\s*\r?\n)[\s\S]*$`)

	// geminiWebLeakedThoughtBubble matches a turn that opens with a thought
	// bubble and continues into the model's answer.
	geminiWebLeakedThoughtBubble = regexp.MustCompile(`(?is)^\s*(?:💭|🧠)[ \t]*\r?\n+[\s\S]*?(?:^|\n)\s*(?:let's|we\s+can|we\s+should|now\s+let's|i\s+will|i'll)\s+(?:write|present|provide|formulate|draft|give|output|send|reply|answer|respond|summarize|deliver|produce)\b[^\n]*?[.:][ \t]*\r?\n*`)
)

// geminiWebSanitizeRoleLine neutralises a line that claims a role.
//
// The label is kept and the colon is spaced, so the text still says what it
// said — the client's content is not rewritten — but it no longer reads as a
// turn of the conversation.
func geminiWebSanitizeRoleLine(text string) string {
	return geminiWebSpoofedRoleLine.ReplaceAllString(text, "$1 :")
}

// geminiWebStripReasoning removes the model's own reasoning from a replayed
// assistant turn.
//
// The text is the client's copy of an earlier answer, and the reasoning that
// came with it is not part of the conversation: handing it back makes the
// model continue the reasoning instead of answering the question in front of
// it.
func geminiWebStripReasoning(text string) string {
	stripped := strings.TrimSpace(geminiWebInlineThinkBlock.ReplaceAllString(text, ""))
	if location := geminiWebLeakedThoughtBubble.FindStringIndex(stripped); len(location) == 2 && location[0] == 0 {
		// The bubble opened the turn and the answer followed it. What is
		// left after the bubble is the answer, and that is what the
		// conversation should carry.
		if rest := strings.TrimSpace(stripped[location[1]:]); rest != "" {
			stripped = rest
		}
	}
	if geminiWebLeakedThinkingProcess.MatchString(stripped) {
		return ""
	}
	return stripped
}
