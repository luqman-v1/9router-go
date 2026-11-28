package chat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers/shared"
	"9router/proxy/internal/models"
)

// TestPrewarmLiveCatalogs_WarmsActiveConnections pins that a prewarm pass
// populates the live catalog cache for an active connection without any
// /v1/models request. A later request must hit the cache, not the upstream.
func TestPrewarmLiveCatalogs_WarmsActiveConnections(t *testing.T) {
	resetLiveCatalog(t)

	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"prewarmed-model","context_length":128000}]}`))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`DELETE FROM providerConnections`); err != nil {
		t.Fatalf("delete connections: %v", err)
	}
	connData := `{"apiKey":"sk-nous","baseUrl":"` + srv.URL + `/v1/chat/completions"}`
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-prewarm', 'nous', 'apikey', 'Prewarm', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, connData); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	h := NewChatHandler(db.NewRepo(database))
	h.Client = srv.Client()

	h.PrewarmLiveCatalogs(context.Background())

	conn := &models.ProviderConnection{ID: "conn-prewarm", Provider: "nous", IsActive: 1}
	cd := &shared.ConnectionData{APIKey: "sk-nous", BaseURL: srv.URL + "/v1/chat/completions"}
	key := liveCatalogKey("nous", conn, cd)
	cached, ok := liveCatalogCached(key)
	if !ok || len(cached) != 1 || cached[0].ID != "prewarmed-model" {
		t.Fatalf("cache after prewarm = %v (ok=%v), want [prewarmed-model]", idsOf(cached), ok)
	}

	// A normal resolve now returns from cache without another upstream call.
	got := h.resolveLiveCatalog(context.Background(), conn, cd, "nous")
	if len(got) != 1 || got[0].ID != "prewarmed-model" {
		t.Errorf("resolve after prewarm = %v, want the cached model", idsOf(got))
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("upstream calls = %d, want exactly 1 (the prewarm fetch)", n)
	}
}

// TestPrewarmLiveCatalogs_SkipsInactive pins that a disabled connection does
// not trigger an upstream fetch during prewarm.
func TestPrewarmLiveCatalogs_SkipsInactive(t *testing.T) {
	resetLiveCatalog(t)

	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"should-not-fetch"}]}`))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`DELETE FROM providerConnections`); err != nil {
		t.Fatalf("delete connections: %v", err)
	}
	connData := `{"apiKey":"sk-nous","baseUrl":"` + srv.URL + `/v1/chat/completions"}`
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-inactive', 'nous', 'apikey', 'Inactive', 1, 0, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, connData); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	h := NewChatHandler(db.NewRepo(database))
	h.Client = srv.Client()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	h.PrewarmLiveCatalogs(ctx)

	if n := hits.Load(); n != 0 {
		t.Errorf("upstream calls = %d, want 0 for an inactive connection", n)
	}
}
