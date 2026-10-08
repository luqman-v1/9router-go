package handlers

import (
	"context"
	"database/sql"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers/shared"
	"9router/proxy/internal/translator"
)

// SetupRoutes must hand back the chat handler that owns the engine's semantic
// cache, because SetupServerRouter mounts the dashboard group on exactly that
// handler.
//
// The bug this pins: the dashboard group was mounted on the handler built for
// /version instead. That handler is constructed before any traffic, so it owns
// a separate PersistentStore over the same SQLite file whose in-memory LRU never
// fills — /api/cache/entries reported zero entries while the engine served hits,
// and DELETE /api/cache wiped the rows without stopping the engine serving them.
//
// A seeded-row fixture cannot catch this: two instances hydrate from the same
// file and agree. What separates them is which instance the router hands the
// dashboard, so the contract asserted here is the return value itself, and the
// dashboard route is then exercised over HTTP against it.
func TestSetupRoutes_ReturnsTheEngineHandler(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	ts := shared.NewTokenSaverConfig(false, false, false)
	ts.SetSemanticCache(true)

	engineChatH := SetupRoutes(chi.NewRouter(), repo, ts)
	if engineChatH == nil {
		t.Fatal("SetupRoutes returned nil; the dashboard group would be mounted on a nil handler")
	}
	if engineChatH.SemanticCache == nil {
		t.Fatal("returned handler carries no semantic cache, so the dashboard reads a different one")
	}
	if !engineChatH.SemanticCache.Enabled() {
		t.Fatal("cache is enabled in the config but not on the handler SetupRoutes returned")
	}

	req := &translator.OpenAIRequest{
		Model:    "gpt-4o",
		Messages: []translator.OpenAIMessage{{Role: "user", Content: "prompt"}},
	}
	if err := engineChatH.SemanticCache.Store(context.Background(), req, []byte(`{"ok":true}`), "application/json"); err != nil {
		t.Fatalf("Store: %v", err)
	}

	// Mount the dashboard on the returned handler — what SetupServerRouter does.
	dashR := chi.NewRouter()
	SetupDashboardRoutes(dashR, repo, engineChatH)

	rec := httptest.NewRecorder()
	dashR.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/cache/entries", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/cache/entries status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}

	var resp struct {
		Pagination struct {
			Total int `json:"total"`
		} `json:"pagination"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if resp.Pagination.Total != 1 {
		t.Fatalf("dashboard reports total=%d, want 1 (the entry just stored on the returned handler)", resp.Pagination.Total)
	}

	// A live write through the engine must be immediately visible to the
	// dashboard: one instance, one state.
	second := &translator.OpenAIRequest{
		Model:    "gpt-4o",
		Messages: []translator.OpenAIMessage{{Role: "user", Content: "another prompt"}},
	}
	if err := engineChatH.SemanticCache.Store(context.Background(), second, []byte(`{"ok":2}`), "application/json"); err != nil {
		t.Fatalf("Store second: %v", err)
	}

	rec = httptest.NewRecorder()
	dashR.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/cache/entries", nil))
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode second response %q: %v", rec.Body.String(), err)
	}
	if resp.Pagination.Total != 2 {
		t.Fatalf("after a second engine write the dashboard reports total=%d, want 2", resp.Pagination.Total)
	}
}

// seedSemanticCacheRow exists so a future fixture can reproduce the on-disk
// state a running engine leaves behind. Kept out of the assertions above: two
// instances hydrate identically, so a seeded row cannot distinguish them.
func seedSemanticCacheRow(t *testing.T, database *sql.DB) {
	t.Helper()
	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS semanticCacheEntries (
		key TEXT PRIMARY KEY, model TEXT NOT NULL, responseBody BLOB NOT NULL,
		contentType TEXT, storedAt TEXT NOT NULL,
		hitCount INTEGER DEFAULT 0, tokensSaved INTEGER DEFAULT 0)`); err != nil {
		t.Fatalf("create semanticCacheEntries: %v", err)
	}
	if _, err := database.Exec(
		`INSERT OR REPLACE INTO semanticCacheEntries (key, model, responseBody, contentType, storedAt, hitCount, tokensSaved)
		 VALUES (?, ?, ?, ?, ?, 0, 0)`,
		"probe-key", "gpt-4o", []byte(`{"ok":true}`), "application/json",
		time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z07:00"),
	); err != nil {
		t.Fatalf("seed semanticCacheEntries: %v", err)
	}
}
