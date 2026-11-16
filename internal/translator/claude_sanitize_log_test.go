package translator

import (
	"strings"
	"testing"
)

// sanitizeDropLines runs one sanitize with the sink installed and returns every
// line it emitted (none when nothing was removed).
func sanitizeDropLines(t *testing.T, body []byte, intentionalPrefill bool) []string {
	t.Helper()
	var lines []string
	sanitizeDropSink = func(line string) { lines = append(lines, line) }
	t.Cleanup(func() { sanitizeDropSink = nil })
	SanitizeClaudePassthrough(body, intentionalPrefill)
	return lines
}

// lastSanitizeDropLog runs one sanitize with the sink installed and returns
// the emitted drop line ("" when nothing was removed).
func lastSanitizeDropLog(t *testing.T, body []byte) string {
	t.Helper()
	lines := sanitizeDropLines(t, body, false)
	if len(lines) == 0 {
		return ""
	}
	return lines[len(lines)-1]
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
	if lines := sanitizeDropLines(t, body, false); len(lines) != 0 {
		t.Errorf("clean body emitted %d lines: %q", len(lines), lines)
	}
}

// A pure trailing-whitespace trim on an intentional prefill is a repair: the
// body is re-encoded, but nothing was removed, so nothing is logged.
func TestSanitizeClaudePassthrough_TrimOnlyLogsNothing(t *testing.T) {
	body := []byte(`{"model":"m","max_tokens":16,"messages":[
		{"role":"user","content":[{"type":"text","text":"hi"}]},
		{"role":"assistant","content":[{"type":"text","text":"ok   "}]}
	]}`)
	if lines := sanitizeDropLines(t, body, true); len(lines) != 0 {
		t.Errorf("trim-only body emitted %d lines: %q", len(lines), lines)
	}
}

func TestSanitizeClaudePassthrough_ReportsCallerDrop(t *testing.T) {
	body := []byte(`{"model":"m","max_tokens":16,"messages":[
		{"role":"user","content":[{"type":"text","text":"hi","caller":"agent"}]}
	]}`)
	if got := lastSanitizeDropLog(t, body); !strings.Contains(got, "key:caller") {
		t.Errorf("drop log missing %q, got %q", "key:caller", got)
	}
}

// A blank text block removed from a turn that still has content is a block
// removal, not a dropped turn.
func TestSanitizeClaudePassthrough_BlankBlockInKeptTurn(t *testing.T) {
	body := []byte(`{"model":"m","max_tokens":16,"messages":[
		{"role":"user","content":[{"type":"text","text":"hi"},{"type":"text","text":"   "}]}
	]}`)
	got := lastSanitizeDropLog(t, body)
	if !strings.Contains(got, "block:text") {
		t.Errorf("drop log missing %q, got %q", "block:text", got)
	}
	if strings.Contains(got, "block:message") {
		t.Errorf("kept turn mislabelled as dropped: %q", got)
	}
}
