package chat

import "testing"

// A combo converts the client's body once, for its first member, so a Claude
// member can receive a Chat-shaped body. The provider name cannot say which
// shape is on the wire; this detector is what decides.
func TestBodySpeaksOpenAIChat(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "claude body is not chat",
			body: `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],
				"tools":[{"name":"Bash","input_schema":{"type":"object"}}]}`,
		},
		{
			name: "chat tools are detected",
			body: `{"model":"m","messages":[{"role":"user","content":"hi"}],
				"tools":[{"type":"function","function":{"name":"Bash","parameters":{"type":"object"}}}]}`,
			want: true,
		},
		{
			name: "tool turn is detected without tools",
			body: `{"model":"m","messages":[{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"Bash","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"ok"}]}`,
			want: true,
		},
		{
			name: "claude tool_use is not chat",
			body: `{"model":"m","messages":[{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := bodySpeaksOpenAIChat([]byte(tc.body)); got != tc.want {
				t.Errorf("bodySpeaksOpenAIChat = %v, want %v", got, tc.want)
			}
		})
	}
}
