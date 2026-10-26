package clientconfig

import (
	"fmt"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

// TOML is patched with two parsers on purpose. The unstable parser is the only
// one that reports byte ranges, but it does not validate semantics — it accepts
// a file with a duplicate table or a duplicate key, which would let a patch
// create a second [table] header. toml.Unmarshal catches those, and is therefore
// run as a gate on the input, once per Patch call.
//
// It runs on the input only, not on the output as well: toml.Unmarshal is
// quadratic in the number of keys a table holds (its SeenTracker.find walks the
// siblings of every key), so the gate is the most expensive thing Patch does and
// doubling it doubled the whole cost — 512 KB of TOML went from 1.16 s to 2.32 s
// and X5's 64 KB budget became a coin flip. The output is verified by the
// unstable parser plus a read-back of the edited key (`verifyTOML`), which is
// linear, and the two conflict checks below make that weaker gate sufficient:
// after the input is proven valid, this package only ever replaces one scalar
// with a quoted string or inserts one key line, so the only way to produce an
// invalid document is a key that already exists in another form — and each of
// those is refused before the splice.
//
// Two measured limits shape the code below (docs/specs/config-patch-spec.md
// §4.2.2): Table and KeyValue nodes carry no byte range, so a header's range is
// derived from its Key children, and a node pointer dies at the next
// NextExpression call, so everything is copied out inside the loop.

type tomlKeyValue struct {
	tablePath []string // enclosing [table], empty at the root
	fullPath  []string // tablePath + dotted key parts
	key       string
	valStart  int
	valEnd    int
	valKind   unstable.Kind
	multiLine bool
}

type tomlSection struct {
	path        []string
	arrayTable  bool
	headerStart int
	headerEnd   int
}

type tomlDoc struct {
	kvs      []tomlKeyValue
	sections []tomlSection
}

// bomLen is the length of a UTF-8 byte order mark.
const bomLen = 3

func hasBOM(src []byte) bool {
	return len(src) >= bomLen && src[0] == 0xef && src[1] == 0xbb && src[2] == 0xbf
}

// parseableTOML returns a buffer the TOML parser accepts. TOML has no BOM
// syntax, so a leading BOM is replaced by three spaces: same length, so every
// offset the parser reports still indexes the caller's bytes.
func parseableTOML(src []byte) []byte {
	if !hasBOM(src) {
		return src
	}
	out := append([]byte(nil), src...)
	copy(out, "   ")
	return out
}

// collectTOML is the entry gate: the strict parser first (it is the only layer
// that catches a duplicate key or table), then the range scan.
func collectTOML(src []byte) (*tomlDoc, error) {
	buf := parseableTOML(src)
	var scratch any
	if err := toml.Unmarshal(buf, &scratch); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrParseFailure, err)
	}
	return scanTOML(buf, src)
}

// scanTOML builds the byte-range index with the unstable parser alone. It is
// linear in the document size, which is what makes it usable as the output
// check where the strict parser would cost another O(n²) pass.
func scanTOML(buf, src []byte) (*tomlDoc, error) {
	p := &unstable.Parser{KeepComments: true}
	p.Reset(buf)
	doc := &tomlDoc{}
	var cur []string
	for p.NextExpression() {
		n := p.Expression()
		switch n.Kind {
		case unstable.Table, unstable.ArrayTable:
			path, first, last := tomlKeyParts(n)
			if len(path) == 0 {
				return nil, fmt.Errorf("%w: table without a name", ErrParseFailure)
			}
			cur = path
			s := tomlSection{path: path, arrayTable: n.Kind == unstable.ArrayTable}
			if !s.arrayTable {
				s.headerStart, s.headerEnd = tomlHeaderRange(src, first, last)
			}
			doc.sections = append(doc.sections, s)
		case unstable.KeyValue:
			parts, _, _ := tomlKeyParts(n)
			if len(parts) == 0 {
				return nil, fmt.Errorf("%w: key without a name", ErrParseFailure)
			}
			v := n.Value()
			kv := tomlKeyValue{
				tablePath: append([]string(nil), cur...),
				fullPath:  append(append([]string(nil), cur...), parts...),
				key:       parts[len(parts)-1],
				valStart:  int(v.Raw.Offset),
				valEnd:    int(v.Raw.Offset + v.Raw.Length),
				valKind:   v.Kind,
			}
			if v.Kind == unstable.String {
				kv.multiLine = isTOMLMultiLine(src, kv.valStart)
			}
			doc.kvs = append(doc.kvs, kv)
		}
	}
	if err := p.Error(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrParseFailure, err)
	}
	return doc, nil
}

