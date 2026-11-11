package translator

import (
	"bytes"
	"strings"
	"testing"

	"9router/proxy/internal/fastjson"
)

func TestNormalizeToolCallFinish(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantPatch bool // finish_reason must become tool_calls
	}{
		{
			name:      "stop with tool call becomes tool_calls",
			body:      `{"id":"1","object":"chat.completion","model":"vllm/llama","choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"padang\"}"}}]},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
			wantPatch: true,
		},
		{
			name:      "empty finish with tool call becomes tool_calls",
			body:      `{"choices":[{"message":{"tool_calls":[{"function":{"name":"f"}}]},"finish_reason":""}]}`,
			wantPatch: true,
		},
		{
			name: "length with tool call stays length",
			body: `{"choices":[{"message":{"tool_calls":[{"function":{"name":"f"}}]},"finish_reason":"length"}]}`,
		},
		{
			name: "tool_calls finish unchanged",
			body: `{"choices":[{"message":{"tool_calls":[{"function":{"name":"f"}}]},"finish_reason":"tool_calls"}]}`,
		},
		{
			name: "stop without tool call unchanged",
			body: `{"choices":[{"message":{"content":"hi"},"finish_reason":"stop"}]}`,
		},
		{
			name: "stop with unnamed tool call unchanged",
			body: `{"choices":[{"message":{"tool_calls":[{"function":{"name":""}}]},"finish_reason":"stop"}]}`,
		},
		{
			name: "tool_calls word in content only, no real tool call",
			body: `{"choices":[{"message":{"content":"the word tool_calls here"},"finish_reason":"stop"}]}`,
		},
		{
			name: "unparseable body relayed untouched",
			body: `{"choices":[{"message":{"tool_calls":[{"function":{"name":"f"`,
		},
		{
			name: "claude messages shape untouched",
			body: `{"type":"message","content":[{"type":"text","text":"hi"}],"stop_reason":"tool_use"}`,
		},
		{
			name:      "mixed choices patch only the eligible one",
			body:      `{"choices":[{"message":{"content":"a"},"finish_reason":"stop"},{"message":{"tool_calls":[{"function":{"name":"f"}}]},"finish_reason":"stop"}]}`,
			wantPatch: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := []byte(tt.body)
			got := NormalizeToolCallFinish(in)

			if tt.wantPatch {
				if !strings.Contains(string(got), `"finish_reason":"tool_calls"`) {
					t.Fatalf("expected finish_reason=tool_calls, got:\n%s", got)
				}
				if bytes.Equal(got, in) {
					t.Fatalf("expected a patched body, got the input back unchanged")
				}
				return
			}
			// Not a patch case: the body must come back byte-identical.
			if !bytes.Equal(got, in) {
				t.Fatalf("expected byte-identical body, got:\n%s", got)
			}
		})
	}
}

// TestNormalizeToolCallFinishPreservesFields verifies the patch only touches
// the finish_reason value: model, usage, created, fingerprint and the tool
// call survive the round-trip intact, and the old "stop" is gone.
func TestNormalizeToolCallFinishPreservesFields(t *testing.T) {
	in := `{"id":"chatcmpl-9","object":"chat.completion","created":1731000000,"model":"vllm/llama","choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"padang\"}"}}]},"finish_reason":"stop"}],"usage":{"prompt_tokens":42,"completion_tokens":7,"total_tokens":49},"system_fingerprint":"fp_1"}`
	out := string(NormalizeToolCallFinish([]byte(in)))

	for _, want := range []string{
		`"model":"vllm/llama"`,
		`"created":1731000000`,
		`"id":"chatcmpl-9"`,
		`"prompt_tokens":42`,
		`"system_fingerprint":"fp_1"`,
		`"name":"get_weather"`,
		`"finish_reason":"tool_calls"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("patched body missing %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, `"finish_reason":"stop"`) {
		t.Errorf("old finish_reason stop still present:\n%s", out)
	}
	// The tool call's arguments is a JSON document stored as a JSON string:
	// the round-trip must leave it the same wire-escaped document.
	var m map[string]any
	if err := fastjson.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("patched body does not parse: %v\n%s", err, out)
	}
	choices, _ := m["choices"].([]any)
	choice, _ := choices[0].(map[string]any)
	msg, _ := choice["message"].(map[string]any)
	calls, _ := msg["tool_calls"].([]any)
	tc, _ := calls[0].(map[string]any)
	fn, _ := tc["function"].(map[string]any)
	if args, _ := fn["arguments"].(string); args != `{"city":"padang"}` {
		t.Errorf("tool call arguments mangled: %q", args)
	}
}

// TestNormalizeToolCallFinishNilAndEmpty guards the degenerate inputs.
func TestNormalizeToolCallFinishNilAndEmpty(t *testing.T) {
	if got := NormalizeToolCallFinish(nil); got != nil {
		t.Errorf("nil in, got %v", got)
	}
	if got := NormalizeToolCallFinish([]byte{}); len(got) != 0 {
		t.Errorf("empty in, got %q", got)
	}
}
