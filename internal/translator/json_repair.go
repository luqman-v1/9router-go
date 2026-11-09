package translator

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"strings"
)

// RepairTruncatedJSON attempts to close open string literals and open brackets/braces
// in JSON that was cut off mid-stream or truncated by upstream max_tokens limits.
// If the input is already valid JSON, it is returned unchanged.
// If the input cannot be repaired, it returns the original string.
func RepairTruncatedJSON(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "{}"
	}
	if jsontext.Value(s).IsValid() {
		return s
	}

	var stack []byte
	inString := false
	escape := false

	for i := 0; i < len(s); i++ {
		c := s[i]
		if inString {
			if escape {
				escape = false
			} else if c == '\\' {
				escape = true
			} else if c == '"' {
				inString = false
			}
		} else {
			switch c {
			case '"':
				inString = true
			case '{':
				stack = append(stack, '}')
			case '[':
				stack = append(stack, ']')
			case '}':
				if len(stack) > 0 && stack[len(stack)-1] == '}' {
					stack = stack[:len(stack)-1]
				}
			case ']':
				if len(stack) > 0 && stack[len(stack)-1] == ']' {
					stack = stack[:len(stack)-1]
				}
			}
		}
	}

	res := s
	if inString {
		if escape {
			res = res[:len(res)-1]
		}
		res += `"`
	}

	trimmed := strings.TrimRight(res, " \t\r\n")
	if strings.HasSuffix(trimmed, ":") {
		res = trimmed + `""`
	} else if strings.HasSuffix(trimmed, ",") {
		res = strings.TrimRight(trimmed, ",")
	}

	for i := len(stack) - 1; i >= 0; i-- {
		res += string(stack[i])
	}

	if jsontext.Value(res).IsValid() {
		return res
	}
	return s
}

// SafeToolCallInput ensures that a tool call input string is valid JSON so it can
// be safely marshaled as jsontext.Value into a Claude message content block without
// causing marshal errors.
func SafeToolCallInput(raw string) jsontext.Value {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return jsontext.Value("{}")
	}
	if jsontext.Value(raw).IsValid() {
		return jsontext.Value(raw)
	}
	repaired := RepairTruncatedJSON(raw)
	if jsontext.Value(repaired).IsValid() {
		return jsontext.Value(repaired)
	}
	b, err := json.Marshal(map[string]any{"raw": raw})
	if err == nil && jsontext.Value(b).IsValid() {
		return jsontext.Value(b)
	}
	return jsontext.Value("{}")
}
