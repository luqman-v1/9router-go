package chat

import "testing"

// The action is the last colon segment of the path suffix; a model id never
// contains one, and both the one-segment and two-segment forms are accepted.
func TestGeminiPathAction(t *testing.T) {
	cases := []struct {
		suffix string
		model  string
		stream bool
		ok     bool
	}{
		{"deepseek/deepseek-chat:generateContent", "deepseek/deepseek-chat", false, true},
		{"deepseek/deepseek-chat:streamGenerateContent", "deepseek/deepseek-chat", true, true},
		{"gemini-4-argon:generateContent", "gemini-4-argon", false, true},
		{"models/deepseek/deepseek-chat:generateContent", "deepseek/deepseek-chat", false, true},
		{"deepseek/deepseek-chat", "", false, false},
		{"deepseek/deepseek-chat:countTokens", "", false, false},
		{"", "", false, false},
	}
	for _, tc := range cases {
		model, stream, ok := GeminiPathAction(tc.suffix)
		if ok != tc.ok || model != tc.model || stream != tc.stream {
			t.Errorf("GeminiPathAction(%q) = (%q,%v,%v), want (%q,%v,%v)",
				tc.suffix, model, stream, ok, tc.model, tc.stream, tc.ok)
		}
	}
}
