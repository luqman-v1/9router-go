package translator

import (
	"encoding/json"
	"testing"
)

// --- sanitizeToolArgs ---

func TestSanitizeToolArgs(t *testing.T) {
	t.Run("Read tool sanitizes limit/offset/pages", func(t *testing.T) {
		args := `{"limit":3000,"offset":-10,"file_path":"test.txt","pages":"1-5"}`
		got := sanitizeToolArgs("Read", args)
		var m map[string]any
		json.Unmarshal([]byte(got), &m)
		if m["limit"] != float64(2000) {
			t.Errorf("expected limit 2000, got %v", m["limit"])
		}
		if m["offset"] != float64(0) {
			t.Errorf("expected offset 0, got %v", m["offset"])
		}
		if _, ok := m["pages"]; ok {
			t.Errorf("expected pages removed for non-PDF, got %v", m["pages"])
		}
	})

	t.Run("Read tool preserves PDF pages", func(t *testing.T) {
		args := `{"limit":150,"offset":20,"file_path":"doc.pdf","pages":"1-5"}`
		got := sanitizeToolArgs("Read", args)
		var m map[string]any
		json.Unmarshal([]byte(got), &m)
		if m["pages"] != "1-5" {
			t.Errorf("expected pages '1-5', got %v", m["pages"])
		}
	})

	t.Run("proxy_Read tool strips prefix", func(t *testing.T) {
		args := `{"limit":999}`
		got := sanitizeToolArgs("proxy_Read", args)
		var m map[string]any
		json.Unmarshal([]byte(got), &m)
		if m["limit"] != float64(999) {
			t.Errorf("expected limit 999, got %v", m["limit"])
		}
	})

	t.Run("Non-Read tool passes through", func(t *testing.T) {
		args := `{"location":"Paris"}`
		got := sanitizeToolArgs("get_weather", args)
		if got != args {
			t.Errorf("expected unchanged, got %s", got)
		}
	})

	t.Run("Invalid JSON returns raw", func(t *testing.T) {
		got := sanitizeToolArgs("Read", `not-json`)
		if got != "not-json" {
			t.Errorf("expected raw, got %s", got)
		}
	})

	t.Run("Read tool sanitize limit < 1 deletes it", func(t *testing.T) {
		args := `{"limit":0}`
		got := sanitizeToolArgs("Read", args)
		var m map[string]any
		json.Unmarshal([]byte(got), &m)
		if _, ok := m["limit"]; ok {
			t.Errorf("expected limit deleted, got %v", m["limit"])
		}
	})
}

// --- isValidPdfPagesArg ---

func TestIsValidPdfPagesArg(t *testing.T) {
	tests := []struct {
		filePath string
		pages    string
		want     bool
	}{
		{"doc.pdf", "1-5", true},
		{"doc.pdf", "1", true},
		{"doc.pdf", "", false},
		{"", "1-5", false},
		{"doc.txt", "1-5", false},
		{"doc.pdf", "abc", false},
		{"doc.PDF", "1-5", true},
	}
	for _, tt := range tests {
		got := isValidPdfPagesArg(tt.filePath, tt.pages)
		if got != tt.want {
			t.Errorf("isValidPdfPagesArg(%q, %q) = %v, want %v", tt.filePath, tt.pages, got, tt.want)
		}
	}
}
