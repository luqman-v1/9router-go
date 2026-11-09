package translator

import (
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"strings"
	"testing"
)

func TestGeminiResponseFormat(t *testing.T) {
	tests := []struct {
		name       string
		raw        string // json.RawMessage for response_format; "" = absent
		wantMime   string
		wantSchema bool
		wantLower  string // expected substring of the marshalled schema
		wantOk     bool
	}{
		{
			name:   "absent response_format",
			raw:    "",
			wantOk: false,
		},
		{
			name:   "explicit null",
			raw:    "null",
			wantOk: false,
		},
		{
			name:     "json_object",
			raw:      `{"type":"json_object"}`,
			wantMime: "application/json",
			wantOk:   true,
		},
		{
			name:       "json_schema with OpenAPI uppercase types",
			raw:        `{"type":"json_schema","json_schema":{"name":"resp","schema":{"type":"OBJECT","properties":{"name":{"type":"STRING"}}}}}`,
			wantMime:   "application/json",
			wantSchema: true,
			wantLower:  `"type":"object"`,
			wantOk:     true,
		},
		{
			name:       "json_schema with array and nested items",
			raw:        `{"type":"json_schema","json_schema":{"schema":{"type":"ARRAY","items":{"type":"OBJECT","properties":{"v":{"type":"BOOLEAN"}}}}}}`,
			wantMime:   "application/json",
			wantSchema: true,
			wantLower:  `"type":"array"`,
			wantOk:     true,
		},
		{
			name:     "json_schema without inner schema degrades to json_object",
			raw:      `{"type":"json_schema","json_schema":{"name":"resp"}}`,
			wantMime: "application/json",
			wantOk:   true,
		},
		{
			name:     "json_schema with null inner schema",
			raw:      `{"type":"json_schema","json_schema":{"schema":null}}`,
			wantMime: "application/json",
			wantOk:   true,
		},
		{
			name:   "unknown type",
			raw:    `{"type":"csv"}`,
			wantOk: false,
		},
		{
			name:   "malformed json",
			raw:    `{"type":`,
			wantOk: false,
		},
		{
			name:   "not an object",
			raw:    `"json_object"`,
			wantOk: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var raw jsontext.Value
			if tt.raw != "" {
				raw = jsontext.Value(tt.raw)
			}
			mime, schema, ok := geminiResponseFormat(raw)
			if ok != tt.wantOk {
				t.Fatalf("geminiResponseFormat ok = %v, want %v", ok, tt.wantOk)
			}
			if !ok {
				return
			}
			if mime != tt.wantMime {
				t.Errorf("mime = %q, want %q", mime, tt.wantMime)
			}
			if tt.wantSchema {
				if schema == nil {
					t.Fatalf("schema = nil, want non-nil")
				}
				marshalled, err := json.Marshal(schema)
				if err != nil {
					t.Fatalf("marshal schema: %v", err)
				}
				if !strings.Contains(string(marshalled), tt.wantLower) {
					t.Errorf("schema %q does not contain %q", string(marshalled), tt.wantLower)
				}
			} else {
				if schema != nil {
					t.Errorf("schema = %v, want nil", schema)
				}
			}
		})
	}
}

func TestGeminiResponseFormatLowercasesOnlyTypeKeys(t *testing.T) {
	// A "type" value inside an enum / example must not be clobbered: only the
	// JSON-Schema type keyword (a "type" key holding a string) is lowercased.
	in := map[string]any{
		"type": "OBJECT",
		"properties": map[string]any{
			"status": map[string]any{
				"type": "STRING",
				"enum": []any{"OBJECT", "active"}, // "OBJECT" here is a value, not a type key
			},
		},
	}
	out := lowercaseJSONSchemaTypes(in)
	status := out.(map[string]any)["properties"].(map[string]any)["status"].(map[string]any)
	if got := status["type"]; got != "string" {
		t.Errorf("type key = %v, want \"string\"", got)
	}
	// The enum values are passed through untouched.
	if !reflectEnum(status["enum"], "OBJECT", "active") {
		t.Errorf("enum values altered: %v", status["enum"])
	}
}

func reflectEnum(enum any, want0, want1 string) bool {
	arr, ok := enum.([]any)
	if !ok || len(arr) != 2 {
		return false
	}
	return arr[0] == want0 && arr[1] == want1
}

func TestTranslateOpenAIToGeminiCarriesResponseFormat(t *testing.T) {
	base := func(responseFormat string) string {
		body := `{"model":"gemini-2.5-pro","messages":[{"role":"user","content":"hi"}]`
		if responseFormat != "" {
			body += `,"response_format":` + responseFormat
		}
		return body + `}`
	}

	t.Run("json_schema carries mime and schema", func(t *testing.T) {
		out, err := TranslateOpenAIToGemini([]byte(base(`{"type":"json_schema","json_schema":{"name":"r","schema":{"type":"OBJECT","properties":{"a":{"type":"STRING"}}}}}`)))
		if err != nil {
			t.Fatalf("TranslateOpenAIToGemini: %v", err)
		}
		assertGenerationConfig(t, out, "application/json", true)
	})

	t.Run("json_object carries mime only", func(t *testing.T) {
		out, err := TranslateOpenAIToGemini([]byte(base(`{"type":"json_object"}`)))
		if err != nil {
			t.Fatalf("TranslateOpenAIToGemini: %v", err)
		}
		assertGenerationConfig(t, out, "application/json", false)
	})

	t.Run("absent response_format sets no mime", func(t *testing.T) {
		out, err := TranslateOpenAIToGemini([]byte(base("")))
		if err != nil {
			t.Fatalf("TranslateOpenAIToGemini: %v", err)
		}
		var body struct {
			GenerationConfig map[string]any `json:"generationConfig"`
		}
		if err := jsonv2.Unmarshal(out, &body); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if _, present := body.GenerationConfig["responseMimeType"]; present {
			t.Errorf("responseMimeType present without response_format: %s", out)
		}
	})
}

func assertGenerationConfig(t *testing.T, body []byte, wantMime string, wantSchema bool) {
	t.Helper()
	var out struct {
		GenerationConfig map[string]any `json:"generationConfig"`
	}
	if err := jsonv2.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal gemini body: %v", err)
	}
	gc := out.GenerationConfig
	if gc == nil {
		t.Fatalf("no generationConfig in %s", body)
	}
	if got := gc["responseMimeType"]; got != wantMime {
		t.Errorf("responseMimeType = %v, want %q", got, wantMime)
	}
	if _, present := gc["responseJsonSchema"]; present != wantSchema {
		t.Errorf("responseJsonSchema present = %v, want %v (body %s)", present, wantSchema, body)
	}
}
