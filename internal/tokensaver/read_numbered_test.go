package tokensaver

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// Upstream has no tool-name protection at all: it selects its filter by the
// shape of the content (open-sse/rtk/autodetect.js), and a long line-numbered
// file dump — the shape Cursor's and Codex's read_file produce — is the one
// shape it truncates safely. The head and the tail are kept and the cut is
// marked, so the content stays recognisable.
//
// This port keeps a protected-tool list (PR #454), so a read or a grep result
// reached the model intact even when it was a line-numbered dump. That is the
// parity gap this file pins: the shape is truncated the way upstream truncates
// it, and every other read/grep result stays byte-for-byte intact.

// numberedDump builds a Cursor/Codex read_file result: every line is prefixed
// with its line number.
func numberedDump(lines int) string {
	parts := make([]string, 0, lines)
	for i := 1; i <= lines; i++ {
		parts = append(parts, fmt.Sprintf("%3d|content line %d with some text", i, i))
	}
	return strings.Join(parts, "\n")
}

// plainDump builds a result of the same size whose content is not
// line-numbered.
func plainDump(lines int) string {
	parts := make([]string, 0, lines)
	for i := 1; i <= lines; i++ {
		parts = append(parts, fmt.Sprintf("plain content line %d with some text", i))
	}
	return strings.Join(parts, "\n")
}

