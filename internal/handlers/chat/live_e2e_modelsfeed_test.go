package chat

import (
	"context"
	"testing"
	"time"

	json "encoding/json/v2"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers/shared"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
)

// TestLiveE2E_ModelsFeedWindow runs the live-feed resolver against the real
// provider feeds (no fake upstream) and asserts the context window the provider
// itself declares comes back authoritative. This is the end-to-end proof for
// the reported nous/qwen4-max 131k-vs-1M case: the resolver must read the real
// /v1/models, not a static family guess.
//
// Gated on 9ROUTER_LIVE_TESTS=1 like every other live test. nous publishes a
// public catalogue (placeholder bearer suffices); opencode zen/go reject an
// invalid bearer, so those lanes use the operator's real credential from
// ~/.9router/db/data.sqlite via getRealUserDB and skip when none is configured.
func TestLiveE2E_ModelsFeedWindow(t *testing.T) {
	requireLiveUpstream(t)
	resetLiveCatalog(t)

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	h := NewChatHandler(db.NewRepo(database))

	// nous: public catalogue, placeholder bearer exercises the production path.
	for _, model := range []string{"qwen/qwen4-max", "qwen/qwen4-max:free"} {
		t.Run("nous/"+model, func(t *testing.T) {
			conn := &models.ProviderConnection{ID: "live-nous", Provider: "nous", IsActive: 1}
			connData := &shared.ConnectionData{APIKey: "public-catalog-probe"}
			assertLiveWindow(t, h, conn, connData, "nous", model, 1000000)
		})
	}

	// opencode zen/go: real credential required (invalid bearer -> auth error).
	repo, cleanup := getRealUserDB(t)
	defer cleanup()
	hReal := NewChatHandler(repo)
	zenGo := []struct {
		provider string
		model    string
		wantCW   int
	}{
		{"opencode-zen", "mimo-v2.6-flash-free", 200000},
		{"opencode-go", "hy4-preview", 1024000},
		{"opencode-go", "muse-spark-1.3-contributor", 1048576},
	}
	for _, tc := range zenGo {
		t.Run(tc.provider+"/"+tc.model, func(t *testing.T) {
			conns, err := repo.GetProviderConnections(tc.provider, true)
			if err != nil || len(conns) == 0 {
				t.Skipf("no active %s connection configured", tc.provider)
			}
			conn := conns[0]
			var cd shared.ConnectionData
			if json.Unmarshal([]byte(conn.Data), &cd) != nil {
				t.Skipf("cannot parse %s connection data", tc.provider)
			}
			assertLiveWindow(t, hReal, conn, &cd, tc.provider, tc.model, tc.wantCW)
		})
	}

	// Cross-check: the static fallback (what a client gets when the feed is
	// unreachable) must also carry the 1M window now, not the old 131k floor.
	t.Run("static-fallback-nous-qwen4-max", func(t *testing.T) {
		providers.InvalidateCapabilitiesCache()
		d := providers.GetCapabilitiesDetailForModel("nous", "qwen/qwen4-max")
		t.Logf("static nous/qwen/qwen4-max -> cw=%d max=%d", d.ContextWindow, d.MaxOutput)
		if d.ContextWindow != 1000000 {
			t.Errorf("static fallback contextWindow = %d, want 1000000", d.ContextWindow)
		}
	})
}

// assertLiveWindow resolves the provider's real feed and asserts the model's
// declared window comes back authoritative and equal to wantCW.
func assertLiveWindow(t *testing.T, h *ChatHandler, conn *models.ProviderConnection, connData *shared.ConnectionData, providerID, model string, wantCW int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	got := h.resolveLiveCatalog(ctx, conn, connData, providerID)
	if len(got) == 0 {
		t.Skipf("%s feed unreachable or empty (not a gateway defect)", providerID)
	}
	var entry *LiveModel
	for i := range got {
		if got[i].ID == model {
			entry = &got[i]
			break
		}
	}
	if entry == nil {
		t.Skipf("%s not in live %s feed right now (%d entries)", model, providerID, len(got))
	}
	t.Logf("live %s/%s -> contextLength=%d maxOutput=%d authoritative=%v",
		providerID, model, entry.ContextLength, entry.MaxOutput, entry.LimitsAuthoritative)
	if !entry.LimitsAuthoritative {
		t.Errorf("%s/%s: feed entry not authoritative — resolver lost the provider's window", providerID, model)
	}
	if entry.ContextLength != wantCW {
		t.Errorf("%s/%s contextLength = %d, want provider-declared %d", providerID, model, entry.ContextLength, wantCW)
	}
}
