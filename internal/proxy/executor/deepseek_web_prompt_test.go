package executor

import (
	"strings"
	"testing"
)

// The prompt bound is an account-protection default, so an unconfigured
// connection gets it, a connection can only lower or disable it explicitly,
// and a negative or malformed value falls back to it.
func TestDeepSeekMaxPromptChars(t *testing.T) {
	tests := []struct {
		name     string
		connData map[string]any
		want     int
	}{
		{name: "nil connection data gets the default", connData: nil, want: deepSeekDefaultMaxPromptChars},
		{name: "empty connection data gets the default", connData: map[string]any{}, want: deepSeekDefaultMaxPromptChars},
		{name: "explicit bound", connData: map[string]any{"maxPromptChars": float64(1000)}, want: 1000},
		{name: "zero disables", connData: map[string]any{"maxPromptChars": float64(0)}, want: 0},
		{name: "negative falls back to the default", connData: map[string]any{"maxPromptChars": float64(-5)}, want: deepSeekDefaultMaxPromptChars},
		{name: "string spelling", connData: map[string]any{"maxPromptChars": "2000"}, want: 2000},
		{name: "nonsense falls back to the default", connData: map[string]any{"maxPromptChars": "lots"}, want: deepSeekDefaultMaxPromptChars},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deepSeekMaxPromptChars(tt.connData); got != tt.want {
				t.Errorf("deepSeekMaxPromptChars() = %d, want %d", got, tt.want)
			}
		})
	}
}

// The clamp keeps the recent end of the prompt, whole: a paragraph boundary
// early in the dropped head is used when there is one, and the kept tail
// never starts with half a line.
func TestDeepSeekClampPrompt(t *testing.T) {
	t.Run("under the bound is untouched", func(t *testing.T) {
		prompt := "User: one\n\nUser: two"
		if got := deepSeekClampPrompt(prompt, 100); got != prompt {
			t.Errorf("clamp = %q, want the prompt unchanged", got)
		}
	})

	t.Run("disabled leaves any prompt alone", func(t *testing.T) {
		prompt := strings.Repeat("a", 5000)
		if got := deepSeekClampPrompt(prompt, 0); got != prompt {
			t.Errorf("clamp = %d bytes, want the prompt unchanged", len(got))
		}
	})

	t.Run("over the bound keeps the tail at a paragraph boundary", func(t *testing.T) {
		paragraphs := make([]string, 50)
		for i := range paragraphs {
			paragraphs[i] = "paragraph " + itoa(i) + " " + strings.Repeat("x", 100)
		}
		prompt := strings.Join(paragraphs, "\n\n")
		got := deepSeekClampPrompt(prompt, 1000)
		if len(got) > 1000 {
			t.Fatalf("clamp kept %d bytes, want at most the bound", len(got))
		}
		// The kept text is the prompt's own tail, so the recent turns are the
		// ones that survive, and the boundary cut leaves it starting at a
		// paragraph rather than at half a line.
		if !strings.HasSuffix(prompt, got) {
			t.Fatal("clamp did not keep the prompt's tail")
		}
		if !strings.HasPrefix(got, "paragraph ") {
			t.Errorf("clamp starts mid-line: %q...", got[:min(30, len(got))])
		}
	})
}

// itoa avoids importing strconv for one test-side counter.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	digits := ""
	for i > 0 {
		digits = string(rune('0'+i%10)) + digits
		i /= 10
	}
	return digits
}
