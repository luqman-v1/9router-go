package tokensaver

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
)

func TestTokensaverSystemPromptsAreDeterministic(t *testing.T) {
	tests := []struct {
		name string
		call func() ([]byte, bool)
	}{
		{
			name: "chat system prompt injection",
			call: func() ([]byte, bool) {
				body := []byte(`{
					"model": "gpt-4o",
					"messages": [
						{"role": "system", "content": "You are helpful"},
						{"role": "user", "content": "hello"}
					],
					"temperature": 0.7,
					"max_tokens": 100
				}`)
				return InjectSystemPrompt(body, "Respond tersely.")
			},
		},
		{
			name: "responses system prompt injection",
			call: func() ([]byte, bool) {
				body := []byte(`{
					"model": "gpt-5.3-codex",
					"input": [
						{"type": "message", "role": "system", "content": [{"type": "input_text", "text": "base instructions"}]},
						{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "ping"}]}
					],
					"store": false
				}`)
				return InjectSystemPrompt(body, "Respond tersely.")
			},
		},
		{
			name: "claude system prompt injection",
			call: func() ([]byte, bool) {
				body := []byte(`{
					"model": "claude-sonnet-4-5",
					"system": [{"type": "text", "text": "base"}],
					"messages": [{"role": "user", "content": "hello"}],
					"max_tokens": 1024
				}`)
				return InjectSystemPromptClaude(body, "Respond tersely.")
			},
		},
		{
			name: "rtk tool message compression",
			call: func() ([]byte, bool) {
				content := strings.Repeat(" 1\tfunc example() {\n 2\t\tline := 1\n 3\t\treturn\n 4\t}\n", 30)
				body := []byte(`{
					"model": "gpt-4o",
					"messages": [
						{"role": "user", "content": "view file"},
						{"role": "tool", "tool_call_id": "call_1", "content": ` + strconv.Quote(content) + `}
					],
					"temperature": 0.5,
					"top_p": 0.9
				}`)
				return CompressMessagesWithConfig(body, DefaultRTKConfig())
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first, did := tt.call()
			if !did {
				t.Fatalf("expected modification, got did=false")
			}
			for i := 0; i < 50; i++ {
				got, did := tt.call()
				if !did {
					t.Fatalf("call %d: expected modification, got did=false", i)
				}
				if !bytes.Equal(first, got) {
					t.Fatalf("call %d output differed from first:\nfirst: %s\ngot:   %s", i, first, got)
				}
			}
		})
	}
}
