package clientconfig

import (
	"encoding/json"
	"fmt"
)

// The parser and splicer for JSONC. It consumes the token stream produced by
// jsonclex.go, keeps a tree of tokens rather than of decoded values, and
// replaces the bytes of exactly one literal.

type jsonMember struct {
	key    string
	keyTok int
	val    *jsonNode
}

// jsonNode is a parsed value. Scalars and containers share the type so the
// navigator has one case to handle per path component.
type jsonNode struct {
	kind     jsonKind // jLBrace, jLBracket, or a leaf token kind
	tok      int      // index of the token that opens the node
	closeTok int      // index of '}' or ']' for containers
	members  []jsonMember
	elems    []*jsonNode
	trailing bool // container had a comma before its closing bracket
}

func (n *jsonNode) isContainer() bool { return n.kind == jLBrace || n.kind == jLBracket }

type jsonParser struct {
	src  []byte
	toks []jsonTok
	i    int
}

func (p *jsonParser) at(k jsonKind) bool {
	return p.i < len(p.toks) && p.toks[p.i].kind == k
}

// offset is the byte offset of the current token, or the end of input when the
// token stream is exhausted. A truncated file reaches this state, and reading
// past the stream would be a panic on input a user typed.
func (p *jsonParser) offset() int {
	if p.i < len(p.toks) {
		return p.toks[p.i].off
	}
	return len(p.src)
}

func (p *jsonParser) skipComments() {
	for p.i < len(p.toks) && p.toks[p.i].kind == jComment {
		p.i++
	}
}

func (p *jsonParser) parseDoc() (*jsonNode, error) {
	p.skipComments()
	if p.i >= len(p.toks) {
		return nil, fmt.Errorf("%w: empty document", ErrParseFailure)
	}
	n, err := p.parseValue()
	if err != nil {
		return nil, err
	}
	p.skipComments()
	if p.i != len(p.toks) {
		return nil, fmt.Errorf("%w: trailing content at offset %d", ErrParseFailure, p.offset())
	}
	return n, nil
}

func (p *jsonParser) parseValue() (*jsonNode, error) {
	switch {
	case p.at(jLBrace):
		return p.parseObject()
	case p.at(jLBracket):
		return p.parseArray()
	case p.at(jString), p.at(jScalar):
		t := p.toks[p.i]
		p.i++
		return &jsonNode{kind: t.kind, tok: p.i - 1}, nil
	}
	off := 0
	if p.i < len(p.toks) {
		off = p.offset()
	}
	return nil, fmt.Errorf("%w: expected a value at offset %d", ErrParseFailure, off)
}

func (p *jsonParser) parseObject() (*jsonNode, error) {
	n := &jsonNode{kind: jLBrace, tok: p.i}
	p.i++
	// Duplicate detection is a map, not a rescan of n.members. The rescan is
	// quadratic in the member count, and a config file with a few thousand
	// keys is exactly the case X5 measures: at 512 KB it turned a linear walk
	// into a second of wall clock.
	seen := make(map[string]struct{})
	for {
		p.skipComments()
		if p.at(jRBrace) {
			n.closeTok = p.i
			p.i++
			return n, nil
		}
		if len(n.members) > 0 {
			if !p.at(jComma) {
				return nil, fmt.Errorf("%w: expected ',' before offset %d", ErrParseFailure, p.offset())
			}
			p.i++
			p.skipComments()
			if p.at(jRBrace) {
				n.trailing = true
				n.closeTok = p.i
				p.i++
				return n, nil
			}
		}
		if !p.at(jString) {
			return nil, fmt.Errorf("%w: expected an object key at offset %d", ErrParseFailure, p.offset())
		}
		keyTok := p.i
		key, err := decodeJSONString(p.src[p.toks[keyTok].off:p.toks[keyTok].end()])
		if err != nil {
			return nil, err
		}
		if _, dup := seen[key]; dup {
			return nil, fmt.Errorf("%w: duplicate key %q", ErrParseFailure, key)
		}
		seen[key] = struct{}{}
		p.i++
		if !p.at(jColon) {
			return nil, fmt.Errorf("%w: expected ':' at offset %d", ErrParseFailure, p.offset())
		}
		p.i++
		p.skipComments()
		val, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		n.members = append(n.members, jsonMember{key: key, keyTok: keyTok, val: val})
	}
}

