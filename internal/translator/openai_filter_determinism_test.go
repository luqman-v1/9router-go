package translator

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	json "encoding/json/v2"
)

// TestFilterToOpenAIFormatJSONDeterministic pins the byte-stability of the
// OpenAI wire body. The same logical request must serialize identically on
// every call, or a prefix-caching upstream misses the cache it would
// otherwise hit.
func TestFilterToOpenAIFormatJSONDeterministic(t *testing.T) {
	// The fixture must be one the filter actually rewrites: a body the filter
	// leaves alone is returned byte for byte, so the assertion would pass
	// through the passthrough and never reach the deterministic marshal.
	body := []byte(`{"model":"m","max_tokens":128,"temperature":0.5,"stream":true,` +
		`"messages":[{"role":"developer","content":"sys"},` +
		`{"role":"user","content":[{"type":"thinking","thinking":"hmm"},{"type":"text","text":"hi"}]}],` +
		`"tool_choice":{"type":"any"}}`)
	first, err := FilterToOpenAIFormatJSON(body, FilterOptions{})
	if err != nil {
		t.Fatalf("FilterToOpenAIFormatJSON: %v", err)
	}
	if !bytes.Contains(first, []byte(`"role":"system"`)) {
		t.Fatalf("the fixture must be rewritten by the filter, otherwise the determinism assertion is vacuous: %s", first)
	}
	for i := 0; i < 100; i++ {
		got, err := FilterToOpenAIFormatJSON(body, FilterOptions{})
		if err != nil {
			t.Fatalf("FilterToOpenAIFormatJSON: %v", err)
		}
		if !bytes.Equal(got, first) {
			t.Fatalf("the OpenAI wire body is not stable:\nfirst: %s\ngot:   %s", first, got)
		}
	}
}

// TestTranslateClaudeToOpenAIToolChoiceIsDeterministic pins the byte-stability
// of the Claude translation. A forced single tool becomes a nested
// tool_choice object, and a map marshalled without the deterministic flag
// orders its keys differently from one call to the next.
func TestTranslateClaudeToOpenAIToolChoiceIsDeterministic(t *testing.T) {
	body := []byte(`{"model":"claude-x","max_tokens":100,"tool_choice":{"type":"tool","name":"f"},` +
		`"messages":[{"role":"user","content":"hi"}]}`)
	first, err := TranslateClaudeToOpenAI(body)
	if err != nil {
		t.Fatalf("TranslateClaudeToOpenAI: %v", err)
	}
	for i := 0; i < 200; i++ {
		got, err := TranslateClaudeToOpenAI(body)
		if err != nil {
			t.Fatalf("TranslateClaudeToOpenAI: %v", err)
		}
		if !bytes.Equal(got, first) {
			t.Fatalf("the translated body is not stable:\nfirst: %s\ngot:   %s", first, got)
		}
	}
}

// TestFilterToOpenAIFormatJSONNormalizesToolsWithoutMessages pins the shape
// normalisation: an empty tool list is dropped and a Claude-shaped tool_choice
// is rewritten whether or not the body carries a messages key.
func TestFilterToOpenAIFormatJSONNormalizesToolsWithoutMessages(t *testing.T) {
	out, err := FilterToOpenAIFormatJSON([]byte(`{"tools":[]}`), FilterOptions{})
	if err != nil {
		t.Fatalf("FilterToOpenAIFormatJSON: %v", err)
	}
	if bytes.Contains(out, []byte("tools")) {
		t.Fatalf("an empty tool list must be dropped, got %s", out)
	}
	out, err = FilterToOpenAIFormatJSON([]byte(`{"tool_choice":{"type":"any"}}`), FilterOptions{})
	if err != nil {
		t.Fatalf("FilterToOpenAIFormatJSON: %v", err)
	}
	if !bytes.Contains(out, []byte(`"required"`)) {
		t.Fatalf("a Claude-shaped tool_choice must be normalised without a messages key, got %s", out)
	}
}

// TestFilterToOpenAIFormatJSONReportsAnEmptyMessageList pins the local
// failure: a request whose every turn carried nothing ends up with an empty
// message list, and both OpenAI and Anthropic answer that with a 400. The
// gateway answers it itself instead of forwarding the array.
func TestFilterToOpenAIFormatJSONReportsAnEmptyMessageList(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":[` +
		`{"type":"thinking","thinking":"hmm"},{"type":"text","text":""}]}]}`)
	_, err := FilterToOpenAIFormatJSON(body, FilterOptions{})
	if !errors.Is(err, ErrNoMessages) {
		t.Fatalf("an emptied message list must be reported, got %v", err)
	}
	// A body without a messages key is not that case: the provider decides.
	if _, err := FilterToOpenAIFormatJSON([]byte(`{"model":"m"}`), FilterOptions{}); err != nil {
		t.Fatalf("a body without messages must be forwarded, got %v", err)
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
	out, err := FilterToOpenAIFormatJSON(body, FilterOptions{})
	if err != nil {
		t.Fatalf("FilterToOpenAIFormatJSON: %v", err)
	}
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
		got, err := FilterToOpenAIFormatJSON(body, FilterOptions{})
		var m map[string]any
		if err := json.Unmarshal(body, &m); err != nil {
			t.Fatalf("unmarshal %s: %v", raw, err)
		}
		FilterToOpenAIFormat(m, FilterOptions{})
		if errors.Is(err, ErrNoMessages) {
			// The filter emptied the list. The JSON entry point reports that
			// instead of forwarding the empty array; the map filter leaves
			// the empty list behind.
			msgs, ok := m["messages"].([]any)
			if !ok || len(msgs) != 0 {
				t.Errorf("the empty-list case must agree with the filter for %s: %v", raw, m["messages"])
			}
			continue
		}
		if err != nil {
			t.Fatalf("FilterToOpenAIFormatJSON(%s): %v", raw, err)
		}
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
	out, err := FilterToOpenAIFormatJSON(body, FilterOptions{PreserveCacheControl: true})
	if err != nil {
		t.Fatalf("FilterToOpenAIFormatJSON: %v", err)
	}
	if !bytes.Contains(out, []byte("cache_control")) {
		t.Errorf("cache_control was stripped for a provider that keeps it: %s", out)
	}
}
