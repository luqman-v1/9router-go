package tokensaver

import (
	"fmt"
	"strings"
	"testing"
)

// Upstream has ONE threshold constant and TWO filters that compare against
// it — open-sse/rtk/filters/smartTruncate.js and
// open-sse/rtk/filters/readNumbered.js — both with a `<`:
//
//	if (lines.length < SMART_TRUNCATE_MIN_LINES) return input;
//
// So a payload of exactly 250 lines IS truncated, and a shorter one is not.
// This port keeps one definition of the number (SmartTruncMin, aliased as
// ReadNumberedMinLines) and one comparison per path, so the boundary is
// pinned from both sides: 250 truncates, 249 does not.

// lineCounted builds a payload of exactly n lines, with no trailing newline,
// so that strings.Split yields exactly n elements.
func lineCounted(n int) string {
	parts := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		parts = append(parts, fmt.Sprintf("line %d of the payload", i))
	}
	return strings.Join(parts, "\n")
}

// The smartTruncate boundary is upstream's: a payload of exactly
// SMART_TRUNCATE_MIN_LINES (250) lines is truncated, a shorter one is
// returned untouched.
func TestSmartTruncateBoundaryMatchesUpstream(t *testing.T) {
	tests := []struct {
		name        string
		lines       int
		wantTruncat bool
	}{
		{name: "249 lines is below the threshold", lines: 249, wantTruncat: false},
		{name: "250 lines is at the threshold", lines: 250, wantTruncat: true},
		{name: "251 lines is above the threshold", lines: 251, wantTruncat: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := lineCounted(tc.lines)
			if got := len(strings.Split(in, "\n")); got != tc.lines {
				t.Fatalf("the fixture has %d lines, want %d", got, tc.lines)
			}

			out := smartTruncate(in)
			if !tc.wantTruncat {
				if out != in {
					t.Error("a payload below the threshold was truncated")
				}
				return
			}
			if out == in {
				t.Fatal("a payload at the threshold was not truncated")
			}
			lines := strings.Split(out, "\n")
			if want := SmartTruncHead + 1 + SmartTruncTail; len(lines) != want {
				t.Errorf("truncated payload has %d lines, want %d", len(lines), want)
			}
			wantMarker := fmt.Sprintf("... (%d lines truncated)", tc.lines-SmartTruncHead-SmartTruncTail)
			if lines[SmartTruncHead] != wantMarker {
				t.Errorf("marker = %q, want %q", lines[SmartTruncHead], wantMarker)
			}
		})
	}
}

// The read-numbered boundary is the same constant and the same comparison:
// a dump of exactly 250 lines IS truncated, a shorter one is not.
func TestCompressReadNumberedBoundaryMatchesUpstream(t *testing.T) {
	tests := []struct {
		name        string
		lines       int
		wantTruncat bool
	}{
		{name: "249 lines is below the threshold", lines: 249, wantTruncat: false},
		{name: "250 lines is at the threshold", lines: 250, wantTruncat: true},
		{name: "251 lines is above the threshold", lines: 251, wantTruncat: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := numberedDump(tc.lines)
			out := compressReadNumbered(in)
			if !tc.wantTruncat {
				if out != in {
					t.Error("a dump below the threshold was truncated")
				}
				return
			}
			if out == in {
				t.Fatal("a dump at the threshold was not truncated")
			}
			lines := strings.Split(out, "\n")
			if want := SmartTruncHead + 1 + SmartTruncTail; len(lines) != want {
				t.Errorf("truncated dump has %d lines, want %d", len(lines), want)
			}
			wantMarker := fmt.Sprintf("... +%d lines truncated (file continues)", tc.lines-SmartTruncHead-SmartTruncTail)
			if lines[SmartTruncHead] != wantMarker {
				t.Errorf("marker = %q, want %q", lines[SmartTruncHead], wantMarker)
			}
		})
	}
}

// A protected tool (read, grep, edit, write, etc.) is never truncated regardless of line count.
func TestProtectedReadResultNeverTruncatedRegardlessOfLines(t *testing.T) {
	for _, lines := range []int{249, 250, 500} {
		t.Run(fmt.Sprintf("%d lines", lines), func(t *testing.T) {
			content := numberedDump(lines)
			out, changed := CompressMessagesWithConfig(toolBody(t, "read", content), DefaultRTKConfig())
			if changed {
				t.Fatalf("changed = true, want false: protected tool must never be truncated")
			}
			got := toolContent(t, out)
			if got != content {
				t.Error("content was altered under protected tool")
			}
		})
	}
}
