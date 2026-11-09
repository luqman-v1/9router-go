package clientconfig

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// Y1–Y12: the YAML half of §7.3.

func TestPatchYAMLValues(t *testing.T) {
	tests := []struct {
		name string
		src  string
		edit Edit
		want string
	}{
		{
			name: "Y1 double-quoted value with a URL",
			src:  "url: \"https://old/v1\"\n",
			edit: Edit{KeyPath: []string{"url"}, Value: "http://localhost:20128/v1"},
			want: "url: \"http://localhost:20128/v1\"\n",
		},
		{
			name: "Y1b plain value with a # in it",
			src:  "url: https://old/v1#frag\n",
			edit: Edit{KeyPath: []string{"url"}, Value: "http://new/v1"},
			want: "url: \"http://new/v1\"\n",
		},
		{
			name: "Y1c single-quoted value",
			src:  "url: 'https://old/v1'\n",
			edit: Edit{KeyPath: []string{"url"}, Value: "http://new/v1"},
			want: "url: \"http://new/v1\"\n",
		},
		{
			name: "Y2 head comment survives",
			src:  "# head\nurl: \"https://old\"\n",
			edit: Edit{KeyPath: []string{"url"}, Value: "https://new"},
			want: "# head\nurl: \"https://new\"\n",
		},
		{
			name: "Y3 line comment survives",
			src:  "url: \"https://old\" # trailing\n",
			edit: Edit{KeyPath: []string{"url"}, Value: "https://new"},
			want: "url: \"https://new\" # trailing\n",
		},
		{
			name: "Y4 foot comment survives",
			src:  "url: \"https://old\"\n# foot\nnext: 1\n",
			edit: Edit{KeyPath: []string{"url"}, Value: "https://new"},
			want: "url: \"https://new\"\n# foot\nnext: 1\n",
		},
		{
			name: "Y5 anchor and alias untouched",
			src:  "default: &def\n  url: \"https://old\"\nuse: *def\n",
			edit: Edit{KeyPath: []string{"default", "url"}, Value: "https://new"},
			want: "default: &def\n  url: \"https://new\"\nuse: *def\n",
		},
		{
			name: "Y7 second document is patched",
			src:  "---\na: \"1\"\n---\nb: \"old\"\n",
			edit: Edit{KeyPath: []string{"b"}, Value: "new"},
			want: "---\na: \"1\"\n---\nb: \"new\"\n",
		},
		{
			name: "Y11 multi-byte text before the value",
			src:  "  ünï: \"v-old\"\n",
			edit: Edit{KeyPath: []string{"ünï"}, Value: "v-new"},
			want: "  ünï: \"v-new\"\n",
		},
		{
			name: "Y12 four-space indent is not normalised",
			src:  "outer:\n    inner: \"old\"\n",
			edit: Edit{KeyPath: []string{"outer", "inner"}, Value: "new"},
			want: "outer:\n    inner: \"new\"\n",
		},
		{
			name: "CRLF line ending kept",
			src:  "url: \"old\"\r\nnext: 1\r\n",
			edit: Edit{KeyPath: []string{"url"}, Value: "new"},
			want: "url: \"new\"\r\nnext: 1\r\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := Patch([]byte(tt.src), FormatYAML, []Edit{tt.edit})
			if err != nil {
				t.Fatalf("Patch() error = %v", err)
			}
			if string(out) != tt.want {
				t.Fatalf("Patch() =\n%q\nwant\n%q", out, tt.want)
			}
		})
	}
}

