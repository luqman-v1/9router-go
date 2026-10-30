package middleware

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/models"
)

// openQuotaRepo builds a real sqlite-backed Repo so the quota path is exercised
// against the actual schema and the actual usageHistory sum, not a stub. The
// limiter only ever sees the TokenUsageReader interface, so a stub proves the
// middleware logic but not that the sum queries the right rows; this does.
func openQuotaRepo(t *testing.T) *db.Repo {
	t.Helper()
	conn, err := db.OpenDatabase(filepath.Join(t.TempDir(), "data.sqlite"))
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := db.EnsureCoreSchema(conn); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	return db.NewRepo(conn)
}

// TestQuotaEnforcedEndToEnd proves the whole chain: usage rows written under a
// key push it past its tokenQuota, and the very next request through the
// middleware — wired to the real repository — is refused with 403.
func TestQuotaEnforcedEndToEnd(t *testing.T) {
	repo := openQuotaRepo(t)

	const rawKey = "sk-e2e-quota-abcdefghijklmnop"
	if err := repo.CreateApiKey("ke2e", rawKey, "e2e", "m"); err != nil {
		t.Fatalf("CreateApiKey: %v", err)
	}

	rl := NewRateLimiter(time.Minute)
	handler := RequireRateLimit(rl, nil, repo)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	mkReq := func() *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		return req.WithContext(withAPIKey(req, &models.APIKey{
			ID:         "ke2e",
			Key:        rawKey,
			TokenQuota: new(1000),
		}))
	}

	// Under budget: served.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, mkReq())
	if rec.Code != http.StatusOK {
		t.Fatalf("under budget status = %d, want 200", rec.Code)
	}

	// Spend past the quota through the real usage table, under the masked form
	// the gateway actually writes.
	if err := repo.InsertUsageHistory("openai", "gpt-4o", "c1",
		handlerutil.MaskAPIKey(rawKey), "/v1/chat/completions",
		700, 400, 0, "success", 1100, "{}", "{}"); err != nil {
		t.Fatalf("InsertUsageHistory: %v", err)
	}

	used, err := repo.GetApiKeyTokenUsage(rawKey)
	if err != nil {
		t.Fatalf("GetApiKeyTokenUsage: %v", err)
	}
	if used != 1100 {
		t.Fatalf("usage = %d, want 1100", used)
	}

	// Over budget: refused with 429 and the spend headers.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, mkReq())
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("over budget status = %d, want 429", rec.Code)
	}
	if got := rec.Header().Get("X-Token-Quota-Limit"); got != "1000" {
		t.Errorf("X-Token-Quota-Limit = %q, want 1000", got)
	}
	if got := rec.Header().Get("X-Token-Quota-Used"); got != "1100" {
		t.Errorf("X-Token-Quota-Used = %q, want 1100", got)
	}
}

// TestQuotaAndRPMOrdering proves an exhausted quota is refused even when the
// key's RPM budget is otherwise untouched, and does not consume an RPM stamp:
// the refusal must happen before the per-window limiter sees the request.
func TestQuotaAndRPMOrdering(t *testing.T) {
	repo := openQuotaRepo(t)

	const rawKey = "sk-order-test-abcdefghijklmnop"
	if err := repo.CreateApiKey("kord", rawKey, "ord", "m"); err != nil {
		t.Fatalf("CreateApiKey: %v", err)
	}
	if err := repo.SetApiKeyRateLimit("kord", 1, 0, 0); err != nil { // 1 request/min
		t.Fatalf("SetApiKeyRateLimit: %v", err)
	}
	if err := repo.InsertUsageHistory("openai", "gpt-4o", "c1",
		handlerutil.MaskAPIKey(rawKey), "/v1/chat/completions",
		5000, 5000, 0, "success", 10000, "{}", "{}"); err != nil {
		t.Fatalf("InsertUsageHistory: %v", err)
	}
	if err := repo.SetApiKeyBudget("kord", 1000, 0); err != nil {
		t.Fatalf("SetApiKeyBudget: %v", err)
	}

	key, err := repo.GetApiKeyByID("kord")
	if err != nil || key == nil {
		t.Fatalf("GetApiKeyByID: %v", err)
	}
	if key.TokenQuota == nil || *key.TokenQuota != 1000 {
		t.Fatalf("TokenQuota not persisted: %+v", key.TokenQuota)
	}

	rl := NewRateLimiter(time.Minute)
	handler := RequireRateLimit(rl, nil, repo)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Two requests, both over quota. If the quota check ran after the RPM
	// limiter, the first would refuse but consume the single RPM stamp and the
	// second would 429; with quota first, both are refused by the quota.
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		req = req.WithContext(withAPIKey(req, key))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("request %d status = %d, want 429 (quota must precede RPM)", i, rec.Code)
		}
	}
}
