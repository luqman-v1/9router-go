package handlerutil

import (
	"unique"
)

// Intern returns a canonical, deduplicated string using Go's standard library unique package.
// This reduces heap duplication for high-frequency model identifiers, provider names, and header keys.
func Intern(s string) string {
	if s == "" {
		return ""
	}
	return unique.Make(s).Value()
}

// InternHandle returns a comparable unique.Handle[string] for fast O(1) equality comparisons.
func InternHandle(s string) unique.Handle[string] {
	return unique.Make(s)
}
