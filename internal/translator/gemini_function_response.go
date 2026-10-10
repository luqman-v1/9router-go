package translator

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"reflect"
)

// GeminiReservedResponseKeys are keys Gemini reserves inside a
// functionResponse payload. A tool result that carries a JSON Schema or
// OpenAPI document (webfetch, a repo read, an MCP server) legitimately
// contains "$ref", but Gemini reads that key as a pointer into
// functionResponse.parts and rejects the whole request with
// "The referenced name `#/$defs/...` in function_response.response does not
// match to a display_name in the function_response.parts" — a valid tool
// result turns the whole turn into a 400. Renaming the key keeps the payload
// readable and leaves every other field byte-identical.
// Parity with GEMINI_RESERVED_RESPONSE_KEYS in
// open-sse/translator/formats/gemini.js (#625df74d).
var GeminiReservedResponseKeys = map[string]string{"$ref": "_ref"}

// SanitizeGeminiFunctionResponsePayload renames every reserved key in a
// tool-result payload, recursing through nested arrays and objects. Scalars
// and values already free of reserved keys are returned unchanged, and the
// input is never mutated: the sanitized copy only replaces a result whose
// subtree actually contained a reserved key.
func SanitizeGeminiFunctionResponsePayload(value any) any {
	switch typed := value.(type) {
	case jsontext.Value:
		return sanitizeGeminiRawJSON(typed)
	case map[string]any:
		return sanitizeGeminiResponseObject(typed)
	case []any:
		out := make([]any, len(typed))
		changed := false
		for i, item := range typed {
			out[i] = SanitizeGeminiFunctionResponsePayload(item)
			if !sameJSONValue(out[i], item) {
				changed = true
			}
		}
		if !changed {
			return typed
		}
		return out
	default:
		return value
	}
}

// sanitizeGeminiResponseObject builds the renamed copy of one object and
// reports whether anything actually moved, so an untouched payload keeps its
// original reference.
func sanitizeGeminiResponseObject(in map[string]any) any {
	out := make(map[string]any, len(in))
	changed := false
	for key, val := range in {
		newKey := key
		if replacement, reserved := GeminiReservedResponseKeys[key]; reserved {
			newKey = replacement
		}
		sanitized := SanitizeGeminiFunctionResponsePayload(val)
		if newKey != key || !sameJSONValue(sanitized, val) {
			changed = true
		}
		out[newKey] = sanitized
	}
	if !changed {
		return in
	}
	return out
}

// sameJSONValue compares two decoded tool-result values structurally. A result
// holds plain maps and slices, but a payload may also carry a jsontext.Value
// (a verbatim JSON byte slice), which is not comparable with ==, so anything
// that is neither map nor slice falls back to a deep comparison.
func sameJSONValue(a, b any) bool {
	switch left := a.(type) {
	case map[string]any:
		right, ok := b.(map[string]any)
		if !ok || len(left) != len(right) {
			return false
		}
		for k, lv := range left {
			rv, ok := right[k]
			if !ok || !sameJSONValue(lv, rv) {
				return false
			}
		}
		return true
	case []any:
		right, ok := b.([]any)
		if !ok || len(left) != len(right) {
			return false
		}
		for i := range left {
			if !sameJSONValue(left[i], right[i]) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(a, b)
	}
}

// sanitizeGeminiFunctionResponsePart returns the part with its tool-result
// payload sanitized. A part without a functionResponse, or with a scalar
// result, is returned untouched.
func sanitizeGeminiFunctionResponsePart(part GeminiPart) GeminiPart {
	if part.FunctionResponse == nil || part.FunctionResponse.Response == nil {
		return part
	}
	result := part.FunctionResponse.Response.Result
	sanitized := SanitizeGeminiFunctionResponsePayload(result)
	if sameJSONValue(sanitized, result) {
		return part
	}
	out := part
	out.FunctionResponse = &GeminiFunctionResp{
		Name: part.FunctionResponse.Name,
		ID:   part.FunctionResponse.ID,
		Response: &GeminiFuncResp{
			Result: sanitized,
		},
	}
	return out
}

// sanitizeGeminiRawJSON renames reserved keys in a tool result that was kept
// as verbatim JSON bytes rather than decoded into a map. The bytes are decoded,
// sanitized and re-encoded only when a reserved key was actually present, so a
// result with none keeps its original bytes.
func sanitizeGeminiRawJSON(raw jsontext.Value) any {
	if !raw.IsValid() || len(raw) == 0 {
		return raw
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return raw
	}
	sanitized := SanitizeGeminiFunctionResponsePayload(decoded)
	if sameJSONValue(sanitized, decoded) {
		return raw
	}
	out, err := json.Marshal(sanitized)
	if err != nil {
		return raw
	}
	return jsontext.Value(out)
}