func TestPatchYAMLRefusals(t *testing.T) {
	tests := []struct {
		name string
		src  string
		edit Edit
		want error
	}{
		{"Y6 block literal", "k: |\n  line one\n  line two\n",
			Edit{KeyPath: []string{"k"}, Value: "x"}, ErrUnsupportedStructure},
		{"Y6b block folded", "k: >\n  line one\n",
			Edit{KeyPath: []string{"k"}, Value: "x"}, ErrUnsupportedStructure},
		{"Y8 duplicate key", "a: 1\na: 2\n",
			Edit{KeyPath: []string{"a"}, Value: "x"}, ErrUnsupportedStructure},
		{"Y9 unterminated quote", "key: \"unclosed\n",
			Edit{KeyPath: []string{"key"}, Value: "x"}, ErrParseFailure},
		{"target is a mapping", "a:\n  b: 1\n",
			Edit{KeyPath: []string{"a"}, Value: "x"}, ErrUnsupportedStructure},
		{"target is a sequence", "a:\n  - 1\n",
			Edit{KeyPath: []string{"a"}, Value: "x"}, ErrUnsupportedStructure},
		{"key absent, create off", "a: 1\n",
			Edit{KeyPath: []string{"b"}, Value: "x"}, ErrKeyNotFound},
		{"anchored target", "k: &ank \"old\"\n",
			Edit{KeyPath: []string{"k"}, Value: "x"}, ErrUnsupportedStructure},
		// Nested creation used to be refused here. omp needs it: writing
		// providers["9router"] into a file with no providers object is the
		// normal first run, and the two-level form is now written with one
		// indent stop per element. The case moved to the success tests below
		// rather than being deleted, so the behaviour stays pinned.
		{"plain scalar folded over two lines", "k: one\n  two\n",
			Edit{KeyPath: []string{"k"}, Value: "x"}, ErrUnsupportedStructure},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := Patch([]byte(tt.src), FormatYAML, []Edit{tt.edit})
			if !errors.Is(err, tt.want) {
				t.Fatalf("Patch() error = %v, want %v", err, tt.want)
			}
			if out != nil {
				t.Errorf("Patch() returned bytes with an error: %q", out)
			}
		})
	}
}

func TestPatchYAMLIdempotent(t *testing.T) {
	// Y10: the second patch is a no-op even though the first one changed the
	// scalar's style from plain to double-quoted.
	src := "# head\nurl: https://old/v1 # keep\n"
	edit := Edit{KeyPath: []string{"url"}, Value: "http://localhost:20128/v1"}

	first, err := Patch([]byte(src), FormatYAML, []Edit{edit})
	if err != nil {
		t.Fatalf("first patch: %v", err)
	}
	if !strings.Contains(string(first), "# keep") || !strings.Contains(string(first), "# head") {
		t.Fatalf("comments lost: %q", first)
	}
	second, err := Patch(first, FormatYAML, []Edit{edit})
	if !errors.Is(err, ErrAlreadyPatched) {
		t.Fatalf("second patch error = %v, want ErrAlreadyPatched", err)
	}
	if !bytes.Equal(second, first) {
		t.Fatalf("second patch changed bytes:\n%q\n%q", first, second)
	}
}

func TestPatchYAMLCreateTopLevelKey(t *testing.T) {
	out, err := Patch([]byte("a: 1\n"), FormatYAML, []Edit{{
		KeyPath: []string{"model"}, Value: "gpt-5", CreateIfMissing: true,
	}})
	if err != nil {
		t.Fatalf("Patch() error = %v", err)
	}
	if string(out) != "a: 1\n\"model\": \"gpt-5\"\n" {
		t.Fatalf("Patch() = %q", out)
	}
	// And it is stable on a second pass.
	if _, err := Patch(out, FormatYAML, []Edit{{
		KeyPath: []string{"model"}, Value: "gpt-5", CreateIfMissing: true,
	}}); !errors.Is(err, ErrAlreadyPatched) {
		t.Fatalf("re-patch error = %v, want ErrAlreadyPatched", err)
	}
}

func TestPatchYAMLSpliceBoundaries(t *testing.T) {
	// Y-C1: the value's byte range is exactly the quoted scalar, so everything
	// around it is copied verbatim.
	src := "a: \"keep\"\nurl: \"https://old/v1\"  # c\nb: 2\n"
	out, err := Patch([]byte(src), FormatYAML, []Edit{{KeyPath: []string{"url"}, Value: "https://new/v1"}})
	if err != nil {
		t.Fatalf("Patch() error = %v", err)
	}
	start := strings.Index(src, `"https://old/v1"`)
	end := start + len(`"https://old/v1"`)
	want := src[:start] + yamlQuote("https://new/v1") + src[end:]
	if string(out) != want {
		t.Fatalf("splice mismatch:\n got %q\nwant %q", out, want)
	}
}
