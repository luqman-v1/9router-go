package translator

import (
	json "encoding/json/v2"
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

func TestSanitizeOpenAITools(t *testing.T) {
	body := []byte(`{
		"model": "gemini-3.5-flash",
		"messages": [{"role": "user", "content": "hi"}],
		"tools": [{
			"type": "function",
			"function": {
				"name": "do_thing",
				"parameters": {
					"type": "object",
					"properties": {
						"tags": {"type": "array", "uniqueItems": true, "items": {"type": "string"}},
						"opts": {}
					}
				}
			}
		}]
	}`)

	out, err := SanitizeOpenAITools(body)
	if err != nil {
		t.Fatalf("SanitizeOpenAITools failed: %v", err)
	}
	s := string(out)
	if strings.Contains(s, `"uniqueItems"`) {
		t.Errorf("uniqueItems must be stripped in OpenAI-compat body, got: %s", s)
	}
	if strings.Contains(s, `"opts":{}`) {
		t.Errorf("empty opts {} must become placeholder in OpenAI-compat body, got: %s", s)
	}
}

func TestSanitizeOpenAITools_NoToolsUnchanged(t *testing.T) {
	body := []byte(`{"model":"gemini-3.5-flash","messages":[{"role":"user","content":"hi"}]}`)
	out, err := SanitizeOpenAITools(body)
	if err != nil {
		t.Fatalf("SanitizeOpenAITools failed: %v", err)
	}
	if string(out) != string(body) {
		t.Errorf("no-tools body must be returned unchanged, got: %s", out)
	}
}

func TestCleanParametersSchema_RequiredPropertiesValidation(t *testing.T) {
	// A tool schema where required contains fields not defined in properties (or stripped)
	input := `{"type":"object","properties":{
		"path":{"type":"string"},
		"format":{"type":"string"}
	},"required":["path","format","undefined_field","another_ghost"]}`

	out := CleanParametersSchema([]byte(input))
	var parsed struct {
		Properties map[string]any `json:"properties"`
		Required   []string       `json:"required"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	// Must keep "path" and "format", but strip "undefined_field" and "another_ghost"
	if len(parsed.Required) != 2 {
		t.Fatalf("expected 2 required fields, got %d: %v", len(parsed.Required), parsed.Required)
	}
	if parsed.Required[0] != "path" || parsed.Required[1] != "format" {
		t.Errorf("expected ['path', 'format'], got %v", parsed.Required)
	}
	if _, ok := parsed.Properties["format"]; !ok {
		t.Errorf("expected property 'format' to be preserved")
	}
}

func TestCleanParametersSchema_NestedRequiredValidation(t *testing.T) {
	input := `{"type":"object","properties":{
		"options":{
			"type":"object",
			"properties":{
				"enabled":{"type":"boolean"}
			},
			"required":["enabled","missing_sub_prop"]
		}
	},"required":["options"]}`

	out := CleanParametersSchema([]byte(input))
	var parsed struct {
		Properties map[string]struct {
			Properties map[string]any `json:"properties"`
			Required   []string       `json:"required"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	opts, ok := parsed.Properties["options"]
	if !ok {
		t.Fatal("expected options property")
	}
	if len(opts.Required) != 1 || opts.Required[0] != "enabled" {
		t.Errorf("expected nested required to only contain ['enabled'], got %v", opts.Required)
	}
}
