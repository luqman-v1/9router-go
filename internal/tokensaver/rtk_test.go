package tokensaver

import (
	json "encoding/json/v2"
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