// tomlKeyParts copies every key component out of the node before the parser
// moves on, which is what keeps a stale pointer out of the result (P7).
func tomlKeyParts(n *unstable.Node) (parts []string, first, last int) {
	first = -1
	it := n.Key()
	for it.Next() {
		k := it.Node()
		parts = append(parts, string(k.Data))
		if first < 0 {
			first = int(k.Raw.Offset)
		}
		last = int(k.Raw.Offset + k.Raw.Length)
	}
	return parts, first, last
}

// tomlHeaderRange derives a table header's byte range from its key bytes, the
// way §4.2.3 step 3 specifies: back to the '[' that opens it, forward to the
// ']' that closes it. Table nodes themselves report {0,0}.
func tomlHeaderRange(src []byte, first, last int) (int, int) {
	start := first
	for start > 0 && src[start-1] != '[' {
		start--
	}
	if start == 0 {
		return first, last
	}
	end := last
	for end < len(src) && src[end] != ']' {
		end++
	}
	if end < len(src) {
		end++
	}
	return start - 1, end
}

func isTOMLMultiLine(src []byte, off int) bool {
	if off < 0 || off+3 > len(src) {
		return false
	}
	t := string(src[off : off+3])
	return t == `"""` || t == `'''`
}

// tomlKey quotes a key component only when it is not a bare key. Keys are not
// values: a bare key may not contain a quote or a space, and naming one
// unquoted would produce a header the parser reads as something else.
func tomlKey(s string) string {
	if s == "" {
		return tomlQuote(s)
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		bare := c == '-' || c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if !bare {
			return tomlQuote(s)
		}
	}
	return s
}

func tomlJoinKey(path []string) string {
	out := make([]string, len(path))
	for i, p := range path {
		out[i] = tomlKey(p)
	}
	return strings.Join(out, ".")
}

func patchTOML(src []byte, e Edit) ([]byte, error) {
	doc, err := collectTOML(src)
	if err != nil {
		return nil, err
	}
	key := e.KeyPath[len(e.KeyPath)-1]
	sectionPath := e.KeyPath[:len(e.KeyPath)-1]

	for _, kv := range doc.kvs {
		if !samePath(kv.fullPath, e.KeyPath) {
			continue
		}
		if e.Delete {
			return verifyTOMLRemoved(removeTOMLLine(src, kv, kv.tablePath), e)
		}
		if e.ValueJSON != "" {
			// TOML has no container form this package will write: inline tables
			// are refused as unstable above, so a caller that set ValueJSON for
			// TOML asked for something the format path cannot honour.
			return nil, fmt.Errorf("%w: TOML writes no container value", ErrUnsupportedStructure)
		}
		switch {
		case kv.valKind == unstable.Array || kv.valKind == unstable.InlineTable:
			return nil, fmt.Errorf("%w: value of %q is a %s", ErrUnsupportedStructure, key, kv.valKind)
		case kv.multiLine:
			return nil, fmt.Errorf("%w: value of %q is a multi-line string", ErrUnsupportedStructure, key)
		}
		return verifyTOML(splice(src, kv.valStart, kv.valEnd, []byte(tomlQuote(e.Value))), e)
	}
	if e.Delete {
		return nil, fmt.Errorf("%w: %q", ErrKeyNotFound, strings.Join(e.KeyPath, "."))
	}

	for _, s := range doc.sections {
		if samePath(s.path, e.KeyPath) {
			return nil, fmt.Errorf("%w: %q is a table", ErrUnsupportedStructure, key)
		}
		if s.arrayTable && samePath(s.path, sectionPath) {
			return nil, fmt.Errorf("%w: %q is an array of tables", ErrUnsupportedStructure, key)
		}
	}
	if !e.CreateIfMissing {
		return nil, fmt.Errorf("%w: %q", ErrKeyNotFound, strings.Join(e.KeyPath, "."))
	}
	// Every remaining path inserts a key. A key whose name an existing dotted
	// key already uses as a table — "a.b = 1" and then "a = ..." — parses as
	// the document it is, not as the one the caller asked for, and appending
	// it produces a file TOML itself rejects. The strict parser used to say so
	// on the output; it no longer runs there, so the refusal is here.
	if err := tomlCreateConflict(doc, e.KeyPath); err != nil {
		return nil, err
	}
	if len(sectionPath) == 0 {
		return verifyTOML(insertTOMLRootKey(src, doc, key, e.Value), e)
	}
	if idx := tomlFindSection(doc, sectionPath); idx >= 0 {
		return verifyTOML(insertTOMLKey(src, doc, idx, sectionPath, key, e.Value), e)
	}
	for _, kv := range doc.kvs {
		if len(kv.fullPath) > len(sectionPath) && samePath(kv.fullPath[:len(sectionPath)], sectionPath) {
			return nil, fmt.Errorf("%w: %q exists only as a dotted key",
				ErrUnsupportedStructure, strings.Join(sectionPath, "."))
		}
	}
	return verifyTOML(appendTOMLSection(src, sectionPath, key, e.Value), e)
}

