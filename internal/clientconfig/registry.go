package clientconfig

import (
	"fmt"
	"sort"
	"strings"
)

// The nine clients `connect` targets, in the order
// docs/specs/client-schema-matrix.md §1.1 lists them. The list is the source of
// truth that `--tools` validates against, and `all` expands to it.
//
// Aliases come from the same place (connectTools.js:540-543): they exist
// because users type both forms, and refusing one of them would be a needless
// failure.

var adapterFactories = []func() Adapter{
	newClaudeAdapter,
	newCodexAdapter,
	newOpenCodeAdapter,
	newDroidAdapter,
	newCrushAdapter,
	newKiloAdapter,
	newClineAdapter,
	newPiAdapter,
	newOmpAdapter,
}

// adapterAliases maps every accepted spelling to its canonical id.
var adapterAliases = map[string]string{
	"claude":          "claude",
	"claude-code":     "claude",
	"claudecode":      "claude",
	"codex":           "codex",
	"opencode":        "opencode",
	"droid":           "droid",
	"factory":         "droid",
	"crush":           "crush",
	"kilo":            "kilo",
	"kilocode":        "kilo",
	"cline":           "cline",
	"pi":              "pi",
	"pi-coding-agent": "pi",
	"omp":             "omp",
	"oh-my-pi":        "omp",
}

// registry is built once: the adapters are stateless values, so a shared map is
// safe and keeps Resolve from allocating on every lookup.
var registry = func() map[string]Adapter {
	m := make(map[string]Adapter, len(adapterFactories))
	for _, f := range adapterFactories {
		a := f()
		m[a.ID()] = a
	}
	return m
}()

// IDs returns the canonical ids in their declared order.
func IDs() []string {
	out := make([]string, 0, len(adapterFactories))
	for _, f := range adapterFactories {
		out = append(out, f().ID())
	}
	return out
}

// Resolve turns a user-typed tool name into an adapter.
//
// "all" expands to every adapter, in declared order. An unknown name is an
// error naming what is accepted, rather than a silent skip: a connect that
// quietly ignored a misspelled tool would configure nothing and report success.
func Resolve(name string) (Adapter, error) {
	id, ok := adapterAliases[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return nil, fmt.Errorf("%w: unknown tool %q (accepted: %s)",
			ErrUnsupportedStructure, name, strings.Join(knownNames(), ", "))
	}
	return registry[id], nil
}

// ResolveAll resolves a list of names, expanding "all" and dropping duplicates
// while keeping the first mention's position.
func ResolveAll(names []string) ([]Adapter, error) {
	seen := make(map[string]struct{}, len(names))
	var out []Adapter
	add := func(a Adapter) {
		if _, dup := seen[a.ID()]; dup {
			return
		}
		seen[a.ID()] = struct{}{}
		out = append(out, a)
	}
	for _, n := range names {
		if strings.EqualFold(strings.TrimSpace(n), "all") {
			for _, id := range IDs() {
				add(registry[id])
			}
			continue
		}
		a, err := Resolve(n)
		if err != nil {
			return nil, err
		}
		add(a)
	}
	return out, nil
}

// knownNames lists the canonical ids and aliases for an error message.
func knownNames() []string {
	out := make([]string, 0, len(adapterAliases))
	for k := range adapterAliases {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
