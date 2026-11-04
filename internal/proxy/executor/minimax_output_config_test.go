package executor

import (
	"reflect"
	"testing"

	json "encoding/json/v2"
)

// TestStripMinimaxOutputConfig covers the passthrough-lane helper: output_config
// must be dropped from Claude Messages bodies headed to MiniMax, invalid input
// passes through unchanged, and the result re-serializes deterministically.
func TestStripMinimaxOutputConfig(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string // expected JSON after stripping; empty means "input unchanged"
		raw  bool   // compare raw bytes instead of parsing JSON
	}{
		{
			name: "strips output_config",
			body: `{"model":"m","messages":[{"role":"user","content":"hi"}],"output_config":{"effort":"high"}}`,
			want: `{"messages":[{"role":"user","content":"hi"}],"model":"m"}`,
		},
		{
			name: "passes through unchanged when no output_config",
			body: `{"model":"m","messages":[{"role":"user","content":"hi"}],"max_tokens":1024}`,
			want: `{"model":"m","messages":[{"role":"user","content":"hi"}],"max_tokens":1024}`,
		},
		{
			name: "passes through unchanged on invalid JSON",
			body: `not json`,
			want: `not json`,
			raw:  true,
		},
		{
			name: "strips empty output_config",
			body: `{"model":"m","output_config":{}}`,
			want: `{"model":"m"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripMinimaxOutputConfig([]byte(tt.body))
			if tt.raw {
				if string(got) != tt.want {
					t.Errorf("got = %q, want %q", got, tt.want)
				}
				return
			}
			var gotMap, wantMap map[string]any
			if err := json.Unmarshal(got, &gotMap); err != nil {
				t.Fatalf("got is not JSON: %v (%s)", err, got)
			}
			if err := json.Unmarshal([]byte(tt.want), &wantMap); err != nil {
				t.Fatalf("want is not JSON: %v", err)
			}
			if !reflect.DeepEqual(gotMap, wantMap) {
				t.Errorf("got = %v, want %v", gotMap, wantMap)
			}
			if _, exists := gotMap["output_config"]; exists {
				t.Errorf("output_config still present: %v", gotMap)
			}
		})
	}
}

// TestEnsureClaudeMessagesFor_MinimaxStripsOutputConfig verifies that
// output_config is dropped only for minimax and minimax-cn (Claude-compatible
// endpoints that reject it), while minimax-code/minimax-code-global and native
// Claude lanes keep it intact.
func TestEnsureClaudeMessagesFor_MinimaxStripsOutputConfig(t *testing.T) {
	input := `{"model":"m","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high","output_config":{"effort":"high"}}`

	// For minimax: output_config must be stripped
	out := EnsureClaudeMessagesFor([]byte(input), "minimax", "m")
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if _, exists := m["output_config"]; exists {
		t.Errorf("expected output_config stripped for minimax, but found %v", m["output_config"])
	}

	// For minimax-cn: output_config must be stripped
	outCn := EnsureClaudeMessagesFor([]byte(input), "minimax-cn", "m")
	var mCn map[string]any
	if err := json.Unmarshal(outCn, &mCn); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if _, exists := mCn["output_config"]; exists {
		t.Errorf("expected output_config stripped for minimax-cn, but found %v", mCn["output_config"])
	}

	// For standard claude/anthropic: output_config is retained
	claudeOut := EnsureClaudeMessagesFor([]byte(input), "claude", "claude-haiku-5-5")
	var claudeMap map[string]any
	if err := json.Unmarshal(claudeOut, &claudeMap); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if _, exists := claudeMap["output_config"]; !exists {
		t.Errorf("expected output_config preserved for claude, but was missing")
	}

	// For minimax-code and minimax-code-global (translated lane): output_config
	// is retained here since stripping happens inside ForwardMinimaxCode's
	// passthrough branch rather than in EnsureClaudeMessagesFor.
	for _, p := range []string{"minimax-code", "minimax-code-global"} {
		out := EnsureClaudeMessagesFor([]byte(input), p, "m")
		var m map[string]any
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		if _, exists := m["output_config"]; !exists {
			t.Errorf("expected output_config preserved for %s, but was missing", p)
		}
	}
}
