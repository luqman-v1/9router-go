package executor

import (
	json "encoding/json/v2"
	"strings"
	"testing"
)

func TestMarshalOpenCodeV2Chat_LeadingKeyOrder(t *testing.T) {
	body := map[string]any{
		"temperature":           0.2,
		"model":                 "glm-5.3-flash-free",
		"max_completion_tokens": 32000,
		"stream":                true,
		"messages":              []any{map[string]any{"role": "user", "content": "hi"}},
		"stream_options":        map[string]any{"include_usage": true},
		"tools":                 []any{},
		"top_p":                 1,
	}

	out, err := marshalOpenCodeV2Chat(body)
	if err != nil {
		t.Fatalf("marshalOpenCodeV2Chat: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("output is not a JSON object: %v\n%s", err, out)
	}
	if parsed["model"] != "glm-5.3-flash-free" {
		t.Errorf("model lost in rewriting: %v", parsed["model"])
	}

	// The leading keys must appear in v2ChatKeyOrder order.
	text := string(out)
	at := -1
	for _, key := range v2ChatKeyOrder {
		idx := strings.Index(text, `"`+key+`"`)
		if idx < 0 {
			t.Fatalf("leading key %q missing from output: %s", key, text)
		}
		if idx < at {
			t.Errorf("key %q appears out of order: %s", key, text)
		}
		at = idx
	}
	// The tail follows, alphabetically, after every leading key.
	if strings.Index(text, `"temperature"`) < at {
		t.Errorf("tail key temperature must follow the leading block: %s", text)
	}
	if strings.Index(text, `"temperature"`) > strings.Index(text, `"top_p"`) {
		t.Errorf("tail keys must stay alphabetical: %s", text)
	}
}

// An absent budget must stay absent. The gate distinguishes "no ceiling" from
// "ceiling null", and clampOutputBudget deliberately never invents one.
func TestMarshalOpenCodeV2Chat_SkipsAbsentKeys(t *testing.T) {
	out, err := marshalOpenCodeV2Chat(map[string]any{
		"model":    "m",
		"messages": []any{},
	})
	if err != nil {
		t.Fatalf("marshalOpenCodeV2Chat: %v", err)
	}
	if strings.Contains(string(out), "max_completion_tokens") {
		t.Errorf("absent budget must not be materialized: %s", out)
	}
	if !strings.HasPrefix(string(out), `{"model":"m"`) {
		t.Errorf("model must lead: %s", out)
	}
}

// Only leading keys present in the body participate, so a body with no leading
// keys at all still serializes as a plain object.
func TestMarshalOpenCodeV2Chat_NoLeadingKeys(t *testing.T) {
	out, err := marshalOpenCodeV2Chat(map[string]any{"zeta": 1, "alpha": 2})
	if err != nil {
		t.Fatalf("marshalOpenCodeV2Chat: %v", err)
	}
	if string(out) != `{"alpha":2,"zeta":1}` {
		t.Errorf("got %s", out)
	}
}

// Determinism is the property marshalStable exists for: the same logical request
// must produce byte-identical output on every call, or a prefix-caching upstream
// misses on every turn.
func TestMarshalOpenCodeV2Chat_IsDeterministic(t *testing.T) {
	build := func() map[string]any {
		return map[string]any{
			"model":            "m",
			"messages":         []any{map[string]any{"role": "user", "content": "hi"}},
			"stream":           true,
			"temperature":      0.1,
			"top_p":            0.9,
			"presence_penalty": 0,
			"seed":             7,
		}
	}
	first, err := marshalOpenCodeV2Chat(build())
	if err != nil {
		t.Fatalf("marshalOpenCodeV2Chat: %v", err)
	}
	for i := range 20 {
		next, err := marshalOpenCodeV2Chat(build())
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if string(next) != string(first) {
			t.Fatalf("output is not deterministic:\n first %s\n  next %s", first, next)
		}
	}
}
