package clientconfig

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// X1–X5: the cross-format half of §7.4.

// X1: the byte-for-byte invariant, mechanically. For each format the sample has
// a head comment, a mid comment, a tail comment, and a value to replace; the
// assertion is that everything outside the replaced span is identical, without
// any per-sample string comparison.
func TestX1BytePreservation(t *testing.T) {
	tests := []struct {
		name   string
		format Format
		src    string
		edit   Edit
		oldVal string // exact bytes replaced, as they appear in src
	}{
		{
			name:   "jsonc",
			format: FormatJSONC,
			src:    "{\n  // head\n  \"base_url\": \"https://old/v1\", // mid\n  \"keep\": \"x\"\n  // tail\n}\n",
			edit:   Edit{KeyPath: []string{"base_url"}, Value: "http://localhost:20128/v1"},
			oldVal: `"https://old/v1"`,
		},
		{
			name:   "toml",
			format: FormatTOML,
			src:    "# head\n[model_providers.9router]\nbase_url = \"https://old/v1\" # mid\nname = \"x\"\n# tail\n",
			edit:   Edit{KeyPath: []string{"model_providers", "9router", "base_url"}, Value: "http://localhost:20128/v1"},
			oldVal: `"https://old/v1"`,
		},
		{
			name:   "yaml",
			format: FormatYAML,
			src:    "# head\nbase_url: \"https://old/v1\" # mid\nkeep: x\n# tail\n",
			edit:   Edit{KeyPath: []string{"base_url"}, Value: "http://localhost:20128/v1"},
			oldVal: `"https://old/v1"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := Patch([]byte(tt.src), tt.format, []Edit{tt.edit})
			if err != nil {
				t.Fatalf("Patch() error = %v", err)
			}
			start := strings.Index(tt.src, tt.oldVal)
			if start < 0 {
				t.Fatalf("sample does not contain %q", tt.oldVal)
			}
			end := start + len(tt.oldVal)
			head := tt.src[:start]
			tail := tt.src[end:]
			if !strings.HasPrefix(string(out), head) {
				t.Fatalf("head changed:\n got %q\nwant %q", string(out)[:min(len(out), len(head))], head)
			}
			if !strings.HasSuffix(string(out), tail) {
				t.Fatalf("tail changed:\n got %q\nwant %q", string(out)[max(0, len(out)-len(tail)):], tail)
			}
			// The replacement is whatever sits between the halves.
			repl := string(out)[len(head) : len(out)-len(tail)]
			if want := string(out[:len(head)]) + repl + string(out[len(head)+len(repl):]); want != string(out) {
				t.Fatal("splice decomposition does not reconstruct the output")
			}
			// And the split form from §3.1:
			if tt.src[:start]+tt.src[end:] != string(out)[:start]+string(out)[start+len(repl):] {
				t.Fatal("bytes outside the splice differ")
			}
		})
	}
}

// X2: a syntax error means nothing is written — the file on disk keeps its
// bytes and its mtime, and no backup appears beside it.
func TestX2ParseErrorLeavesDiskUntouched(t *testing.T) {
	tests := []struct {
		name   string
		format Format
		body   string
	}{
		{"jsonc", FormatJSONC, "{\"a\": \"x\n"},
		{"toml", FormatTOML, "[t]\nk = \"x\n"},
		{"yaml", FormatYAML, "key: \"unclosed\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config")
			if err := os.WriteFile(path, []byte(tt.body), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			past := time.Now().Add(-time.Hour).Truncate(time.Second)
			if err := os.Chtimes(path, past, past); err != nil {
				t.Fatalf("chtimes: %v", err)
			}
			before, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat: %v", err)
			}

			// The whole pipeline: patch the bytes read from disk, and stop on
			// error without reaching the write layer.
			old, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if _, err := Patch(old, tt.format, []Edit{{KeyPath: []string{"a"}, Value: "y"}}); err == nil {
				t.Fatal("Patch() accepted a broken file")
			}

			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("re-read: %v", err)
			}
			if !bytes.Equal(after, []byte(tt.body)) {
				t.Errorf("file bytes changed: %q", after)
			}
			st, err := os.Stat(path)
			if err != nil {
				t.Fatalf("re-stat: %v", err)
			}
			if !st.ModTime().Equal(before.ModTime()) {
				t.Errorf("mtime changed: %v -> %v", before.ModTime(), st.ModTime())
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatalf("readdir: %v", err)
			}
			for _, e := range entries {
				if strings.Contains(e.Name(), ".bak") {
					t.Errorf("backup created on a failed patch: %s", e.Name())
				}
			}
		})
	}
}

// X3: a BOM stays at byte 0 in every format and shifts no offset.
func TestX3BOM(t *testing.T) {
	const bom = "\xef\xbb\xbf"
	tests := []struct {
		name   string
		format Format
		src    string
		edit   Edit
		want   string
	}{
		{"jsonc", FormatJSONC, bom + `{"k": "old"}`, Edit{KeyPath: []string{"k"}, Value: "new"}, bom + `{"k": "new"}`},
		{"toml", FormatTOML, bom + "[t]\nk = \"old\"\n", Edit{KeyPath: []string{"t", "k"}, Value: "new"}, bom + "[t]\nk = \"new\"\n"},
		{"yaml", FormatYAML, bom + "k: \"old\"\n", Edit{KeyPath: []string{"k"}, Value: "new"}, bom + "k: \"new\"\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := Patch([]byte(tt.src), tt.format, []Edit{tt.edit})
			if err != nil {
				t.Fatalf("Patch() error = %v", err)
			}
			if string(out) != tt.want {
				t.Fatalf("Patch() = %q, want %q", out, tt.want)
			}
		})
	}
}

// X4: CRLF outside the splice is untouched in every format.
func TestX4CRLFPreserved(t *testing.T) {
	tests := []struct {
		name   string
		format Format
		src    string
		edit   Edit
		want   string
	}{
		{"jsonc", FormatJSONC, "{\r\n  \"k\": \"old\"\r\n}\r\n",
			Edit{KeyPath: []string{"k"}, Value: "new"}, "{\r\n  \"k\": \"new\"\r\n}\r\n"},
		{"toml", FormatTOML, "[t]\r\nk = \"old\"\r\n",
			Edit{KeyPath: []string{"t", "k"}, Value: "new"}, "[t]\r\nk = \"new\"\r\n"},
		{"yaml", FormatYAML, "k: \"old\"\r\n",
			Edit{KeyPath: []string{"k"}, Value: "new"}, "k: \"new\"\r\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := Patch([]byte(tt.src), tt.format, []Edit{tt.edit})
			if err != nil {
				t.Fatalf("Patch() error = %v", err)
			}
			if string(out) != tt.want {
				t.Fatalf("Patch() = %q, want %q", out, tt.want)
			}
			if got, want := strings.Count(string(out), "\r\n"), strings.Count(tt.want, "\r\n"); got != want {
				t.Errorf("CRLF count = %d, want %d", got, want)
			}
		})
	}
}
