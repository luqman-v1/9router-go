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
				return nil, fmt.Errorf("%w: intermediate object %q is absent", ErrKeyNotFound, part)
			}
			out := insertJSONMember(src, p.toks, cur, part, e.Value)
			return verifyJSONC(out)
		}
		if idx == len(e.KeyPath)-1 {
			if m.val.isContainer() {
				return nil, fmt.Errorf("%w: value of %q is not a scalar", ErrUnsupportedStructure, part)
			}
			t := p.toks[m.val.tok]
			out := splice(src, t.off, t.end(), []byte(jsoncQuote(e.Value)))
			return verifyJSONC(out)
		}
		cur = m.val
	}
	return nil, fmt.Errorf("%w: empty key path", ErrKeyNotFound)
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
func insertJSONMember(src []byte, toks []jsonTok, obj *jsonNode, key, val string) []byte {
	entry := jsoncQuote(key) + ": " + jsoncQuote(val)
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
