package chat

import (
	json "encoding/json/v2"
	"testing"
)

// TestNormalizeChatTokenFields pins what the normalizer does to the body,
// including the parts the full dispatch path cannot demonstrate on its own.
func TestNormalizeChatTokenFields(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantBody string // "" means "expect the input unchanged"
	}{
		{
			name: "both fields keep max_completion_tokens",
			body: `{"model":"m","max_tokens":1024,"max_completion_tokens":2048}`,
			// Deterministic marshal orders keys; the point is max_tokens is gone.
			wantBody: `{"max_completion_tokens":2048,"model":"m"}`,
		},
		{
			name:     "only max_tokens is left alone",
			body:     `{"model":"m","max_tokens":1024}`,
			wantBody: "",
		},
		{
			name:     "only max_completion_tokens is left alone",
			body:     `{"model":"m","max_completion_tokens":2048}`,
			wantBody: "",
		},
		{
			name:     "neither field is left alone",
			body:     `{"model":"m","messages":[]}`,
			wantBody: "",
		},
		{
			name:     "a non-object body is returned unchanged",
			body:     `[1,2,3]`,
			wantBody: "",
		},
		{
			// A value above 2^53. Decoding into map[string]any would turn it
			// into a float64 and emit 9007199254740992, silently changing a
			// field the client set.
			name:     "values outside float64 precision keep their bytes",
			body:     `{"model":"m","seed":9007199254740993,"max_tokens":1,"max_completion_tokens":2}`,
			wantBody: `{"max_completion_tokens":2,"model":"m","seed":9007199254740993}`,
		},
		{
			// Both keys are written with \u escapes, so neither appears as
			// bytes on the wire while both decode to the names the parser
			// looks for. A scan matching only the literal spelling skips this
			// body and leaves both fields on the upstream — the exact bug this
			// function exists to prevent. Both keys are escaped on purpose: if
			// either were literal, the scan would find that one and the case
			// would pass without exercising the escape rule at all.
			name:     "both keys escaped are still the keys",
			body:     `{"model":"m","\u006dax_tokens":1024,"\u006dax_completion_tokens":2048}`,
			wantBody: `{"max_completion_tokens":2048,"model":"m"}`,
		},
		{
			// A body with an escape anywhere may hide an escaped key, so it
			// must never be skipped by the scan. This one carries no token
			// field at all and still has to come back byte-identical.
			name:     "an escaped value without token fields is untouched",
			body:     `{"model":"m","messages":[{"role":"user","content":"caf\u00e9"}]}`,
			wantBody: "",
		},
		{
			// JSON keys are compared exactly, so this body declares neither
			// key the normalizer looks for. The scan skips it and the parse
			// would have left it alone too — the two agree.
			name:     "a differently cased key is not the key",
			body:     `{"model":"m","MAX_TOKENS":1024,"Max_Completion_Tokens":2048}`,
			wantBody: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(normalizeChatTokenFields([]byte(tt.body)))
			want := tt.wantBody
			if want == "" {
				want = tt.body
			}
			if got != want {
				t.Errorf("normalizeChatTokenFields() = %s, want %s", got, want)
			}
			if tt.wantBody == "" {
				return
			}
			// The rewritten body must still be valid JSON.
			var probe map[string]any
			if err := json.Unmarshal([]byte(got), &probe); err != nil {
				t.Errorf("rewritten body is not valid JSON: %v", err)
			}
		})
	}
}
