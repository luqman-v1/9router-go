package translator

import (
	json "encoding/json/v2"
	"strings"
	"testing"
)

// Upstream #4283 (aafe3002): MCP tool schemas carry annotation and validation
// keywords that are not JSON Schema and that Gemini's schema proto has no
// field for. One surviving keyword anywhere in a nested node rejects the whole
// generateContent call with `Unknown name errorMessage: Cannot find field`, so
// a tool set that works on every other provider takes the whole request down
// here.
//
// These are the bare spellings. The vendor-prefixed forms (x-errorMessage,
// x-taplo, x-intellij-html-description, …) were already covered by the
// separate `x-` rule, which is why they are not repeated.
func TestTranslateOpenAIToGemini_StripsMCPAnnotationKeywords(t *testing.T) {
	body := []byte(`{
		"model": "gemini-3.5-flash",
		"messages": [{"role": "user", "content": "validate the config"}],
		"tools": [{
			"type": "function",
			"function": {
				"name": "validate_config",
				"description": "Check a config file",
				"parameters": {
					"type": "object",
					"properties": {
						"name": {
							"type": "string",
							"markdownDescription": "The setting name",
							"doNotSuggest": false,
							"suggestSortText": "aaa"
						},
						"shape": {
							"type": "object",
							"errorMessage": "bad shape",
							"errorMessages": {"name": "must be set"},
							"minProperties": 1,
							"maxProperties": 9,
							"properties": {
								"retries": {"type": "integer", "errorMessage": "must be >= 0"}
							}
						}
					},
					"required": ["name"]
				}
			}
		}]
	}`)

	out, err := TranslateOpenAIToGemini(body)
	if err != nil {
		t.Fatalf("TranslateOpenAIToGemini: %v", err)
	}

	// What Gemini actually receives: a keyword left anywhere here becomes a 400.
	s := string(out)
	for _, kw := range []string{
		"errorMessage", "errorMessages", "markdownDescription",
		"doNotSuggest", "suggestSortText", "minProperties", "maxProperties",
	} {
		if strings.Contains(s, `"`+kw+`"`) {
			t.Errorf("keyword %q reached the Gemini payload (upstream #4283): %s", kw, s)
		}
	}

	// Stripping the annotations must not strip the schema they describe.
	var req GeminiRequest
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatalf("unmarshal gemini request: %v", err)
	}
	if len(req.Tools) != 1 || len(req.Tools[0].FunctionDeclarations) != 1 {
		t.Fatalf("tool declarations lost: %s", out)
	}
	params, err := json.Marshal(req.Tools[0].FunctionDeclarations[0].Parameters)
	if err != nil {
		t.Fatalf("marshal declaration parameters: %v", err)
	}
	schema := string(params)
	for _, want := range []string{`"name"`, `"shape"`, `"retries"`, `"required"`} {
		if !strings.Contains(schema, want) {
			t.Errorf("parameter schema lost %s after annotation stripping: %s", want, schema)
		}
	}
}

// Upstream #5573 (cdcd09cc): Google Antigravity rejects tool schemas carrying
// `~optional` with HTTP 400 INVALID_ARGUMENT: Unknown name "~optional": Cannot find field.
// cleanGeminiSchema must strip `~optional` recursively at root, nested properties, and items.
func TestTranslateOpenAIToGemini_StripsTildeOptional(t *testing.T) {
	body := []byte(`{
		"model": "gemini-3.5-flash",
		"messages": [{"role": "user", "content": "search files"}],
		"tools": [{
			"type": "function",
			"function": {
				"name": "search",
				"description": "Search codebase",
				"parameters": {
					"type": "object",
					"~optional": true,
					"properties": {
						"query": {
							"type": "string",
							"description": "Search query",
							"optional": true,
							"~optional": true
						},
						"options": {
							"type": "object",
							"~optional": true,
							"properties": {
								"tags": {
									"type": "array",
									"~optional": true,
									"items": {
										"type": "object",
										"~optional": true,
										"properties": {
											"key": {"type": "string", "~optional": false},
											"val": {"type": "string", "~optional": true}
										}
									}
								}
							}
						}
					},
					"required": ["query"]
				}
			}
		}]
	}`)

	out, err := TranslateOpenAIToGemini(body)
	if err != nil {
		t.Fatalf("TranslateOpenAIToGemini: %v", err)
	}

	s := string(out)
	if strings.Contains(s, `"~optional"`) {
		t.Fatalf("~optional reached Gemini payload (upstream #5573): %s", s)
	}
	if strings.Contains(s, `"optional"`) {
		t.Fatalf("optional reached Gemini payload: %s", s)
	}

	var req GeminiRequest
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatalf("unmarshal gemini request: %v", err)
	}
	if len(req.Tools) != 1 || len(req.Tools[0].FunctionDeclarations) != 1 {
		t.Fatalf("tool declarations lost: %s", out)
	}
	params, err := json.Marshal(req.Tools[0].FunctionDeclarations[0].Parameters)
	if err != nil {
		t.Fatalf("marshal declaration parameters: %v", err)
	}
	schema := string(params)
	for _, want := range []string{`"query"`, `"options"`, `"tags"`, `"key"`, `"val"`, `"required"`} {
		if !strings.Contains(schema, want) {
			t.Errorf("parameter schema lost %s after ~optional stripping: %s", want, schema)
		}
	}
}