// tomlCreateConflict refuses a create whose key path runs through a key that
// already exists as a dotted key.
//
// "a.b = 1" makes a a table holding b. Writing "a = \"v\"" next to it is the
// same namespace as "[a] b = 1", so the file would hold a scalar where a table
// already lives — rejected by TOML, and by this check before the bytes are
// built.
func tomlCreateConflict(doc *tomlDoc, fullPath []string) error {
	for _, kv := range doc.kvs {
		// Either side may be the shorter one. "a.b = 1" then "a": the new key
		// takes a name a table already owns. "a = 1" then "a.b": the new key
		// would live inside a scalar. Both produce a file TOML rejects.
		if len(kv.fullPath) < len(fullPath) && samePath(kv.fullPath, fullPath[:len(kv.fullPath)]) {
			return fmt.Errorf("%w: %q exists only as a dotted key",
				ErrUnsupportedStructure, strings.Join(fullPath, "."))
		}
		if len(fullPath) < len(kv.fullPath) && samePath(fullPath, kv.fullPath[:len(fullPath)]) {
			return fmt.Errorf("%w: %q is already a value, not a table",
				ErrUnsupportedStructure, strings.Join(fullPath, "."))
		}
	}
	return nil
}

// removeTOMLLine deletes the whole line a key/value occupies.
//
// TOML has no separator to leave behind, so unlike JSON there is nothing to
// splice around: the value is the rest of its line, and removing that line
// removes the entry.
//
// Two headers can be left empty by this and are cleaned up with it: the table
// whose last key just went, and — because a nested table's header is written
// under its parent's — the parent if that removal empties it in turn. A config
// holding a bare "[model_providers.9router]" line is not what "reset" means,
// and TOML itself reads the empty table as a value the user did not ask for
// (matrix §5.2 "xoá container rỗng").
func removeTOMLLine(src []byte, kv tomlKeyValue, tablePath []string) []byte {
	start := lineStartOf(src, kv.valStart)
	end := lineEnd(src, kv.valEnd)
	if end < len(src) && src[end] == '\r' {
		end++
	}
	if end < len(src) && src[end] == '\n' {
		end++
	}
	out := splice(src, start, end, nil)
	return dropEmptyTOMLTables(out, tablePath)
}

// dropEmptyTOMLTables removes the header of tablePath when nothing is left
// under it, then repeats for its parent. It stops at the first table that still
// holds a key, so a reset that leaves the user's siblings behind keeps their
// header.
func dropEmptyTOMLTables(src []byte, tablePath []string) []byte {
	for len(tablePath) > 0 {
		doc, err := scanTOML(parseableTOML(src), src)
		if err != nil {
			return src
		}
		// Anything still inside this table — a key or a child table — means the
		// header has to stay.
		occupied := false
		for _, kv := range doc.kvs {
			if len(kv.fullPath) > len(tablePath) && samePath(kv.fullPath[:len(tablePath)], tablePath) {
				occupied = true
				break
			}
		}
		for _, s := range doc.sections {
			if len(s.path) > len(tablePath) && samePath(s.path[:len(tablePath)], tablePath) {
				occupied = true
				break
			}
		}
		if occupied {
			return src
		}
		idx := tomlFindSection(doc, tablePath)
		if idx < 0 {
			return src
		}
		start := doc.sections[idx].headerStart
		end := doc.sections[idx].headerEnd
		if end < len(src) && src[end] == '\r' {
			end++
		}
		if end < len(src) && src[end] == '\n' {
			end++
		}
		src = splice(src, start, end, nil)
		tablePath = tablePath[:len(tablePath)-1]
	}
	return src
}

