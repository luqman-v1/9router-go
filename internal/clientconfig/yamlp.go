package clientconfig

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	yaml "go.yaml.in/yaml/v3"
)

// YAML is decoded into Node trees and never marshalled back. Encoding a tree
// returns the file with normalised whitespace — measured: `key:    "v"` and
// `-   one` both come back reformatted (§4.3.2) — so the only way to keep a
// file byte-identical outside the edit is to replace one scalar's bytes and
// copy everything else verbatim.
//
// Node.Column counts runes, not bytes, so it is converted rather than used as
// an offset. That conversion is also what makes a multi-byte key work: the
// column still names the right rune, and the rune maps to the right byte.

func decodeYAML(src []byte) ([]*yaml.Node, error) {
	dec := yaml.NewDecoder(bytes.NewReader(src))
	var docs []*yaml.Node
	for {
		var n yaml.Node
		err := dec.Decode(&n)
		if errors.Is(err, io.EOF) {
			return docs, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrParseFailure, err)
		}
		docs = append(docs, &n)
	}
}

// yamlLineStarts maps every 1-based line number to its byte offset.
func yamlLineStarts(src []byte) []int {
	starts := []int{0}
	for i, b := range src {
		if b == '\n' {
			starts = append(starts, i+1)
		}
	}
	return starts
}

// runeColumnToByte converts a 1-based rune column within a line to a byte
// offset in src. It stops at the end of the line when the column runs past it.
func runeColumnToByte(src []byte, lineStart, column int) int {
	i := lineStart
	for n := 1; n < column && i < len(src) && src[i] != '\n'; n++ {
		_, size := utf8.DecodeRune(src[i:])
		i += size
	}
	return i
}

// findYAMLScalar returns the node at path, refusing a file whose mappings hold
// a duplicate key: silently picking one of two same-named keys is how a config
// gets edited in the wrong place.
func findYAMLScalar(docs []*yaml.Node, path []string) (*yaml.Node, error) {
	var found []*yaml.Node
	for _, d := range docs {
		n, err := yamlDescend(d, path)
		if err != nil {
			return nil, err
		}
		if n != nil {
			found = append(found, n)
		}
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("%w: %q", ErrKeyNotFound, path[len(path)-1])
	}
	if len(found) > 1 {
		return nil, fmt.Errorf("%w: %q appears in more than one document",
			ErrUnsupportedStructure, path[len(path)-1])
	}
	return found[0], nil
}

func yamlDescend(n *yaml.Node, path []string) (*yaml.Node, error) {
	if n.Kind == yaml.DocumentNode {
		if len(n.Content) == 0 {
			return nil, nil
		}
		n = n.Content[0]
	}
	for _, part := range path {
		if n == nil || n.Kind != yaml.MappingNode {
			return nil, nil
		}
		var next *yaml.Node
		count := 0
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == part {
				count++
				next = n.Content[i+1]
			}
		}
		if count > 1 {
			return nil, fmt.Errorf("%w: duplicate key %q", ErrUnsupportedStructure, part)
		}
		n = next
	}
	return n, nil
}

// yamlScalarRange returns the byte range of a scalar's representation. It
// refuses anything it cannot delimit exactly.
func yamlScalarRange(src []byte, n *yaml.Node, starts []int) (int, int, error) {
	if n.Kind != yaml.ScalarNode {
		return 0, 0, fmt.Errorf("%w: target is not a scalar", ErrUnsupportedStructure)
	}
	if n.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		return 0, 0, fmt.Errorf("%w: block scalar", ErrUnsupportedStructure)
	}
	if n.Anchor != "" {
		// The column points at the "&name", so a splice from there would drop
		// the anchor. Refusing beats re-emitting an anchor whose references
		// this patcher has not counted.
		return 0, 0, fmt.Errorf("%w: target is anchored", ErrUnsupportedStructure)
	}
	if n.Line <= 0 || n.Line > len(starts) {
		return 0, 0, fmt.Errorf("%w: target has no usable position", ErrUnsupportedStructure)
	}
	lineStart := starts[n.Line-1]
	if n.Line == 1 && hasBOM(src) {
		// The decoder does not count the BOM as a column, but it does occupy
		// three bytes, so the rune walk must start after it.
		lineStart += bomLen
	}
	start := runeColumnToByte(src, lineStart, n.Column)
	if start > len(src) {
		start = len(src)
	}
	end, err := yamlScalarEnd(src, start)
	if err != nil {
		return 0, 0, err
	}
	// A plain scalar may continue on the following lines, where YAML folds the
	// breaks into spaces. Replacing only the first line would leave the rest of
	// the old value behind, so refuse when the raw bytes do not already read as
	// the whole value.
	if n.Style&(yaml.SingleQuotedStyle|yaml.DoubleQuotedStyle) == 0 && string(src[start:end]) != n.Value {
		return 0, 0, fmt.Errorf("%w: scalar is not on a single line", ErrUnsupportedStructure)
	}
	return start, end, nil
}

