package chat

import (
	"slices"
	"testing"
)

// TestAccessCandidatesDropTheThinkingSuffix pins the candidate derivation: a
// "(level)" suffix is a request override, not part of the model id, so an
// allowlist written as "gpt-5" covers a request for "gpt-5(high)". Without
// the strip the suffixed spelling matched neither the bare entry nor the
// provider-qualified one, and the same key was allowed on one endpoint and
// denied on another.
func TestAccessCandidatesDropTheThinkingSuffix(t *testing.T) {
	h := &ChatHandler{}
	candidates := h.accessCandidates("gpt-5(high)")
	if !slices.Contains(candidates, "gpt-5") {
		t.Fatalf("candidates = %v, want the suffix-free model among them", candidates)
	}
}

// TestModelAllowedPatterns pins issue #365's pattern contract for the per-key
// model allowlist: cross-segment wildcards, deny entries (!term / -term), and a
// deny-only list that opens everything it does not name.
//
// The decision function is the single gate dispatch and listing both call, so
// pinning it here pins both.
func TestModelAllowedPatterns(t *testing.T) {
	cases := []struct {
		name       string
		patterns   []string
		candidates []string
		want       bool
	}{
		{"no allowlist allows everything", nil, []string{"openai/gpt-4o"}, true},
		{"blank entries are not an allowlist", []string{" ", ""}, []string{"openai/gpt-4o"}, true},
		{"exact entry", []string{"openai/gpt-4o"}, []string{"openai/gpt-4o"}, true},
		{"exact entry misses another model", []string{"openai/gpt-4o"}, []string{"openai/gpt-4o-mini"}, false},

		// The gap: the resolver's own glob is segment-local, so `*claude*` never
		// reached `anthropic/claude-sonnet-4`.
		{"wildcard spanning the slash", []string{"*claude*"}, []string{"anthropic/claude-sonnet-4"}, true},
		{"wildcard spanning both sides", []string{"*haiku*"}, []string{"anthropic/claude-haiku-4"}, true},
		{"provider prefix wildcard", []string{"oc/*"}, []string{"oc/gpt-5"}, true},
		{"provider prefix wildcard misses other providers", []string{"oc/*"}, []string{"openai/gpt-4o"}, false},
		{"wildcard is case-insensitive", []string{"*CLAUDE*"}, []string{"anthropic/claude-sonnet-4"}, true},

		// Deny entries.
		{"deny-only opens what it does not name", []string{"!opus"}, []string{"anthropic/claude-sonnet-4"}, true},
		{"deny-only denies the named term", []string{"!opus"}, []string{"anthropic/opus-4"}, false},
		{"bare deny term matches as a substring", []string{"-haiku"}, []string{"anthropic/claude-haiku-4"}, false},
		{"bare deny term still allows the rest", []string{"-haiku"}, []string{"openai/gpt-4o"}, true},
		{"deny beats a wildcard allow", []string{"*", "!opus"}, []string{"google/opus-3"}, false},
		{"deny beats an explicit allow", []string{"anthropic/*", "-opus"}, []string{"anthropic/opus-4"}, false},
		{"allow still works beside a deny", []string{"anthropic/*", "-haiku"}, []string{"anthropic/claude-sonnet-4"}, true},
		{"deny matches either candidate spelling", []string{"!claude"}, []string{"claude-sonnet-4", "anthropic/claude-sonnet-4"}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := modelAllowed(tc.patterns, tc.candidates...); got != tc.want {
				t.Errorf("modelAllowed(%q, %q) = %v, want %v", tc.patterns, tc.candidates, got, tc.want)
			}
		})
	}
}
