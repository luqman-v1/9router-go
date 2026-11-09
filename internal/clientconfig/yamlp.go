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
		if !errors.Is(err, ErrKeyNotFound) || !e.CreateIfMissing || e.Delete {
			return nil, err
		}
		return appendYAMLTopKey(src, docs, e.KeyPath, e.Value)
	}
	if e.Delete {
		return deleteYAMLKey(src, target, e)
	}
	// yamlScalarRange stops at anything that is not a scalar. A collection being
	// replaced by ValueJSON is not a scalar case, so it needs its own range: the
	// node's own line and column, through the end of that line.
	if e.ValueJSON != "" && target.Kind != yaml.ScalarNode {
		start, end, err := yamlNodeLineRange(src, target)
		if err != nil {
			return nil, err
		}
		return replaceYAMLWithFlow(src, start, end, e)
	}
	start, end, err := yamlScalarRange(src, target, yamlLineStarts(src))
	if err != nil {
		return nil, err
	}
	repl := []byte(yamlQuote(e.Value))
	if e.ValueJSON != "" {
		// A collection replaces the scalar outright. The caller's text is
		// parsed first so a malformed flow collection is reported as the
		// caller's mistake, not as bytes this package wrote.
		flow := strings.TrimSpace(e.ValueJSON)
		if !strings.HasPrefix(flow, "[") && !strings.HasPrefix(flow, "{") {
			return nil, fmt.Errorf("%w: ValueJSON must be a flow collection", ErrUnsupportedStructure)
		}
		if _, err := decodeYAML([]byte("k: " + flow + "\n")); err != nil {
			return nil, fmt.Errorf("%w: ValueJSON does not parse: %w", ErrUnsupportedStructure, err)
		}
		repl = []byte(flow)
	}
	return verifyYAML(splice(src, start, end, repl), Edit{KeyPath: e.KeyPath, Value: e.Value})
}

// yamlNodeLineRange brackets a collection by the line its key sits on.
//
// Only flow and single-line block collections are replaced this way: a block
// collection spanning lines would need its indentation re-derived, which this
// package refuses to guess.
func yamlNodeLineRange(src []byte, n *yaml.Node) (int, int, error) {
	starts := yamlLineStarts(src)
	if n.Line <= 0 || n.Line > len(starts) {
		return 0, 0, fmt.Errorf("%w: target has no usable position", ErrUnsupportedStructure)
	}
	lineStart := starts[n.Line-1]
	if n.Line == 1 && hasBOM(src) {
		lineStart += bomLen
	}
	// The value begins after the "key:" on this line; find the colon and skip to
	// the first non-blank byte after it.
	i := lineStart
	for i < len(src) && src[i] != ':' && src[i] != '\n' {
		i++
	}
	if i >= len(src) || src[i] != ':' {
		return 0, 0, fmt.Errorf("%w: target line has no ':'", ErrUnsupportedStructure)
	}
	i++
	for i < len(src) && (src[i] == ' ' || src[i] == '\t') {
		i++
	}
	start := i
	end := yamlLineEnd(src, start)
	// A block collection puts its children on later lines. Replacing only the
	// first would leave the children behind, pointing at a value that no longer
	// exists, so this path handles flow collections and empty block ones only.
	if strings.HasPrefix(string(src[start:end]), "[]") || strings.HasPrefix(string(src[start:end]), "{}") {
		return start, end, nil
	}
	if n.Line < len(starts) {
		nextStart := starts[n.Line]
		nextEnd := yamlLineEnd(src, nextStart)
		indent := len(src[nextStart:nextEnd]) - len(strings.TrimLeft(string(src[nextStart:nextEnd]), " \t"))
		if strings.TrimSpace(string(src[nextStart:nextEnd])) != "" && indent > len(src[lineStart:start])-len(strings.TrimLeft(string(src[lineStart:start]), " \t")) {
			return 0, 0, fmt.Errorf("%w: collection spans more than one line", ErrUnsupportedStructure)
		}
	}
	return start, end, nil
}

