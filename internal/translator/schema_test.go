package translator

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCleanGeminiSchema_ParsesStringIntConstraints(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  map[string]any
	}{
		{
			name:  "minLength maxLength strings become ints",
			input: `{"type":"string","minLength":"1","maxLength":"10"}`,
			want:  map[string]any{"type": "string", "minLength": 1, "maxLength": 10},
		},
		{
			name:  "minItems maxItems strings become ints",
			input: `{"type":"array","minItems":"2","maxItems":"5","items":{"type":"string"}}`,
			want:  map[string]any{"type": "array", "minItems": 2, "maxItems": 5, "items": map[string]any{"type": "string"}},
		},
		{
			name:  "minimum maximum strings become ints",
			input: `{"type":"integer","minimum":"0","maximum":"100"}`,
			want:  map[string]any{"type": "integer", "minimum": 0, "maximum": 100},
		},
		{
			name:  "already int values are left alone",
			input: `{"type":"string","minLength":3,"maxLength":7}`,
			want:  map[string]any{"type": "string", "minLength": 3, "maxLength": 7},
		},
		{
			name:  "non-numeric strings are left untouched",
			input: `{"type":"string","minLength":"foo"}`,
			want:  map[string]any{"type": "string", "minLength": "foo"},
		},
		{
			name:  "constraints nested in properties are coerced too",
			input: `{"type":"object","properties":{"name":{"type":"string","minLength":"1"}}}`,
			want:  map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string", "minLength": 1}}},
		},
		{
			name:  "constraints surviving anyOf flattening are re-coerced",
			input: `{"type":"object","properties":{"n":{"type":"string","minLength":"1"}},"anyOf":[{"type":"object","maxLength":"5"}]}`,
			want:  map[string]any{"type": "object", "properties": map[string]any{"n": map[string]any{"type": "string", "minLength": 1}}, "maxLength": 5},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := CleanParametersSchema([]byte(tt.input))
			var got map[string]any
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("unmarshal output: %v\n%s", err, out)
			}
			if !jsonEqual(tt.want, got) {
				t.Errorf("CleanParametersSchema(%q):\n got: %s\nwant: %s", tt.input, marshalT(got), marshalT(tt.want))
			}
		})
	}
}

func TestCleanGeminiSchema_StripsNonIntegerConstraints(t *testing.T) {
	// Constraints that were previously stripped remain stripped even when string.
	for _, kw := range []string{"exclusiveMinimum", "exclusiveMaximum", "format", "multipleOf"} {
		t.Run(kw, func(t *testing.T) {
			out := CleanParametersSchema([]byte(`{"type":"number","` + kw + `":"5"}`))
			if strings.Contains(string(out), `"`+kw+`"`) {
				t.Errorf("%q must be stripped even as a string, got: %s", kw, out)
			}
		})
	}
}

func TestCleanGeminiSchema_FiltersEmptyStringsFromEnum(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string // expected enum JSON, or "" when enum key removed
	}{
		{
			name:  "drops empty strings keeps the rest",
			input: `{"type":"string","enum":["","foo","","bar"]}`,
			want:  `["foo","bar"]`,
		},
		{
			name:  "all empty -> enum key removed",
			input: `{"type":"string","enum":["","",""]}`,
			want:  "",
		},
		{
			name:  "no empties -> unchanged",
			input: `{"type":"string","enum":["foo","bar"]}`,
			want:  `["foo","bar"]`,
		},
		{
			name:  "empty added via type flattening then filtered",
			input: `{"enum":["","foo"],"anyOf":[{"enum":["","baz"]}]}`,
			want:  `["baz"]`,
		},
		{
			name:  "non-string enum values converted then empties dropped",
			input: `{"type":"string","enum":[1,"","0",""]}`,
			want:  `["1","0"]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := CleanParametersSchema([]byte(tt.input))
			var got map[string]any
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("unmarshal output: %v\n%s", err, out)
			}
			if tt.want == "" {
				if _, has := got["enum"]; has {
					t.Errorf("expected enum key removed, got: %s", out)
				}
				return
			}
			var wantArr []string
			if err := json.Unmarshal([]byte(tt.want), &wantArr); err != nil {
				t.Fatalf("bad want JSON: %v", err)
			}
			gotArr, ok := got["enum"].([]any)
			if !ok {
				t.Fatalf("expected enum array, got: %s", out)
			}
			var gotStrs []string
			for _, v := range gotArr {
				gotStrs = append(gotStrs, v.(string))
			}
			if len(gotStrs) != len(wantArr) {
				t.Errorf("enum mismatch:\n got: %s\nwant: %s", gotStrs, wantArr)
				return
			}
			for i := range gotStrs {
				if gotStrs[i] != wantArr[i] {
					t.Errorf("enum mismatch at %d:\n got: %s\nwant: %s", i, gotStrs, wantArr)
				}
			}
		})
	}
}

func TestCleanGeminiSchema_EnumGetsStringTypeWhenMissing(t *testing.T) {
	out := CleanParametersSchema([]byte(`{"enum":["","foo"]}`))
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal output: %v\n%s", err, out)
	}
	if got["type"] != "string" {
		t.Errorf("expected type=string added with enum, got: %s", out)
	}
	if _, has := got["enum"]; !has {
		t.Errorf("expected enum preserved, got: %s", out)
	}
}

// jsonEqual compares two decoded JSON values structurally via stable marshaling.
func jsonEqual(a, b any) bool {
	return marshalT(a) == marshalT(b)
}

func marshalT(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
