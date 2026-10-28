package clientconfig

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// J1–J9: the JSONC half of §7.1. Each case is one file shape and one claim
// about the patch, so a failure names the rule that broke.

func TestPatchJSONCValues(t *testing.T) {
	tests := []struct {
		name   string
		src    string
		edit   Edit
		want   string
		checks func(t *testing.T, out string)
	}{
		{
			name: "J1 url value is not cut at //",
			src:  `{"base_url": "https://api.example.com/v1"}`,
			edit: Edit{KeyPath: []string{"base_url"}, Value: "http://localhost:20128/v1"},
			want: `{"base_url": "http://localhost:20128/v1"}`,
			checks: func(t *testing.T, out string) {
				if got := strings.Count(out, "https://"); got != 0 {
					t.Errorf("old value still present %d times", got)
				}
				if got := strings.Count(out, "//"); got != 1 {
					t.Errorf("count of // = %d, want 1 (the one in the new URL)", got)
				}
			},
		},
		{
			name: "J2 comments before and after survive",
			src: "{\n  // endpoint của gateway\n  \"base_url\": \"https://old/v1\", // giữ nguyên\n" +
				"  \"other\": \"x\"\n}\n",
			edit: Edit{KeyPath: []string{"base_url"}, Value: "https://new/v1"},
			want: "{\n  // endpoint của gateway\n  \"base_url\": \"https://new/v1\", // giữ nguyên\n" +
				"  \"other\": \"x\"\n}\n",
			checks: func(t *testing.T, out string) {
				if !strings.Contains(out, "// endpoint của gateway") || !strings.Contains(out, "// giữ nguyên") {
					t.Error("a comment was lost")
				}
			},
		},
		{
			name: "J3 trailing comma is preserved",
			src:  `{"a": 1, ` + "\n" + `"b": "old",}` + "\n",
			edit: Edit{KeyPath: []string{"b"}, Value: "new"},
			want: `{"a": 1, ` + "\n" + `"b": "new",}` + "\n",
			checks: func(t *testing.T, out string) {
				if !strings.Contains(out, ",}") {
					t.Error("trailing comma was removed")
				}
			},
		},
		{
			name: "J4 block comment containing a quote is not a string",
			src:  "{/* a \" inside a comment */\"k\": \"old\"}",
			edit: Edit{KeyPath: []string{"k"}, Value: "new"},
			want: "{/* a \" inside a comment */\"k\": \"new\"}",
			checks: func(t *testing.T, out string) {
				if !strings.Contains(out, `/* a " inside a comment */`) {
					t.Error("block comment was altered")
				}
			},
		},
		{
			name: "J5 backslash outside the splice is untouched",
			src:  `{"path": "C:\\Program Files\\x", "model": "old"}`,
			edit: Edit{KeyPath: []string{"model"}, Value: "new"},
			want: `{"path": "C:\\Program Files\\x", "model": "new"}`,
			checks: func(t *testing.T, out string) {
				if !strings.Contains(out, `"C:\\Program Files\\x"`) {
					t.Error("an unrelated escaped string was rewritten")
				}
			},
		},
		{
			name: "J6 // inside a string is data, not a comment",
			src:  `{"url": "https://example.com/a//b", "k": "old"}`,
			edit: Edit{KeyPath: []string{"k"}, Value: "new"},
			want: `{"url": "https://example.com/a//b", "k": "new"}`,
			checks: func(t *testing.T, out string) {
				if !strings.Contains(out, `"https://example.com/a//b"`) {
					t.Error("scanner treated // inside a string as a comment")
				}
			},
		},
		{
			name: "nested object key",
			src:  "{\"env\": {\"ANTHROPIC_BASE_URL\": \"https://old\"}}",
			edit: Edit{KeyPath: []string{"env", "ANTHROPIC_BASE_URL"}, Value: "https://new"},
			want: "{\"env\": {\"ANTHROPIC_BASE_URL\": \"https://new\"}}",
		},
		{
			name: "create a missing key",
			src:  "{\"a\": 1}",
			edit: Edit{KeyPath: []string{"base_url"}, Value: "https://new", CreateIfMissing: true},
			want: `{"a": 1, "base_url": "https://new"}`,
		},
		{
			name: "create a key in an empty object",
			src:  "{}",
			edit: Edit{KeyPath: []string{"k"}, Value: "v", CreateIfMissing: true},
			want: `{"k": "v"}`,
		},
		{
			// The existing trailing comma becomes the separator, so the result
			// is valid and no second comma is invented.
			name: "create after a trailing comma",
			src:  `{"a": 1,}`,
			edit: Edit{KeyPath: []string{"k"}, Value: "v", CreateIfMissing: true},
			want: `{"a": 1, "k": "v"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := Patch([]byte(tt.src), FormatJSONC, []Edit{tt.edit})
			if err != nil {
				t.Fatalf("Patch() error = %v", err)
			}
			if string(out) != tt.want {
				t.Fatalf("Patch() =\n%q\nwant\n%q", out, tt.want)
			}
			if tt.checks != nil {
				tt.checks(t, string(out))
			}
		})
	}
}

func TestPatchJSONCErrors(t *testing.T) {
	tests := []struct {
		name string
		src  string
		edit Edit
		want error
	}{
		{"J7 unterminated string", `{"a": "x`, Edit{KeyPath: []string{"a"}, Value: "y"}, ErrParseFailure},
		{"J8 unterminated block comment", `{/* x`, Edit{KeyPath: []string{"a"}, Value: "y"}, ErrParseFailure},
		{"lone slash", `{"a": /}`, Edit{KeyPath: []string{"a"}, Value: "y"}, ErrParseFailure},
		{"missing key", `{"a": "x"}`, Edit{KeyPath: []string{"b"}, Value: "y"}, ErrKeyNotFound},
		{"missing key, create off", `{"a": "x"}`, Edit{KeyPath: []string{"a", "b"}, Value: "y"}, ErrKeyNotFound},
		{"target is an object", `{"a": {"b": 1}}`, Edit{KeyPath: []string{"a"}, Value: "y"}, ErrUnsupportedStructure},
		{"duplicate key", `{"a": 1, "a": 2}`, Edit{KeyPath: []string{"a"}, Value: "y"}, ErrParseFailure},
		{"trailing garbage", `{"a": 1} x`, Edit{KeyPath: []string{"a"}, Value: "y"}, ErrParseFailure},
		{"empty document", ``, Edit{KeyPath: []string{"a"}, Value: "y"}, ErrParseFailure},
		{"empty key path", `{"a": 1}`, Edit{Value: "y"}, ErrKeyNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := Patch([]byte(tt.src), FormatJSONC, []Edit{tt.edit})
			if !errors.Is(err, tt.want) {
				t.Fatalf("Patch() error = %v, want %v", err, tt.want)
			}
			if out != nil {
				t.Errorf("Patch() returned bytes with an error: %q", out)
			}
		})
	}
}

func TestPatchJSONCIdempotent(t *testing.T) {
	// J9: the second patch of the same value changes nothing at all.
	src := "{\n  // keep\n  \"base_url\": \"https://old/v1\" // keep\n}\n"
	edit := Edit{KeyPath: []string{"base_url"}, Value: "https://new/v1"}

	first, err := Patch([]byte(src), FormatJSONC, []Edit{edit})
	if err != nil {
		t.Fatalf("first patch: %v", err)
	}
	second, err := Patch(first, FormatJSONC, []Edit{edit})
	if !errors.Is(err, ErrAlreadyPatched) {
		t.Fatalf("second patch error = %v, want ErrAlreadyPatched", err)
	}
	if !bytes.Equal(second, first) {
		t.Fatalf("second patch changed bytes:\n%q\n%q", first, second)
	}
}

func TestPatchJSONCSpliceBoundaries(t *testing.T) {
	// J2/X1 in the strict form: everything outside [start,end) is identical to
	// the source. The offsets come from the lexer, so this checks the lexer and
	// the splicer agree rather than hard-coding a guess.
	src := "{\n  \"a\": \"https://old/v1\",\n  \"b\": \"keep\"\n}\n"
	edit := Edit{KeyPath: []string{"a"}, Value: "https://new/v1"}
	out, err := Patch([]byte(src), FormatJSONC, []Edit{edit})
	if err != nil {
		t.Fatalf("Patch() error = %v", err)
	}
	repl := jsoncQuote(edit.Value)
	start := strings.Index(src, `"https://old/v1"`)
	end := start + len(`"https://old/v1"`)
	want := src[:start] + repl + src[end:]
	if string(out) != want {
		t.Fatalf("splice mismatch:\n got %q\nwant %q", out, want)
	}
}

func TestPatchJSONCBOM(t *testing.T) {
	// X3: a BOM at byte 0 stays at byte 0 and does not shift any offset.
	src := "\xef\xbb\xbf{\"k\": \"old\"}"
	out, err := Patch([]byte(src), FormatJSONC, []Edit{{KeyPath: []string{"k"}, Value: "new"}})
	if err != nil {
		t.Fatalf("Patch() error = %v", err)
	}
	if !strings.HasPrefix(string(out), "\xef\xbb\xbf") {
		t.Errorf("BOM was dropped: %q", out)
	}
	if string(out) != "\xef\xbb\xbf{\"k\": \"new\"}" {
		t.Errorf("Patch() = %q", out)
	}
}

func TestPatchJSONCMultipleEdits(t *testing.T) {
	src := `{"a": "1", "b": "2"}`
	out, err := Patch([]byte(src), FormatJSONC, []Edit{
		{KeyPath: []string{"a"}, Value: "x"},
		{KeyPath: []string{"b"}, Value: "y"},
	})
	if err != nil {
		t.Fatalf("Patch() error = %v", err)
	}
	if string(out) != `{"a": "x", "b": "y"}` {
		t.Fatalf("Patch() = %q", out)
	}
	// Already correct: no edit changes a byte.
	if _, err := Patch(out, FormatJSONC, []Edit{
		{KeyPath: []string{"a"}, Value: "x"},
		{KeyPath: []string{"b"}, Value: "y"},
	}); !errors.Is(err, ErrAlreadyPatched) {
		t.Fatalf("re-patch error = %v, want ErrAlreadyPatched", err)
	}
}

// A truncated file is ordinary user input, not an exceptional state: every
// prefix of a valid file must produce an error and never a panic.
func TestJSONCTruncatedNeverPanics(t *testing.T) {
	src := "{\n  \"a\": \"https://x/v1\", // c\n  \"b\": [1, {\"c\": null}]\n}\n"
	for i := 0; i <= len(src); i++ {
		out, err := Patch([]byte(src[:i]), FormatJSONC, []Edit{{KeyPath: []string{"a"}, Value: "y"}})
		if err != nil && out != nil {
			t.Fatalf("prefix %d returned bytes with an error", i)
		}
	}
}
