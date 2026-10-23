package clientconfig

import (
	"errors"
	"strings"
	"testing"
)

// The tests here cover the two primitives added in
// docs/specs/client-adapter-miniplan.md §1.3: ValueJSON and Delete. Both are
// splice-only, and both must leave every other byte alone — the same invariant
// the scalar path has carried since Task 2.

func TestPatchJSONCReplaceContainer(t *testing.T) {
	tests := []struct {
		name    string
		src     string
		edit    Edit
		want    string
		wantErr error
	}{
		{
			name: "object replaced by object",
			src:  `{"a": {"old": 1}, "keep": "x"}`,
			edit: Edit{KeyPath: []string{"a"}, ValueJSON: `{"new": 2}`},
			want: `{"a": {"new": 2}, "keep": "x"}`,
		},
		{
			name: "array rendered inside an object",
			src:  `{"models": []}`,
			edit: Edit{KeyPath: []string{"models"}, ValueJSON: `[{"id": "m"}]`},
			want: `{"models": [{"id": "m"}]}`,
		},
		{
			name: "scalar replaced by object",
			src:  `{"a": "x", "keep": 1}`,
			edit: Edit{KeyPath: []string{"a"}, ValueJSON: `{"b": true}`},
			want: `{"a": {"b": true}, "keep": 1}`,
		},
		{
			name: "comments and spacing outside the target survive",
			src:  "{\n  // above\n  \"a\": {\"old\": 1}, // beside\n  \"keep\": \"x\"\n}\n",
			edit: Edit{KeyPath: []string{"a"}, ValueJSON: `{"new": 2}`},
			want: "{\n  // above\n  \"a\": {\"new\": 2}, // beside\n  \"keep\": \"x\"\n}\n",
		},
		{
			name: "created when missing",
			src:  `{"keep": 1}`,
			edit: Edit{KeyPath: []string{"a"}, ValueJSON: `{"b": 2}`, CreateIfMissing: true},
			want: `{"keep": 1, "a": {"b": 2}}`,
		},
		{
			name:    "malformed ValueJSON is refused before the splice",
			src:     `{"a": 1}`,
			edit:    Edit{KeyPath: []string{"a"}, ValueJSON: `{"b": `},
			wantErr: ErrUnsupportedStructure,
		},
		{
			name:    "scalar edit on a container still refused",
			src:     `{"a": {"b": 1}}`,
			edit:    Edit{KeyPath: []string{"a"}, Value: "y"},
			wantErr: ErrUnsupportedStructure,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := Patch([]byte(tt.src), FormatJSONC, []Edit{tt.edit})
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error = %v, want %v", err, tt.wantErr)
				}
				if out != nil {
					t.Errorf("returned bytes with an error: %q", out)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(out) != tt.want {
				t.Errorf("got %q, want %q", out, tt.want)
			}
			// The invariant every splice path must hold: the result parses.
			if _, _, err := parseJSONC(out); err != nil {
				t.Errorf("output does not parse: %v", err)
			}
		})
	}
}

