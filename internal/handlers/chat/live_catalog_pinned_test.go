package chat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers/shared"
	"9router/proxy/internal/models"
)

// TestResolveLiveCatalogPinnedEnabledModelsSkipsFetch pins that a connection
// carrying an operator-pinned model list never reaches the provider. The pinned
// list is the whole catalog for that connection, and the fetch is what made
// /v1/models walk one upstream call per connection — and one per combo leaf
// through liveCapabilityLeaf, which does not consult enabledModels.
func TestResolveLiveCatalogPinnedEnabledModelsSkipsFetch(t *testing.T) {
	resetLiveCatalog(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Write([]byte(`{"data":[{"id":"m-one"}]}`))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	h := NewChatHandler(db.NewRepo(database))

	conn := &models.ProviderConnection{
		ID:       "conn-pinned-skip",
		Provider: "openai-compatible-pinned",
		IsActive: 1,
	}
	connData := &shared.ConnectionData{
		APIKey: "sk-pinned",
		ProviderSpecificData: map[string]any{
			"baseUrl":       srv.URL,
			"enabledModels": []any{"m-one"},
		},
	}

	if got := h.resolveLiveCatalog(context.Background(), conn, connData, "openai-compatible-pinned"); got != nil {
		t.Fatalf("pinned connection must not return a live catalog, got %v", got)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("upstream calls = %d, want 0 for a pinned connection", got)
	}
}

// TestResolveLiveCatalogCachesFailure pins the negative cache. A failed catalog
// fetch used to be stored nowhere, so every combo leaf re-ran it: with hundreds
// of combos and thousands of connections a single /v1/models request sat in
// sequential 10s timeouts and never answered.
func TestResolveLiveCatalogCachesFailure(t *testing.T) {
	resetLiveCatalog(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	h := NewChatHandler(db.NewRepo(database))

	conn := &models.ProviderConnection{
		ID:       "conn-negative-cache",
		Provider: "openai-compatible-negcache",
		IsActive: 1,
	}
	connData := &shared.ConnectionData{
		APIKey:               "sk-neg",
		ProviderSpecificData: map[string]any{"baseUrl": srv.URL},
	}
	for range 3 {
		if got := h.resolveLiveCatalog(context.Background(), conn, connData, "openai-compatible-negcache"); got != nil {
			t.Fatalf("failed fetch must return nil, got %v", got)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("upstream calls = %d, want 1: a failed fetch must be cached", got)
	}
}