// yamlScalarEnd finds the end of the scalar that begins at start.
func yamlScalarEnd(src []byte, start int) (int, error) {
	if start >= len(src) {
		return 0, fmt.Errorf("%w: scalar runs past the end of input", ErrUnsupportedStructure)
	}
	switch src[start] {
	case '"', '\'':
		return scanYAMLQuoted(src, start, len(src), src[start])
	}
	return plainYAMLEnd(src, start), nil
}

// scanYAMLQuoted returns the index just past the closing quote.
func scanYAMLQuoted(src []byte, start, to int, quote byte) (int, error) {
	for i := start + 1; i < to; i++ {
		switch src[i] {
		case '\\':
			if quote == '"' {
				i++ // the escaped byte cannot close the scalar
			}
		case quote:
			if quote == '\'' && i+1 < to && src[i+1] == '\'' {
				i++ // '' is an escaped quote in a single-quoted scalar
				continue
			}
			return i + 1, nil
		}
	}
	return 0, fmt.Errorf("%w: unterminated quoted scalar", ErrParseFailure)
}

// plainYAMLEnd finds the end of a plain scalar. A '#' starts a comment only at
// the start of the value or after whitespace — a '#' inside a URL is data.
func plainYAMLEnd(src []byte, start int) int {
	end := len(src)
	for i := start; i < len(src); i++ {
		if src[i] == '\n' || src[i] == '\r' {
			end = i
			break
		}
		if src[i] == '#' && (i == start || src[i-1] == ' ' || src[i-1] == '\t') {
			end = i
			break
		}
	}
	for end > start && (src[end-1] == ' ' || src[end-1] == '\t') {
		end--
	}
	return end
}

func patchYAML(src []byte, e Edit) ([]byte, error) {
	docs, err := decodeYAML(src)
	if err != nil {
		return nil, err
	}
	target, err := findYAMLScalar(docs, e.KeyPath)
	if err != nil {
		if !errors.Is(err, ErrKeyNotFound) || !e.CreateIfMissing {
			return nil, err
		}
		return appendYAMLTopKey(src, docs, e.KeyPath, e.Value)
	}
	start, end, err := yamlScalarRange(src, target, yamlLineStarts(src))
	if err != nil {
		return nil, err
	}
	return verifyYAML(splice(src, start, end, []byte(yamlQuote(e.Value))), e)
}

// appendYAMLTopKey adds one top-level key to the last document. Only a
// single-element path is created: nesting would need an indentation policy this
// patcher has no grounds to infer, and guessing one is how a config ends up
// structurally wrong.
func appendYAMLTopKey(src []byte, docs []*yaml.Node, path []string, value string) ([]byte, error) {
	if len(path) != 1 {
		return nil, fmt.Errorf("%w: cannot create nested key %q", ErrUnsupportedStructure, path[len(path)-1])
	}
	if len(docs) == 0 || len(docs[len(docs)-1].Content) == 0 {
		return nil, fmt.Errorf("%w: no document to extend", ErrUnsupportedStructure)
	}
	if strings.HasSuffix(strings.TrimRight(string(src), " \t\r\n"), "...") {
		return nil, fmt.Errorf("%w: document ends with an explicit end marker", ErrUnsupportedStructure)
	}
	nl := newlineFor(src)
	out := append([]byte(nil), src...)
	if len(out) > 0 && out[len(out)-1] != '\n' {
		out = append(out, nl...)
	}
	// The key is quoted too: a plain key containing ":" or starting with a
	// reserved indicator would change meaning, and one style keeps a second
	// patch byte-identical.
	out = append(out, (yamlQuote(path[0]) + ": " + yamlQuote(value) + nl)...)
	return verifyYAML(out, Edit{KeyPath: path, Value: value})
}

// verifyYAML re-decodes the output and confirms the target now reads back as
// the value asked for. The re-decode proves the file still parses; the value
// check proves the splice landed on the scalar and not on part of it.
func verifyYAML(out []byte, e Edit) ([]byte, error) {
	docs, err := decodeYAML(out)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInternalInconsistency, err)
	}
	n, err := findYAMLScalar(docs, e.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInternalInconsistency, err)
	}
	if n.Kind != yaml.ScalarNode || n.Value != e.Value {
		return nil, fmt.Errorf("%w: %q did not read back as written", ErrInternalInconsistency, e.KeyPath[len(e.KeyPath)-1])
	}
	return out, nil
}
