package providers

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// glm-5.4 is served by the ZCode start-plan lane but models.dev files its
// limits under the coding-plan catalog id, not the gateway provider id. The
// static cap must not fall through to the `*glm-5*` 200k pattern, and the
// catalog alias must make the feed authoritative when a catalog is loaded.
func TestZcodeGlm54StaticLimits(t *testing.T) {
	if !slices.Contains(catalogProviderKeys("zcode"), "zai-coding-plan") {
		t.Fatalf("catalogProviderKeys(zcode) = %v, missing zai-coding-plan", catalogProviderKeys("zcode"))
	}

	d := GetCapabilitiesDetailForModel("zcode", "glm-5.4")
	if d.ContextWindow != 1000000 || d.MaxOutput != 131072 {
		t.Errorf("zcode/glm-5.4 static detail = (%d, %d), want (1000000, 131072)", d.ContextWindow, d.MaxOutput)
	}
	if d.ThinkingFormat == nil || *d.ThinkingFormat != "zai" {
		t.Errorf("zcode/glm-5.4 thinking format = %v, want zai", d.ThinkingFormat)
	}
}

func TestZcodeGlm54CatalogAliasLimits(t *testing.T) {
	catalogMu.RLock()
	previous := globalCatalog
	catalogMu.RUnlock()
	t.Cleanup(func() {
		catalogMu.Lock()
		globalCatalog = previous
		catalogMu.Unlock()
		InvalidateCapabilitiesCache()
	})

	path := filepath.Join(t.TempDir(), "model-catalog.json")
	body := `{"providers":{"zai-coding-plan":{"glm-5.4":{"contextWindow":1000000,"maxOutput":131072}}}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write catalog fixture: %v", err)
	}
	if err := LoadCatalogFromFile(path); err != nil {
		t.Fatalf("load catalog fixture: %v", err)
	}

	cw, maxOut := GetCatalogLimits("zcode", "glm-5.4")
	if cw != 1000000 || maxOut != 131072 {
		t.Errorf("GetCatalogLimits(zcode, glm-5.4) = (%d, %d), want (1000000, 131072)", cw, maxOut)
	}
	d := GetCapabilitiesDetailForModel("zcode", "glm-5.4")
	if d.ContextWindow != 1000000 || d.MaxOutput != 131072 {
		t.Errorf("zcode/glm-5.4 resolved detail = (%d, %d), want (1000000, 131072)", d.ContextWindow, d.MaxOutput)
	}
}
