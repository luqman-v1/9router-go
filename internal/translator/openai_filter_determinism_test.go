package translator

import (
	"bytes"
	"testing"
)

// TestFilterToOpenAIFormatJSONDeterministic pins the byte-stability of the
// OpenAI wire body. The same logical request must serialize identically on
// every call, or a prefix-caching upstream misses the cache it would
// otherwise hit.
func TestFilterToOpenAIFormatJSONDeterministic(t *testing.T) {
	body := []byte(`{"model":"m","max_tokens":128,"temperature":0.5,"stream":true,` +
		`"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"yo"}],` +
		`"tools":[{"type":"function","function":{"name":"f","description":"d","parameters":{"type":"object","properties":{"a":{"type":"string"}}}}}]}`)
	first := FilterToOpenAIFormatJSON(body, FilterOptions{})
	for i := 0; i < 100; i++ {
		got := FilterToOpenAIFormatJSON(body, FilterOptions{})
		if !bytes.Equal(got, first) {
			t.Fatalf("the OpenAI wire body is not stable:\nfirst: %s\ngot:   %s", first, got)
		}
	}
}

// TestTranslateOpenAIToGeminiDeterministic pins the byte-stability of the
// Gemini request body, whose generationConfig is a map.
func TestTranslateOpenAIToGeminiDeterministic(t *testing.T) {
	body := []byte(`{"model":"gemini-2.5-flash","reasoning_effort":"high","max_tokens":1024,` +
		`"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"yo"}]}`)
	first, err := TranslateOpenAIToGemini(body)
	if err != nil {
		t.Fatalf("TranslateOpenAIToGemini: %v", err)
	}
	for i := 0; i < 100; i++ {
		got, err := TranslateOpenAIToGemini(body)
		if err != nil {
			t.Fatalf("TranslateOpenAIToGemini: %v", err)
		}
		if !bytes.Equal(got, first) {
			t.Fatalf("the Gemini body is not stable:\nfirst: %s\ngot:   %s", first, got)
		}
	}
}
