package translator

import (
	"strings"
	"testing"
)

// Payload shapes a Claude Code CLI sends. SanitizeClaudePassthrough must not
// drop what it does not recognize: a newer CLI's block or key is content, and
// removing it silently corrupts the turn. Each case pins one Review Focus line.
func TestClaudeCLICorpus(t *testing.T) {
	cases := []struct {
		name           string
		body           []byte
		wantSubstrings []string
		wantAbsent     []string
	}{
		{
			name: "unknown block survives",
			body: []byte(`{"model":"m","max_tokens":8,"messages":[
				{"role":"user","content":[
					{"type":"text","text":"hi"},
					{"type":"future_block","payload":{}}]}]}`),
			wantSubstrings: []string{"future_block"},
		},
		{
			name: "unknown message key survives",
			body: []byte(`{"model":"m","max_tokens":8,"messages":[
				{"role":"user","content":"hi","cache_config":{"ttl":1}}]}`),
			wantSubstrings: []string{"cache_config"},
		},
		{
			name: "tool_addition and tool_removal survive",
			body: []byte(`{"model":"m","max_tokens":8,"messages":[
				{"role":"user","content":"hi"},
				{"role":"assistant","content":[{"type":"tool_addition","tool":{"type":"tool_definition","name":"z"}}]},
				{"role":"assistant","content":[{"type":"tool_removal","name":"z"}]},
				{"role":"user","content":"go"}]}`),
			wantSubstrings: []string{"tool_addition", "tool_removal"},
		},
		{
			name: "mid-history system turn is lifted",
			body: []byte(`{"model":"m","max_tokens":8,"messages":[
				{"role":"user","content":"one"},
				{"role":"assistant","content":"two"},
				{"role":"system","content":"reminder"},
				{"role":"user","content":"three"}]}`),
			wantSubstrings: []string{`"system":"reminder"`},
			wantAbsent:     []string{`"role":"system"`},
		},
		{
			name: "foreign key is stripped",
			body: []byte(`{"model":"m","max_tokens":8,"messages":[
				{"role":"user","content":"hi","reasoning_content":"x"}]}`),
			wantAbsent: []string{"reasoning_content"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := string(SanitizeClaudePassthrough(tc.body, false))
			for _, want := range tc.wantSubstrings {
				if !strings.Contains(out, want) {
					t.Errorf("missing %q in %s", want, out)
				}
			}
			for _, absent := range tc.wantAbsent {
				if strings.Contains(out, absent) {
					t.Errorf("unexpected %q in %s", absent, out)
				}
			}
		})
	}
}

// A tool_use with a null input is not argumentless in the sense the sweep
// targets: there is no same-name call carrying arguments to compare against, so
// the block is content. Its input must also not be rewritten, because inventing
// an empty object changes what the model called.
func TestClaudeCLICorpus_ToolUseNullInputSurvives(t *testing.T) {
	body := []byte(`{"model":"m","max_tokens":8,"messages":[
		{"role":"user","content":"hi"},
		{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"z","input":null}]}]}`)

	out := SanitizeClaudePassthrough(body, false)

	msgs, _ := decodeBody(t, out)["messages"].([]any)
	if len(msgs) < 2 {
		t.Fatalf("the turn was dropped, got %d messages: %s", len(msgs), out)
	}
	blocks, _ := msgs[1].(map[string]any)["content"].([]any)
	if len(blocks) != 1 {
		t.Fatalf("the tool_use block was dropped: %s", out)
	}
	block, _ := blocks[0].(map[string]any)
	if block["type"] != "tool_use" {
		t.Errorf("block type changed: %s", out)
	}
	if block["input"] != nil {
		t.Errorf("the null input was rewritten: %s", out)
	}
}
