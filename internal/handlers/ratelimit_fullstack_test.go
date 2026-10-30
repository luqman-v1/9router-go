package handlers

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/middleware"
)

// setupFullStackDB builds a database with the real core schema, so the apiKeys
// table has the governance columns this change added — the shared dbtest schema
// predates them and would make the quota path untestable.
func setupFullStackDB(t *testing.T) (*db.Repo, func()) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "full.sqlite")
	conn, err := db.OpenDatabase(path)
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	if err := db.EnsureCoreSchema(conn); err != nil {
		conn.Close()
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	cleanup := func() {
		conn.Close()
		os.Remove(path)
	}
	return db.NewRepo(conn), cleanup
}

// newFullStackRouter assembles the exact middleware chain the real router wraps
// around the /v1 surface: RequireApiKey, then RequireRateLimit (with the same
// defaults closure and the repo as the usage reader). A simple terminal handler
// stands in for the chat handler so the test asserts on the gate, not on
// provider dispatch.
func newFullStackRouter(repo *db.Repo) http.Handler {
	window := time.Duration(repo.GetRateLimitDefaults().WindowSeconds) * time.Second
	rl := middleware.NewRateLimiter(window)
	defaults := func() middleware.Limits {
		d := repo.GetRateLimitDefaults()
		if !d.Enabled {
			return middleware.Limits{}
		}
		return middleware.Limits{RPM: d.RPM, TPM: d.TPM, Concurrency: d.Concurrency}
	}

	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireApiKey(repo))
		r.Use(middleware.RequireRateLimit(rl, defaults, repo))
		r.Post("/v1/chat/completions", func(w http.ResponseWriter, req *http.Request) {
			handlerutil.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		})
	})
	return r
}

// TestFullStackQuotaRefusesRealBearerKey is the end-to-end acceptance check:
// a real HTTP request carrying a real API key, through the real auth middleware
// and the real limiter, is served under budget and refused with 429 once the
// key's usageHistory crosses its quota.
func TestFullStackQuotaRefusesRealBearerKey(t *testing.T) {
	repo, cleanup := setupFullStackDB(t)
	defer cleanup()

	const rawKey = "sk-fullstack-abcdefghijklmnop"
	if err := repo.CreateApiKey("full1", rawKey, "full stack", "m"); err != nil {
		t.Fatalf("CreateApiKey: %v", err)
	}
	if err := repo.SetApiKeyBudget("full1", 1000, 0); err != nil {
		t.Fatalf("SetApiKeyBudget: %v", err)
	}

	router := newFullStackRouter(repo)

	do := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		req.Header.Set("Authorization", "Bearer "+rawKey)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	// Under budget.
	if rec := do(); rec.Code != http.StatusOK {
		t.Fatalf("under-budget status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	// Spend past the quota, under the masked form the gateway writes.
	if err := repo.InsertUsageHistory("openai", "gpt-4o", "c1",
		handlerutil.MaskAPIKey(rawKey), "/v1/chat/completions",
		800, 300, 0, "success", 1100, "{}", "{}"); err != nil {
		t.Fatalf("InsertUsageHistory: %v", err)
	}

	rec := do()
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("over-budget status = %d, want 429: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Token-Quota-Limit"); got != "1000" {
		t.Errorf("X-Token-Quota-Limit = %q, want 1000", got)
	}
}

// TestFullStackRejectsBadKey proves the auth gate still runs before the limiter:
// an unknown key is 401, never a rate-limit decision.
func TestFullStackRejectsBadKey(t *testing.T) {
	repo, cleanup := setupFullStackDB(t)
	defer cleanup()

	router := newFullStackRouter(repo)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer sk-does-not-exist-000000")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

// TestFullStackRPMLimitEnforced proves the per-window limit is applied to a real
// bearer key through the full chain: the second request in the window is 429.
func TestFullStackRPMLimitEnforced(t *testing.T) {
	repo, cleanup := setupFullStackDB(t)
	defer cleanup()

	const rawKey = "sk-rpm-fullstack-abcdefghij"
	if err := repo.CreateApiKey("rpm1", rawKey, "rpm", "m"); err != nil {
		t.Fatalf("CreateApiKey: %v", err)
	}
	if err := repo.SetApiKeyRateLimit("rpm1", 1, 0, 0); err != nil {
		t.Fatalf("SetApiKeyRateLimit: %v", err)
	}

	router := newFullStackRouter(repo)
	do := func() int {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		req.Header.Set("Authorization", "Bearer "+rawKey)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec.Code
	}

	if got := do(); got != http.StatusOK {
		t.Fatalf("first request = %d, want 200", got)
	}
	if got := do(); got != http.StatusTooManyRequests {
		t.Errorf("second request = %d, want 429", got)
	}
}
