package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The wiki documents what the gateway does. Nothing else forced the two to
// agree: a provider added to the registry, or a route registered in a handler,
// shipped without a wiki edit, and the docs described the previous release as
// if it were the contract. These tests are the enforcement. They read the real
// registry and the real wiki/ directory from the repository — not fixtures —
// so a change to either side that drops coverage fails the deploy, which runs
// them through `go test ./tools/sitegen/...` (see .github/workflows/pages.yml).
//
// The provider half is a ratchet, in the shape of web/scripts/
// svelte-check-ratchet.ts: the baseline file pins how many visible providers
// the wiki does not yet mention, and the count may only shrink. A provider
// added to the registry without a wiki mention raises the number and fails;
// fixing the wiki lets the baseline drop.

// baselinePath is the ratchet file, read relative to this test's directory so
// the test works from any working directory.
const baselineFileName = "wiki-baseline.json"

type wikiBaseline struct {
	// UnmentionedProviders is the count of visible (non-hidden) registry
	// providers that no wiki page mentions by ID or display name.
	UnmentionedProviders int `json:"unmentionedProviders"`
}

// providerEntry is one parsed registry entry: the ID and display Name a wiki
// page may mention, and whether the provider is hidden from the dashboard.
type providerEntry struct {
	id     string
	name   string
	hidden bool
}

// registryEntryRe parses one map entry in provider_registry.go. The file is
// generated from the upstream registry with a stable shape: `"id": {` on one
// line, then `ID:`, `Name:`, and the rest of the struct. Parsing it here
// rather than importing the package keeps this test independent of the
// package's compile-time dependencies — and means the test reads the same
// bytes the dashboard ships.
// commentLineRe matches the Go line comments the generated registry places
// between the map key and the struct fields; two entries (sensenova-intl,
// synthetic) carry several of them.
var commentLineRe = regexp.MustCompile(`(?m)^	*//.*\n`)

var registryEntryRe = regexp.MustCompile(
	`"([a-z0-9-]+)": \{\s*\n\s*ID:\s+"([^"]+)",\s*\n\s*Name:\s+"([^"]+)",`)

// hiddenRe matches the Hidden flag inside one entry's body.
var hiddenRe = regexp.MustCompile(`Hidden:\s*true`)

// wordBoundaryRe builds a case-insensitive matcher for a provider ID or name
// that must not be a substring of a longer identifier. "kimi" must not match
// "kimi-coding"; "claude" must not match "claude-code".
func mentionRe(needle string) *regexp.Regexp {
	quoted := regexp.QuoteMeta(strings.ToLower(needle))
	// IDs may contain hyphens; names may contain spaces and punctuation. The
	// boundary is "not an identifier character" on either side.
	return regexp.MustCompile(`(?i)(^|[^a-z0-9])` + quoted + `([^a-z0-9]|$)`)
}

// parseProvidersBody is the single-pass parser: entries with their bodies, so
// the Hidden flag can be read per entry.
func parseProvidersBody(t *testing.T) []providerEntry {
	t.Helper()
	path := filepath.Join("..", "..", "internal", "providers", "provider_registry.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read provider registry: %v", err)
	}
	// Strip line comments first: the generated registry places explanatory
	// comments between a map key and its ID/Name fields, and the entry regex
	// requires those two fields to follow the key directly. Two entries
	// (sensenova-intl, synthetic) carry several such lines.
	src := commentLineRe.ReplaceAllString(string(raw), "")
	ms := registryEntryRe.FindAllStringSubmatchIndex(src, -1)
	if len(ms) == 0 {
		t.Fatalf("provider registry parsed to zero entries; the generated file's shape changed and this test needs its regex updated")
	}
	var out []providerEntry
	for i, m := range ms {
		end := len(src)
		if i+1 < len(ms) {
			end = ms[i+1][0]
		}
		body := src[m[0]:end]
		out = append(out, providerEntry{
			id:     src[m[4]:m[5]],
			name:   src[m[6]:m[7]],
			hidden: hiddenRe.MatchString(body),
		})
	}
	return out
}

