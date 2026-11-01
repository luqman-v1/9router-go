package translator

import (
	json "encoding/json/v2"
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

	t.Run("Search tool normalizes q and search_query to query", func(t *testing.T) {
		args := `{"q":"makanan favorit prabowo"}`
		got := sanitizeToolArgs("WebSearch", args)
		var m map[string]any
		json.Unmarshal([]byte(got), &m)
		if m["query"] != "makanan favorit prabowo" {
			t.Errorf("expected query 'makanan favorit prabowo', got %v", m["query"])
		}
		if m["q"] != "makanan favorit prabowo" {
			t.Errorf("expected q 'makanan favorit prabowo', got %v", m["q"])
		}
	})

	t.Run("Search tool unwraps query array", func(t *testing.T) {
		args := `{"query":["makanan favorit prabowo"]}`
		got := sanitizeToolArgs("search_web", args)
		var m map[string]any
		json.Unmarshal([]byte(got), &m)
		if m["query"] != "makanan favorit prabowo" {
			t.Errorf("expected unwrapped query string, got %v", m["query"])
		}
	})

	t.Run("Bash tool normalizes cmd to command", func(t *testing.T) {
		args := `{"cmd":"ls -la"}`
		got := sanitizeToolArgs("Bash", args)
		var m map[string]any
		json.Unmarshal([]byte(got), &m)
		if m["command"] != "ls -la" {
			t.Errorf("expected command 'ls -la', got %v", m["command"])
		}
	})

	t.Run("Search tool unwraps queries array with objects", func(t *testing.T) {
		args := `{"queries":[{"q":"makanan favorit prabowo"}]}`
		got := sanitizeToolArgs("WebSearch", args)
		var m map[string]any
		json.Unmarshal([]byte(got), &m)
		if m["query"] != "makanan favorit prabowo" {
			t.Errorf("expected query extracted from queries object, got %v", m["query"])
		}
	})

	t.Run("Search tool unwraps input parameter wrapper", func(t *testing.T) {
		args := `{"input":{"search_query":"makanan favorit prabowo"}}`
		got := sanitizeToolArgs("web_search", args)
		var m map[string]any
		json.Unmarshal([]byte(got), &m)
		if m["query"] != "makanan favorit prabowo" {
			t.Errorf("expected query extracted from input wrapper, got %v", m["query"])
		}
	})

	t.Run("Read tool normalizes path aliases to file_path", func(t *testing.T) {
		args := `{"path":"/tmp/code.go","thought":"reading file"}`
		got := sanitizeToolArgs("Read", args)
		var m map[string]any
		json.Unmarshal([]byte(got), &m)
		if m["file_path"] != "/tmp/code.go" {
			t.Errorf("expected file_path '/tmp/code.go', got %v", m["file_path"])
		}
		if _, ok := m["path"]; ok {
			t.Errorf("expected path to be removed, got %v", m["path"])
		}
		if _, ok := m["thought"]; ok {
			t.Errorf("expected thought to be stripped, got %v", m["thought"])
		}
	})

	t.Run("Write tool normalizes path and contents aliases", func(t *testing.T) {
		args := `{"filePath":"/tmp/main.go","contents":"package main","description":"saving file"}`
		got := sanitizeToolArgs("Write", args)
		var m map[string]any
		json.Unmarshal([]byte(got), &m)
		if m["file_path"] != "/tmp/main.go" {
			t.Errorf("expected file_path '/tmp/main.go', got %v", m["file_path"])
		}
		if m["content"] != "package main" {
			t.Errorf("expected content 'package main', got %v", m["content"])
		}
		if _, ok := m["description"]; ok {
			t.Errorf("expected description to be stripped, got %v", m["description"])
		}
	})

	t.Run("Edit tool normalizes old/new string aliases and replaceAll", func(t *testing.T) {
		args := `{"path":"/tmp/test.go","old_text":"foo","new_text":"bar","replaceAll":true,"explanation":"replace"}`
		got := sanitizeToolArgs("Edit", args)
		var m map[string]any
		json.Unmarshal([]byte(got), &m)
		if m["file_path"] != "/tmp/test.go" {
			t.Errorf("expected file_path '/tmp/test.go', got %v", m["file_path"])
		}
		if m["old_string"] != "foo" {
			t.Errorf("expected old_string 'foo', got %v", m["old_string"])
		}
		if m["new_string"] != "bar" {
			t.Errorf("expected new_string 'bar', got %v", m["new_string"])
		}
		if m["replace_all"] != true {
			t.Errorf("expected replace_all true, got %v", m["replace_all"])
		}
		if _, ok := m["explanation"]; ok {
			t.Errorf("expected explanation to be stripped, got %v", m["explanation"])
		}
	})

	t.Run("Grep/Glob tool normalizes query and dir aliases and strips n", func(t *testing.T) {
		args := `{"query":"func Test","dir":"/workspace","n":true}`
		got := sanitizeToolArgs("Grep", args)
		var m map[string]any
		json.Unmarshal([]byte(got), &m)
		if m["pattern"] != "func Test" {
			t.Errorf("expected pattern 'func Test', got %v", m["pattern"])
		}
		if m["path"] != "/workspace" {
			t.Errorf("expected path '/workspace', got %v", m["path"])
		}
		if _, ok := m["n"]; ok {
			t.Errorf("expected n to be stripped, got %v", m["n"])
		}
	})

	t.Run("Monitor tool strips dummy ws when command is present", func(t *testing.T) {
		args := `{"command":"python3 script.py","ws":{"protocols":[],"url":"wss://localhost"}}`
		got := sanitizeToolArgs("Monitor", args)
		var m map[string]any
		json.Unmarshal([]byte(got), &m)
		if m["command"] != "python3 script.py" {
			t.Errorf("expected command preserved, got %v", m["command"])
		}
		if _, ok := m["ws"]; ok {
			t.Errorf("expected ws stripped to prevent 'exactly one of command or ws' validation error, got %v", m["ws"])
		}
	})

	t.Run("Skill tool normalizes name alias to skill", func(t *testing.T) {
		args := `{"name":"impeccable","args":"init"}`
		got := sanitizeToolArgs("Skill", args)
		var m map[string]any
		json.Unmarshal([]byte(got), &m)
		if m["skill"] != "impeccable" {
			t.Errorf("expected skill 'impeccable', got %v", m["skill"])
		}
		if _, ok := m["name"]; ok {
			t.Errorf("expected name alias removed, got %v", m["name"])
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
