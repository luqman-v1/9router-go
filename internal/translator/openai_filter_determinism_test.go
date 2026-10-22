package translator

import (
	"bytes"
	"reflect"
	"testing"

	json "encoding/json/v2"
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

// TestFilterToOpenAIFormatJSONLeavesAnUnchangedBodyByteIdentical pins the
// fast path: a body the filter would not change is returned byte for byte, so
// a plain request pays no re-serialisation on the way out.
func TestFilterToOpenAIFormatJSONLeavesAnUnchangedBodyByteIdentical(t *testing.T) {
	body := []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"hello"}],"stream":true}`)
	out := FilterToOpenAIFormatJSON(body, FilterOptions{})
	if !bytes.Equal(out, body) {
		t.Errorf("body was rewritten:\n got %s\nwant %s", out, body)
	}
}

// TestFilterToOpenAIFormatJSONFastPathMatchesTheFilter pins the fast path
// against the filter itself: for every body in the corpus the result the
// entry point returns and the result the filter produces must carry the same
// request. The bytes may differ (an unchanged body keeps the client's key
// order), the request may not.
func TestFilterToOpenAIFormatJSONFastPathMatchesTheFilter(t *testing.T) {
	corpus := []string{
		`{"model":"gpt-5","messages":[{"role":"user","content":"hello"}]}`,
		`{"model":"gpt-5","messages":[{"role":"user","content":""}]}`,
		`{"model":"gpt-5","messages":[{"role":"user","content":"   "}]}`,
		`{"model":"gpt-5","messages":[{"role":"developer","content":"x"}]}`,
		`{"model":"gpt-5","messages":[{"role":"user","content":[{"type":"text","text":"a"}]}]}`,
		`{"model":"gpt-5","messages":[{"role":"user","content":[]}]}`,
		`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}],"tools":[]}`,
		`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}]}`,
		`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}],"tool_choice":{"type":"any"}}`,
		`{"model":"gpt-5","messages":[{"role":"user","content":[{"type":"thinking","thinking":"x"},{"type":"text","text":"a"}]}]}`,
		`{"model":"gpt-5","messages":[{"role":"user","content":[{"type":"text","text":"a","cache_control":{"type":"ephemeral"}}]}]}`,
		`{"model":"gpt-5","messages":[{"role":"user","content":[{"type":"text","text":"a","signature":"s"}]}]}`,
		`{"model":"gpt-5","messages":[{"role":"assistant","content":"a","tool_calls":[{"id":"1","function":{"name":"f","arguments":"{}"}}]}]}`,
		`{"model":"gpt-5","messages":[{"role":"tool","tool_call_id":"1","content":"r"}]}`,
	}
	for _, raw := range corpus {
		body := []byte(raw)
		got := FilterToOpenAIFormatJSON(body, FilterOptions{})
		var m map[string]any
		if err := json.Unmarshal(body, &m); err != nil {
			t.Fatalf("unmarshal %s: %v", raw, err)
		}
		FilterToOpenAIFormat(m, FilterOptions{})
		var want map[string]any
		if err := json.Unmarshal(got, &want); err != nil {
			t.Fatalf("unmarshal result %s: %v", got, err)
		}
		if !reflect.DeepEqual(m, want) {
			t.Errorf("fast path disagrees with the filter for %s:\n got %v\nwant %v", raw, want, m)
		}
	}
}

// TestFilterToOpenAIFormatJSONKeepsCacheControlForTheQuirkProvider pins the
// quirk: a provider registered with preserveCacheControl keeps the markers,
// and the fast path must not strip them either.
func TestFilterToOpenAIFormatJSONKeepsCacheControlForTheQuirkProvider(t *testing.T) {
	body := []byte(`{"model":"qwen","messages":[{"role":"user","content":[{"type":"text","text":"a","cache_control":{"type":"ephemeral"}}]}]}`)
	out := FilterToOpenAIFormatJSON(body, FilterOptions{PreserveCacheControl: true})
	if !bytes.Contains(out, []byte("cache_control")) {
		t.Errorf("cache_control was stripped for a provider that keeps it: %s", out)
	}
}