func TestPatchJSONCDelete(t *testing.T) {
	tests := []struct {
		name    string
		src     string
		edit    Edit
		want    string
		wantErr error
	}{
		{
			name: "middle member takes the following comma",
			src:  `{"a": 1, "b": 2, "c": 3}`,
			edit: Edit{KeyPath: []string{"b"}, Delete: true},
			want: `{"a": 1, "c": 3}`,
		},
		{
			name: "last member takes the preceding comma",
			src:  `{"a": 1, "b": 2}`,
			edit: Edit{KeyPath: []string{"b"}, Delete: true},
			want: `{"a": 1}`,
		},
		{
			name: "first member leaves a well-formed object",
			src:  `{"a": 1, "b": 2}`,
			edit: Edit{KeyPath: []string{"a"}, Delete: true},
			want: `{"b": 2}`,
		},
		{
			name: "only member leaves an empty object",
			src:  `{"a": 1}`,
			edit: Edit{KeyPath: []string{"a"}, Delete: true},
			want: `{}`,
		},
		{
			name: "container member is deleted whole",
			src:  `{"a": {"deep": 1}, "b": 2}`,
			edit: Edit{KeyPath: []string{"a"}, Delete: true},
			want: `{"b": 2}`,
		},
		{
			name: "nested path deletes only the named member",
			src:  `{"env": {"A": "1", "B": "2"}}`,
			edit: Edit{KeyPath: []string{"env", "A"}, Delete: true},
			want: `{"env": {"B": "2"}}`,
		},
		{
			// The comment belongs to the line the deleted member is on, so it goes
			// with it. Keeping a comment that describes a key which no longer
			// exists is worse than dropping it.
			name: "the deleted member's own trailing comment goes with it",
			src:  "{\n  \"a\": 1, // note\n  \"b\": 2\n}\n",
			edit: Edit{KeyPath: []string{"b"}, Delete: true},
			want: "{\n  \"a\": 1, // note\n}\n",
		},
		{
			name: "a comment on the previous line survives",
			src:  "{\n  \"a\": 1,\n  // about b\n  \"b\": 2\n}\n",
			edit: Edit{KeyPath: []string{"b"}, Delete: true},
			want: "{\n  \"a\": 1,\n  // about b\n}\n",
		},
		{
			name:    "absent key is an error, not a no-op",
			src:     `{"a": 1}`,
			edit:    Edit{KeyPath: []string{"b"}, Delete: true},
			wantErr: ErrKeyNotFound,
		},
		{
			name:    "delete plus a value is refused",
			src:     `{"a": 1}`,
			edit:    Edit{KeyPath: []string{"a"}, Delete: true, Value: "x"},
			wantErr: ErrUnsupportedStructure,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := Patch([]byte(tt.src), FormatJSONC, []Edit{tt.edit})
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(out) != tt.want {
				t.Errorf("got %q, want %q", out, tt.want)
			}
			if _, _, err := parseJSONC(out); err != nil {
				t.Errorf("output does not parse: %v", err)
			}
		})
	}
}

// TestPatchJSONCDeleteSurvivesReparse walks every member of a multi-member
// object in turn. A comma-handling rule that is right for the last member and
// wrong for the middle one passes a single case and fails here.
func TestPatchJSONCDeleteSurvivesReparse(t *testing.T) {
	src := `{"k0": 0, "k1": 1, "k2": 2, "k3": 3}`
	for i := 0; i < 4; i++ {
		key := "k" + string(rune('0'+i))
		edit := Edit{KeyPath: []string{key}, Delete: true}
		out, err := Patch([]byte(src), FormatJSONC, []Edit{edit})
		if err != nil {
			t.Fatalf("delete %s: %v", key, err)
		}
		if strings.Contains(string(out), `"`+key+`"`) {
			t.Errorf("delete %s: key still present in %q", key, out)
		}
		if _, _, err := parseJSONC(out); err != nil {
			t.Errorf("delete %s: output does not parse: %v (%q)", key, err, out)
		}
	}
}

func TestPatchTOMLDelete(t *testing.T) {
	tests := []struct {
		name string
		src  string
		edit Edit
		want string
	}{
		{
			name: "root key",
			src:  "model = \"x\"\nprovider = \"y\"\n",
			edit: Edit{KeyPath: []string{"model"}, Delete: true},
			want: "provider = \"y\"\n",
		},
		{
			name: "key inside a table",
			src:  "[model_providers.9router]\nname = \"9Router\"\nbase_url = \"http://x/v1\"\n",
			edit: Edit{KeyPath: []string{"model_providers", "9router", "name"}, Delete: true},
			want: "[model_providers.9router]\nbase_url = \"http://x/v1\"\n",
		},
		{
			name: "comments elsewhere survive",
			src:  "# top\nmodel = \"x\"\n# below\nmodel_provider = \"9router\"\n",
			edit: Edit{KeyPath: []string{"model"}, Delete: true},
			want: "# top\n# below\nmodel_provider = \"9router\"\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := Patch([]byte(tt.src), FormatTOML, []Edit{tt.edit})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(out) != tt.want {
				t.Errorf("got %q, want %q", out, tt.want)
			}
			if _, err := scanTOML(parseableTOML(out), out); err != nil {
				t.Errorf("output does not parse: %v", err)
			}
		})
	}
}

