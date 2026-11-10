package translator

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"strings"
)

// geminiResponseFormat decodes an OpenAI request's response_format and maps it
// onto Gemini's native structured-output fields. It returns:
//
//	mime   — always "application/json" when the request asks for JSON.
//	schema — the inner json_schema.schema as a JSON value (types lowercased),
//	         or nil for json_object / a schema-less json_schema.
//	ok     — false when the request carries nothing mappable: no response_format
//	         at all, an explicit null, an unknown type, or an unparseable body.
//
// Gemini returns a request with responseMimeType set as part.text (native JSON),
// which the existing Gemini->OpenAI response translators already surface as
// content, so carrying the schema to the request needs no response-side change.
// Parity with KeiRouter transform.geminiResponseFormat.
func geminiResponseFormat(raw jsontext.Value) (mime string, schema any, ok bool) {
	if k := raw.Kind(); k == jsontext.KindInvalid || k == jsontext.KindNull {
		return "", nil, false
	}
	var rf struct {
		Type       string         `json:"type"`
		JSONSchema jsontext.Value `json:"json_schema"`
	}
	if err := json.Unmarshal(raw, &rf); err != nil {
		return "", nil, false
	}
	switch rf.Type {
	case "json_object":
		return "application/json", nil, true
	case "json_schema":
	default:
		return "", nil, false
	}
	// A schema-less json_schema degrades to plain JSON mode (same as
	// json_object): the client asked for JSON, so we still set the MIME type.
	if k := rf.JSONSchema.Kind(); k == jsontext.KindInvalid || k == jsontext.KindNull {
		return "application/json", nil, true
	}
	var inner struct {
		Schema jsontext.Value `json:"schema"`
	}
	if err := json.Unmarshal(rf.JSONSchema, &inner); err != nil {
		return "", nil, false
	}
	if k := inner.Schema.Kind(); k == jsontext.KindInvalid || k == jsontext.KindNull {
		return "application/json", nil, true
	}
	var s any
	if err := json.Unmarshal(inner.Schema, &s); err != nil {
		return "", nil, false
	}
	return "application/json", lowercaseJSONSchemaTypes(s), true
}

// lowercaseJSONSchemaTypes rewrites an OpenAPI-style schema (Gemini's
// responseSchema convention: "type":"OBJECT") into standard JSON Schema type
// names ("type":"object") so the schema passes Gemini's validator. It only
// touches string values stored under a "type" key — in JSON Schema that key is
// always the type keyword — and leaves every other value verbatim, including
// unknown keywords the client sent. Walks in place and returns the same value.
func lowercaseJSONSchemaTypes(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			if s, isStr := child.(string); isStr && k == "type" {
				t[k] = strings.ToLower(s)
				continue
			}
			t[k] = lowercaseJSONSchemaTypes(child)
		}
		return t
	case []any:
		for i, child := range t {
			t[i] = lowercaseJSONSchemaTypes(child)
		}
		return t
	default:
		return v
	}
}
