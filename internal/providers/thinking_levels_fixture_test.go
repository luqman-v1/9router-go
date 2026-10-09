package providers

import (
	json "encoding/json/v2"
	"os"
	"slices"
	"testing"
)

// upstreamLevelsFixtureVersion is the upstream tag the fixture was captured
// from. The fixture is the only guard on the whole resolver, so a stale capture
// makes the test pass while the code is wrong — that is exactly what happened
// once: the v0.5.91 MiMo pattern rows were missing here and the fixture pinned
// the older v0.5.86 values, so 15 (provider, model) pairs diverged unnoticed.
// When upstream moves, re-capture and bump this:
//
//	DUMP_CATALOG_PAIRS=testdata/catalog_pairs.json \
//	  go test ./internal/providers/ -run TestDumpCatalogPairs -count=1
//	node scripts/gen-thinking-levels.mjs <upstream-checkout> v0.5.99 \
//	  internal/providers/testdata/catalog_pairs.json
const upstreamLevelsFixtureVersion = "v0.5.99"

// pendingUpstreamDivergences lists (provider, model) pairs where the Go
// resolver intentionally answers differently from the captured tag. Each entry
// names why, so the exemption can be deleted the moment it no longer holds.
//
// decolua/9router#4614 hoists the provider-qualified codebuddy-cn rows above
// the unqualified `*deepseek-v4.*` glob, which changes what
// codebuddy-cn/deepseek-v4.1-flash resolves to: the tag's answer is the
// generic set, the PR's is codebuddy-cn's published set.
var pendingUpstreamDivergences = map[string]map[string][]string{
	// Upstream #4614 also dropped glm-5v-turbo / kimi-k2.7 / kimi-k2.6 from
	// the codebuddy-cn capability table, and v0.5.99 keeps them dropped. Go
	// keeps the rows so a saved combo naming one stays routable, which leaves
	// it on the openai format default instead of the zai/kimi set upstream
	// no longer publishes for them. See the comment on those rows in
	// capabilities.go. These are Go-intentional, not pending.
	"codebuddy-cn": {
		"deepseek-v4.1-flash": {"low", "high", "max"},
		"glm-5v-turbo":        {"minimal", "low", "medium", "high", "xhigh"},
		"kimi-k2.7":           {"minimal", "low", "medium", "high", "xhigh"},
		"kimi-k2.6":           {"minimal", "low", "medium", "high", "xhigh"},
	},
	// Upstream has no agnes entry in capabilities.js at all, so it publishes
	// no reasoning levels for the provider. Go declares agnes-3.0-pro and
	// agnes-3.0-flash as reasoning models (they stream reasoning_content), so
	// the picker offers the openai format default. Publishing reasoning to a
	// client that would rather have no levels is a worse outcome than
	// diverging here.
	"agnes": {
		"agnes-3.0-pro":   {"none", "minimal", "low", "medium", "high", "xhigh"},
		"agnes-3.0-flash": {"none", "minimal", "low", "medium", "high", "xhigh"},
	},
}

// divergentFromPending reports whether a mismatch is one of the documented
// pending-PR divergences, in which case it is expected rather than a failure.
func divergentFromPending(provider, model string, got []string) bool {
	want, ok := pendingUpstreamDivergences[provider][model]
	return ok && slices.Equal(got, want)
}

// upstreamLevelsFixture is what open-sse/providers/thinkingLevels.js returns for
// every (provider, model) pair in the Go model catalog, captured from the
// upstream checkout. It pins the port against the real implementation instead of
// a hand-written subset.
var upstreamLevelsFixture = "testdata/thinking_levels.json"

type thinkingLevelsFixture struct {
	UpstreamVersion string                         `json:"upstreamVersion"`
	Models          map[string]map[string][]string `json:"models"`
}

// TestGetThinkingLevels_MatchesUpstreamFixture compares the Go resolver against
// the upstream one for the whole catalog. A diff here means a level set, pattern
// row or capability declaration drifted; the dashboard picker would then offer a
// level the provider rejects, or hide one it accepts.
func TestGetThinkingLevels_MatchesUpstreamFixture(t *testing.T) {
	raw, err := os.ReadFile(upstreamLevelsFixture)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fixture thinkingLevelsFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if fixture.UpstreamVersion != upstreamLevelsFixtureVersion {
		t.Fatalf("fixture captured from %s, test expects %s — re-capture it",
			fixture.UpstreamVersion, upstreamLevelsFixtureVersion)
	}
	want := fixture.Models

	var checked, mismatched int
	pending := make([]string, 0, 8)
	for provider, models := range want {
		for model, wantLevels := range models {
			checked++
			InvalidateCapabilitiesCache()
			got := GetThinkingLevels(provider, model)

			if slices.Equal(got, wantLevels) {
				continue
			}
			if divergentFromPending(provider, model, got) {
				pending = append(pending, provider+"/"+model)
				continue
			}
			mismatched++
			if mismatched <= 20 {
				t.Errorf("%s/%s: got %v, upstream %v", provider, model, got, wantLevels)
			}
		}
	}

	if mismatched > 0 {
		t.Errorf("%d of %d (provider, model) pairs diverge from upstream", mismatched, checked)
	}
	if len(pending) > 0 {
		t.Logf("%d pair(s) intentionally ahead of %s, pending an upstream merge: %v",
			len(pending), upstreamLevelsFixtureVersion, pending)
	}
	t.Logf("verified %d (provider, model) pairs against %s", checked, upstreamLevelsFixture)
}
