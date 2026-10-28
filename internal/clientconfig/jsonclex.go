package clientconfig

import "fmt"

// JSONC has no stdlib reader that reports byte offsets or keeps comments, so
// the lexer here is written by hand. It is the smallest thing that satisfies
// §3.3 of docs/specs/config-patch-spec.md: it knows when it is inside a string,
// so the "//" of "https://…" is never mistaken for a comment.
//
// The lexer only classifies bytes; it never rewrites the document. A patch
// replaces the byte range of exactly one literal and lets every other byte
// through untouched, which is what keeps comments, key order, indentation and
// trailing commas intact.

type jsonKind int

const (
	jSpace jsonKind = iota
	jLBrace
	jRBrace
	jLBracket
	jRBracket
	jColon
	jComma
	jString
	jScalar // number, true, false, null
	jComment
)

// jsonTok is one lexical token. Offsets are absolute byte positions, so a token
// can be spliced without any line/column arithmetic.
type jsonTok struct {
	kind jsonKind
	off  int
	len  int
}

func (t jsonTok) end() int { return t.off + t.len }

func lexJSONC(src []byte) ([]jsonTok, error) {
	var toks []jsonTok
	i := 0
	if len(src) >= 3 && src[0] == 0xef && src[1] == 0xbb && src[2] == 0xbf {
		i = 3 // BOM stays where it is; it is simply not a token
	}
	for i < len(src) {
		kind, n, err := nextJSONToken(src, i)
		if err != nil {
			return nil, err
		}
		if kind != jSpace {
			toks = append(toks, jsonTok{kind, i, n})
		}
		i += n
	}
	return toks, nil
}

func nextJSONToken(src []byte, i int) (jsonKind, int, error) {
	switch c := src[i]; {
	case c == ' ' || c == '\t' || c == '\r' || c == '\n':
		return jSpace, 1, nil
	case c == '{':
		return jLBrace, 1, nil
	case c == '}':
		return jRBrace, 1, nil
	case c == '[':
		return jLBracket, 1, nil
	case c == ']':
		return jRBracket, 1, nil
	case c == ':':
		return jColon, 1, nil
	case c == ',':
		return jComma, 1, nil
	case c == '/':
		return scanJSONComment(src, i)
	case c == '"':
		n, err := scanJSONString(src, i)
		return jString, n, err
	case c == '-' || (c >= '0' && c <= '9'):
		n := scanJSONNumber(src, i)
		return jScalar, n, nil
	default:
		for _, lit := range []string{"true", "false", "null"} {
			if len(src)-i >= len(lit) && string(src[i:i+len(lit)]) == lit {
				return jScalar, len(lit), nil
			}
		}
		return 0, 0, fmt.Errorf("%w: unexpected byte %q at offset %d", ErrParseFailure, c, i)
	}
}

func scanJSONComment(src []byte, i int) (jsonKind, int, error) {
	if i+1 >= len(src) {
		return 0, 0, fmt.Errorf("%w: lone '/' at offset %d", ErrParseFailure, i)
	}
	switch src[i+1] {
	case '/':
		j := i + 2
		// The terminator is left out of the span: a "\r\n" comment ends before
		// the "\r" so the line ending is never part of a token.
		for j < len(src) && src[j] != '\n' && src[j] != '\r' {
			j++
		}
		return jComment, j - i, nil
	case '*':
		for j := i + 2; j+1 < len(src); j++ {
			if src[j] == '*' && src[j+1] == '/' {
				return jComment, j + 2 - i, nil
			}
		}
		return 0, 0, fmt.Errorf("%w: unterminated block comment at offset %d", ErrParseFailure, i)
	}
	return 0, 0, fmt.Errorf("%w: lone '/' at offset %d", ErrParseFailure, i)
}

// scanJSONString returns the length of the literal including both quotes.
func scanJSONString(src []byte, i int) (int, error) {
	for j := i + 1; j < len(src); j++ {
		switch src[j] {
		case '\\':
			j++ // the escaped byte cannot terminate the string
		case '"':
			return j + 1 - i, nil
		case '\n', '\r':
			return 0, fmt.Errorf("%w: newline in string at offset %d", ErrParseFailure, i)
		}
	}
	return 0, fmt.Errorf("%w: unterminated string at offset %d", ErrParseFailure, i)
}

func scanJSONNumber(src []byte, i int) int {
	j := i
	for j < len(src) {
		c := src[j]
		if (c >= '0' && c <= '9') || c == '-' || c == '+' || c == '.' || c == 'e' || c == 'E' {
			j++
			continue
		}
		break
	}
	return j - i
}
