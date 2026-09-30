package translator

import (
	json "encoding/json/v2"
	"testing"
)

// Gemini rejects the draft-07 / 2020-12 tuple keywords outright: one occurrence
// anywhere in a tool parameter schema 400s the whole request
// (`Unknown name "additionalItems" at functionDeclaration.parameters`).
// prefixItems is stripped too, so both its promotion to `items` and its
// coexistence with an existing `items` are asserted here to keep that from
// regressing.
func TestCleanParametersSchema_StripsTupleKeywords(t *testing.T) {
	arraySiblings := func(t *testing.T, got any) {
		t.Helper()
		if v := schemaNode(t, got, "type"); v != "array" {
			t.Errorf("sibling type must survive, got %#v", v)
		}
		if v := schemaNode(t, got, "items", "type"); v != "string" {
			t.Errorf("sibling items must survive, got %#v", v)
		}
		if v := schemaNode(t, got, "description"); v != "keep me" {
			t.Errorf("sibling description must survive, got %#v", v)
		}
	}

	tests := []struct {
		name      string
		input     string
		forbidden []string
		verify    func(t *testing.T, got map[string]any)
	}{
		{
			name:      "top level additionalItems schema",
			input:     `{"type":"array","items":{"type":"string"},"description":"keep me","additionalItems":{"type":"integer"}}`,
			forbidden: []string{"additionalItems"},
			verify:    func(t *testing.T, got map[string]any) { arraySiblings(t, got) },
		},
		{
			name:      "additionalItems inside an array item sub-object",
			input:     `{"type":"object","properties":{"rows":{"type":"array","description":"keep me","items":{"type":"object","properties":{"cells":{"type":"string"},"more":{"type":"array","items":{"type":"number"},"additionalItems":false}}}}}}`,
			forbidden: []string{"additionalItems"},
			verify: func(t *testing.T, got map[string]any) {
				if v := schemaNode(t, got, "properties", "rows", "description"); v != "keep me" {
					t.Errorf("row description must survive, got %#v", v)
				}
				if v := schemaNode(t, got, "properties", "rows", "items", "properties", "cells", "type"); v != "string" {
					t.Errorf("sibling cell property must survive, got %#v", v)
				}
				if v := schemaNode(t, got, "properties", "rows", "items", "properties", "more", "items", "type"); v != "number" {
					t.Errorf("items of the array that carried additionalItems must survive, got %#v", v)
				}
			},
		},
		{
			name:      "additionalItems null",
			input:     `{"type":"array","items":{"type":"string"},"description":"keep me","additionalItems":null}`,
			forbidden: []string{"additionalItems"},
			verify:    func(t *testing.T, got map[string]any) { arraySiblings(t, got) },
		},
		{
			name:      "additionalItems false",
			input:     `{"type":"array","items":{"type":"string"},"description":"keep me","additionalItems":false}`,
			forbidden: []string{"additionalItems"},
			verify:    func(t *testing.T, got map[string]any) { arraySiblings(t, got) },
		},
		{
			name:      "additionalItems true",
			input:     `{"type":"array","items":{"type":"string"},"description":"keep me","additionalItems":true}`,
			forbidden: []string{"additionalItems"},
			verify:    func(t *testing.T, got map[string]any) { arraySiblings(t, got) },
		},
		{
			name:      "additionalItems list of schemas",
			input:     `{"type":"array","items":{"type":"string"},"description":"keep me","additionalItems":[{"type":"integer"}]}`,
			forbidden: []string{"additionalItems"},
			verify:    func(t *testing.T, got map[string]any) { arraySiblings(t, got) },
		},
		{
			name:      "additionalItems re-injected by anyOf flatten",
			input:     `{"type":"object","properties":{"v":{"anyOf":[{"type":"array","items":{"type":"string"},"additionalItems":{"type":"integer"}}]}}}`,
			forbidden: []string{"additionalItems", "anyOf"},
			verify: func(t *testing.T, got map[string]any) {
				if v := schemaNode(t, got, "properties", "v", "type"); v != "array" {
					t.Errorf("merged branch type must survive, got %#v", v)
				}
				if v := schemaNode(t, got, "properties", "v", "items", "type"); v != "string" {
					t.Errorf("merged branch items must survive, got %#v", v)
				}
			},
		},
		{
			name:      "prefixItems promoted to items",
			input:     `{"type":"array","description":"keep me","prefixItems":[{"type":"string"},{"type":"number"}]}`,
			forbidden: []string{"prefixItems"},
			verify:    func(t *testing.T, got map[string]any) { arraySiblings(t, got) },
		},
		{
			name:      "prefixItems does not clobber existing items",
			input:     `{"type":"array","items":{"type":"number"},"description":"keep me","prefixItems":[{"type":"string"}]}`,
			forbidden: []string{"prefixItems"},
			verify: func(t *testing.T, got map[string]any) {
				if v := schemaNode(t, got, "type"); v != "array" {
					t.Errorf("sibling type must survive, got %#v", v)
				}
				if v := schemaNode(t, got, "items", "type"); v != "number" {
					t.Errorf("an existing items must win over prefixItems[0], got %#v", v)
				}
				if v := schemaNode(t, got, "description"); v != "keep me" {
					t.Errorf("sibling description must survive, got %#v", v)
				}
			},
		},
		{
			name:      "additionalItems inside a promoted prefixItems entry",
			input:     `{"type":"object","properties":{"pair":{"type":"array","prefixItems":[{"type":"array","items":{"type":"string"},"additionalItems":{"type":"integer"}},{"type":"number"}]}}}`,
			forbidden: []string{"additionalItems", "prefixItems"},
			verify: func(t *testing.T, got map[string]any) {
				if v := schemaNode(t, got, "properties", "pair", "items", "type"); v != "array" {
					t.Errorf("first prefixItems entry must become items, got %#v", v)
				}
				if v := schemaNode(t, got, "properties", "pair", "items", "items", "type"); v != "string" {
					t.Errorf("promoted entry items must survive, got %#v", v)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := CleanParametersSchema([]byte(tt.input))

			var got map[string]any
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("unmarshal cleaned schema: %v (out: %s)", err, string(out))
			}

			keys := map[string]bool{}
			schemaKeysEverywhere(got, keys)
			for _, kw := range tt.forbidden {
				if keys[kw] {
					t.Errorf("%s survived at some depth, got: %s", kw, string(out))
				}
			}
			tt.verify(t, got)
		})
	}
}