// wikiText concatenates every wiki page's markdown, lowercased, so a mention
// anywhere in the wiki counts.
func wikiText(t *testing.T) string {
	t.Helper()
	dir := filepath.Join("..", "..", "wiki")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read wiki dir: %v", err)
	}
	var b strings.Builder
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read wiki page %s: %v", e.Name(), err)
		}
		b.Write(raw)
		b.WriteByte('\n')
	}
	return strings.ToLower(b.String())
}

// unmentionedProviders returns the visible registry providers the wiki never
// mentions, sorted so the failure output is stable.
func unmentionedProviders(t *testing.T) []string {
	t.Helper()
	providers := parseProvidersBody(t)
	wiki := wikiText(t)
	var missing []string
	for _, p := range providers {
		if p.hidden {
			continue
		}
		if mentionRe(p.id).MatchString(wiki) {
			continue
		}
		if p.name != "" && mentionRe(p.name).MatchString(wiki) {
			continue
		}
		missing = append(missing, p.id+" ("+p.name+")")
	}
	sort.Strings(missing)
	return missing
}

// TestWikiMentionsProviders is the ratchet. It fails when a provider is added
// to the registry without a wiki mention, and passes once the wiki catches up
// and the baseline is lowered.
func TestWikiMentionsProviders(t *testing.T) {
	missing := unmentionedProviders(t)

	baselinePath := filepath.Join(baselineFileName)
	raw, err := os.ReadFile(baselinePath)
	if err != nil {
		t.Fatalf("read baseline: %v", err)
	}
	var baseline wikiBaseline
	if err := json.Unmarshal(raw, &baseline); err != nil {
		t.Fatalf("parse baseline: %v", err)
	}

	if len(missing) > baseline.UnmentionedProviders {
		t.Errorf("the wiki does not mention %d providers, above the baseline of %d.\n"+
			"Add each provider to wiki/providers.md (ID or display name), then lower\n"+
			"%s to the new count. Providers added to the registry must be documented\n"+
			"in the same PR.\n\nMissing:\n  %s",
			len(missing), baseline.UnmentionedProviders, baselineFileName,
			strings.Join(missing, "\n  "))
		return
	}

	if len(missing) < baseline.UnmentionedProviders {
		t.Errorf("the wiki now covers %d more providers than the baseline records.\n"+
			"Lower %s from %d to %d so a future provider addition fails the gate\n"+
			"instead of silently passing.",
			baseline.UnmentionedProviders-len(missing), baselineFileName,
			baseline.UnmentionedProviders, len(missing))
	}
}

// wikiRouteRe extracts the (method, path) pairs the wiki's API surface table
// documents, in the `GET /api/keys` shape the page uses.
var wikiRouteRe = regexp.MustCompile(`(?m)^(GET|POST|PUT|DELETE|PATCH)\s+(/[a-zA-Z0-9/{}._-]+)`)

// codeRouteRe extracts a route registration from Go source. chi registers
// routes as r.Get("/path", ...); HandleFunc is method-agnostic.
var codeRouteRe = regexp.MustCompile(
	`\br\.(Get|Post|Put|Delete|Patch|HandleFunc)\("([^"]+)"`)

// routeWildcardRe matches a chi wildcard segment, which the wiki writes as a
// named parameter: /v1/models/* and /v1/models/{id} are the same route.
var routeWildcardRe = regexp.MustCompile(`/\*$`)

// routeParamRe matches a wiki-style parameter segment.
var routeParamRe = regexp.MustCompile(`\{[^}]+\}`)

// normalizeRoute maps both spellings onto one shape so the comparison is about
// the route, not the notation: every parameter or wildcard becomes "{}".
func normalizeRoute(p string) string {
	p = routeParamRe.ReplaceAllString(p, "{}")
	return routeWildcardRe.ReplaceAllString(p, "/{}")
}

