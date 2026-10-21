package clientconfig

import "strings"

// One emitter per format, each the single source of truth for how a value
// becomes bytes. The patcher only ever writes a string value, and every escape
// written here is one the same format's parser accepts — an asymmetric escape
// is how a value round-trips into something else.
//
// The escapes are deliberately minimal: no "/" escaping, no escaping of valid
// UTF-8 runes, no rewriting to a different style. Anything more would make the
// output differ from what a human writes, and would break the byte-equality
// idempotence check in Patch (§6 of docs/specs/config-patch-spec.md).

const hexDigits = "0123456789abcdef"

// quoteBasic renders s as a double-quoted string with minimal escapes. Shared
// by JSONC and TOML because they agree on every escape that matters.
// escapeDEL is set for TOML, which forbids a literal 0x7F inside a basic string.
func quoteBasic(s string, escapeDEL bool) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			b.WriteString(`\"`)
		case c == '\\':
			b.WriteString(`\\`)
		case c < 0x20 || (escapeDEL && c == 0x7f):
			b.WriteString(`\u00`)
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&0xf])
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// jsoncQuote renders s as a JSON string literal, quotes included.
func jsoncQuote(s string) string { return quoteBasic(s, false) }

// tomlQuote renders s as a TOML basic string, quotes included.
func tomlQuote(s string) string { return quoteBasic(s, true) }

// yamlQuote renders s as a YAML double-quoted scalar, quotes included.
//
// Double-quoted is the only style that is always safe: a plain scalar's meaning
// depends on context (leading '-', ': ', '#', and the true/null/number
// resolutions), and single quotes cannot express a control character. Choosing
// one style unconditionally is also what makes a second patch byte-identical
// (D4, §4.3.3 step 5).
func yamlQuote(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
			// C0, DEL and C1 are outside YAML's printable set, so they cannot
			// appear literally even inside double quotes.
			b.WriteString(`\u00`)
			b.WriteByte(hexDigits[(r>>4)&0xf])
			b.WriteByte(hexDigits[r&0xf])
		case r == 0x2028 || r == 0x2029:
			// Unicode line/paragraph separators: YAML 1.1 parsers treat these
			// as line breaks, which would fold the scalar.
			b.WriteString(`\u202`)
			b.WriteByte(hexDigits[r&0xf])
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
