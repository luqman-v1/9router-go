package clientconfig

import (
	"bytes"
	"fmt"
)

// Format is the on-disk syntax of a client configuration file.
type Format int

const (
	// FormatJSONC is JSON with comments and trailing commas, as used by
	// ~/.claude/settings.json and ~/.config/zed/settings.json.
	FormatJSONC Format = iota
	// FormatTOML is TOML 1.0, as used by ~/.codex/config.toml.
	FormatTOML
	// FormatYAML is YAML 1.2, as used by many CLI tools.
	FormatYAML
)

// String names the format for error messages.
func (f Format) String() string {
	switch f {
	case FormatJSONC:
		return "jsonc"
	case FormatTOML:
		return "toml"
	case FormatYAML:
		return "yaml"
	}
	return fmt.Sprintf("format(%d)", int(f))
}

// Edit is one key to set.
//
// KeyPath addresses the value from the document root, one map key per element.
// For TOML the last element is the key and the rest is the table path, which is
// exactly how TOML itself divides a key from its table.
//
// CreateIfMissing decides what happens when the key is absent: false reports
// ErrKeyNotFound (the dry-run behaviour), true inserts it — and for TOML, adds
// the enclosing [table] header when that does not exist either (D6). YAML has
// no sections, so the flag only governs the key there. The zero value is false,
// because a patch that silently invents structure in a file the user did not
// expect to change is the worse default.
//
// ValueJSON and Delete exist because Value alone cannot express what the client
// adapters need (docs/specs/client-adapter-miniplan.md §1.2): it is a scalar
// string, and seven of the nine clients write an object, an array, or remove a
// key. Both are still splice-only — the caller supplies already-serialized
// text, and the result goes through the same verify* pass as any other edit.
type Edit struct {
	KeyPath         []string
	Value           string
	CreateIfMissing bool

	// ValueJSON replaces the target with a container the caller has already
	// serialized in the file's own dialect: a JSON object or array for JSONC,
	// a YAML flow collection for YAML. It is validated by re-parsing before the
	// splice, and overrides Value when non-empty.
	//
	// TOML has no entry here on purpose. This package refuses inline tables as
	// an unstable structure, so a TOML table is written by editing its keys
	// with CreateIfMissing — the first edit appends the [table] header and the
	// rest land inside it. That keeps one representation per concept.
	ValueJSON string

	// Delete removes the named key from its parent. It conflicts with Value and
	// ValueJSON.
	//
	// Exactly one key goes; the parent object is left in place even when it
	// becomes empty. A client whose reset must also drop the emptied parent
	// decides that itself — the primitive has no way to know whether an empty
	// object is leftovers or a value the user wants.
	Delete bool
}

// LargeFileWarningBytes is the size above which a caller should warn the user
// that it is about to rewrite a large config file. Patch enforces no cap (D7):
// it is a pure function with no way to report a warning, and a hard limit on a
// file the user owns would be a refusal they cannot override.
//
// The cost this warns about is real and measured: Patch holds the whole file in
// memory twice over (the input and the spliced output), and each parser builds
// its own tree on top — a 256 KB TOML peaked at 32.9 MB allocated, 121x the
// file, and 1 MiB at 140 MB. That ratio belongs to the parsers, not to this
// function, but the caller deciding whether to rewrite a file is the one who
// can decline it, so the number is exported for that decision.
const LargeFileWarningBytes = 1 << 20

// Patch applies every edit to src and returns the new bytes.
//
// The function is pure — no filesystem, no clock, no environment — so the whole
// acceptance matrix runs offline against byte slices. It never writes anything;
// the caller owns backup and atomic replacement (Task 1).
//
// The invariant it upholds is the one in §3.1 of
// docs/specs/config-patch-spec.md: for each edit, exactly one byte range is
// replaced and every other byte is copied verbatim. Comments, key order,
// indentation, line endings, anchors and trailing commas are not reconstructed,
// they are simply never touched.
//
// When the result is byte-identical to src, Patch returns src together with
// ErrAlreadyPatched. That is a success: the caller must not write, back up, or
// rename anything (§6).
func Patch(src []byte, format Format, edits []Edit) ([]byte, error) {
	if len(edits) == 0 {
		return src, ErrAlreadyPatched
	}
	cur := src
	for _, e := range edits {
		if err := validateEdit(e); err != nil {
			return nil, err
		}
		var (
			next []byte
			err  error
		)
		switch format {
		case FormatJSONC:
			next, err = patchJSONC(cur, e)
		case FormatTOML:
			next, err = patchTOML(cur, e)
		case FormatYAML:
			next, err = patchYAML(cur, e)
		default:
			return nil, fmt.Errorf("%w: unknown %s", ErrUnsupportedStructure, format)
		}
		if err != nil {
			return nil, err
		}
		cur = next
	}
	if bytes.Equal(cur, src) {
		return cur, ErrAlreadyPatched
	}
	return cur, nil
}

func validateEdit(e Edit) error {
	if len(e.KeyPath) == 0 {
		return fmt.Errorf("%w: empty key path", ErrKeyNotFound)
	}
	for _, part := range e.KeyPath {
		if part == "" {
			return fmt.Errorf("%w: empty component in key path", ErrKeyNotFound)
		}
	}
	// Delete and a value are contradictory instructions. Refusing here rather
	// than letting one silently win means a caller that sets both by mistake
	// finds out, instead of losing a key it meant to write.
	if e.Delete && (e.Value != "" || e.ValueJSON != "") {
		return fmt.Errorf("%w: Delete and a value in the same edit", ErrUnsupportedStructure)
	}
	return nil
}

// splice is the only way any of the three formats changes a file.
func splice(src []byte, start, end int, repl []byte) []byte {
	out := make([]byte, 0, len(src)-(end-start)+len(repl))
	out = append(out, src[:start]...)
	out = append(out, repl...)
	out = append(out, src[end:]...)
	return out
}

// samePath compares two key paths element by element, including length.
//
// Not a join: "a.b" as one quoted key and a.b as a dotted key join to the same
// string but are different tables (§4.2.3).
func samePath(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// lineEnd returns the index just past the newline that ends the line containing
// off, or len(src) when the last line has no newline.
func lineEnd(src []byte, off int) int {
	if off < 0 {
		off = 0
	}
	for i := off; i < len(src); i++ {
		if src[i] == '\n' {
			return i + 1
		}
	}
	return len(src)
}

// lineStartOf returns the index of the first byte of the line containing off.
func lineStartOf(src []byte, off int) int {
	if off > len(src) {
		off = len(src)
	}
	for i := off - 1; i >= 0; i-- {
		if src[i] == '\n' {
			return i + 1
		}
	}
	return 0
}

// newlineFor picks the line ending new lines are written with. A file is either
// essentially LF or essentially CRLF; a mixed one gets its dominant form.
func newlineFor(src []byte) string {
	if bytes.Contains(src, []byte("\r\n")) {
		return "\r\n"
	}
	return "\n"
}

// endsWithBlankLine reports whether src already ends with an empty line, so an
// appended block does not add a second one. A blank line is two consecutive
// line terminators, so strip one and look for another.
func endsWithBlankLine(src []byte) bool {
	s := src
	if len(s) == 0 || s[len(s)-1] != '\n' {
		return false
	}
	s = s[:len(s)-1]
	if len(s) > 0 && s[len(s)-1] == '\r' {
		s = s[:len(s)-1]
	}
	return len(s) > 0 && s[len(s)-1] == '\n'
}
