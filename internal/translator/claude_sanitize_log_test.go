package translator

import (
	"strings"
	"testing"
)

// lastSanitizeDropLog runs one sanitize with the sink installed and returns
// the emitted drop line ("" when nothing was removed).
func lastSanitizeDropLog(t *testing.T, body []byte) string {
	t.Helper()
	var got string
	sanitizeDropSink = func(line string) { got = line }
	t.Cleanup(func() { sanitizeDropSink = nil })
	SanitizeClaudePassthrough(body, false)
	return got
}

func TestSanitizeClaudePassthrough_ReportsDrops(t *testing.T) {
	body := []byte(`{"model":"m","max_tokens":16,"messages":[
		{"role":"user","content":[{"type":"text","text":"   "}],"reasoning_content":"x"},
		{"role":"assistant","content":[{"type":"text","text":"ok"}]}
	]}`)
	req := decodeBody(t, SanitizeClaudePassthrough(body, false))
	if _, ok := req["messages"].([]any); !ok {
		t.Fatal("expected messages to survive")
	}
	got := lastSanitizeDropLog(t, body)
	for _, want := range []string{"key:reasoning_content", "block:text"} {
		if !strings.Contains(got, want) {
			t.Errorf("drop log missing %q, got %q", want, got)
		}
	}
}

func TestSanitizeClaudePassthrough_CleanBodyLogsNothing(t *testing.T) {
	body := []byte(`{"model":"m","max_tokens":16,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	if got := lastSanitizeDropLog(t, body); got != "" {
		t.Errorf("clean body emitted %q", got)
	}
}
