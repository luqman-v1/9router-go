package translator

import (
	json "encoding/json/v2"
	"testing"
)

// geminiThinkingConfig extracts generationConfig.thinkingConfig from a
// translated Gemini body.
func geminiThinkingConfig(t *testing.T, geminiBytes []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(geminiBytes, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	gc, ok := m["generationConfig"].(map[string]any)
	if !ok {
		t.Fatal("expected generationConfig")
	}
	tc, ok := gc["thinkingConfig"].(map[string]any)
	if !ok {
		t.Fatal("expected thinkingConfig")
	}
	return tc
}

func geminiMaxTokens(t *testing.T, geminiBytes []byte) float64 {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(geminiBytes, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	gc, ok := m["generationConfig"].(map[string]any)
	if !ok {
		t.Fatalf("expected generationConfig in %s", geminiBytes)
	}
	v, ok := gc["maxOutputTokens"].(float64)
	if !ok {
		t.Fatalf("expected maxOutputTokens in %s", geminiBytes)
	}
	return v
}

// TestTranslateOpenAIToGeminiWritesMaxOutputTokens pins that a reasoning
// request carries an output-token budget. The helper that reads it used to
// report 0 for a missing field, which let a translator regression pass.
func TestTranslateOpenAIToGeminiWritesMaxOutputTokens(t *testing.T) {
	out, err := TranslateOpenAIToGemini([]byte(`{"model":"gemini-2.5-flash","reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("TranslateOpenAIToGemini: %v", err)
	}
	if got := geminiMaxTokens(t, out); got <= 0 {
		t.Fatalf("maxOutputTokens = %v, want a positive budget", got)
	}
}

func TestTranslateOpenAIToGemini_ThinkingParity(t *testing.T) {
	translate := func(body string) []byte {
		out, err := TranslateOpenAIToGemini([]byte(body))
		if err != nil {
			t.Fatalf("TranslateOpenAIToGemini: %v", err)
		}
		return out
	}
	t.Run("high effort on gemini-2.5 writes budget 24576", func(t *testing.T) {
		out := translate(`{"model":"gemini-2.5-flash","reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`)
		tc := geminiThinkingConfig(t, out)
		if tc["thinkingBudget"] != 24576.0 || tc["includeThoughts"] != true {
			t.Fatalf("thinkingConfig = %+v", tc)
		}
		if got := geminiMaxTokens(t, out); got != 32768 {
			t.Fatalf("maxOutputTokens = %v, want 32768", got)
		}
	})
	t.Run("high effort on gemini-3 writes level", func(t *testing.T) {
		out := translate(`{"model":"gemini-3-pro","reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`)
		tc := geminiThinkingConfig(t, out)
		if tc["thinkingLevel"] != "high" || tc["includeThoughts"] != true {
			t.Fatalf("thinkingConfig = %+v", tc)
		}
		if got := geminiMaxTokens(t, out); got != 65535 {
			t.Fatalf("maxOutputTokens = %v, want 65535", got)
		}
	})
	t.Run("auto maps to high level and -1 budget", func(t *testing.T) {
		out := translate(`{"model":"gemini-3-pro","reasoning_effort":"auto","messages":[{"role":"user","content":"hi"}]}`)
		if tc := geminiThinkingConfig(t, out); tc["thinkingLevel"] != "high" {
			t.Fatalf("thinkingConfig = %+v", tc)
		}
		out = translate(`{"model":"gemini-2.5-flash","reasoning_effort":"auto","messages":[{"role":"user","content":"hi"}]}`)
		if tc := geminiThinkingConfig(t, out); tc["thinkingBudget"] != -1.0 {
			t.Fatalf("thinkingConfig = %+v", tc)
		}
	})
	t.Run("none disables budget, minimal level", func(t *testing.T) {
		out := translate(`{"model":"gemini-2.5-flash","reasoning_effort":"none","messages":[{"role":"user","content":"hi"}]}`)
		tc := geminiThinkingConfig(t, out)
		if tc["thinkingBudget"] != 0.0 || tc["includeThoughts"] != false {
			t.Fatalf("thinkingConfig = %+v", tc)
		}
		out = translate(`{"model":"gemini-3-pro","reasoning_effort":"none","messages":[{"role":"user","content":"hi"}]}`)
		tc = geminiThinkingConfig(t, out)
		if tc["thinkingLevel"] != "minimal" || tc["includeThoughts"] != false {
			t.Fatalf("thinkingConfig = %+v", tc)
		}
	})
	t.Run("no reasoning field writes no thinkingConfig", func(t *testing.T) {
		out, err := TranslateOpenAIToGemini([]byte(`{"model":"gemini-3-pro","messages":[{"role":"user","content":"hi"}]}`))
		if err != nil {
			t.Fatalf("TranslateOpenAIToGemini: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if gc, ok := m["generationConfig"].(map[string]any); ok {
			if _, has := gc["thinkingConfig"]; has {
				t.Fatalf("unexpected thinkingConfig in %+v", gc)
			}
		}
	})
	t.Run("maxOutputTokens raised to floor only", func(t *testing.T) {
		out := translate(`{"model":"gemini-3-pro","reasoning_effort":"high","max_tokens":100000,"messages":[{"role":"user","content":"hi"}]}`)
		if got := geminiMaxTokens(t, out); got != 100000 {
			t.Fatalf("maxOutputTokens = %v, want 100000", got)
		}
	})
}
