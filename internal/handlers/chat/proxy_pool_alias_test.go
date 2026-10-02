package chat

import (
	json "encoding/json/v2"
	"testing"

	"9router/proxy/internal/db"
)

// The dashboard stores a provider-level pool under the provider's short alias
// (storageAlias in ProviderDetailView) while requests arrive carrying the
// canonical id. A provider with no hand-written alias pair silently reads no
// pool at all, so the UI shows an assignment that no request ever uses.
func TestResolveProviderProxyPoolID_ReadsEveryProviderKey(t *testing.T) {
	cases := []struct {
		name     string
		storedAs string
		askedFor string
	}{
		{"no-auth provider stored under its alias", "mmf", "mimo-free"},
		{"cline stored under its alias", "cl", "clinepass"},
		{"opencode-go stored under ocg", "ocg", "opencode-go"},
		{"opencode stored under oc", "oc", "opencode"},
		{"opencode-zen stored under ocz", "ocz", "opencode-zen"},
		{"antigravity stored under ag", "ag", "antigravity"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			database, cleanup := setupChatTestDB(t)
			defer cleanup()
			repo := db.NewRepo(database)
			raw := `{"` + tt.storedAs + `":{"proxyPoolId":"pool-x"}}`
			var strategies map[string]any
			if err := json.Unmarshal([]byte(raw), &strategies); err != nil {
				t.Fatalf("decode strategies %q: %v", raw, err)
			}
			if err := repo.UpdateSettingsRaw(map[string]any{"providerStrategies": strategies}); err != nil {
				t.Fatalf("write settings: %v", err)
			}
			h := NewChatHandler(repo)
			if got := h.ResolveProviderProxyPoolID(tt.askedFor); got != "pool-x" {
				t.Errorf("ResolveProviderProxyPoolID(%q) = %q, want pool-x (stored under %q)",
					tt.askedFor, got, tt.storedAs)
			}
		})
	}
}
