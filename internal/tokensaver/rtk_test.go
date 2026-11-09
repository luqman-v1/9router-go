package tokensaver

import (
	json "encoding/json/v2"
	"strconv"
	"strings"
	"testing"
)

func TestRTK_DeduplicateLines(t *testing.T) {
	input := "log line 1\nrepeat\nrepeat\nrepeat\nrepeat\nlog line 2"
	out, did := DeduplicateLines(input)
	if !did {
		t.Fatal("expected DeduplicateLines to report changed=true")
	}
	if !strings.Contains(out, "... (repeated 3 times)") {
		t.Fatalf("expected repetition summary, got: %s", out)
	}
	if !strings.Contains(out, "log line 1") || !strings.Contains(out, "log line 2") {
		t.Fatalf("expected non-repeated lines preserved, got: %s", out)
	}
}

func TestRTK_DetectCategory(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"git diff", "diff --git a/foo.go b/foo.go\n--- a/foo.go\n+++ b/foo.go", "git"},
		{"git status", "On branch main\nChanges not staged for commit:\n  modified: main.go", "git"},
		{"tsc error", "src/index.ts:14:5 - error TS2322: Type 'string' is not assignable to type 'number'.", "build"},
		{"go build error", "main.go:42:10: undefined: SomeService", "build"},
		{"jest test", "FAIL src/app.test.ts\n  ● Test suite\nTests: 1 failed, 4 passed", "test"},
		{"pytest", "=== FAILURES ===\n____ test_login ____\nFAILED test_app.py::test_login", "test"},
		{"pip requirement", "Requirement already satisfied: requests in /usr/lib/python", "package"},
		{"npm warning", "npm WARN deprecated package\nadded 15 packages in 2s", "package"},
		{"docker ps", "CONTAINER ID   IMAGE          COMMAND    CREATED\n123abc456def   nginx:latest   \"nginx\"   2 hours ago", "docker"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectCategory(tt.input)
			if got != tt.expected {
				t.Errorf("DetectCategory(%s) = %q; want %q", tt.name, got, tt.expected)
			}
		})
	}
}

func TestRTK_CompressTextDetailed(t *testing.T) {
	cfg := DefaultRTKConfig()
	input := "On branch dev\n  (use \"git add <file>...\" to include in what will be committed)\n  modified: internal/db/settings.go\n" + strings.Repeat("repeated log line\n", 10)
	res := CompressTextDetailed(input, cfg)
	if res.DetectedCategory != "git" {
		t.Errorf("expected category 'git', got: %s", res.DetectedCategory)
	}
	if res.OriginalTokens <= 0 || res.CompressedTokens <= 0 {
		t.Errorf("expected non-zero token counts: orig=%d, comp=%d", res.OriginalTokens, res.CompressedTokens)
	}
	if res.SavedTokens <= 0 {
		t.Errorf("expected saved tokens > 0, got %d", res.SavedTokens)
	}
	if strings.Contains(res.Text, "use \"git add") {
		t.Errorf("expected git advice stripped from output")
	}
}

func TestRTK_CompressMessagesWithConfig(t *testing.T) {
	cfg := DefaultRTKConfig()
	input := strings.Repeat("some build log line with lots of information\n", 50)
	req := map[string]any{
		"messages": []any{
			map[string]any{
				"role":    "tool",
				"content": input,
			},
		},
	}
	body, _ := json.Marshal(req)
	out, changed := CompressMessagesWithConfig(body, cfg)
	if !changed {
		t.Fatal("expected CompressMessagesWithConfig to compress large input")
	}
	if len(out) >= len(body) {
		t.Fatalf("expected compressed body (%d) to be smaller than original (%d)", len(out), len(body))
	}
}

func TestRTK_NoCharTruncationUnder10MB(t *testing.T) {
	cfg := DefaultRTKConfig()
	// 15,000 chars - was truncated at 8000 chars previously
	var b strings.Builder
	for i := 0; i < 300; i++ {
		b.WriteString("unique_log_line_with_content_identifying_step_")
		b.WriteString(string(rune('a' + (i % 26))))
		b.WriteString(string(rune(10)))
	}
	text := b.String()
	if len(text) <= 8000 {
		t.Fatalf("test precondition failed: text length %d <= 8000", len(text))
	}
	res := CompressTextDetailed(text, cfg)
	if strings.Contains(res.Text, "... (output truncated at") {
		t.Errorf("output was aggressively char-truncated under 10MB cap")
	}
}

func TestRTK_NoSmartTruncationUnder250Lines(t *testing.T) {
	cfg := DefaultRTKConfig()
	// 150 lines: below 250 lines SmartTruncMin, must not drop middle lines
	lines := make([]string, 150)
	for i := range lines {
		lines[i] = "alpha beta gamma delta plain line " + strconv.Itoa(i)
	}
	text := strings.Join(lines, string(rune(10)))
	res := CompressTextDetailed(text, cfg)
	if strings.Contains(res.Text, "lines truncated") {
		t.Errorf("150-line output was truncated, should require >= 250 lines: techs=%v", res.TechniquesUsed)
	}
}