func (p *jsonParser) parseArray() (*jsonNode, error) {
	n := &jsonNode{kind: jLBracket, tok: p.i}
	p.i++
	for {
		p.skipComments()
		if p.at(jRBracket) {
			n.closeTok = p.i
			p.i++
			return n, nil
		}
		if len(n.elems) > 0 {
			if !p.at(jComma) {
				return nil, fmt.Errorf("%w: expected ',' before offset %d", ErrParseFailure, p.offset())
			}
			p.i++
			p.skipComments()
			if p.at(jRBracket) {
				n.trailing = true
				n.closeTok = p.i
				p.i++
				return n, nil
			}
		}
		v, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		n.elems = append(n.elems, v)
	}
}

// decodeJSONString unquotes a string literal. stdlib does the escape work, and
// rejects an escape this lexer let through, which is the fail-closed answer.
func decodeJSONString(raw []byte) (string, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("%w: bad string literal: %w", ErrParseFailure, err)
	}
	return s, nil
}

func parseJSONC(src []byte) (*jsonParser, *jsonNode, error) {
	toks, err := lexJSONC(src)
	if err != nil {
		return nil, nil, err
	}
	p := &jsonParser{src: src, toks: toks}
	n, err := p.parseDoc()
	if err != nil {
		return nil, nil, err
	}
	return p, n, nil
}

// patchJSONC finds the literal at e.KeyPath and replaces only its bytes.
func patchJSONC(src []byte, e Edit) ([]byte, error) {
	p, root, err := parseJSONC(src)
	if err != nil {
		return nil, err
	}
	cur := root
	for idx, part := range e.KeyPath {
		if cur.kind != jLBrace {
			return nil, fmt.Errorf("%w: %q is not inside an object", ErrKeyNotFound, part)
		}
		m := findJSONMember(cur, part)
		if m == nil {
			if !e.CreateIfMissing {
				return nil, fmt.Errorf("%w: %q", ErrKeyNotFound, part)
			}
			if idx != len(e.KeyPath)-1 {
				// CreateIfMissing covers the whole path, not just the leaf:
				// connect writing env.ANTHROPIC_BASE_URL into a settings file
				// with no "env" object is the common first-run case, and
				// refusing it would make the caller special-case it.
				rest := e.KeyPath[idx:]
				nested, err := jsonNestedEntry(e, rest)
				if err != nil {
					return nil, err
				}
				return verifyJSONC(insertJSONMember(src, p.toks, cur, part, nested))
			}
			entry, err := jsonMemberEntry(e, part)
			if err != nil {
				return nil, err
			}
			return verifyJSONC(insertJSONMember(src, p.toks, cur, part, entry))
		}
		if idx == len(e.KeyPath)-1 {
			if e.Delete {
				return verifyJSONC(removeJSONMember(src, p.toks, cur, part))
			}
			// A scalar edit aimed at a container is almost always a caller that
			// meant to update a key inside it: silently replacing the whole
			// object with one string would delete the rest of its contents.
			// Replacing a container is allowed only when the caller says so by
			// passing ValueJSON.
			if m.val.isContainer() && e.ValueJSON == "" {
				return nil, fmt.Errorf("%w: value of %q is not a scalar", ErrUnsupportedStructure, part)
			}
			repl, err := jsonValueBytes(e)
			if err != nil {
				return nil, err
			}
			return verifyJSONC(splice(src, m.val.bytesStart(p.toks), m.val.bytesEnd(p.toks), repl))
		}
		cur = m.val
	}
	return nil, fmt.Errorf("%w: empty key path", ErrKeyNotFound)
}

