package chat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers/shared"
	"9router/proxy/internal/models"
)

// TestParseCommandcodeLiveModels pins the two things the CLI feed carries that
// the public /v1/models list does not: the `hidden` withdrawal flag, and the
// provider's own per-model context_length.
func TestParseCommandcodeLiveModels(t *testing.T) {
	body := `{"object":"list","catalogVersion":"abc","data":[
		{"id":"claude-sonnet-5-5","name":"Claude Sonnet 5.5","context_length":1000000,"display":{"label":"Claude Sonnet 5.5"},"hidden":false},
		{"id":"xiaomi/mimo-v2.5","name":"MiMo v2.5","context_length":262144,"hidden":true},
		{"id":"gpt-6-astra","name":"GPT-6 Astra","context_length":1050000,"display":{"label":"GPT-6 Astra"}},
		{"id":"gpt-6-astra","name":"duplicate","context_length":1},
		{"id":"nvidia/nemotron-3-ultra-550b-a55b","name":"Nemotron","context_length":200000},
		{"id":"","name":"no id","context_length":1000}
	]}`

	got := parseCommandcodeLiveModels([]byte(body))
	want := []LiveModel{
		{ID: "claude-sonnet-5-5", Name: "Claude Sonnet 5.5", ContextLength: 1000000, LimitsAuthoritative: true},
		{ID: "gpt-6-astra", Name: "GPT-6 Astra", ContextLength: 1050000, LimitsAuthoritative: true},
		{ID: "nvidia/nemotron-3-ultra-550b-a55b", Name: "Nemotron", ContextLength: 200000, LimitsAuthoritative: true},
	}
	if len(got) != len(want) {
		t.Fatalf("parsed %v, want %v", idsOf(got), idsOf(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	if got := parseCommandcodeLiveModels([]byte(`not-json`)); got != nil {
		t.Errorf("unparseable body must yield nothing, got %v", idsOf(got))
	}
}

// TestResolveLiveCatalogCommandcode pins the request the resolver dials and the
// filter it applies.
//
// Both halves are load-bearing. Without `view=client` the feed answers with the
// public list, which has no `hidden` flag, so a withdrawn model is offered
// again. Without the registry's CLI identity headers the same URL answers 403 —
// it is the User-Agent that is checked, not the bearer, so a connection whose
// key was revoked must still discover the catalogue.
func TestResolveLiveCatalogCommandcode(t *testing.T) {
	resetLiveCatalog(t)
	var (
		calls   atomic.Int32
		gotView string
		gotUA   string
		gotVer  string
		gotAuth string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		gotView = r.URL.RawQuery
		gotUA = r.Header.Get("User-Agent")
		gotVer = r.Header.Get("x-command-code-version")
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"object":"list","data":[
			{"id":"claude-sonnet-5-5","name":"Claude Sonnet 5.5","context_length":1000000},
			{"id":"reflection/beam:free","name":"Beam 1.0","context_length":1048576,"hidden":true}
		]}`))
	}))
	defer srv.Close()
	prevURL := commandcodeCatalogURL
	commandcodeCatalogURL = srv.URL + "?view=client"
	t.Cleanup(func() { commandcodeCatalogURL = prevURL })

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	h := NewChatHandler(db.NewRepo(database))
	h.Client = srv.Client()

	conn := &models.ProviderConnection{ID: "conn-cmc-live", Provider: "commandcode", IsActive: 1}
	connData := &shared.ConnectionData{APIKey: "user_dead_key"}

	for _, providerID := range []string{"commandcode", "cmc"} {
		got := h.resolveLiveCatalog(context.Background(), conn, connData, providerID)
		if len(got) != 1 || got[0].ID != "claude-sonnet-5-5" {
			t.Fatalf("%s: resolved %v, want only the visible model", providerID, idsOf(got))
		}
		if got[0].ContextLength != 1000000 || !got[0].LimitsAuthoritative {
			t.Errorf("%s: entry = %+v, want the feed's own authoritative window", providerID, got[0])
		}
	}
	// Two calls because the cache key is seeded with the requested provider id,
	// not its canonical form — one cache entry per spelling, as for every other
	// resolver. A third call must not reach upstream again.
	if _, ok := liveCatalogCached(liveCatalogKey("commandcode", conn, connData)); !ok {
		t.Error("the fetched catalogue must be cached")
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("upstream calls = %d, want 2 (one per provider spelling)", n)
	}
	if gotView != "view=client" {
		t.Errorf("query = %q, want view=client", gotView)
	}
	if gotUA != "cli" || gotVer == "" {
		t.Errorf("CLI identity headers missing: User-Agent=%q x-command-code-version=%q", gotUA, gotVer)
	}
	if gotAuth != "Bearer user_dead_key" {
		t.Errorf("Authorization = %q, want the connection's key", gotAuth)
	}
}

// TestHandleModels_CommandcodeLiveCatalogOvertakesStatic pins the listing the
// operator sees: the CLI feed's visible models replace the 23-row static
// snapshot, and a `hidden` model is not offered.
//
// It also pins that discovery survives a revoked key. The feed gates on CLI
// identity, not on the bearer, so a connection whose key has been rotated or
// revoked still lists what the provider serves instead of reporting a failed
// fetch.
func TestHandleModels_CommandcodeLiveCatalogOvertakesStatic(t *testing.T) {
	resetLiveCatalog(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"object":"list","data":[
			{"id":"claude-sonnet-5-5","name":"Claude Sonnet 5.5","context_length":1000000},
			{"id":"gemini-4-argon","name":"Gemini 4 Argon","context_length":1000000},
			{"id":"xiaomi/mimo-v2.5","name":"MiMo v2.5","context_length":262144,"hidden":true}
		]}`))
	}))
	defer srv.Close()
	prevURL := commandcodeCatalogURL
	commandcodeCatalogURL = srv.URL + "?view=client"
	t.Cleanup(func() { commandcodeCatalogURL = prevURL })

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`DELETE FROM providerConnections`); err != nil {
		t.Fatalf("delete connections: %v", err)
	}
	if _, err := database.Exec(`DELETE FROM kv WHERE scope='customModels'`); err != nil {
		t.Fatalf("delete customs: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-cmc-live', 'commandcode', 'apikey', 'Command Code', 1, 1, '{"apiKey":"user_revoked"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`); err != nil {
		t.Fatalf("seed commandcode: %v", err)
	}

	h := NewChatHandler(db.NewRepo(database))
	h.Client = srv.Client()
	joined := strings.Join(modelsIDs(t, h), "\n")

	if !strings.Contains(joined, "cmc/claude-sonnet-5-5") || !strings.Contains(joined, "cmc/gemini-4-argon") {
		t.Errorf("live commandcode models missing, got:\n%s", joined)
	}
	if strings.Contains(joined, "cmc/xiaomi/mimo-v2.5") {
		t.Errorf("hidden model was published, got:\n%s", joined)
	}
	// The static snapshot must be gone once the live feed answers: a model the
	// feed does not publish is not in the picker.
	if strings.Contains(joined, "cmc/moonshotai/Kimi-K2.5") {
		t.Errorf("static commandcode entry leaked next to the live catalog, got:\n%s", joined)
	}
}
