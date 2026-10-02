package executor

import (
	json "encoding/json/v2"
	"testing"
)

// The catalog id that reaches the wire must be the base id for the extended
// and review variants. This is the client-visible half of the mapping: the
// body forwarded upstream carries the base model, never the catalog id.
func TestRewriteCodexUpstreamModel(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "extended context variant",
			body: `{"model":"gpt-6-sol[1m]","input":[],"stream":true}`,
			want: "gpt-6-sol",
		},
		{
			name: "review variant",
			body: `{"model":"gpt-5.6-sol-review","input":[],"stream":true}`,
			want: "gpt-5.6-sol",
		},
		{
			name: "auto review is forwarded verbatim",
			body: `{"model":"codex-auto-review","input":[],"stream":true}`,
			want: "codex-auto-review",
		},
		{
			name: "plain id is untouched",
			body: `{"model":"gpt-5.5","input":[],"stream":true}`,
			want: "gpt-5.5",
		},
		{
			name: "model below the extended suffix survives",
			body: `{"model":"gpt-5.6-sol[1m]","max_output_tokens":1024,"input":[]}`,
			want: "gpt-5.6-sol",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := rewriteCodexUpstreamModel([]byte(tt.body))
			var m map[string]any
			if err := json.Unmarshal(out, &m); err != nil {
				t.Fatalf("unmarshal rewritten body: %v", err)
			}
			if got, _ := m["model"].(string); got != tt.want {
				t.Errorf("model = %q, want %q (body %s)", got, tt.want, out)
			}
			if _, ok := m["max_output_tokens"]; ok && tt.name == "model below the extended suffix survives" {
				if v, _ := m["max_output_tokens"].(float64); v != 1024 {
					t.Errorf("rewrite dropped max_output_tokens: %s", out)
				}
			}
		})
	}
}

// A body the gateway does not shape must be relayed rather than replaced: an
// unparseable body, or one carrying no model field, has nothing to rewrite.
func TestRewriteCodexUpstreamModel_LeavesUnusableBodiesAlone(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "not json", body: `not json at all`},
		{name: "no model field", body: `{"input":[],"stream":true}`},
		{name: "model is not a string", body: `{"model":42,"input":[]}`},
		{name: "model is empty", body: `{"model":"","input":[]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := rewriteCodexUpstreamModel([]byte(tt.body)); string(got) != tt.body {
				t.Errorf("body was rewritten to %s, want it untouched (%s)", got, tt.body)
			}
		})
	}
}