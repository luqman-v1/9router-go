package providers

import (
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"testing"
)

// TestDumpCatalogPairs is the Go half of scripts/gen-thinking-levels.mjs: it
// writes the (provider, model) pair list the upstream fixture is captured over,
// taken straight from the catalog this port serves so a new provider or model
// cannot slip past the parity test. It never asserts anything — the fixture
// test does that — and it only writes when the harness names a target. The
// path resolves against the test binary's own directory, so the value is
// relative to internal/providers:
//
//	DUMP_CATALOG_PAIRS=testdata/catalog_pairs.json \
//	  go test ./internal/providers/ -run TestDumpCatalogPairs -count=1
func TestDumpCatalogPairs(t *testing.T) {
	target := os.Getenv("DUMP_CATALOG_PAIRS")
	if target == "" {
		t.Skip("DUMP_CATALOG_PAIRS not set; nothing to dump")
	}

	raw, err := json.Marshal(ProviderModels)
	if err != nil {
		t.Fatalf("marshal catalog: %v", err)
	}
	if dir := filepath.Dir(target); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(target, raw, 0o644); err != nil {
		t.Fatalf("write %s: %v", target, err)
	}
	t.Logf("dumped %d providers", len(ProviderModels))
}