// toolBody builds an OpenAI Chat Completions body whose tool result belongs
// to a tool with the given name.
//
// The tool name is on the assistant's tool_calls, which is where the OpenAI
// wire format puts it — the protected-tool list is keyed on that name, so the
// name has to travel through the tool_call id for the test to exercise the
// real path.
func toolBody(t *testing.T, toolName string, content string) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"messages": []any{
			map[string]any{
				"role": "assistant",
				"tool_calls": []any{
					map[string]any{
						"id":   "call_1",
						"type": "function",
						"function": map[string]any{
							"name":      toolName,
							"arguments": `{"path":"/tmp/example.txt"}`,
						},
					},
				},
			},
			map[string]any{
				"role":         "tool",
				"tool_call_id": "call_1",
				"content":      content,
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return body
}

// toolContent extracts the tool result's content from a compressed body.
func toolContent(t *testing.T, body []byte) string {
	t.Helper()
	var decoded struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, msg := range decoded.Messages {
		if msg.Role == "tool" {
			return msg.Content
		}
	}
	t.Fatal("no tool message in the compressed body")
	return ""
}

// A protected tool's result (read, grep, edit, write, etc.) is never truncated,
// even if it is a line-numbered file dump. Truncating file reads drops critical
// code lines from the agent's context.
func TestProtectedReadResultThatIsALineNumberedDumpIsUntouched(t *testing.T) {
	content := numberedDump(300)
	out, changed := CompressMessagesWithConfig(toolBody(t, "read", content), DefaultRTKConfig())
	if changed {
		t.Fatal("a line-numbered dump under a protected tool must not be compressed")
	}
	got := toolContent(t, out)
	if got != content {
		t.Errorf("the read result was rewritten: got %q, want %q", got, content)
	}
}

// The same applies to grep: protected tool output is preserved byte-for-byte.
func TestProtectedGrepResultThatIsALineNumberedDumpIsUntouched(t *testing.T) {
	content := numberedDump(300)
	out, changed := CompressMessagesWithConfig(toolBody(t, "grep", content), DefaultRTKConfig())
	if changed {
		t.Fatal("a grep result under a protected tool must not be compressed")
	}
	got := toolContent(t, out)
	if got != content {
		t.Errorf("the grep result was rewritten: got %q, want %q", got, content)
	}
}

// A protected tool's result that is not a line-numbered dump is left exactly
// as it was. The protected list exists so the content the agent asked for
// reaches the model intact, and only the one shape upstream truncates safely
// is let through.
func TestProtectedReadResultThatIsNotALineNumberedDumpIsUntouched(t *testing.T) {
	content := plainDump(300)
	body := toolBody(t, "read", content)

	out, changed := CompressMessagesWithConfig(body, DefaultRTKConfig())
	if changed {
		t.Error("a non-line-numbered read result was compressed")
	}
	if got := toolContent(t, out); got != content {
		t.Errorf("the read result was rewritten:\ngot:  %q\nwant: %q", got, content)
	}
	if string(out) != string(body) {
		t.Errorf("the body was rewritten: %d bytes -> %d bytes", len(body), len(out))
	}
	t.Logf("bytes before=%d after=%d (unchanged)", len(content), len(toolContent(t, out)))
}

// A line-numbered dump that is shorter than upstream's threshold is left
// alone: upstream only truncates a dump of at least SMART_TRUNCATE_MIN_LINES
// (250) lines.
func TestLineNumberedDumpBelowTheLineThresholdIsUntouched(t *testing.T) {
	for _, lines := range []int{1, 100, 249} {
		t.Run(fmt.Sprintf("%d lines", lines), func(t *testing.T) {
			content := numberedDump(lines)
			body := toolBody(t, "read", content)

			out, changed := CompressMessagesWithConfig(body, DefaultRTKConfig())
			if changed {
				t.Error("a dump below the line threshold was compressed")
			}
			if got := toolContent(t, out); got != content {
				t.Error("the read result was rewritten")
			}
		})
	}
}

// A dump whose leading lines are only partly line-numbered is not the shape
// upstream's read-numbered filter is written for: upstream requires at least
// READ_NUMBERED_MIN_HIT_RATIO (0.7) of the sampled lines to look like
// "  N|content".
func TestLineNumberedDumpBelowTheHitRatioIsUntouched(t *testing.T) {
	parts := make([]string, 0, 300)
	for i := 1; i <= 300; i++ {
		if i <= 69 {
			parts = append(parts, fmt.Sprintf("%3d|content line %d with some text", i, i))
			continue
		}
		parts = append(parts, fmt.Sprintf("plain content line %d with some text", i))
	}
	content := strings.Join(parts, "\n")

	// The ratio the shape test computes on this content.
	ratio := float64(69) / float64(100)
	t.Logf("hit ratio = %.2f (threshold %.2f)", ratio, ReadNumberedMinHitRatio)
	if ratio >= ReadNumberedMinHitRatio {
		t.Fatalf("the fixture no longer exercises the ratio rule: %.2f", ratio)
	}

	body := toolBody(t, "read", content)
	out, changed := CompressMessagesWithConfig(body, DefaultRTKConfig())
	if changed {
		t.Error("a dump below the hit ratio was compressed")
	}
	if got := toolContent(t, out); got != content {
		t.Error("the read result was rewritten")
	}
}

// The generic path is not touched by this shape rule: a tool that is not on
// the protected list keeps whatever the generic path did with the content.
func TestGenericPathIsUnchangedForALineNumberedDump(t *testing.T) {
	content := numberedDump(300)
	cfg := DefaultRTKConfig()

	before := CompressTextWithCommand(content, "", cfg)
	after := CompressTextWithCommand(content, "", cfg)
	if before != after {
		t.Error("the generic path is not deterministic")
	}

	out, changed := CompressMessagesWithConfig(toolBody(t, "shell", content), cfg)
	if !changed {
		t.Fatal("the generic path no longer compresses the dump")
	}
	got := toolContent(t, out)
	if strings.Contains(got, "... +120 lines truncated (file continues)") {
		t.Error("the read-numbered marker leaked into the generic path")
	}
	t.Logf("generic path bytes before=%d after=%d", len(content), len(got))
}

// The shape test is upstream's: at least 250 lines, and at least 70% of the
// sampled leading lines look like "  N|content".
func TestLineNumberedShapeThresholds(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{
			name:    "a 300 line dump is the shape",
			content: numberedDump(300),
			want:    true,
		},
		{
			name:    "a 249 line dump is below the line threshold",
			content: numberedDump(249),
			want:    false,
		},
		{
			name:    "a 250 line dump is at the line threshold",
			content: numberedDump(250),
			want:    true,
		},
		{
			name:    "a dump without line numbers is not the shape",
			content: plainDump(300),
			want:    false,
		},
		{
			name:    "a dump with a pipe but no line number is not the shape",
			content: strings.Repeat("not a number | just a pipe\n", 300),
			want:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isLineNumberedDump(tc.content); got != tc.want {
				t.Errorf("isLineNumberedDump() = %v, want %v", got, tc.want)
			}
		})
	}
}

// The truncation keeps upstream's marker and upstream's kept-line counts.
func TestCompressReadNumberedMatchesUpstreamShape(t *testing.T) {
	content := numberedDump(400)
	got := compressReadNumbered(content)

	lines := strings.Split(got, "\n")
	if want := SmartTruncHead + 1 + SmartTruncTail; len(lines) != want {
		t.Errorf("truncated dump has %d lines, want %d", len(lines), want)
	}
	wantMarker := fmt.Sprintf("... +%d lines truncated (file continues)", 400-SmartTruncHead-SmartTruncTail)
	if lines[SmartTruncHead] != wantMarker {
		t.Errorf("marker = %q, want %q", lines[SmartTruncHead], wantMarker)
	}

	// A dump below the threshold is returned untouched.
	small := numberedDump(100)
	if got := compressReadNumbered(small); got != small {
		t.Error("a dump below the threshold was truncated")
	}
}
