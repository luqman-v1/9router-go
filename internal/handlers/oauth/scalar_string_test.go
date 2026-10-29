package oauth

import (
	"testing"
)

// The Copilot token endpoint answers `expires_at` as a unix second count. A
// string-only reader stored it as "", which left every github row with a
// short-lived token and no recorded lifetime: nothing could tell a fresh one
// from an expired one, so the refresh path had nothing to trigger on.
func TestScalarString(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want string
	}{
		{"unix seconds as number", float64(1793000000), "1793000000"},
		{"whole number keeps no decimal", float64(1793000000.0), "1793000000"},
		{"string passes through", "1793000000", "1793000000"},
		{"string is trimmed", "  abc  ", "abc"},
		{"int", 42, "42"},
		{"int64", int64(42), "42"},
		{"bool", true, "true"},
		{"nil", nil, ""},
		{"map is not a scalar", map[string]any{"a": 1}, ""},
		{"slice is not a scalar", []any{1}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := scalarString(tt.in); got != tt.want {
				t.Errorf("scalarString(%#v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
