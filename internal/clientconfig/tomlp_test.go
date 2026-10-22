package clientconfig

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// T1–T12: the TOML half of §7.2.

const tomlCodex = `# head comment
[model_providers.9router]
base_url = "https://api.example.com/v1" # keep me
name = "keep"
`

func TestPatchTOMLValues(t *testing.T) {
	tests := []struct {
		name string
		src  string
		edit Edit
		want string
	}{
		{
			name: "T1 splice the value of an existing key",
			src:  tomlCodex,
			edit: Edit{KeyPath: []string{"model_providers", "9router", "base_url"}, Value: "http://localhost:20128/v1"},
			want: "# head comment\n[model_providers.9router]\n" +
				"base_url = \"http://localhost:20128/v1\" # keep me\nname = \"keep\"\n",
		},
		{
			name: "T3 add a key inside an existing section",
			src:  tomlCodex,
			edit: Edit{KeyPath: []string{"model_providers", "9router", "wire_api"}, Value: "responses", CreateIfMissing: true},
			want: "# head comment\n[model_providers.9router]\n" +
				"base_url = \"https://api.example.com/v1\" # keep me\nname = \"keep\"\n" +
				"wire_api = \"responses\"\n",
		},
		{
			name: "T4 append a new section",
			src:  "name = \"keep\"\n",
			edit: Edit{KeyPath: []string{"model_providers", "9router", "base_url"}, Value: "http://x", CreateIfMissing: true},
			want: "name = \"keep\"\n\n[model_providers.9router]\nbase_url = \"http://x\"\n",
		},
		{
			name: "root key goes above the first header",
			src:  "[a]\nk = 1\n",
			edit: Edit{KeyPath: []string{"model"}, Value: "gpt", CreateIfMissing: true},
			want: "model = \"gpt\"\n[a]\nk = 1\n",
		},
		{
			name: "quoted key is compared unquoted",
			src:  "[\"weird.section\"]\nk = \"old\"\n",
			edit: Edit{KeyPath: []string{"weird.section", "k"}, Value: "new"},
			want: "[\"weird.section\"]\nk = \"new\"\n",
		},
		{
			name: "dotted key inside a table",
			src:  "[a]\nb.c = \"old\"\n",
			edit: Edit{KeyPath: []string{"a", "b", "c"}, Value: "new"},
			want: "[a]\nb.c = \"new\"\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := Patch([]byte(tt.src), FormatTOML, []Edit{tt.edit})
			if err != nil {
				t.Fatalf("Patch() error = %v", err)
			}
			if string(out) != tt.want {
				t.Fatalf("Patch() =\n%q\nwant\n%q", out, tt.want)
			}
		})
	}
}

func TestPatchTOMLPreservesCommentsAndTail(t *testing.T) {
	// T2: head and line comments survive, and the suffix is byte-identical.
	edit := Edit{KeyPath: []string{"model_providers", "9router", "base_url"}, Value: "http://localhost:20128/v1"}
	out, err := Patch([]byte(tomlCodex), FormatTOML, []Edit{edit})
	if err != nil {
		t.Fatalf("Patch() error = %v", err)
	}
	for _, want := range []string{"# head comment", "# keep me"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("comment %q was lost", want)
		}
	}
	oldTail := `" # keep me` + "\nname = \"keep\"\n"
	if !bytes.HasSuffix(out, []byte(oldTail)) {
		t.Errorf("suffix changed: %q", out)
	}
}

func TestPatchTOMLDoesNotDuplicateSection(t *testing.T) {
	// T3 and T5: three patches of the same new key leave exactly one header and
	// go quiet after the first.
	edit := Edit{KeyPath: []string{"model_providers", "9router", "wire_api"}, Value: "responses", CreateIfMissing: true}
	src := []byte(tomlCodex)

	first, err := Patch(src, FormatTOML, []Edit{edit})
	if err != nil {
		t.Fatalf("first patch: %v", err)
	}
	if got := strings.Count(string(first), "[model_providers.9router]"); got != 1 {
		t.Fatalf("header count after first patch = %d, want 1", got)
	}
	cur := first
	for i := 0; i < 2; i++ {
		next, err := Patch(cur, FormatTOML, []Edit{edit})
		if !errors.Is(err, ErrAlreadyPatched) {
			t.Fatalf("patch %d error = %v, want ErrAlreadyPatched", i+2, err)
		}
		if !bytes.Equal(next, cur) {
			t.Fatalf("patch %d changed bytes", i+2)
		}
		cur = next
	}
	if got := strings.Count(string(cur), "[model_providers.9router]"); got != 1 {
		t.Fatalf("header count = %d, want 1", got)
	}
}

