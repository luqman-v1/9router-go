package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
)

// writeUsageRow records one usage row for a client key.
func writeUsageRow(t *testing.T, repo *db.Repo, timestamp, clientKey, label string, prompt, completion int, cost float64) {
	t.Helper()
	err := repo.InsertUsageHistory("prov", "model", "conn", "upstream-key", "/v1/chat/completions",
		prompt, completion, cost, "ok", prompt+completion, "", "", clientKey)
	if err != nil {
		t.Fatalf("insert usage row (%s): %v", label, err)
	}
}

// decodeUsageByAPIKey reads the panel's body.
func decodeUsageByAPIKey(t *testing.T, rec *httptest.ResponseRecorder) UsageByAPIKeyResponse {
	t.Helper()
	var body UsageByAPIKeyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal usage by api key: %v (body: %s)", err, rec.Body.String())
	}
	return body
}

// TestUsageByAPIKeyGroupsByTheCallerKey proves the panel answers the reseller's
// question: what did each key the callers presented spend.
//
// The Overview's own "Usage by API Key" table groups the upstream credential
// the router spent, so a row there is a connection — not the key that was
// sold. The two are different axes and the panel must not confuse them.
func TestUsageByAPIKeyGroupsByTheCallerKey(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	// The test database carries a hand-written schema; the usage ledger is
	// part of the core schema the app applies on boot.
	if err := db.EnsureCoreSchema(repo.RawDB()); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	if err := repo.UpdateSettingsRaw(map[string]any{"requireLogin": false}); err != nil {
		t.Fatalf("disable requireLogin: %v", err)
	}
	router := setupTestRouter(repo)

	_, first := createTestKey(t, router, "First Client")
	_, second := createTestKey(t, router, "Second Client")

	now := time.Now().UTC().Format(time.RFC3339)
	writeUsageRow(t, repo, now, handlerutil.MaskAPIKey(first), "first client", 120, 30, 0.01)
	writeUsageRow(t, repo, now, handlerutil.MaskAPIKey(second), "second client", 40, 10, 0.002)
	// A request that carried no client key still spent tokens, so it must not
	// vanish from the totals.
	writeUsageRow(t, repo, now, "", "no client key", 5, 1, 0.0001)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/usage/api-keys?period=7d", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	body := decodeUsageByAPIKey(t, rec)
	if len(body.Keys) != 3 {
		t.Fatalf("keys = %d, want 3 (two clients and the keyless row): %s", len(body.Keys), rec.Body.String())
	}
	// Ordered by spend, heaviest first.
	if body.Keys[0].PromptTokens != 120 {
		t.Errorf("first row promptTokens = %d, want 120", body.Keys[0].PromptTokens)
	}
	if body.Keys[0].Requests != 1 {
		t.Errorf("first row requests = %d, want 1", body.Keys[0].Requests)
	}
	if body.Keys[0].Cost != 0.01 {
		t.Errorf("first row cost = %v, want 0.01", body.Keys[0].Cost)
	}
	if body.Totals.PromptTokens != 165 {
		t.Errorf("totals promptTokens = %d, want 165", body.Totals.PromptTokens)
	}
	if body.Totals.Requests != 3 {
		t.Errorf("totals requests = %d, want 3", body.Totals.Requests)
	}

	// The keyless row keeps its own bucket.
	var keyless bool
	for _, k := range body.Keys {
		if k.Name == "No client key" {
			keyless = true
		}
	}
	if !keyless {
		t.Errorf("the keyless spend is missing from the rows: %s", rec.Body.String())
	}
	_ = first
	_ = second
}

// TestUsageByAPIKeyResolvesTheKeyName proves a row is reported under the name
// the operator gave the key, not under a raw secret.
func TestUsageByAPIKeyResolvesTheKeyName(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	// The test database carries a hand-written schema; the usage ledger is
	// part of the core schema the app applies on boot.
	if err := db.EnsureCoreSchema(repo.RawDB()); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	if err := repo.UpdateSettingsRaw(map[string]any{"requireLogin": false}); err != nil {
		t.Fatalf("disable requireLogin: %v", err)
	}
	router := setupTestRouter(repo)

	id, plaintext := createTestKey(t, router, "Named Client")

	// The ledger holds the masked form of the key the caller presented.
	now := time.Now().UTC().Format(time.RFC3339)
	writeUsageRow(t, repo, now, handlerutil.MaskAPIKey(plaintext), "named client", 10, 2, 0.001)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/usage/api-keys?period=7d", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	body := decodeUsageByAPIKey(t, rec)
	if len(body.Keys) != 1 {
		t.Fatalf("keys = %d, want 1: %s", len(body.Keys), rec.Body.String())
	}
	row := body.Keys[0]
	if row.Name != "Named Client" {
		t.Errorf("name = %q, want the key's own name", row.Name)
	}
	if row.ID != id {
		t.Errorf("id = %q, want %q", row.ID, id)
	}
	if !row.Active {
		t.Error("active = false, want true for a key that exists and is enabled")
	}
	if row.Key == "" {
		t.Error("key is empty: the row must carry the masked key")
	}
	if row.Key != handlerutil.MaskAPIKey(plaintext) {
		t.Errorf("key = %q, want the masked form the ledger holds", row.Key)
	}
}

// TestUsageByAPIKeyEmptyWindow proves an empty window answers an empty list
// rather than an error, and that the period is echoed back.
func TestUsageByAPIKeyEmptyWindow(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	// The test database carries a hand-written schema; the usage ledger is
	// part of the core schema the app applies on boot.
	if err := db.EnsureCoreSchema(repo.RawDB()); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	if err := repo.UpdateSettingsRaw(map[string]any{"requireLogin": false}); err != nil {
		t.Fatalf("disable requireLogin: %v", err)
	}
	router := setupTestRouter(repo)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/usage/api-keys?period=7d", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := decodeUsageByAPIKey(t, rec)
	if len(body.Keys) != 0 {
		t.Errorf("keys = %d, want 0", len(body.Keys))
	}
	if body.Period == "" {
		t.Error("period is empty: the client needs to know which window it got")
	}
}
