package dashboard

import (
	"context"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"9router/proxy/internal/db"
	"9router/proxy/internal/middleware"
)

// nodeLimitsRequest drives one provider-node request through the real router
// registration, so a route that was never mounted fails here rather than in a
// browser.
func nodeLimitsRequest(t *testing.T, router http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func seedLimitNode(t *testing.T, repo *db.Repo, id string) {
	t.Helper()
	if _, err := repo.CreateProviderNode(id, "openai-compatible", "Nara AI",
		`{"prefix":"nara","apiType":"chat","baseUrl":"https://api.example/v1"}`); err != nil {
		t.Fatalf("create provider node: %v", err)
	}
}

// The PUT merges: a field the dashboard did not send keeps its stored value, an
// explicit 0 clears it, and a negative count is refused rather than stored as a
// limit that means the opposite of what the operator typed.
func TestUpdateProviderNodeLimits_MergesPartialEditsAndRefusesNegatives(t *testing.T) {
	repo, cleanup := setupNodeTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)
	seedLimitNode(t, repo, "node-nara")

	rec := nodeLimitsRequest(t, router, http.MethodPut, "/api/provider-nodes/node-nara/limits",
		`{"rpm":60,"tpm":10000,"concurrency":2,"tokenQuota":5000,"queueTimeoutMs":1500,"contractExpiresAt":"2027-01-31"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT limits = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	stored, err := repo.GetProviderLimits(context.Background(), "node-nara")
	if err != nil {
		t.Fatalf("GetProviderLimits: %v", err)
	}
	if stored.RPM != 60 || stored.TPM != 10000 || stored.Concurrency != 2 ||
		stored.TokenQuota != 5000 || stored.QueueTimeoutMs != 1500 {
		t.Fatalf("stored limits = %+v, want the whole policy saved", stored)
	}
	// A date input sends "2027-01-31"; storing it verbatim would be a value the
	// dispatch gate cannot parse, which reads as "not expired".
	if stored.ContractExpiresAt != "2027-01-31T00:00:00Z" {
		t.Fatalf("stored contractExpiresAt = %q, want it normalised to RFC3339 UTC", stored.ContractExpiresAt)
	}

	rec = nodeLimitsRequest(t, router, http.MethodPut, "/api/provider-nodes/node-nara/limits", `{"tpm":9000}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT tpm only = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	stored, err = repo.GetProviderLimits(context.Background(), "node-nara")
	if err != nil {
		t.Fatalf("GetProviderLimits: %v", err)
	}
	if stored.TPM != 9000 || stored.RPM != 60 || stored.Concurrency != 2 ||
		stored.TokenQuota != 5000 || stored.QueueTimeoutMs != 1500 ||
		stored.ContractExpiresAt != "2027-01-31T00:00:00Z" {
		t.Fatalf("stored limits after a partial edit = %+v, want only tpm changed", stored)
	}

	rec = nodeLimitsRequest(t, router, http.MethodPut, "/api/provider-nodes/node-nara/limits", `{"concurrency":0}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT concurrency 0 = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	stored, err = repo.GetProviderLimits(context.Background(), "node-nara")
	if err != nil {
		t.Fatalf("GetProviderLimits: %v", err)
	}
	if stored.Concurrency != 0 || stored.RPM != 60 {
		t.Fatalf("stored limits after clearing concurrency = %+v, want concurrency 0 and rpm kept", stored)
	}

	rec = nodeLimitsRequest(t, router, http.MethodPut, "/api/provider-nodes/node-nara/limits", `{"rpm":-5}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT a negative rpm = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
	stored, err = repo.GetProviderLimits(context.Background(), "node-nara")
	if err != nil {
		t.Fatalf("GetProviderLimits: %v", err)
	}
	if stored.RPM != 60 {
		t.Fatalf("rpm after a refused write = %d, want the stored 60 untouched", stored.RPM)
	}

	rec = nodeLimitsRequest(t, router, http.MethodPut, "/api/provider-nodes/node-nara/limits",
		`{"contractExpiresAt":"yesterday"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT an unparseable contractExpiresAt = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
	stored, err = repo.GetProviderLimits(context.Background(), "node-nara")
	if err != nil {
		t.Fatalf("GetProviderLimits: %v", err)
	}
	if stored.ContractExpiresAt != "2027-01-31T00:00:00Z" {
		t.Fatalf("contractExpiresAt after a refused write = %q, want the stored value untouched", stored.ContractExpiresAt)
	}

	rec = nodeLimitsRequest(t, router, http.MethodPut, "/api/provider-nodes/node-nara/limits", `{"rpm":`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT malformed JSON = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
}

// A policy saved against an id that names no row would be a dashboard reporting a
// limit nobody can find afterwards, so the write is refused instead.
func TestUpdateProviderNodeLimits_UnknownNodeIs404(t *testing.T) {
	repo, cleanup := setupNodeTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	rec := nodeLimitsRequest(t, router, http.MethodPut, "/api/provider-nodes/node-missing/limits", `{"rpm":1}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("PUT limits on an unknown node = %d, want 404 (body: %s)", rec.Code, rec.Body.String())
	}
	reset := nodeLimitsRequest(t, router, http.MethodPost, "/api/provider-nodes/node-missing/quota/reset", "")
	if reset.Code != http.StatusNotFound {
		t.Fatalf("quota reset on an unknown node = %d, want 404 (body: %s)", reset.Code, reset.Body.String())
	}
}

// The reset moves the measurement mark instead of deleting history, so the spend
// reports zero while the budget itself and the analytics behind it stay put.
func TestResetProviderNodeQuota_ClearsSpendAndKeepsTheBudget(t *testing.T) {
	repo, cleanup := setupNodeTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)
	seedLimitNode(t, repo, "node-nara")
	if err := repo.SetProviderLimits(context.Background(), "node-nara", db.ProviderLimits{TokenQuota: 1000}); err != nil {
		t.Fatalf("SetProviderLimits: %v", err)
	}
	seedNodeSpend(t, repo, "node-nara", 4000)

	before, err := repo.ProviderTokenUsage(context.Background(), "node-nara")
	if err != nil {
		t.Fatalf("ProviderTokenUsage: %v", err)
	}
	if before != 4000 {
		t.Fatalf("recorded spend = %d, want 4000", before)
	}

	rec := nodeLimitsRequest(t, router, http.MethodPost, "/api/provider-nodes/node-nara/quota/reset", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("quota reset = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var out struct {
		Status       string `json:"status"`
		QuotaResetID int    `json:"quotaResetId"`
		Limits       struct {
			TokenQuota int `json:"tokenQuota"`
			UsedTokens int `json:"usedTokens"`
		} `json:"limits"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode reset response %s: %v", rec.Body.String(), err)
	}
	if out.Status != "ok" || out.QuotaResetID <= 0 {
		t.Fatalf("reset response = %+v, want status ok and the recorded mark", out)
	}
	if out.Limits.UsedTokens != 0 {
		t.Errorf("usedTokens after reset = %d, want 0", out.Limits.UsedTokens)
	}
	if out.Limits.TokenQuota != 1000 {
		t.Errorf("tokenQuota after reset = %d, want the configured 1000 kept — a reset must not clear the budget", out.Limits.TokenQuota)
	}

	after, err := repo.ProviderTokenUsage(context.Background(), "node-nara")
	if err != nil {
		t.Fatalf("ProviderTokenUsage: %v", err)
	}
	if after != 0 {
		t.Fatalf("spend after reset = %d, want 0 — the mark did not move", after)
	}
	// The history itself is untouched: only the measurement moved.
	var rows int
	if err := repo.RawDB().QueryRow(`SELECT COUNT(*) FROM usageHistory WHERE provider = 'node-nara'`).Scan(&rows); err != nil {
		t.Fatalf("count usage rows: %v", err)
	}
	if rows == 0 {
		t.Fatal("the reset deleted the node's history; a reset records a mark, it does not prune")
	}
}

// seedNodeSpend records one usage row worth tokens for the node.
func seedNodeSpend(t *testing.T, repo *db.Repo, nodeID string, tokens int) {
	t.Helper()
	if _, err := repo.RawDB().Exec(
		`INSERT INTO usageHistory (timestamp, provider, model, connectionId, promptTokens, completionTokens, status)
		 VALUES (?, ?, 'glm-5.3', 'conn-1', ?, 0, 'success')`,
		time.Now().UTC().Format(time.RFC3339), nodeID, tokens,
	); err != nil {
		t.Fatalf("seed usage: %v", err)
	}
}

// The list is what the dashboard reads: policy, spend, and the live window figures
// from the same limiter the data plane meters with.
func TestGetProviderNodes_ReportsLimitsSpendAndLiveUsage(t *testing.T) {
	repo, cleanup := setupNodeTestDB(t)
	defer cleanup()
	seedLimitNode(t, repo, "node-nara")
	if err := repo.SetProviderLimits(context.Background(), "node-nara", db.ProviderLimits{RPM: 10, Concurrency: 2, TokenQuota: 1000}); err != nil {
		t.Fatalf("SetProviderLimits: %v", err)
	}
	seedNodeSpend(t, repo, "node-nara", 250)

	handler := NewDashboardHandler(repo)
	handler.Limiter = middleware.NewRateLimiter(time.Minute)
	router := chi.NewRouter()
	RegisterRoutes(router, handler)

	// One request in flight against the node, so the live figures are non-zero
	// and the assertion cannot pass on a handler that reports nothing.
	slot, refusal := handler.Limiter.AcquireProvider(context.Background(), "node-nara",
		middleware.Limits{RPM: 10, Concurrency: 2}, 0)
	if slot == nil || refusal != nil {
		t.Fatalf("AcquireProvider = slot %v refusal %v, want an admitted slot", slot, refusal)
	}
	defer slot.Release()

	rec := nodeLimitsRequest(t, router, http.MethodGet, "/api/provider-nodes", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/provider-nodes = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Nodes []struct {
			ID     string `json:"id"`
			Limits struct {
				RPM               int  `json:"rpm"`
				Concurrency       int  `json:"concurrency"`
				TokenQuota        int  `json:"tokenQuota"`
				UsedTokens        int  `json:"usedTokens"`
				RPMUsedWindow     int  `json:"rpmUsedWindow"`
				ActiveConcurrency int  `json:"activeConcurrency"`
				Expired           bool `json:"expired"`
			} `json:"limits"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode node list %s: %v", rec.Body.String(), err)
	}
	row, ok := loFindNode(body.Nodes, "node-nara")
	if !ok {
		t.Fatalf("node-nara is absent from the list: %s", rec.Body.String())
	}
	if row.Limits.RPM != 10 || row.Limits.Concurrency != 2 || row.Limits.TokenQuota != 1000 {
		t.Errorf("reported policy = %+v, want rpm 10, concurrency 2, tokenQuota 1000", row.Limits)
	}
	if row.Limits.UsedTokens != 250 {
		t.Errorf("reported spend = %d, want 250", row.Limits.UsedTokens)
	}
	if row.Limits.RPMUsedWindow != 1 {
		t.Errorf("rpmUsedWindow = %d, want 1 — the list must read the live limiter", row.Limits.RPMUsedWindow)
	}
	if row.Limits.ActiveConcurrency != 1 {
		t.Errorf("activeConcurrency = %d, want 1", row.Limits.ActiveConcurrency)
	}
	if row.Limits.Expired {
		t.Error("a node with no expiry reported expired")
	}
}

// loFindNode is the tiny linear search the list assertions need; the package's lo
// helpers are generic over the row type only here.
func loFindNode(rows []struct {
	ID     string `json:"id"`
	Limits struct {
		RPM               int  `json:"rpm"`
		Concurrency       int  `json:"concurrency"`
		TokenQuota        int  `json:"tokenQuota"`
		UsedTokens        int  `json:"usedTokens"`
		RPMUsedWindow     int  `json:"rpmUsedWindow"`
		ActiveConcurrency int  `json:"activeConcurrency"`
		Expired           bool `json:"expired"`
	} `json:"limits"`
}, id string) (struct {
	ID     string `json:"id"`
	Limits struct {
		RPM               int  `json:"rpm"`
		Concurrency       int  `json:"concurrency"`
		TokenQuota        int  `json:"tokenQuota"`
		UsedTokens        int  `json:"usedTokens"`
		RPMUsedWindow     int  `json:"rpmUsedWindow"`
		ActiveConcurrency int  `json:"activeConcurrency"`
		Expired           bool `json:"expired"`
	} `json:"limits"`
}, bool) {
	for _, row := range rows {
		if row.ID == id {
			return row, true
		}
	}
	return rows[0], false
}
