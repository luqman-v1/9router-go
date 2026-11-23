package chat

import (
	"bytes"
	json "encoding/json/v2"
	"strings"
	"testing"
)

const claudeToolBody = `{"model":"claude-opus-5-5","system":"be brief","messages":[
 {"role":"user","content":"Check the repo."},
 {"role":"assistant","content":[{"type":"tool_use","id":"toolu_01","name":"Bash","input":{"command":"git status"}}]},
 {"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01","content":"NEEDLE-TOKEN"}]},
 {"role":"user","content":"Report what the command printed."}],
 "tools":[{"name":"Bash","description":"run shell","input_schema":{"type":"object","properties":{"command":{"type":"string"}}}}]}`

const openAIChatBody = `{"model":"gpt-6.2-astra","messages":[
 {"role":"user","content":"Check the repo."},
 {"role":"assistant","content":null,"tool_calls":[{"id":"call_01","type":"function","function":{"name":"Bash","arguments":"{\"command\":\"git status\"}"}}]},
 {"role":"tool","tool_call_id":"call_01","content":"NEEDLE-TOKEN"},
 {"role":"user","content":"Report what the command printed."}],
 "tools":[{"type":"function","function":{"name":"Bash","parameters":{"type":"object","properties":{"command":{"type":"string"}}}}}]}`

// A combo whose first seat speaks Claude leaves the client's body in Messages
// shape for a later non-Claude seat. Unconverted, the tool_use/tool_result
// blocks are dropped by the Responses converter, so the model never sees its
// own tool output and re-issues the call forever.
func TestClaudeBodyForNonClaudeUpstreamConvertsToolRoundTrip(t *testing.T) {
	out := claudeBodyForNonClaudeUpstream([]byte(claudeToolBody), false, "openrouter", "anthropic/claude-3.5-sonnet")

	var body struct {
		Messages []struct {
			Role       string `json:"role"`
			Content    string `json:"content"`
			ToolCallID string `json:"tool_call_id"`
			ToolCalls  []struct {
				ID       string `json:"id"`
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &body); err != nil {
		t.Fatalf("converted body does not parse as Chat Completions: %v\n%s", err, out)
	}
	var sawCall, sawResult bool
	for _, m := range body.Messages {
		for _, tc := range m.ToolCalls {
			if tc.ID == "toolu_01" && tc.Function.Name == "Bash" {
				sawCall = true
			}
		}
		if m.Role == "tool" && m.ToolCallID == "toolu_01" && m.Content == "NEEDLE-TOKEN" {
			sawResult = true
		}
	}
	if !sawCall {
		t.Errorf("assistant tool call lost: %s", out)
	}
	if !sawResult {
		t.Errorf("tool result lost — this is the loop: the model cannot see it: %s", out)
	}
}

func TestClaudeBodyForNonClaudeUpstreamLeavesOtherShapesAlone(t *testing.T) {
	cases := []struct {
		name                 string
		body                 string
		upstreamSpeaksClaude bool
	}{
		{"claude upstream keeps Messages body", claudeToolBody, true},
		{"already-chat body untouched", openAIChatBody, false},
		// A Chat body that also carries a Claude-shaped user block must not be
		// rebuilt: its system turn lives in messages and would be dropped.
		{"chat body with a stray document block untouched", `{"messages":[{"role":"system","content":"be brief"},{"role":"user","content":[{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"AA"}}]}],"tools":[{"type":"function","function":{"name":"Bash","parameters":{}}}]}`, false},
		{"responses body untouched", `{"model":"gpt-6.2-astra","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`, false},
		{"plain text Messages body untouched", `{"model":"x","messages":[{"role":"user","content":"hi"}]}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := []byte(tc.body)
			if out := claudeBodyForNonClaudeUpstream(in, tc.upstreamSpeaksClaude, "openrouter", "anthropic/claude-3.5-sonnet"); !bytes.Equal(out, in) {
				t.Errorf("body was rewritten:\n in: %s\nout: %s", in, out)
			}
		})
	}
}

// The converter defaults the flag to false; a streamed turn that reaches a
// non-Claude combo seat must stay streamed.
func TestClaudeBodyForNonClaudeUpstreamKeepsStreamFlag(t *testing.T) {
	body := []byte(strings.Replace(claudeToolBody, `"model":"claude-opus-5-5"`, `"model":"claude-opus-5-5","stream":true`, 1))
	out := claudeBodyForNonClaudeUpstream(body, false, "openrouter", "anthropic/claude-3.5-sonnet")
	var parsed struct {
		Stream *bool `json:"stream"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("converted body does not parse: %v\n%s", err, out)
	}
	if parsed.Stream == nil || !*parsed.Stream {
		t.Errorf("stream flag lost: %s", out)
	}
}

// The operator prompt (persona, prefix/suffix) is injected into the Claude
// top-level `system` before this conversion runs, and buildResponsesBody maps a
// `system` message back to the Responses `instructions` field. Guard the middle
// hop: a converted body must still carry that text in a system role.
func TestClaudeBodyForNonClaudeUpstreamKeepsOperatorSystemPrompt(t *testing.T) {
	body := []byte(strings.Replace(claudeToolBody, `"system":"be brief"`, `"system":"OPERATOR-RULES-CANARY"`, 1))
	out := claudeBodyForNonClaudeUpstream(body, false, "openrouter", "anthropic/claude-3.5-sonnet")
	var parsed struct {
		System   any `json:"system"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("converted body does not parse: %v\n%s", err, out)
	}
	for _, m := range parsed.Messages {
		if m.Role == "system" && strings.Contains(m.Content, "OPERATOR-RULES-CANARY") {
			return
		}
	}
	t.Errorf("operator prompt did not survive as a system turn: %s", out)
}

func TestBodySpeaksClaudeMessages(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"system field", `{"system":"s","messages":[]}`, true},
		{"input_schema tools", `{"tools":[{"name":"Bash","input_schema":{}}]}`, true},
		{"tool_result block", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"x"}]}]}`, true},
		{"tool_use on an assistant turn", `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"t","name":"Bash","input":{}}]}]}`, true},
		// Role-paired: content shaped this way cannot steer the conversion.
		{"tool_use on a user turn", `{"messages":[{"role":"user","content":[{"type":"tool_use","id":"t","name":"Bash","input":{}}]}]}`, false},
		{"tool_result on an assistant turn", `{"messages":[{"role":"assistant","content":[{"type":"tool_result","tool_use_id":"t","content":"x"}]}]}`, false},
		{"chat tool_calls", openAIChatBody, false},
		{"responses input", `{"input":[{"type":"message","role":"user","content":"hi"}]}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := bodySpeaksClaudeMessages([]byte(tc.body)); got != tc.want {
				t.Errorf("bodySpeaksClaudeMessages(%s) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}
