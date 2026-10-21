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
// run as a gate on both the input and the output.
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

func collectTOML(src []byte) (*tomlDoc, error) {
	buf := parseableTOML(src)
	var scratch any
	if err := toml.Unmarshal(buf, &scratch); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrParseFailure, err)
	}
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
		return nil, fmt.Errorf("%w: %v", ErrParseFailure, err)
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
		switch {
		case kv.valKind == unstable.Array || kv.valKind == unstable.InlineTable:
			return nil, fmt.Errorf("%w: value of %q is a %s", ErrUnsupportedStructure, key, kv.valKind)
		case kv.multiLine:
			return nil, fmt.Errorf("%w: value of %q is a multi-line string", ErrUnsupportedStructure, key)
		}
		return verifyTOML(splice(src, kv.valStart, kv.valEnd, []byte(tomlQuote(e.Value))))
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
	if len(sectionPath) == 0 {
		return verifyTOML(insertTOMLRootKey(src, doc, key, e.Value))
	}
	if idx := tomlFindSection(doc, sectionPath); idx >= 0 {
		return verifyTOML(insertTOMLKey(src, doc, idx, sectionPath, key, e.Value))
	}
	for _, kv := range doc.kvs {
		if len(kv.fullPath) > len(sectionPath) && samePath(kv.fullPath[:len(sectionPath)], sectionPath) {
			return nil, fmt.Errorf("%w: %q exists only as a dotted key",
				ErrUnsupportedStructure, strings.Join(sectionPath, "."))
		}
	}
	return verifyTOML(appendTOMLSection(src, sectionPath, key, e.Value))
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

// verifyTOML re-runs both parser layers on the patched bytes. A failure is this
// package's bug, so it is reported as such and nothing is returned to write.
func verifyTOML(out []byte) ([]byte, error) {
	if _, err := collectTOML(out); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInternalInconsistency, err)
	}
	return out, nil
}
