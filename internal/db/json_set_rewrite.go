package db

import "strings"

// rewriteJSONSetQuery rewrites SQLite's json_set(data, path, value, ...) into
// a call to the PG shim pg_json_set, whose value arguments are all TEXT.
// Every value argument is wrapped in `(...)::text`; json($x) becomes
// to_jsonb($x::text) first so the polymorphic call has a concrete input.
// The first argument (the JSON document) is left alone.
func rewriteJSONSetQuery(query string) string {
	const name = "json_set("
	idx := findJSONSetCall(query, name, 0)
	if idx < 0 {
		return query
	}
	var b strings.Builder
	b.Grow(len(query) + 64)
	pos := 0
	for idx >= 0 {
		b.WriteString(query[pos:idx])
		b.WriteString("pg_json_set(")
		pos = idx + len(name)

		pos = copyJSONSetArg(&b, query, pos, false)
		for pos < len(query) {
			ws := pos
			for pos < len(query) && (query[pos] == ' ' || query[pos] == '\t' || query[pos] == '\n' || query[pos] == ',') {
				pos++
			}
			if pos >= len(query) || query[pos] == ')' {
				break
			}
			b.WriteString(query[ws:pos])
			b.WriteByte('(')
			pos = copyJSONSetArg(&b, query, pos, true)
			b.WriteString(")::text")
		}
		idx = findJSONSetCall(query, name, pos)
	}
	b.WriteString(query[pos:])
	return b.String()
}

// findJSONSetCall finds the next standalone json_set( call at or after from.
// A preceding identifier byte means pg_json_set( or similar — leave it alone.
func findJSONSetCall(query, name string, from int) int {
	for {
		idx := strings.Index(query[from:], name)
		if idx < 0 {
			return -1
		}
		idx += from
		if idx > 0 && isIdentByte(query[idx-1]) {
			from = idx + len(name)
			continue
		}
		return idx
	}
}

// copyJSONSetArg copies one top-level argument starting at pos, respecting
// nested parentheses and single-quoted strings. With castJSON, json(...) is
// rewritten to to_jsonb(...::text) so the caller's ::text cast yields the
// JSON text rather than a Go-formatted value.
func copyJSONSetArg(b *strings.Builder, query string, pos int, castJSON bool) int {
	start := pos
	depth := 0
	for pos < len(query) {
		c := query[pos]
		switch {
		case c == '\'':
			pos++
			for pos < len(query) {
				if query[pos] == '\'' {
					if pos+1 < len(query) && query[pos+1] == '\'' {
						pos += 2
						continue
					}
					pos++
					break
				}
				pos++
			}
			continue
		case c == '(':
			depth++
		case c == ')':
			if depth == 0 {
				writeJSONSetArg(b, query, start, pos, castJSON)
				return pos
			}
			depth--
		case c == ',' && depth == 0:
			writeJSONSetArg(b, query, start, pos, castJSON)
			return pos
		}
		pos++
	}
	writeJSONSetArg(b, query, start, pos, castJSON)
	return pos
}

func writeJSONSetArg(b *strings.Builder, query string, start, end int, castJSON bool) {
	seg := query[start:end]
	if castJSON {
		trimmed := strings.TrimSpace(seg)
		if strings.HasPrefix(trimmed, "json(") {
			// json($x) → $x::text: the shim parses the value with val::jsonb,
			// so a bare text cast keeps JSON objects as objects.
			inner := strings.TrimSuffix(strings.TrimPrefix(trimmed, "json("), ")")
			seg = strings.Replace(seg, trimmed, inner+"::text", 1)
		}
	}
	b.WriteString(seg)
}