// The OpenAI-compat route is the body actually put on the wire, so assert the
// keyword is gone there and the tool declaration around it is untouched.
func TestSanitizeOpenAITools_StripsAdditionalItems(t *testing.T) {
	body := []byte(`{
		"model": "gemini-3.5-flash",
		"tools": [{
			"type": "function",
			"function": {
				"name": "write_rows",
				"description": "keep me",
				"parameters": {
					"type": "object",
					"properties": {
						"rows": {
							"type": "array",
							"items": {
								"type": "object",
								"properties": {
									"values": {"type": "array", "items": {"type": "string"}, "additionalItems": false}
								}
							}
						}
					},
					"additionalItems": false
				}
			}
		}]
	}`)

	out, err := SanitizeOpenAITools(body)
	if err != nil {
		t.Fatalf("SanitizeOpenAITools failed: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal sanitized body: %v (out: %s)", err, string(out))
	}
	keys := map[string]bool{}
	schemaKeysEverywhere(got, keys)
	if keys["additionalItems"] {
		t.Errorf("additionalItems must not reach Gemini, got: %s", string(out))
	}

	fn := schemaNode(t, got, "tools").([]any)[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "write_rows" || fn["description"] != "keep me" {
		t.Errorf("tool declaration must survive, got %#v", fn)
	}
	if v := schemaNode(t, fn, "parameters", "properties", "rows", "items", "properties", "values", "items", "type"); v != "string" {
		t.Errorf("nested items must survive, got %#v", v)
	}
}

// schemaKeysEverywhere collects every object key at every depth of a decoded
// schema, so a strip can be asserted anywhere rather than at one known path.
func schemaKeysEverywhere(node any, out map[string]bool) {
	switch v := node.(type) {
	case map[string]any:
		for k, child := range v {
			out[k] = true
			schemaKeysEverywhere(child, out)
		}
	case []any:
		for _, elem := range v {
			schemaKeysEverywhere(elem, out)
		}
	}
}

// schemaNode walks object keys down a schema and fails the test when a segment
// is missing, so per-case assertions stay one line each.
func schemaNode(t *testing.T, node any, path ...string) any {
	t.Helper()
	cur := node
	for i, key := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("path %v: segment %q is not an object, got %#v", path, key, cur)
		}
		cur, ok = m[key]
		if !ok {
			t.Fatalf("path %v: key %q missing, got keys %v", path[:i+1], key, m)
		}
	}
	return cur
}