// verifyTOMLRemoved confirms a delete did what was asked: the file still
// parses, and the key is gone.
//
// It cannot reuse verifyTOML, which asserts the key is present and reads back
// as the requested value — the opposite of what a delete promises.
func verifyTOMLRemoved(out []byte, e Edit) ([]byte, error) {
	doc, err := scanTOML(parseableTOML(out), out)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInternalInconsistency, err)
	}
	for _, kv := range doc.kvs {
		if samePath(kv.fullPath, e.KeyPath) {
			return nil, fmt.Errorf("%w: %q is still present after delete",
				ErrInternalInconsistency, strings.Join(e.KeyPath, "."))
		}
	}
	return out, nil
}

func tomlFindSection(doc *tomlDoc, path []string) int {
	for i, s := range doc.sections {
		if !s.arrayTable && samePath(s.path, path) {
			return i
		}
	}
	return -1
}

// insertTOMLKey adds one key line to an existing section, after the last line
// that belongs to it — after its line comment, never inside it.
func insertTOMLKey(src []byte, doc *tomlDoc, idx int, sectionPath []string, key, val string) []byte {
	at := lineEnd(src, doc.sections[idx].headerEnd)
	for _, kv := range doc.kvs {
		if !samePath(kv.tablePath, sectionPath) {
			continue
		}
		if le := lineEnd(src, kv.valEnd); le > at {
			at = le
		}
	}
	line := tomlKey(key) + " = " + tomlQuote(val) + newlineFor(src)
	return splice(src, at, at, []byte(line))
}

// insertTOMLRootKey adds a key to the root table, which by TOML's own rule must
// stay above the first [table] header.
func insertTOMLRootKey(src []byte, doc *tomlDoc, key, val string) []byte {
	at := 0
	if len(doc.sections) > 0 {
		at = lineStartOf(src, doc.sections[0].headerStart)
	}
	line := tomlKey(key) + " = " + tomlQuote(val) + newlineFor(src)
	return splice(src, at, at, []byte(line))
}

func appendTOMLSection(src []byte, sectionPath []string, key, val string) []byte {
	nl := newlineFor(src)
	out := append([]byte(nil), src...)
	if len(out) > 0 {
		if out[len(out)-1] != '\n' {
			out = append(out, nl...)
		}
		if !endsWithBlankLine(out) {
			out = append(out, nl...)
		}
	}
	out = append(out, "["+tomlJoinKey(sectionPath)+"]"+nl...)
	out = append(out, tomlKey(key)+" = "+tomlQuote(val)+nl...)
	return out
}

// verifyTOML checks the patched bytes with the linear layer, not the strict
// one, and then reads the edited key back.
//
// The scan proves the output still parses. The read-back proves the splice
// landed on the key path the caller asked for — a splice that lands one byte
// off produces a document that parses and means something else, which a
// parse-only check cannot see. What neither proves is key/table uniqueness,
// and that is covered before the splice instead: the input has already passed
// the strict gate, and patchTOML refuses a create whose name already exists as
// a key, a dotted prefix, or a table (see the conflict checks there). Those are
// the only shapes a single-value insert or a quoted-scalar replace can collide
// with.
func verifyTOML(out []byte, e Edit) ([]byte, error) {
	buf := parseableTOML(out)
	doc, err := scanTOML(buf, out)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInternalInconsistency, err)
	}
	for _, kv := range doc.kvs {
		if !samePath(kv.fullPath, e.KeyPath) {
			continue
		}
		if kv.valStart < 0 || kv.valEnd > len(out) || kv.valStart >= kv.valEnd {
			return nil, fmt.Errorf("%w: %q has no byte range", ErrInternalInconsistency, strings.Join(e.KeyPath, "."))
		}
		if got := string(out[kv.valStart:kv.valEnd]); got != tomlQuote(e.Value) {
			return nil, fmt.Errorf("%w: %q reads back as %s", ErrInternalInconsistency, strings.Join(e.KeyPath, "."), got)
		}
		return out, nil
	}
	return nil, fmt.Errorf("%w: %q is absent after the patch", ErrInternalInconsistency, strings.Join(e.KeyPath, "."))
}
