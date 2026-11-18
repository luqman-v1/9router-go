package proxy

import (
	"strings"
	"testing"
)

// Regression for the hy4-flash-free tool-call streams of 2026-11-18: the
// provider emitted the terminal chunk and `data: [DONE]` back-to-back. Without
// a blank-line separator the OpenAI SDK joins both data lines into one event
// and fails with `JSONDecodeError: Extra data: line 2 column 1`.
func TestSSECopy_DoneSentinelOwnEvent(t *testing.T) {
	terminal := `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"hy4-flash-free","choices":[{"index":0,"finish_reason":"tool_calls","delta":{"role":"assistant","content":""}}]}` + "\n"

	for name, tail := range map[string]string{
		"data_line":              "data: [DONE]\n\n",
		"bare_line":              "[DONE]\n\n",
		"unterminated_data_line": "data: [DONE]",
	} {
		t.Run(name, func(t *testing.T) {
			rec := &mockResponseWriter{}
			if err := SSECopy(rec, strings.NewReader(terminal+tail), rec, nil); err != nil {
				t.Fatalf("SSECopy failed: %v", err)
			}
			out := rec.String()
			if got := strings.Count(out, "data: [DONE]"); got != 1 {
				t.Fatalf("expected exactly one sentinel, got %d in %q", got, out)
			}
			if !strings.Contains(out, `"finish_reason":"tool_calls"`) {
				t.Fatalf("terminal chunk must survive, got %q", out)
			}
			// The chunk and the sentinel must be separate SSE events, so the
			// data line carrying the chunk ends with a blank line.
			marker := `"content":""}}]}` + "\n\n" + "data: [DONE]\n\n"
			if !strings.HasSuffix(out, marker) {
				t.Fatalf("sentinel must be a standalone event, got %q", out)
			}
		})
	}
}
