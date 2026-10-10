package translator

import (
	"encoding/json"
	"strings"
	"testing"
)

// Gemini reads a "$ref" key inside functionResponse.response as a pointer into
// functionResponse.parts, not as data, and 400s the whole turn when the name
// does not resolve. Tool results that legitimately carry a JSON Schema or
// OpenAPI document hit that constantly (#625df74d), so the key is renamed on
// the way out while every other field stays byte-identical.

// geminiResponsePayload returns the tool-result payload the translator emitted
// for the first functionResponse part, so a case can assert on the sanitized
// structure rather than on the surrounding envelope.
func geminiResponsePayload(t *testing.T, body []byte) any {
	t.Helper()
	var req GeminiRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal translated request: %v", err)
	}
	for _, c := range req.Contents {
		for _, p := range c.Parts {
			if p.FunctionResponse != nil {
				return p.FunctionResponse.Response.Result
			}
		}
	}
	t.Fatal("no functionResponse part in translated request")
	return nil
}

func TestTranslateOpenAIToGemini_RenamesReservedResponseKeys(t *testing.T) {
	openaiJSON := []byte(`{
		"model": "gemini-3.8-flash",
		"messages": [
			{"role": "user", "content": "fetch the schema"},
			{"role": "assistant", "content": "", "tool_calls": [
				{"id": "call_s", "type": "function", "function": {"name": "webfetch", "arguments": "{}"}}
			]},
			{"role": "tool", "tool_call_id": "call_s", "content": "{\"schema\":{\"$ref\":\"#/$defs/Node\",\"title\":\"kept\",\"nested\":{\"items\":[{\"$ref\":\"#/$defs/Item\"}]}}}"}
		]
	}`)

	body, err := TranslateOpenAIToGemini(openaiJSON)
	if err != nil {
		t.Fatalf("TranslateOpenAIToGemini failed: %v", err)
	}
	if strings.Contains(string(body), `"$ref"`) {
		t.Fatalf("a reserved $ref key survived the translation; Gemini answers 400 INVALID_ARGUMENT:\n%s", body)
	}

	payload, ok := geminiResponsePayload(t, body).(map[string]any)
	if !ok {
		t.Fatalf("tool result is not an object payload: %T", geminiResponsePayload(t, body))
	}
	schema, ok := payload["schema"].(map[string]any)
	if !ok {
		t.Fatalf("schema payload missing: %v", payload)
	}
	if schema["_ref"] != "#/$defs/Node" {
		t.Errorf("top-level $ref = %v, want the renamed _ref = #/$defs/Node", schema["_ref"])
	}
	if schema["title"] != "kept" {
		t.Errorf("sibling key title = %v, want it preserved", schema["title"])
	}
	nested, ok := schema["nested"].(map[string]any)
	if !ok {
		t.Fatalf("nested object missing: %v", schema)
	}
	items, ok := nested["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("nested array = %v, want one item", nested["items"])
	}
	item, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("nested item = %v, want an object", items[0])
	}
	if item["_ref"] != "#/$defs/Item" {
		t.Errorf("nested $ref = %v, want _ref = #/$defs/Item", item["_ref"])
	}
}

// A result that never contained a reserved key must go out unchanged: the
// sanitizer is not allowed to reshape an ordinary tool result.
func TestTranslateOpenAIToGemini_LeavesOrdinaryResultsUnchanged(t *testing.T) {
	openaiJSON := []byte(`{
		"model": "gemini-3.8-flash",
		"messages": [
			{"role": "user", "content": "run it"},
			{"role": "assistant", "content": "", "tool_calls": [
				{"id": "call_o", "type": "function", "function": {"name": "f", "arguments": "{}"}}
			]},
			{"role": "tool", "tool_call_id": "call_o", "content": "{\"ok\":true,\"rows\":[1,2]}"}
		]
	}`)

	body, err := TranslateOpenAIToGemini(openaiJSON)
	if err != nil {
		t.Fatalf("TranslateOpenAIToGemini failed: %v", err)
	}
	payload, ok := geminiResponsePayload(t, body).(map[string]any)
	if !ok {
		t.Fatalf("tool result is not an object payload: %T", geminiResponsePayload(t, body))
	}
	if payload["ok"] != true {
		t.Errorf("ok = %v, want true", payload["ok"])
	}
	rows, ok := payload["rows"].([]any)
	if !ok || len(rows) != 2 {
		t.Fatalf("rows = %v, want [1 2]", payload["rows"])
	}
}

func TestSanitizeGeminiFunctionResponsePayload(t *testing.T) {
	tests := []struct {
		name  string
		input any
		want  any
	}{
		{
			name:  "scalar passes through",
			input: "plain text",
			want:  "plain text",
		},
		{
			name:  "object without reserved keys is returned as-is",
			input: map[string]any{"a": 1},
			want:  map[string]any{"a": 1},
		},
		{
			name:  "top level ref is renamed",
			input: map[string]any{"$ref": "#/$defs/A"},
			want:  map[string]any{"_ref": "#/$defs/A"},
		},
		{
			name:  "ref inside an array element is renamed",
			input: map[string]any{"allOf": []any{map[string]any{"$ref": "#/$defs/B"}}},
			want:  map[string]any{"allOf": []any{map[string]any{"_ref": "#/$defs/B"}}},
		},
		{
			name:  "ref under a reserved-looking sibling keeps the sibling",
			input: map[string]any{"$ref": "#/$defs/C", "definitions": map[string]any{"$ref": "#/$defs/D"}},
			want:  map[string]any{"_ref": "#/$defs/C", "definitions": map[string]any{"_ref": "#/$defs/D"}},
		},
		{
			name:  "deeply nested arrays and maps are walked",
			input: []any{[]any{map[string]any{"x": map[string]any{"$ref": "#/$defs/E"}}}},
			want:  []any{[]any{map[string]any{"x": map[string]any{"_ref": "#/$defs/E"}}}},
		},
		{
			name:  "other reserved-looking names are untouched",
			input: map[string]any{"$schema": "x", "$id": "y"},
			want:  map[string]any{"$schema": "x", "$id": "y"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeGeminiFunctionResponsePayload(tt.input)
			wantJSON, err := json.Marshal(tt.want)
			if err != nil {
				t.Fatalf("marshal want: %v", err)
			}
			gotJSON, err := json.Marshal(got)
			if err != nil {
				t.Fatalf("marshal got: %v", err)
			}
			if string(gotJSON) != string(wantJSON) {
				t.Errorf("got %s, want %s", gotJSON, wantJSON)
			}
		})
	}
}

// The sanitizer must not mutate its input: the same decoded tool result is
// still read by other parts of the pipeline after this runs.
func TestSanitizeGeminiFunctionResponsePayload_DoesNotMutateInput(t *testing.T) {
	input := map[string]any{"$ref": "#/$defs/A", "keep": "yes"}
	gotMap, ok := SanitizeGeminiFunctionResponsePayload(input).(map[string]any)
	if !ok {
		t.Fatalf("sanitizer returned %T, want a map", SanitizeGeminiFunctionResponsePayload(input))
	}
	if gotMap["_ref"] != "#/$defs/A" {
		t.Errorf("sanitized _ref = %v, want #/$defs/A", gotMap["_ref"])
	}
	if input["$ref"] != "#/$defs/A" {
		t.Errorf("input was mutated: $ref = %v", input["$ref"])
	}
	if _, renamed := input["_ref"]; renamed {
		t.Error("sanitizer wrote _ref into the caller's map")
	}
}