// bytesStart and bytesEnd bracket the whole value, container included. A scalar
// node's own token is the value; a container's is its opening brace or bracket,
// so its end comes from closeTok.
func (n *jsonNode) bytesStart(toks []jsonTok) int { return toks[n.tok].off }

func (n *jsonNode) bytesEnd(toks []jsonTok) int {
	if n.isContainer() {
		return toks[n.closeTok].end()
	}
	return toks[n.tok].end()
}

// jsonValueBytes renders the replacement for one edit: caller-supplied container
// text when ValueJSON is set, otherwise the quoted scalar in Value.
func jsonValueBytes(e Edit) ([]byte, error) {
	if e.ValueJSON == "" {
		return []byte(jsoncQuote(e.Value)), nil
	}
	if err := validateJSONValue(e.ValueJSON); err != nil {
		return nil, err
	}
	return []byte(e.ValueJSON), nil
}

// jsonMemberEntry renders the `key: value` text a create inserts.
func jsonMemberEntry(e Edit, key string) (string, error) {
	if e.Delete {
		return "", fmt.Errorf("%w: cannot delete %q, it does not exist", ErrKeyNotFound, key)
	}
	val, err := jsonValueBytes(e)
	if err != nil {
		return "", err
	}
	return jsoncQuote(key) + ": " + string(val), nil
}

// jsonNestedEntry renders `key: {…}` for a path that has to be created whole.
//
// The value is built from the remaining path rather than from ValueJSON: this
// branch only runs when the leaf is scalar, so the nested objects are all
// scaffolding along the way and the last element carries the caller's value.
func jsonNestedEntry(e Edit, path []string) (string, error) {
	leaf, err := jsonValueBytes(e)
	if err != nil {
		return "", err
	}
	// Fold from the leaf outwards: the last element becomes the innermost
	// pair, and each step wraps what is already built.
	inner := string(leaf)
	for i := len(path) - 1; i > 0; i-- {
		inner = jsoncQuote(path[i]) + ": " + inner
		if i > 1 {
			inner = "{" + inner + "}"
		}
	}
	return jsoncQuote(path[0]) + ": " + "{" + inner + "}", nil
}

// validateJSONValue re-parses the caller's container text once, before any
// splice. A caller that passes malformed JSON gets an error naming its own
// input instead of ErrInternalInconsistency after the file has been touched.
func validateJSONValue(text string) error {
	if _, _, err := parseJSONC([]byte(text)); err != nil {
		return fmt.Errorf("%w: ValueJSON does not parse: %w", ErrUnsupportedStructure, err)
	}
	return nil
}

