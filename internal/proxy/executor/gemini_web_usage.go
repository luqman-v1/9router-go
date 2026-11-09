package executor

import (
	"unicode/utf8"
)

// Token accounting for Gemini Web.
//
// The site reports no token counts: the answer arrives as text and nothing in
// the stream says how many tokens it was. The counts are therefore estimated
// locally, from the prompt that was sent and the answer that came back.
//
// The estimate is the same one the rest of this repository uses — four
// characters per token — with the one adjustment a CJK answer needs: a
// CJK character is roughly one token on its own, so counting it as a quarter
// of a token under-reports a Japanese answer by a factor of four.
//
// The counts are reported as the OpenAI usage object, so the router's own
// accounting reads them the same way it reads an upstream's.
//
// Reference: azrinaldi/gemini-stealth-bridge internal/prompt/prompt.go
// (EstimateTokens, BuildUsage).
const geminiWebCharsPerToken = 4

// geminiWebEstimateTokens estimates the tokens a text is worth.
//
// An empty text is zero tokens: a turn that said nothing costs nothing on
// the answer side. Everything else is at least one token, because a
// non-empty answer is never free.
func geminiWebEstimateTokens(text string) int {
	if text == "" {
		return 0
	}
	cjkRunes := 0
	otherBytes := 0
	for _, r := range text {
		if geminiWebIsCJKRune(r) {
			cjkRunes++
			continue
		}
		otherBytes += utf8.RuneLen(r)
	}
	tokens := 0
	if cjkRunes > 0 {
		// A CJK character is about one and a half bytes of token, so three
		// characters make two tokens.
		tokens += (cjkRunes*3 + 1) / 2
	}
	if otherBytes > 0 {
		tokens += (otherBytes + geminiWebCharsPerToken - 1) / geminiWebCharsPerToken
	}
	if tokens < 1 {
		return 1
	}
	return tokens
}

// geminiWebIsCJKRune reports whether a rune is written in a script whose
// characters cost roughly a token each.
func geminiWebIsCJKRune(r rune) bool {
	return unicodeRangesCJK(r)
}

// geminiWebBuildUsage renders the token accounting as the OpenAI usage object.
func geminiWebBuildUsage(prompt, answer, reasoning string) map[string]any {
	promptTokens := geminiWebEstimateTokens(prompt)
	reasoningTokens := geminiWebEstimateTokens(reasoning)
	completionTokens := geminiWebEstimateTokens(answer) + reasoningTokens
	usage := map[string]any{
		"prompt_tokens":     promptTokens,
		"completion_tokens": completionTokens,
		"total_tokens":      promptTokens + completionTokens,
		// The site reports no cache, so the count is zero. The field is
		// published anyway: a client that reads it finds the shape it
		// expects rather than a missing key.
		"prompt_tokens_details": map[string]any{"cached_tokens": 0},
	}
	if reasoningTokens > 0 {
		usage["completion_tokens_details"] = map[string]any{"reasoning_tokens": reasoningTokens}
	}
	return usage
}

// unicodeRangesCJK reports whether a rune belongs to a CJK script.
//
// The ranges are the ones that cost about a token per character: Chinese,
// Japanese, Korean, and the Vietnamese and fullwidth forms that ride on the
// same width.
func unicodeRangesCJK(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x11FF: // Hangul Jamo
		return true
	case r >= 0x2E80 && r <= 0x2EFF: // CJK Radicals Supplement
		return true
	case r >= 0x3000 && r <= 0x303F: // CJK Symbols and Punctuation
		return true
	case r >= 0x3040 && r <= 0x309F: // Hiragana
		return true
	case r >= 0x30A0 && r <= 0x30FF: // Katakana
		return true
	case r >= 0x3130 && r <= 0x318F: // Hangul Compatibility Jamo
		return true
	case r >= 0x3400 && r <= 0x4DBF: // CJK Unified Ideographs Extension A
		return true
	case r >= 0x4E00 && r <= 0x9FFF: // CJK Unified Ideographs
		return true
	case r >= 0xA960 && r <= 0xA97F: // Hangul Jamo Extended-A
		return true
	case r >= 0xAC00 && r <= 0xD7AF: // Hangul Syllables
		return true
	case r >= 0xF900 && r <= 0xFAFF: // CJK Compatibility Ideographs
		return true
	case r >= 0xFF00 && r <= 0xFFEF: // Halfwidth and Fullwidth Forms
		return true
	case r >= 0x20000 && r <= 0x2A6DF: // CJK Extension B
		return true
	case r >= 0xAC00 && r <= 0xD7A3: // Hangul Syllables Extension
		return true
	}
	return false
}

// geminiWebUsageInt reads one count out of a usage object.
func geminiWebUsageInt(usage map[string]any, key string) int {
	count, _ := usage[key].(int)
	return count
}