// yamlLineEnd returns the byte offset just past the last non-blank byte of the
// line that contains `start`, stopping before a trailing comment.
func yamlLineEnd(src []byte, start int) int {
	end := start
	for i := start; i < len(src); i++ {
		if src[i] == '\n' || src[i] == '\r' {
			break
		}
		if src[i] == '#' && (i == start || src[i-1] == ' ' || src[i-1] == '\t') {
			break
		}
		end = i + 1
	}
	for end > start && (src[end-1] == ' ' || src[end-1] == '\t') {
		end--
	}
	return end
}

// replaceYAMLWithFlow substitutes caller-supplied flow text for a collection.
func replaceYAMLWithFlow(src []byte, start, end int, e Edit) ([]byte, error) {
	flow := strings.TrimSpace(e.ValueJSON)
	if !strings.HasPrefix(flow, "[") && !strings.HasPrefix(flow, "{") {
		return nil, fmt.Errorf("%w: ValueJSON must be a flow collection", ErrUnsupportedStructure)
	}
	if _, err := decodeYAML([]byte("k: " + flow + "\n")); err != nil {
		return nil, fmt.Errorf("%w: ValueJSON does not parse: %w", ErrUnsupportedStructure, err)
	}
	return verifyYAMLReplaced(splice(src, start, end, []byte(flow)), e)
}

// verifyYAMLReplaced confirms the file still parses after a collection was
// swapped. It cannot use verifyYAML, which insists the target reads back as a
// scalar equal to e.Value.
func verifyYAMLReplaced(out []byte, e Edit) ([]byte, error) {
	if _, err := decodeYAML(out); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInternalInconsistency, err)
	}
	return out, nil
}

// deleteYAMLKey removes a key and, when it holds a block collection, every line
// that belongs to it.
//
// The node's Line is the line its key starts on. A block collection's children
// are more indented than that key, so the block ends at the first later line
// whose indentation is at or above the key's own — or at end of input. That is
// the whole rule, and it is why this can delete a subtree where the JSON path
// cannot: there is no comma to keep in step.
//
// A flow collection is rejected rather than half-handled: its children are on
// the same line, and the caller replacing one has ValueJSON for it.
func deleteYAMLKey(src []byte, target *yaml.Node, e Edit) ([]byte, error) {
	starts := yamlLineStarts(src)
	if target.Line <= 0 || target.Line > len(starts) {
		return nil, fmt.Errorf("%w: target has no usable position", ErrUnsupportedStructure)
	}
	if target.Style&yaml.FlowStyle != 0 && target.Kind != yaml.ScalarNode {
		return nil, fmt.Errorf("%w: cannot delete a flow collection %q",
			ErrUnsupportedStructure, e.KeyPath[len(e.KeyPath)-1])
	}
	// The node's Line is where its key starts for a scalar, but for a block
	// mapping it is where the mapping's first child line begins (measured: the
	// node for a nested mapping sits on line 2 of a file where its key is on
	// line 2, and the value is on line 3). Walking back to the line that holds
	// the key is what makes the cut start at the key rather than inside its
	// value.
	keyLine, keyIndent := yamlKeyLine(src, starts, target)
	start := starts[keyLine-1]
	if target.Kind == yaml.ScalarNode {
		keyLine, keyIndent = target.Line, target.Column-1
		start = starts[keyLine-1]
	}
	end := lineEnd(src, start)
	if end < len(src) && src[end] == '\r' {
		end++
	}
	if end < len(src) && src[end] == '\n' {
		end++
	}
	if target.Kind == yaml.MappingNode || target.Kind == yaml.SequenceNode {
		end = yamlBlockEnd(src, starts, keyLine, keyIndent, end)
	}
	return verifyYAMLRemoved(splice(src, start, end, nil), e)
}

