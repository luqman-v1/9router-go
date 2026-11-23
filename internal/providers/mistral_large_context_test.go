package providers

import "testing"

// Set up a live-catalog-shaped source that still reports the old 256k window
// for Mistral Large. The provider pin must win over it for the three native
// Large 4 ids, while every other spelling keeps the catalog/pattern window.
func withMistralCatalog(t *testing.T) {
	t.Helper()
	catalogMu.Lock()
	previous := globalCatalog
	globalCatalog = &SyncedCatalog{
		Providers: map[string]map[string]SyncedModelLimits{
			"mistral": {
				"mistral-large-latest": {ContextWindow: 256000, MaxOutput: 65536},
				"mistral-large-4":      {ContextWindow: 256000, MaxOutput: 65536},
				"mistral-large-4-0":    {ContextWindow: 256000, MaxOutput: 65536},
				"mistral-large-2411":   {ContextWindow: 256000, MaxOutput: 65536},
				"codestral-latest":     {ContextWindow: 256000, MaxOutput: 65536},
			},
			"other-provider": {
				"mistral-large-latest": {ContextWindow: 256000, MaxOutput: 65536},
			},
		},
	}
	catalogMu.Unlock()
	t.Cleanup(func() {
		catalogMu.Lock()
		globalCatalog = previous
		catalogMu.Unlock()
		InvalidateCapabilitiesCache()
	})
}

func TestMistralLarge4ContextWindow(t *testing.T) {
	withMistralCatalog(t)

	tests := []struct {
		name     string
		provider string
		model    string
		wantCW   int
	}{
		// Native Mistral ids are pinned to 1M even though the catalog says 256k.
		{"latest alias", "mistral", "mistral-large-latest", 1048576},
		{"large 4", "mistral", "mistral-large-4", 1048576},
		{"large 4.0", "mistral", "mistral-large-4-0", 1048576},
		// Older Large ids and reseller/codestral spellings must not inherit it.
		{"older dated large", "mistral", "mistral-large-2411", 256000},
		{"other provider", "other-provider", "mistral-large-latest", 256000},
		{"codestral", "mistral", "codestral-latest", 256000},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			InvalidateCapabilitiesCache()
			got := GetCapabilitiesDetailForModel(tc.provider, tc.model)
			if got.ContextWindow != tc.wantCW {
				t.Errorf("ContextWindow = %d, want %d", got.ContextWindow, tc.wantCW)
			}
			if tc.wantCW == 1048576 && (!got.Vision || !got.Tools) {
				t.Errorf("pinned Large 4 must keep Vision+Tools, got %+v", got)
			}
		})
	}
}