// codeRoutes walks the handler tree and collects every registered route.
func codeRoutes(t *testing.T) map[methodPath]bool {
	t.Helper()
	root := filepath.Join("..", "..", "internal")
	routes := map[methodPath]bool{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range codeRouteRe.FindAllStringSubmatch(string(raw), -1) {
			meth := strings.ToUpper(m[1])
			if meth == "HANDLEFUNC" {
				// HandleFunc answers every method; record it once with a
				// wildcard method the comparison treats as matching anything.
				routes[methodPath{method: "*", path: normalizeRoute(m[2])}] = true
				continue
			}
			routes[methodPath{method: meth, path: normalizeRoute(m[2])}] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk handler tree: %v", err)
	}
	return routes
}

// methodPath is one (HTTP method, normalized path) pair.
type methodPath struct {
	method string
	path   string
}

// routeMatches reports whether the wiki's route is served by the code. A
// literal route matches itself. A parameterized route ({id}) matches when the
// code registers a route with the same shape — either a wildcard or a literal
// in that position, because the wiki generalizes per-resource literals
// (/api/media-providers/tts/minimax/voices) into one parameterized line
// (/api/media-providers/tts/{provider}/voices).
func routeMatches(wikiMethod, wikiPath string, code map[methodPath]bool) bool {
	pn := normalizeRoute(wikiPath)
	if code[methodPath{method: wikiMethod, path: pn}] ||
		code[methodPath{method: "*", path: pn}] {
		return true
	}
	if !strings.Contains(pn, "{}") {
		return false
	}
	// Build a regex from the parameterized path and match it against the
	// code's literal routes. Each "{}" consumes one literal segment.
	pattern := regexp.QuoteMeta(pn)
	pattern = strings.ReplaceAll(pattern, `\{\}`, `[^/]+`)
	re := regexp.MustCompile("^" + pattern + "$")
	for r := range code {
		if r.method != wikiMethod && r.method != "*" {
			continue
		}
		if re.MatchString(r.path) {
			return true
		}
	}
	return false
}

// TestWikiRoutesExistInCode fails when the wiki documents a route the gateway
// does not register — the direction that misleads a reader into calling an
// endpoint that 404s. The other direction (routes in code, absent from the
// wiki) is the provider ratchet's problem: documenting every route is a
// writing task, not a correctness one.
func TestWikiRoutesExistInCode(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "wiki", "api.md"))
	if err != nil {
		t.Fatalf("read wiki/api.md: %v", err)
	}
	code := codeRoutes(t)
	var missing []string
	for _, m := range wikiRouteRe.FindAllStringSubmatch(string(raw), -1) {
		method, path := m[1], m[2]
		if !routeMatches(method, path, code) {
			missing = append(missing, method+" "+path)
		}
	}
	if len(missing) > 0 {
		t.Errorf("wiki/api.md documents %d route(s) the gateway does not register.\n"+
			"Either the route was removed and the wiki still documents it, or the\n"+
			"path is spelled differently in code. Fix the wiki or the route.\n\n%s",
			len(missing), strings.Join(missing, "\n"))
	}
}

// TestWikiProviderCountMatchesRegistry fails when the wiki states a provider
// count that no longer matches the registry. The wiki rounds ("40+"), so the
// check reads the number the page actually claims and compares against the
// visible registry count with the same rounding.
func TestWikiProviderCountMatchesRegistry(t *testing.T) {
	providers := parseProvidersBody(t)
	visible := 0
	for _, p := range providers {
		if !p.hidden {
			visible++
		}
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "wiki", "providers.md"))
	if err != nil {
		t.Fatalf("read wiki/providers.md: %v", err)
	}
	// The page writes "supports 40+ providers". Extract the number and the "+",
	// and require the registry count to be at least it — the "+" promises "at
	// least", so a growing registry is fine and a shrinking one is not.
	re := regexp.MustCompile(`supports (\d+)\+? providers`)
	m := re.FindStringSubmatch(string(raw))
	if m == nil {
		t.Fatalf("wiki/providers.md no longer states a provider count in the expected form (\"supports N+ providers\"); update this test or the page")
	}
	var claimed int
	if _, err := fmt.Sscanf(m[1], "%d", &claimed); err != nil {
		t.Fatalf("parse provider count %q: %v", m[1], err)
	}
	if visible < claimed {
		t.Errorf("wiki/providers.md claims %d+ providers but the registry has %d visible.\n"+
			"Update the count on the page.", claimed, visible)
	}
}
