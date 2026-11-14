package translator

import (
	json "encoding/json/v2"
	"strings"
	"testing"
)

// TestReasoningContentPreservedOnAssistantTurns is a table-driven check that an
// assistant message carrying reasoning_content keeps the field through the
// OpenAI filter (which is what carries messages into the request path for
// OpenAI/DeepSeek upstreams). DeepSeek 400s a multi-turn request whose
// assistant turns lack reasoning_content after a thinking turn.
func TestReasoningContentPreservedOnAssistantTurns(t *testing.T) {
	cases := []struct {
		name string
		body string
		want map[int]string // message index -> expected reasoning_content
	}{
		{
			name: "multi turn keeps assistant reasoning_content",
			body: `{"messages":[
				{"role":"user","content":"hello"},
				{"role":"assistant","content":"Let me think","reasoning_content":"thinking about hello"},
				{"role":"user","content":"follow up"},
				{"role":"assistant","content":"done","reasoning_content":"reasoning for follow up"}
			]}`,
			want: map[int]string{
				1: "thinking about hello",
				3: "reasoning for follow up",
			},
		},
		{
			name: "assistant tool_call turn keeps reasoning_content",
			body: `{"messages":[
				{"role":"user","content":"hi"},
				{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{}"}}],"reasoning_content":"planning"},
				{"role":"tool","tool_call_id":"call_1","content":"file contents"},
				{"role":"assistant","content":"done","reasoning_content":"thinking done"}
			]}`,
			want: map[int]string{
				1: "planning",
				3: "thinking done",
			},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			out, err := FilterToOpenAIFormatJSON([]byte(tt.body), FilterOptions{})
			if err != nil {
				t.Fatalf("filter failed: %v", err)
			}
			var m map[string]any
			if err := json.Unmarshal(out, &m); err != nil {
				t.Fatalf("parse output: %v", err)
			}
			msgs, _ := m["messages"].([]any)
			for idx, wantRC := range tt.want {
				if idx >= len(msgs) {
					t.Fatalf("message index %d missing", idx)
				}
				msg, ok := msgs[idx].(map[string]any)
				if !ok {
					t.Fatalf("message %d not an object", idx)
				}
				got, _ := msg["reasoning_content"].(string)
				if got != wantRC {
					t.Errorf("message %d reasoning_content = %q, want %q", idx, got, wantRC)
				}
			}
		})
	}
}

// TestGeminiThoughtMapsToReasoningContent checks that a Gemini model turn with
// thought parts becomes an OpenAI assistant message carrying reasoning_content
// rather than dropping the thoughts.
func TestGeminiThoughtMapsToReasoningContent(t *testing.T) {
	cases := []struct {
		name   string
		parts  string
		wantRC string
	}{
		{
			name:   "single thought part",
			parts:  `[{"text":"I need to check the weather","thought":true}]`,
			wantRC: "I need to check the weather",
		},
		{
			name:   "multiple thought parts join with newline",
			parts:  `[{"text":"step one","thought":true},{"text":"step two","thought":true}]`,
			wantRC: "step one\nstep two",
		},
		{
			name:   "thought alongside plain text",
			parts:  `[{"text":"I am thinking","thought":true},{"text":"final answer"}]`,
			wantRC: "I am thinking",
		},
		{
			name:   "thought with empty text contributes nothing",
			parts:  `[{"text":"","thought":true}]`,
			wantRC: "",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{"contents":[{"role":"model","parts":` + tt.parts + `}]}`)
			out, err := GeminiClientToChatRequest(body, "m", false)
			if err != nil {
				t.Fatalf("convert: %v", err)
			}
			var chat map[string]any
			if err := json.Unmarshal(out, &chat); err != nil {
				t.Fatalf("parse: %v", err)
			}
			msgs, _ := chat["messages"].([]any)
			if len(msgs) == 0 {
				t.Fatal("no messages produced")
			}
			msg := msgs[0].(map[string]any)
			if role, _ := msg["role"].(string); role != "assistant" {
				t.Errorf("model turn became role %v, want assistant", role)
			}
			got, _ := msg["reasoning_content"].(string)
			if got != tt.wantRC {
				t.Errorf("reasoning_content = %q, want %q", got, tt.wantRC)
			}
			if tt.wantRC == "" && strings.Contains(string(out), "reasoning_content") {
				t.Error("reasoning_content should not be present for empty thought")
			}
		})
	}
}
