package executor

import (
	"bytes"
	"strings"
	"testing"
)

func TestZedJSONLToSSE(t *testing.T) {
	// Zed frames its stream as one JSON object per line, with no "data:"
	// prefix and no blank-line terminator. The adapter must re-frame it so
	// the SSE-scanning bridges can read the events.
	input := strings.Join([]string{
		`{"type":"message_start","message":{"id":"msg_1"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"PONG"}}`,
		`{"type":"message_stop"}`,
		"",
	}, "\n")

	got := readAllFramed([]byte(input))
	out := string(got)

	// Every event becomes its own SSE frame.
	if n := strings.Count(out, "data: "); n != 3 {
		t.Errorf("data: frames = %d, want 3\n%s", n, out)
	}
	for _, want := range []string{
		"data: {\"type\":\"message_start\"",
		"data: {\"type\":\"content_block_delta\"",
		"data: {\"type\":\"message_stop\"",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	// Each frame is terminated by a blank line, as SSE requires.
	if n := strings.Count(out, "\n\n"); n != 3 {
		t.Errorf("frame terminators = %d, want 3\n%s", n, out)
	}
	// The payload bytes themselves are untouched.
	if !strings.Contains(out, `"text":"PONG"`) {
		t.Errorf("payload was altered:\n%s", out)
	}
}

func TestZedJSONLToSSEPassesThroughSSE(t *testing.T) {
	// A server that switches to real SSE must keep working.
	input := "data: {\"type\":\"ping\"}\n\n"
	got := readAllFramed([]byte(input))
	if !strings.HasPrefix(string(got), "data: ") {
		t.Errorf("SSE input was not passed through: %q", string(got))
	}
}

func TestZedJSONLToSSEDropsNonJSONLines(t *testing.T) {
	// A line that is neither JSON nor SSE framing would abort the JSON parse
	// of the payload, so it is dropped rather than forwarded.
	input := "garbage\n{\"type\":\"ping\"}\n"
	got := readAllFramed([]byte(input))
	if strings.Contains(string(got), "garbage") {
		t.Errorf("non-JSON line was forwarded: %q", string(got))
	}
	if !strings.Contains(string(got), "data: {\"type\":\"ping\"}") {
		t.Errorf("valid line was dropped: %q", string(got))
	}
}

func TestZedLooksLikeJSONL(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{"jsonl", "{\"type\":\"message_start\"}\n{\"type\":\"ping\"}\n", true},
		{"sse data", "data: {\"type\":\"ping\"}\n\n", false},
		{"sse event", "event: message_start\ndata: {}\n\n", false},
		{"leading blank", "\n  {\"type\":\"ping\"}\n", true},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := zedLooksLikeJSONL([]byte(tt.body)); got != tt.want {
				t.Errorf("zedLooksLikeJSONL(%q) = %v, want %v", tt.body, got, tt.want)
			}
		})
	}
}

func TestZedJSONLToSSEKeepsBlankPayloadOut(t *testing.T) {
	// A blank line is a keep-alive in SSE and carries no event.
	input := "{\"type\":\"ping\"}\n\n{\"type\":\"message_stop\"}\n"
	got := readAllFramed([]byte(input))
	if n := bytes.Count(got, []byte("data: ")); n != 2 {
		t.Errorf("data: frames = %d, want 2\n%s", n, string(got))
	}
}
