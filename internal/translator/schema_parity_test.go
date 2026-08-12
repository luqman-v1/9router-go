package translator

import (
	"encoding/json"
	"strings"
	"testing"
)

// Next.js UNSUPPORTED_SCHEMA_CONSTRAINTS removes these six keywords that Go's
// list misses. Agent/Claude-Code tool schemas set them routinely; one leftover
// occurrence rejects the whole Gemini request ("Invalid tool parameters").
func TestCleanParametersSchema_StripsNextJSUnsupportedKeywords(t *testing.T) {
	input := `{"type":"object","properties":{
		"tags":{"type":"array","uniqueItems":true,"items":{"type":"string"}},
		"ratio":{"type":"number","multipleOf":0.5},
		"nested":{"type":"object","contains":{"type":"string"}},
		"freeform":{"unevaluatedProperties":true},
		"opts":{}
	}}`

	out := CleanParametersSchema([]byte(input))
	s := string(out)

	for _, kw := range []string{"uniqueItems", "multipleOf", "contains", "unevaluatedProperties", "unevaluatedItems", "contentSchema"} {
		if strings.Contains(s, `"`+kw+`"`) {
			t.Errorf("keyword %q must be stripped (Next.js parity), got: %s", kw, s)
		}
	}

	// An empty {} schema must become the object placeholder (Next.js parity) —
	// otherwise Gemini sees a function declaration with no parseable structure.
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	props, ok := parsed["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected top-level properties, got: %s", s)
	}
	opts, ok := props["opts"].(map[string]any)
	if !ok {
		t.Fatalf("expected 'opts' property object, got: %s", s)
	}
	if opts["type"] != "object" || opts["properties"] == nil {
		t.Errorf("empty {} schema must become object placeholder, got: %v", opts)
	}
}
