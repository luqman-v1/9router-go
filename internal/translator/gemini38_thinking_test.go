package translator

import (
	"testing"
)

// The antigravity lane rejects thinkingLevel minimal on gemini-3.8 tiered ids
// with 400, so the full OpenAI→Gemini chain must never put minimal on the
// wire for them: a disable ask (reasoning_effort none/minimal) clamps to low,
// and advertised levels pass through. Includes the maxOutputTokens floor the
// low level raises.
func TestTranslateOpenAIToGemini_Gemini38TierClamp(t *testing.T) {
	tests := []struct {
		effort string
		level  string
	}{
		{effort: "none", level: "low"},
		{effort: "minimal", level: "low"},
		{effort: "low", level: "low"},
		{effort: "medium", level: "medium"},
		{effort: "high", level: "high"},
	}

	for _, m := range []string{"gemini-3.8-flash", "gemini-3.8-flash-low", "gemini-3.8-flash-medium", "gemini-3.8-flash-high"} {
		for _, tt := range tests {
			body := `{"model":"` + m + `","reasoning_effort":"` + tt.effort + `","messages":[{"role":"user","content":"hi"}]}`
			out, err := TranslateOpenAIToGemini([]byte(body))
			if err != nil {
				t.Fatalf("TranslateOpenAIToGemini(%s): %v", body, err)
			}
			tc := geminiThinkingConfig(t, out)
			if tc["thinkingLevel"] != tt.level {
				t.Errorf("%s effort %s: thinkingLevel = %v, want %s", m, tt.effort, tc["thinkingLevel"], tt.level)
			}
			if tc["includeThoughts"] != true {
				t.Errorf("%s effort %s: includeThoughts = %v, want true", m, tt.effort, tc["includeThoughts"])
			}
		}

		// The low floor raises maxOutputTokens so a clamped disable ask still
		// has room to answer.
		body := `{"model":"` + m + `","reasoning_effort":"none","messages":[{"role":"user","content":"hi"}]}`
		out, err := TranslateOpenAIToGemini([]byte(body))
		if err != nil {
			t.Fatalf("TranslateOpenAIToGemini(%s): %v", body, err)
		}
		if got := geminiMaxTokens(t, out); got < 8192 {
			t.Errorf("%s effort none: maxOutputTokens = %v, want >= 8192", m, got)
		}
	}
}
