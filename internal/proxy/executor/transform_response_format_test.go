package executor

import "testing"

// Issue #2896: a Chat Completions request carrying response_format reached a
// Responses-API provider without it — the Codex allowlist strips
// response_format, so a client asking for structured output silently got free
// text back. buildResponsesBody must fold it into text.format instead.
func TestBuildResponsesBody_ResponseFormatMapsToTextFormat(t *testing.T) {
	const schema = `{"type":"object","additionalProperties":false,"required":["guests"],"properties":{"guests":{"type":"integer"}}}`
	decodedSchema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"guests"},
		"properties":           map[string]any{"guests": map[string]any{"type": "integer"}},
	}

	tests := []struct {
		name string
		body string
		want map[string]any
	}{
		{
			name: "json_schema keeps name, strict and schema",
			body: `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"2 people"}],
				"response_format":{"type":"json_schema","json_schema":{"name":"reading","strict":true,"schema":` + schema + `}}}`,
			want: map[string]any{
				"type": "json_schema", "name": "reading", "strict": true, "schema": decodedSchema,
			},
		},
		{
			name: "json_schema without strict defaults to strict",
			body: `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"2 people"}],
				"response_format":{"type":"json_schema","json_schema":{"name":"reading","schema":` + schema + `}}}`,
			want: map[string]any{
				"type": "json_schema", "name": "reading", "strict": true, "schema": decodedSchema,
			},
		},
		{
			name: "json_schema with explicit strict false stays non-strict",
			body: `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"2 people"}],
				"response_format":{"type":"json_schema","json_schema":{"name":"reading","strict":false,"schema":` + schema + `}}}`,
			want: map[string]any{
				"type": "json_schema", "name": "reading", "strict": false, "schema": decodedSchema,
			},
		},
		{
			name: "json_schema without a name falls back to response",
			body: `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"2 people"}],
				"response_format":{"type":"json_schema","json_schema":{"schema":` + schema + `}}}`,
			want: map[string]any{
				"type": "json_schema", "name": "response", "strict": true, "schema": decodedSchema,
			},
		},
		{
			name: "json_object stays json_object",
			body: `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"2 people"}],
				"response_format":{"type":"json_object"}}`,
			want: map[string]any{"type": "json_object"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, _, err := buildResponsesBody([]byte(tt.body))
			if err != nil {
				t.Fatalf("buildResponsesBody: %v", err)
			}
			parsed := decodeBody(t, out)

			if _, ok := parsed["response_format"]; ok {
				t.Error("response_format must not survive into the Responses body")
			}
			text, ok := parsed["text"].(map[string]any)
			if !ok {
				t.Fatalf("text missing or not an object: %#v", parsed["text"])
			}
			got := deterministicJSON(t, mustMarshal(t, text["format"]))
			want := deterministicJSON(t, mustMarshal(t, tt.want))
			if got != want {
				t.Errorf("text.format\n got: %s\nwant: %s", got, want)
			}
		})
	}
}

// response_format only means something when it maps onto a Responses
// text.format: an absent or null field, an unknown type, a json_schema with no
// json_schema object, and a json_schema with no schema all leave the body with
// no "text" at all. A json_schema without a schema is dropped because strict
// output cannot be expressed without one, and an empty schema would be
// rejected by the API rather than honoured.
func TestBuildResponsesBody_ResponseFormatUnmappableAddsNoText(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "no response_format at all",
			body: `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name: "response_format null",
			body: `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"hi"}],"response_format":null}`,
		},
		{
			name: "unknown response_format type",
			body: `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"hi"}],"response_format":{"type":"text"}}`,
		},
		{
			name: "json_schema without the json_schema object",
			body: `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"hi"}],"response_format":{"type":"json_schema"}}`,
		},
		{
			name: "json_schema without a schema",
			body: `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"hi"}],
				"response_format":{"type":"json_schema","json_schema":{"name":"reading","strict":true}}}`,
		},
		{
			name: "json_schema with a null schema",
			body: `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"hi"}],
				"response_format":{"type":"json_schema","json_schema":{"name":"reading","schema":null}}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, _, err := buildResponsesBody([]byte(tt.body))
			if err != nil {
				t.Fatalf("buildResponsesBody: %v", err)
			}
			if parsed := decodeBody(t, out); parsed["text"] != nil {
				t.Errorf("no text field expected, got %#v", parsed["text"])
			}
		})
	}
}

// A client already speaking the Responses dialect supplies text.format itself;
// buildResponsesBody's passthrough branch must keep it verbatim rather than
// deriving anything from a (nonexistent) response_format.
func TestBuildResponsesBody_PassthroughKeepsClientTextFormat(t *testing.T) {
	body := `{"model":"gpt-5.6-sol","input":[{"role":"user","content":"2 people"}],
		"text":{"format":{"type":"json_schema","name":"reading","strict":false,"schema":{"type":"object"}}}}`

	out, _, err := buildResponsesBody([]byte(body))
	if err != nil {
		t.Fatalf("buildResponsesBody: %v", err)
	}
	parsed := decodeBody(t, out)

	text, ok := parsed["text"].(map[string]any)
	if !ok {
		t.Fatalf("text missing or not an object: %#v", parsed["text"])
	}
	format, ok := text["format"].(map[string]any)
	if !ok {
		t.Fatalf("text.format missing or not an object: %#v", text["format"])
	}
	if format["name"] != "reading" {
		t.Errorf("client name must survive, got %#v", format["name"])
	}
	if format["strict"] != false {
		t.Errorf("client strict:false must survive, got %#v", format["strict"])
	}
	if got := deterministicJSON(t, mustMarshal(t, format["schema"])); got != `{"type":"object"}` {
		t.Errorf("client schema must survive verbatim, got %s", got)
	}
}
