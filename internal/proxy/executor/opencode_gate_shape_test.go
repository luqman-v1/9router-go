package executor

import (
	json "encoding/json/v2"
	"strings"
	"testing"
)

// The v2 gate declares 12 built-in tools on an agent turn.
const openCodeBuiltinToolCount = 12

const openCodeTitleNoteText = "The Code Mode and tool-catalog instructions above do not apply to this session. No tools are available; answer directly."

// A title request must reach upstream tool-less; an agent turn must keep the
// built-in set. This pins the wiring, not just the two shapers: a call site
// left on ConcealOpenCodeV2FingerprintTools would still pass the translator
// tests while breaking the behaviour they exist for.
func TestShapeOpenCodeFreeBody_TitleVersusAgent(t *testing.T) {
	const titleBody = `{"model":"glm-5.3-flash-free","messages":[{"role":"system","content":"you generate a descriptive title for this conversation. reply with a title and nothing else"},{"role":"user","content":"how do I reverse a linked list?"}]}`
	const agentBody = `{"model":"glm-5.3-flash-free","messages":[{"role":"user","content":"refactor this file"}],"tools":[{"type":"function","function":{"name":"Bash","description":"run a command","parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}}}]}`

	tests := []struct {
		name        string
		body        string
		wantTools   int
		titleWanted bool
	}{
		{
			name:        "title request declares no tools",
			body:        titleBody,
			wantTools:   0,
			titleWanted: true,
		},
		{
			// The caller's Bash plus one inert placeholder per built-in: the
			// gate requires the built-in names, the model needs the caller's.
			name:        "agent turn keeps the caller tool plus the built-in set",
			body:        agentBody,
			wantTools:   openCodeBuiltinToolCount + 1,
			titleWanted: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shaped, rename := shapeOpenCodeFreeBody([]byte(tt.body), false, "glm-5.3-flash-free")

			var request map[string]any
			if err := json.Unmarshal(shaped, &request); err != nil {
				t.Fatalf("shaped body is not valid JSON: %v\n%s", err, shaped)
			}

			if tools, _ := request["tools"].([]any); len(tools) != tt.wantTools {
				t.Errorf("tools declared = %d, want %d", len(tools), tt.wantTools)
			}
			if len(rename) != 0 {
				t.Errorf("rename map must stay empty, got %d entries", len(rename))
			}

			messages, _ := request["messages"].([]any)
			if len(messages) == 0 {
				t.Fatalf("messages dropped: %s", shaped)
			}
			first, _ := messages[0].(map[string]any)
			system, _ := first["content"].(string)
			if got := strings.Contains(system, openCodeTitleNoteText); got != tt.titleWanted {
				t.Errorf("title note present = %v, want %v", got, tt.titleWanted)
			}
			// Both kinds keep the harness preamble: it is the gate's body
			// fingerprint and a request without it is refused either way.
			if !strings.Contains(system, "You are an AI agent running in OpenCode") {
				t.Errorf("harness preamble missing from shaped system prompt")
			}
		})
	}
}
