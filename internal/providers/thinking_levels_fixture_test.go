package providers

import (
	"encoding/json"
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
//	node scripts/gen-thinking-levels.mjs <upstream-checkout> v0.5.95 \
//	  internal/providers/testdata/catalog_pairs.json
const upstreamLevelsFixtureVersion = "v0.5.95"

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
	for provider, models := range want {
		for model, wantLevels := range models {
			checked++
			InvalidateCapabilitiesCache()
			got := GetThinkingLevels(provider, model)

			if !slices.Equal(got, wantLevels) {
				mismatched++
				if mismatched <= 20 {
					t.Errorf("%s/%s: got %v, upstream %v", provider, model, got, wantLevels)
				}
			}
		}
	}

	if mismatched > 0 {
		t.Errorf("%d of %d (provider, model) pairs diverge from upstream", mismatched, checked)
	}
	t.Logf("verified %d (provider, model) pairs against %s", checked, upstreamLevelsFixture)
}