// removeJSONMember deletes one member and the comma that separated it, keeping
// every other byte in place.
//
// The comma isn't part of either member's node, so it has to be found and
// dropped explicitly: keeping it leaves a dangling separator, and dropping the
// wrong one joins two members. The rules are the two cases JSON allows — a
// member that is not last takes the following comma, the last member takes the
// preceding one — and the caller is responsible for having checked that the
// object is left non-empty, because an empty object after a delete is only
// correct if it was empty-ish already.
func removeJSONMember(src []byte, toks []jsonTok, obj *jsonNode, key string) []byte {
	idx := -1
	for i := range obj.members {
		if obj.members[i].key == key {
			idx = i
			break
		}
	}
	if idx < 0 {
		return src
	}
	m := obj.members[idx]
	start := toks[m.keyTok].off
	end := m.val.bytesEnd(toks)

	// Take the indentation in front of the member, but only when the member
	// opened its own line. On a shared line the blank before the key is the gap
	// the previous member's comma owns: cutting it would join `1,` to `"c"`.
	if start > 0 && src[start-1] != '\n' {
		// look back for a line break to decide
		i := start
		for i > 0 && (src[i-1] == ' ' || src[i-1] == '\t') {
			i--
		}
		if i == 0 || src[i-1] != '\n' {
			i = start // not at line start: leave the gap alone
		}
		start = i
	}

	if idx+1 < len(obj.members) {
		// Not last: the cut runs from this member's indentation through its
		// trailing comma. The blanks after that comma stay: they are what
		// separates the next key from the previous one, and taking them too
		// would join `1,` and `"c"` into `1,"c"`.
		nextKey := toks[obj.members[idx+1].keyTok].off
		commaOff := findJSONComma(toks, end, nextKey)
		if commaOff < 0 {
			return src
		}
		end = commaOff + 1
		if start > 0 && src[start-1] == '\n' {
			// The member opened its own line: take that line's terminator and the
			// next line's indentation, or the deletion leaves a blank line.
			for end < len(src) && (src[end] == ' ' || src[end] == '\t') {
				end++
			}
			if end < len(src) && src[end] == '\r' {
				end++
			}
			if end < len(src) && src[end] == '\n' {
				end++
			}
			start--
		} else {
			// Same line: take the blank after the comma so `{"a": 1, "b": 2}`
			// becomes `{"b": 2}`, not `{ "b": 2}`.
			for end < len(src) && (src[end] == ' ' || src[end] == '\t') {
				end++
			}
		}
		return splice(src, start, end, nil)
	}

	// Last member: its separator sits before the cut, so it is removed in a
	// second pass over the already-shortened buffer. Every offset below `start`
	// is unchanged by the first splice.
	cutStart := start
	if start > 0 && src[start-1] == '\n' {
		cutStart--
	} else {
		// Same line as what precedes it: the cut starts at the separating comma,
		// so the gap it owned disappears with it instead of collapsing onto the
		// closing brace (`{"a": 1 }`).
		if idx > 0 {
			prevEnd := obj.members[idx-1].val.bytesEnd(toks)
			if commaOff := findJSONComma(toks, prevEnd, start); commaOff >= 0 {
				cutStart = commaOff
			}
		}
	}
	out := splice(src, cutStart, end, nil)
	if idx > 0 && cutStart == start {
		// The newline case: the separator was not part of the first cut.
		prevEnd := obj.members[idx-1].val.bytesEnd(toks)
		if commaOff := findJSONComma(toks, prevEnd, start); commaOff >= 0 {
			out = splice(out, commaOff, commaOff+1, nil)
		}
	}
	return out
}

// findJSONComma returns the offset of the comma between two byte positions, or
// -1 when the range holds none.
func findJSONComma(toks []jsonTok, from, to int) int {
	for _, t := range toks {
		if t.kind == jComma && t.off >= from && t.off < to {
			return t.off
		}
	}
	return -1
}

func findJSONMember(n *jsonNode, key string) *jsonMember {
	for i := range n.members {
		if n.members[i].key == key {
			return &n.members[i]
		}
	}
	return nil
}

// insertJSONMember appends a member to an existing object with a single
// insertion before the closing brace. The new member lands on the closing
// brace's line: inventing a newline would mean inventing an indent policy, and
// the result is valid JSONC either way.
func insertJSONMember(src []byte, toks []jsonTok, obj *jsonNode, key, entry string) []byte {
	switch {
	case len(obj.members) == 0:
		// {} -> {"k": "v"}
	case obj.trailing:
		entry = " " + entry // the existing comma now separates it
	default:
		entry = ", " + entry
	}
	at := toks[obj.closeTok].off
	return splice(src, at, at, []byte(entry))
}

// verifyJSONC re-parses the spliced output. A failure here means this package
// wrote something invalid, which is a bug and not a user error.
func verifyJSONC(out []byte) ([]byte, error) {
	if _, _, err := parseJSONC(out); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInternalInconsistency, err)
	}
	return out, nil
}
