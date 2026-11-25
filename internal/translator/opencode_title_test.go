package translator

import (
	json "encoding/json/v2"
	"strings"
	"testing"
)

func TestIsOpenCodeTitleRequest(t *testing.T) {
	systemTitle := "you generate a descriptive title for this conversation. reply with a title and nothing else"
	userTurn := `{"role":"user","content":"how do I reverse a linked list in go?"}`

	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "system prompt title request",
			body: `{"model":"m","messages":[{"role":"system","content":"` + systemTitle + `"},` + userTurn + `]}`,
			want: true,
		},
		{
			name: "instructions carrier",
			body: `{"model":"m","instructions":"` + systemTitle + `","input":[` + userTurn + `]}`,
			want: true,
		},
		{
			name: "opener in the user turn",
			body: `{"model":"m","messages":[{"role":"user","content":"generate a title for this conversation: reverse a linked list"}]}`,
			want: true,
		},
		{
			name: "claude-shaped system string",
			body: `{"model":"m","system":"` + systemTitle + `","messages":[{` + `"role":"user","content":[{"type":"text","text":"hi"}]}]}`,
			want: true,
		},
		{
			name: "empty tools array still counts as tool-less",
			body: `{"model":"m","tools":[],"messages":[{"role":"system","content":"` + systemTitle + `"},` + userTurn + `]}`,
			want: true,
		},

		// The cases below are why detection needs an opener and not just a
		// shape check: a short tool-less turn is a normal request and must
		// keep the agent shaping.
		{
			name: "short tool-less turn without an opener",
			body: `{"model":"m","messages":[{"role":"user","content":"what is a linked list?"}]}`,
			want: false,
		},
		{
			name: "opener but tools are declared",
			body: `{"model":"m","messages":[{"role":"system","content":"` + systemTitle + `"},` + userTurn + `],"tools":[{"type":"function","function":{"name":"Read"}}]}`,
			want: false,
		},
		{
			name: "opener but functions declared",
			body: `{"model":"m","messages":[{"role":"system","content":"` + systemTitle + `"}],"functions":[{"name":"Read"}]}`,
			want: false,
		},
		{
			name: "opener but tool_choice present",
			body: `{"model":"m","messages":[{"role":"system","content":"` + systemTitle + `"}],"tool_choice":"auto"}`,
			want: false,
		},
		{
			name: "opener but an assistant turn is present",
			body: `{"model":"m","messages":[{"role":"system","content":"` + systemTitle + `"},{"role":"user","content":"hi"},{"role":"assistant","content":"hello"}]}`,
			want: false,
		},
		{
			name: "opener but a tool turn is present",
			body: `{"model":"m","messages":[{"role":"system","content":"` + systemTitle + `"},{"role":"tool","content":"ok"}]}`,
			want: false,
		},
		{
			name: "opener but too many turns",
			body: `{"model":"m","messages":[{"role":"system","content":"` + systemTitle + `"},{"role":"user","content":"a"},{"role":"user","content":"b"},{"role":"user","content":"c"}]}`,
			want: false,
		},
		{
			name: "opener but a Responses function_call turn",
			body: `{"model":"m","input":[{"type":"message","role":"user","content":"` + systemTitle + `"},{"type":"function_call","name":"Read","arguments":"{}"}]}`,
			want: false,
		},
		{name: "empty body", body: ``, want: false},
		{name: "broken json", body: `{"model":`, want: false},
		{name: "no turns", body: `{"model":"m"}`, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsOpenCodeTitleRequest([]byte(tt.body)); got != tt.want {
				t.Errorf("IsOpenCodeTitleRequest() = %v, want %v", got, tt.want)
			}
		})
	}
}

// A shaped title request keeps the harness preamble — the gate fingerprints it
// and refuses a request without it — while declaring no tools at all.
func TestShapeOpenCodeV2TitleRequest_KeepsPromptDropsTools(t *testing.T) {
	in := `{"model":"m","messages":[{"role":"system","content":"you generate a descriptive title for this conversation"},{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"Bash"}}]}`

	out := ShapeOpenCodeV2TitleRequest([]byte(in), false)

	var request map[string]any
	if err := json.Unmarshal(out, &request); err != nil {
		t.Fatalf("shaped body is not valid JSON: %v\n%s", err, out)
	}

	messages, ok := request["messages"].([]any)
	if !ok || len(messages) == 0 {
		t.Fatalf("messages missing from shaped body: %s", out)
	}
	first, _ := messages[0].(map[string]any)
	system, _ := first["content"].(string)
	if !strings.HasPrefix(system, openCodeHarnessSystem) {
		t.Errorf("shaped system prompt must start with the harness preamble")
	}
	if !strings.Contains(system, openCodeTitleNote) {
		t.Errorf("shaped system prompt must carry the title note")
	}
	if strings.Contains(system, "# Caller tools") {
		t.Errorf("a title request must not describe caller tools")
	}

	// The 12 agent built-ins must not be installed. The caller's own `tools`
	// is left exactly as it arrived — this path never invents declarations.
	tools, _ := request["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("expected the caller's single tool to survive untouched, got %d", len(tools))
	}
	fn, _ := tools[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "Bash" {
		t.Errorf("caller tool was rewritten: %v", fn["name"])
	}
}

// The two shapers must not be interchangeable: an agent turn through the title
// shaper would lose its tool set, and a title request through the agent shaper
// is the case the gate refuses.
func TestShapeOpenCodeV2TitleRequest_DiffersFromAgentShaping(t *testing.T) {
	titleBody := `{"model":"m","messages":[{"role":"system","content":"you generate a descriptive title for this conversation"},{"role":"user","content":"hi"}]}`

	titled := ShapeOpenCodeV2TitleRequest([]byte(titleBody), false)
	agent, _ := ConcealOpenCodeV2FingerprintTools([]byte(titleBody), false, false)

	var titleReq, agentReq map[string]any
	if err := json.Unmarshal(titled, &titleReq); err != nil {
		t.Fatalf("title shaping produced invalid JSON: %v", err)
	}
	if err := json.Unmarshal(agent, &agentReq); err != nil {
		t.Fatalf("agent shaping produced invalid JSON: %v", err)
	}

	if _, has := titleReq["tools"]; has {
		t.Errorf("title shaping must not declare tools")
	}
	if tools, _ := agentReq["tools"].([]any); len(tools) != len(openCodeGenuineToolNames) {
		t.Errorf("agent shaping must still install one placeholder per built-in, got %d tools", len(tools))
	}
	// Both keep the preamble: it is the gate's body fingerprint. The agent
	// path strips its Code Mode section, the title path keeps it whole.
	agentMsgs, _ := agentReq["messages"].([]any)
	agentFirst, _ := agentMsgs[0].(map[string]any)
	system, _ := agentFirst["content"].(string)
	if !strings.HasPrefix(system, openCodeHarnessPrefix()) {
		t.Errorf("agent shaping must also keep the harness preamble")
	}
	if strings.Contains(system, "# Code Mode") {
		t.Errorf("agent shaping must strip the Code Mode section")
	}
}
