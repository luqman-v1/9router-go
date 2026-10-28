package proxy

import (
	"testing"
)

func TestIsZeroContentResponse(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "empty choices",
			body: `{"choices":[]}`,
			want: true,
		},
		{
			name: "blank content with finish_reason stop",
			body: `{"choices":[{"message":{"content":""},"finish_reason":"stop"}]}`,
			want: false,
		},
		{
			name: "blank content with finish_reason length",
			body: `{"choices":[{"message":{"content":""},"finish_reason":"length"}]}`,
			want: false,
		},
		{
			name: "in_progress is zero content",
			body: `{"choices":[{"message":{"content":""},"finish_reason":"in_progress"}]}`,
			want: true,
		},
		{
			name: "incomplete is zero content",
			body: `{"choices":[{"message":{"content":""},"finish_reason":"incomplete"}]}`,
			want: true,
		},
		{
			name: "no finish reason with blank content is zero content",
			body: `{"choices":[{"message":{"content":""}}]}`,
			want: true,
		},
		{
			name: "tool_calls present is not zero content",
			body: `{"choices":[{"message":{"content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"search"}}]},"finish_reason":"in_progress"}]}`,
			want: false,
		},
		{
			name: "function_call present is not zero content",
			body: `{"choices":[{"message":{"content":"","function_call":{"name":"search","arguments":"{}"}},"finish_reason":"in_progress"}]}`,
			want: false,
		},
		{
			name: "reasoning_content present is not zero content",
			body: `{"choices":[{"message":{"content":"","reasoning_content":"thought process"},"finish_reason":"in_progress"}]}`,
			want: false,
		},
		{
			name: "content text present is not zero content",
			body: `{"choices":[{"message":{"content":"Hello world"},"finish_reason":"stop"}]}`,
			want: false,
		},
		{
			name: "Anthropic content with a text block",
			body: `{"content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn"}`,
			want: false,
		},
		{
			name: "Anthropic content with a tool_use block",
			body: `{"content":[{"type":"tool_use","id":"toolu_1","name":"bash"}],"stop_reason":"in_progress"}`,
			want: false,
		},
		{
			name: "Anthropic content with a thinking block",
			body: `{"content":[{"type":"thinking","thinking":"reasoning step"}],"stop_reason":"in_progress"}`,
			want: false,
		},
		{
			name: "Anthropic empty content with stop_reason max_tokens",
			body: `{"content":[],"stop_reason":"max_tokens"}`,
			want: false,
		},
		{
			name: "Anthropic empty content with no stop_reason",
			body: `{"content":[]}`,
			want: true,
		},
		{
			name: "Anthropic empty content with stop_reason in_progress",
			body: `{"content":[],"stop_reason":"in_progress"}`,
			want: true,
		},
		{
			name: "Anthropic empty content with stop_reason incomplete",
			body: `{"content":[],"stop_reason":"incomplete"}`,
			want: true,
		},
		{
			name: "array reasoning_content counts as no reasoning (zero content)",
			body: `{"choices":[{"message":{"content":"","reasoning_content":[{"type":"text","text":"thinking"}]},"finish_reason":"in_progress"}]}`,
			want: true,
		},
		{
			name: "numeric finish_reason is not normal finish (zero content)",
			body: `{"choices":[{"message":{"content":""},"finish_reason":5}]}`,
			want: true,
		},
		{
			name: "empty object function_call counts as tool call (not zero content)",
			body: `{"choices":[{"message":{"content":"","function_call":{}},"finish_reason":"in_progress"}]}`,
			want: false,
		},
		{
			name: "message preferred over delta when both present (zero content)",
			body: `{"choices":[{"message":{"content":""},"delta":{"content":"text in delta"},"finish_reason":"in_progress"}]}`,
			want: true,
		},
		// upstream: const msg = choice.message || choice.delta;
		// `||` tests truthiness, so a present-but-falsy message must still let
		// delta through rather than being taken as the message.
		{
			name: "falsy empty-string message falls through to delta",
			body: `{"choices":[{"message":"","delta":{"content":"d"},"finish_reason":"in_progress"}]}`,
			want: false,
		},
		{
			name: "falsy false message falls through to delta",
			body: `{"choices":[{"message":false,"delta":{"content":"d"},"finish_reason":"in_progress"}]}`,
			want: false,
		},
		{
			name: "falsy zero message falls through to delta",
			body: `{"choices":[{"message":0,"delta":{"content":"d"},"finish_reason":"in_progress"}]}`,
			want: false,
		},
		{
			name: "falsy negative-zero message falls through to delta",
			body: `{"choices":[{"message":-0,"delta":{"content":"d"},"finish_reason":"in_progress"}]}`,
			want: false,
		},
		{
			name: "falsy message falls through to a delta that carries tool_calls",
			body: `{"choices":[{"message":0,"delta":{"tool_calls":[{"id":"call_1","function":{"name":"search"}}]},"finish_reason":"in_progress"}]}`,
			want: false,
		},
		{
			name: "absent message falls through to delta",
			body: `{"choices":[{"delta":{"content":"d"},"finish_reason":"in_progress"}]}`,
			want: false,
		},
		{
			name: "null message falls through to delta",
			body: `{"choices":[{"message":null,"delta":{"content":"d"},"finish_reason":"in_progress"}]}`,
			want: false,
		},
		{
			name: "falsy message with no delta is still zero content",
			body: `{"choices":[{"message":"","finish_reason":"in_progress"}]}`,
			want: true,
		},
		{
			name: "falsy message with a normal finish and no delta is not zero content",
			body: `{"choices":[{"message":false,"finish_reason":"stop"}]}`,
			want: false,
		},
		// Objects, arrays, non-empty strings and non-zero numbers are truthy in
		// JS, so upstream reads them as the message and never reaches delta.
		{
			name: "empty-array message is truthy and hides the delta (zero content)",
			body: `{"choices":[{"message":[],"delta":{"content":"d"},"finish_reason":"in_progress"}]}`,
			want: true,
		},
		{
			name: "non-empty string message is truthy and hides the delta (zero content)",
			body: `{"choices":[{"message":"0","delta":{"content":"d"},"finish_reason":"in_progress"}]}`,
			want: true,
		},
		{
			name: "true message is truthy and hides the delta (zero content)",
			body: `{"choices":[{"message":true,"delta":{"content":"d"},"finish_reason":"in_progress"}]}`,
			want: true,
		},
		{
			name: "non-zero number message is truthy and hides the delta (zero content)",
			body: `{"choices":[{"message":1,"delta":{"content":"d"},"finish_reason":"in_progress"}]}`,
			want: true,
		},
		// Numbers outside float64 range still have a truth value upstream:
		// JSON.parse("1e400") is Infinity, which is truthy, and
		// JSON.parse("1e-400") is 0, which is falsy. Deciding truthiness by
		// whether the value could be decoded would read both as absent.
		{
			name: "number overflowing to infinity is truthy and hides the delta (zero content)",
			body: `{"choices":[{"message":1e400,"delta":{"content":"d"},"finish_reason":"in_progress"}]}`,
			want: true,
		},
		{
			name: "negative number overflowing to infinity is truthy and hides the delta (zero content)",
			body: `{"choices":[{"message":-1e400,"delta":{"content":"d"},"finish_reason":"in_progress"}]}`,
			want: true,
		},
		{
			name: "number overflowing past the float64 ceiling is truthy and hides the delta (zero content)",
			body: `{"choices":[{"message":1e309,"delta":{"content":"d"},"finish_reason":"in_progress"}]}`,
			want: true,
		},
		{
			name: "number underflowing to zero falls through to delta",
			body: `{"choices":[{"message":1e-400,"delta":{"content":"d"},"finish_reason":"in_progress"}]}`,
			want: false,
		},
		{
			name: "integer too large for float64 is truthy and hides the delta (zero content)",
			body: `{"choices":[{"message":123456789012345678901234567890,"delta":{"content":"d"},"finish_reason":"in_progress"}]}`,
			want: true,
		},
		{
			name: "zero written as a decimal is falsy and falls through to delta",
			body: `{"choices":[{"message":0.0,"delta":{"content":"d"},"finish_reason":"in_progress"}]}`,
			want: false,
		},
		{
			name: "empty-object message is truthy and hides the delta (zero content)",
			body: `{"choices":[{"message":{},"delta":{"content":"d"},"finish_reason":"in_progress"}]}`,
			want: true,
		},
		{
			name: "object message with an out-of-range number is truthy and hides the delta (zero content)",
			body: `{"choices":[{"message":{"tokens":1e400},"delta":{"content":"d"},"finish_reason":"in_progress"}]}`,
			want: true,
		},
		{
			name: "object message with content and an out-of-range number carries content (not zero content)",
			body: `{"choices":[{"message":{"content":"x","t":1e400},"delta":{"content":"d"},"finish_reason":"in_progress"}]}`,
			want: false,
		},
		{
			name: "a non-JSON body",
			body: `upstream error 500`,
			want: false,
		},
		{
			name: "empty body",
			body: ``,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsZeroContentResponse([]byte(tt.body))
			if got != tt.want {
				t.Errorf("IsZeroContentResponse() = %v, want %v", got, tt.want)
			}
		})
	}
}
