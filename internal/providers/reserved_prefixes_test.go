package providers

import (
	"sort"
	"strings"
	"testing"
)

// The dashboard suggests a provider-node prefix derived from the node's name,
// and the create/update handlers refuse one that shadows a registry provider.
// Both sides read this set, so it has to contain exactly the keys the conflict
// guard checks — a key missing here is a suggestion the server answers 409 for
// after the operator already accepted it.
func TestReservedProviderPrefixesCoversGuard(t *testing.T) {
	reserved := ReservedProviderPrefixes()

	if !sort.StringsAreSorted(reserved) {
		t.Fatal("ReservedProviderPrefixes must be sorted: the slice is served to the dashboard, and its order is the payload's")
	}

	seen := make(map[string]struct{}, len(reserved))
	for _, name := range reserved {
		if name == "" {
			t.Error("an empty prefix is not reserved: a blank prefix is what the fill replaces")
		}
		if strings.ToLower(name) != name {
			t.Errorf("%q is not lowercase", name)
		}
		if _, dup := seen[name]; dup {
			t.Errorf("%q is listed twice", name)
		}
		seen[name] = struct{}{}
	}

	for id := range KnownProviders {
		if _, ok := seen[id]; !ok {
			t.Errorf("known provider %q is missing from the reserved set", id)
		}
	}
	for alias := range ProviderAliasMap {
		if _, ok := seen[alias]; !ok {
			t.Errorf("provider alias %q is missing from the reserved set", alias)
		}
	}
}

func TestIsReservedProviderPrefix(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
		want   bool
	}{
		{name: "canonical id", prefix: "openai", want: true},
		{name: "alias", prefix: "gh", want: true},
		{name: "case is folded", prefix: "OpenAI", want: true},
		{name: "surrounding space is trimmed", prefix: "  ant  ", want: true},
		{name: "a free namespace is allowed", prefix: "acme-prod", want: false},
		{name: "a prefix that merely starts with an alias is free", prefix: "ghost", want: false},
		{name: "blank is not reserved", prefix: "", want: false},
		{name: "whitespace only is not reserved", prefix: "   ", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsReservedProviderPrefix(tt.prefix); got != tt.want {
				t.Errorf("IsReservedProviderPrefix(%q) = %v, want %v", tt.prefix, got, tt.want)
			}
		})
	}
}

// The set is memoized behind sync.OnceValue, so a test that mutates the maps
// would leak into every later one. Reading it twice must return the same
// contents rather than an empty second result.
func TestReservedProviderPrefixesIsStable(t *testing.T) {
	first := ReservedProviderPrefixes()
	second := ReservedProviderPrefixes()
	if len(first) != len(second) {
		t.Fatalf("lengths differ between calls: %d then %d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("entry %d differs: %q then %q", i, first[i], second[i])
		}
	}
}
