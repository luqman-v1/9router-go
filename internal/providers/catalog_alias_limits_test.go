package providers

import "testing"

// /v1/models publishes providers under their short alias (`oc`), while
// models.dev files the very same limits under the canonical id (`opencode`).
// A lookup through either spelling must land on the same catalog entry — the
// published context window must not silently fall back to the substring table.
func TestCatalogProviderKeys_AliasAndCanonicalShareEntries(t *testing.T) {
	for _, provider := range []string{"oc", "opencode"} {
		keys := catalogProviderKeys(provider)
		found := map[string]bool{}
		for _, key := range keys {
			found[key] = true
		}
		for _, want := range []string{"oc", "opencode"} {
			if !found[want] {
				t.Errorf("catalogProviderKeys(%q) = %v, missing %q", provider, keys, want)
			}
		}
	}
}

func TestCatalogLimits_AliasResolvesToCanonicalProvider(t *testing.T) {
	if err := LoadCatalogFromFile("testdata/model-catalog.json"); err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	t.Cleanup(func() {
		catalogMu.Lock()
		globalCatalog = nil
		catalogMu.Unlock()
	})

	for _, provider := range []string{"oc", "opencode"} {
		cw, maxOut := GetCatalogLimits(provider, "glm-5.3-flash-free")
		if cw == 0 || maxOut == 0 {
			t.Errorf("GetCatalogLimits(%q, glm-5.3-flash-free) = (%d, %d), want nonzero", provider, cw, maxOut)
		}
	}

	// provider-qualified limits are keyed by canonical id, but /v1/models
	// publishes `oc`, so both spellings must report the same window.
	InvalidateCapabilitiesCache()
	t.Cleanup(InvalidateCapabilitiesCache)
	canonical := GetCapabilitiesDetailForModel("opencode", "deepseek-v4.1-flash-free")
	alias := GetCapabilitiesDetailForModel("oc", "deepseek-v4.1-flash-free")
	if alias.ContextWindow != canonical.ContextWindow || alias.MaxOutput != canonical.MaxOutput {
		t.Errorf("oc detail = (%d, %d), opencode detail = (%d, %d)", alias.ContextWindow, alias.MaxOutput, canonical.ContextWindow, canonical.MaxOutput)
	}
}