// yamlKeyLine finds the line holding the key of a block value: the nearest line
// above the node whose indentation is less than the node's, and which ends in
// a colon.
func yamlKeyLine(src []byte, starts []int, target *yaml.Node) (int, int) {
	childIndent := target.Column - 1
	for line := target.Line - 1; line >= 1; line-- {
		lineStart := starts[line-1]
		lineEndOff := lineEnd(src, lineStart)
		text := string(src[lineStart:lineEndOff])
		if strings.TrimSpace(text) == "" {
			continue
		}
		indent := len(text) - len(strings.TrimLeft(text, " \t"))
		if strings.HasSuffix(strings.TrimSpace(text), ":") {
			return line, indent
		}
		if indent < childIndent {
			return line + 1, childIndent
		}
	}
	return 1, 0
}

// yamlBlockEnd extends endLine past every line indented deeper than the key.
func yamlBlockEnd(src []byte, starts []int, keyLine, keyIndent, end int) int {

	for line := keyLine + 1; line <= len(starts); line++ {
		lineStart := starts[line-1]
		lineEndOff := lineEnd(src, lineStart)
		text := string(src[lineStart:lineEndOff])
		if strings.TrimSpace(text) == "" {
			end = lineEndOff
			if end < len(src) && src[end] == '\r' {
				end++
			}
			if end < len(src) && src[end] == '\n' {
				end++
			}
			continue
		}
		indent := len(text) - len(strings.TrimLeft(text, " \t"))
		if indent <= keyIndent {
			return end
		}
		end = lineEndOff
		if end < len(src) && src[end] == '\r' {
			end++
		}
		if end < len(src) && src[end] == '\n' {
			end++
		}
	}
	return end
}

// verifyYAMLRemoved confirms the key is gone and the file still parses.
// verifyYAML asserts the key is present with the written value, so it cannot
// stand in for a delete.
func verifyYAMLRemoved(out []byte, e Edit) ([]byte, error) {
	docs, err := decodeYAML(out)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInternalInconsistency, err)
	}
	if _, err := findYAMLScalar(docs, e.KeyPath); err == nil {
		return nil, fmt.Errorf("%w: %q is still present after delete",
			ErrInternalInconsistency, e.KeyPath[len(e.KeyPath)-1])
	} else if !errors.Is(err, ErrKeyNotFound) {
		return nil, fmt.Errorf("%w: %w", ErrInternalInconsistency, err)
	}
	return out, nil
}

// appendYAMLTopKey adds one top-level key to the last document. Only a
// single-element path is created: nesting would need an indentation policy this
// patcher has no grounds to infer, and guessing one is how a config ends up
// structurally wrong.
func appendYAMLTopKey(src []byte, docs []*yaml.Node, path []string, value string) ([]byte, error) {
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
	// One indented pair per path element. The key is quoted at every level: a
	// plain key containing ":" or starting with a reserved indicator would
	// change meaning, and one style keeps a second patch byte-identical.
	for i, part := range path {
		indent := strings.Repeat("  ", i)
		last := i == len(path)-1
		out = append(out, (indent + yamlQuote(part) + ":")...)
		if last {
			out = append(out, (" " + yamlQuote(value) + nl)...)
			break
		}
		out = append(out, nl...)
	}
	return verifyYAMLValue(out, Edit{KeyPath: path, Value: value})
}

// verifyYAMLValue confirms the created path reads back as the value asked for.
// It exists because verifyYAML compares against e.Value on a scalar lookup, and
// this path builds the file rather than splicing one.
func verifyYAMLValue(out []byte, e Edit) ([]byte, error) {
	docs, err := decodeYAML(out)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInternalInconsistency, err)
	}
	n, err := findYAMLScalar(docs, e.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInternalInconsistency, err)
	}
	if n.Value != e.Value {
		return nil, fmt.Errorf("%w: %q did not read back as written",
			ErrInternalInconsistency, e.KeyPath[len(e.KeyPath)-1])
	}
	return out, nil
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