func TestPatchYAMLDelete(t *testing.T) {
	tests := []struct {
		name    string
		src     string
		edit    Edit
		want    string
		wantErr error
	}{
		{
			name: "top-level scalar",
			src:  "model: x\nprovider: y\n",
			edit: Edit{KeyPath: []string{"model"}, Delete: true},
			want: "provider: y\n",
		},
		{
			name: "nested scalar",
			src:  "providers:\n  \"9router\":\n    baseUrl: http://x/v1\n    apiKey: k\n",
			edit: Edit{KeyPath: []string{"providers", "9router", "apiKey"}, Delete: true},
			want: "providers:\n  \"9router\":\n    baseUrl: http://x/v1\n",
		},
		{
			name: "comments elsewhere survive",
			src:  "# top\nmodel: x\n# below\nprovider: y\n",
			edit: Edit{KeyPath: []string{"model"}, Delete: true},
			want: "# top\n# below\nprovider: y\n",
		},
		{
			name:    "absent key is an error",
			src:     "model: x\n",
			edit:    Edit{KeyPath: []string{"nope"}, Delete: true},
			wantErr: ErrKeyNotFound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := Patch([]byte(tt.src), FormatYAML, []Edit{tt.edit})
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(out) != tt.want {
				t.Errorf("got %q, want %q", out, tt.want)
			}
			if _, err := decodeYAML(out); err != nil {
				t.Errorf("output does not parse: %v", err)
			}
		})
	}
}

func TestPatchYAMLReplaceWithFlowCollection(t *testing.T) {
	src := "models: []\nkeep: 1\n"
	edit := Edit{KeyPath: []string{"models"}, ValueJSON: `[{id: m}]`}
	out, err := Patch([]byte(src), FormatYAML, []Edit{edit})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "models: [{id: m}]\nkeep: 1\n"
	if string(out) != want {
		t.Errorf("got %q, want %q", out, want)
	}
}

// TestPatchTOMLRefusesValueJSON pins that TOML has no container write path.
// A caller that sets it must be told, not silently given a scalar.
func TestPatchTOMLRefusesValueJSON(t *testing.T) {
	_, err := Patch([]byte("model = \"x\"\n"), FormatTOML, []Edit{{
		KeyPath: []string{"model"}, ValueJSON: "{a = 1}",
	}})
	if !errors.Is(err, ErrUnsupportedStructure) {
		t.Fatalf("error = %v, want %v", err, ErrUnsupportedStructure)
	}
}

// Nested creation replaced a refusal: writing providers["9router"] into a file
// with no providers object is omp's normal first run. One indent stop per path
// element, and the result must read back.
func TestPatchYAMLCreateNestedKey(t *testing.T) {
	out, err := Patch([]byte("a: 1\n"), FormatYAML, []Edit{
		{KeyPath: []string{"providers", "9router", "baseUrl"}, Value: "http://h/v1", CreateIfMissing: true},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Every key is quoted, at every level: one style, and a plain key containing
	// ":" or starting with a reserved indicator would change meaning unquoted.
	for _, want := range []string{"\"providers\":", "\"9router\":", "\"baseUrl\":"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	docs, err := decodeYAML(out)
	if err != nil {
		t.Fatalf("output does not parse: %v", err)
	}
	n, err := findYAMLScalar(docs, []string{"providers", "9router", "baseUrl"})
	if err != nil {
		t.Fatalf("created path does not read back: %v", err)
	}
	if n.Value != "http://h/v1" {
		t.Errorf("got %q, want %q", n.Value, "http://h/v1")
	}
}