func TestPatchTOMLErrors(t *testing.T) {
	tests := []struct {
		name string
		src  string
		edit Edit
		want error
	}{
		{"T6 duplicate section", "[s]\na = 1\n[s]\nb = 2\n",
			Edit{KeyPath: []string{"s", "a"}, Value: "x"}, ErrParseFailure},
		{"T7 duplicate key", "[s]\na = 1\na = 2\n",
			Edit{KeyPath: []string{"s", "a"}, Value: "x"}, ErrParseFailure},
		{"T8 inline table", "p = { url = \"x\" }\n",
			Edit{KeyPath: []string{"p", "url"}, Value: "y"}, ErrKeyNotFound},
		{"T8b inline table value target", "[t]\np = { url = \"x\" }\n",
			Edit{KeyPath: []string{"t", "p"}, Value: "y"}, ErrUnsupportedStructure},
		{"T9 multi-line string", "[t]\nk = \"\"\"line\"\"\"\n",
			Edit{KeyPath: []string{"t", "k"}, Value: "y"}, ErrUnsupportedStructure},
		{"array value", "[t]\nk = [1, 2]\n",
			Edit{KeyPath: []string{"t", "k"}, Value: "y"}, ErrUnsupportedStructure},
		{"missing key without create", "[t]\nk = 1\n",
			Edit{KeyPath: []string{"t", "other"}, Value: "y"}, ErrKeyNotFound},
		{"missing section without create", "[t]\nk = 1\n",
			Edit{KeyPath: []string{"u", "k"}, Value: "y"}, ErrKeyNotFound},
		{"unbalanced quote", "[t]\nk = \"x\n",
			Edit{KeyPath: []string{"t", "k"}, Value: "y"}, ErrParseFailure},
		{"dotted key blocks section creation", "a.b = 1\n",
			Edit{KeyPath: []string{"a", "c"}, Value: "y", CreateIfMissing: true}, ErrUnsupportedStructure},
		{"array of tables", "[[t]]\nk = 1\n",
			Edit{KeyPath: []string{"t", "new"}, Value: "y", CreateIfMissing: true}, ErrUnsupportedStructure},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := Patch([]byte(tt.src), FormatTOML, []Edit{tt.edit})
			if !errors.Is(err, tt.want) {
				t.Fatalf("Patch() error = %v, want %v", err, tt.want)
			}
			if out != nil {
				t.Errorf("Patch() returned bytes with an error: %q", out)
			}
		})
	}
}

func TestPatchTOMLURLNotTruncated(t *testing.T) {
	// T10: the "#" in a TOML basic string is data; only the value range moves.
	src := "[t]\nurl = \"https://api.example.com/v1#frag\"\n"
	out, err := Patch([]byte(src), FormatTOML, []Edit{{KeyPath: []string{"t", "url"}, Value: "http://localhost:20128/v1"}})
	if err != nil {
		t.Fatalf("Patch() error = %v", err)
	}
	if got := strings.Count(string(out), "https://"); got != 0 {
		t.Errorf("old URL still present %d times", got)
	}
	if !strings.Contains(string(out), `url = "http://localhost:20128/v1"`) {
		t.Errorf("value was not replaced cleanly: %q", out)
	}
}

func TestPatchTOMLHeaderWithIndentAndComment(t *testing.T) {
	// T11: the header is not rewritten, and the new key lands inside it.
	src := "  [model_providers.9router]  # hdr cmt\nbase_url = \"x\"\n"
	out, err := Patch([]byte(src), FormatTOML, []Edit{{
		KeyPath: []string{"model_providers", "9router", "wire_api"}, Value: "responses", CreateIfMissing: true,
	}})
	if err != nil {
		t.Fatalf("Patch() error = %v", err)
	}
	want := "  [model_providers.9router]  # hdr cmt\nbase_url = \"x\"\nwire_api = \"responses\"\n"
	if string(out) != want {
		t.Fatalf("Patch() =\n%q\nwant\n%q", out, want)
	}
}

func TestPatchTOMLCRLF(t *testing.T) {
	// T12: inserted lines use the file's own line ending.
	src := "[t]\r\nbase_url = \"x\"\r\n"
	out, err := Patch([]byte(src), FormatTOML, []Edit{{
		KeyPath: []string{"t", "wire_api"}, Value: "responses", CreateIfMissing: true,
	}})
	if err != nil {
		t.Fatalf("Patch() error = %v", err)
	}
	want := "[t]\r\nbase_url = \"x\"\r\nwire_api = \"responses\"\r\n"
	if string(out) != want {
		t.Fatalf("Patch() = %q, want %q", out, want)
	}
}

func TestPatchTOMLNewSectionInFileWithoutTrailingNewline(t *testing.T) {
	src := "[a]\nk = 1"
	out, err := Patch([]byte(src), FormatTOML, []Edit{{
		KeyPath: []string{"b", "j"}, Value: "v", CreateIfMissing: true,
	}})
	if err != nil {
		t.Fatalf("Patch() error = %v", err)
	}
	want := "[a]\nk = 1\n\n[b]\nj = \"v\"\n"
	if string(out) != want {
		t.Fatalf("Patch() = %q, want %q", out, want)
	}
}

// The output gate is gone (see the note at the top of tomlp.go): the strict
// parser now runs on the input only, because it is quadratic and running it
// twice doubled the cost of every patch. These are the conflicts it used to
// catch, pinned here so the cheaper pre-splice check cannot silently lose one.
func TestPatchTOMLCreateConflicts(t *testing.T) {
	tests := []struct {
		name string
		src  string
		edit Edit
	}{
		{"root key where a dotted key already owns the name",
			"a.b = 1\n",
			Edit{KeyPath: []string{"a"}, Value: "v", CreateIfMissing: true}},
		{"section key where a dotted key already owns the name",
			"[s]\nb.c = 1\n",
			Edit{KeyPath: []string{"s", "b"}, Value: "v", CreateIfMissing: true}},
		{"key under a name that is already a scalar",
			"a = 1\n",
			Edit{KeyPath: []string{"a", "b"}, Value: "v", CreateIfMissing: true}},
		{"section key under a name that is already a scalar",
			"[s]\nb = 1\n",
			Edit{KeyPath: []string{"s", "b", "c"}, Value: "v", CreateIfMissing: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := Patch([]byte(tt.src), FormatTOML, []Edit{tt.edit})
			if !errors.Is(err, ErrUnsupportedStructure) {
				t.Fatalf("Patch() error = %v, want ErrUnsupportedStructure", err)
			}
			if out != nil {
				t.Errorf("Patch() returned bytes with an error: %q", out)
			}
		})
	}
}
