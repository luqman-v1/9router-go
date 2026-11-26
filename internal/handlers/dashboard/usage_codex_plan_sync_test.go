package dashboard

import (
	"context"

	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

// newCodexPlanSyncConn seeds one Codex connection whose stored provider-specific
// data is the given map, and answers the live usage read with the plan under
// test. It returns the handler, the repo and the connection id.
func newCodexPlanSyncConn(t *testing.T, psd map[string]any, livePlan string) (*DashboardHandler, *db.Repo, string) {
	t.Helper()
	repo, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)

	psd["chatgptAccountId"] = "acct-1"
	encoded, err := json.Marshal(map[string]any{
		"accessToken":          "tok",
		"refreshToken":         "rt",
		"providerSpecificData": psd,
	})
	if err != nil {
		t.Fatalf("marshal connection data: %v", err)
	}
	if err := repo.CreateProviderConnectionFull(context.Background(), "codex-1", "codex", "oauth", "Codex Plan", nil, string(encoded)); err != nil {
		t.Fatalf("create connection: %v", err)
	}

	withCodexUsageServer(t, http.StatusOK,
		`{"plan_type":"`+livePlan+`","rate_limit":{"primary_window":{"used_percent":10,"reset_at":1712345678}}}`)

	return NewDashboardHandler(repo), repo, "codex-1"
}

// readStoredCodexPSD reads the connection's providerSpecificData back the way
// every other reader of that blob does.
func readStoredCodexPSD(t *testing.T, repo *db.Repo, connID string) map[string]any {
	t.Helper()
	conn, err := repo.GetProviderConnectionByID(connID)
	if err != nil {
		t.Fatalf("read connection: %v", err)
	}
	if conn == nil {
		t.Fatalf("connection %s disappeared", connID)
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(conn.Data), &data); err != nil {
		t.Fatalf("unmarshal connection data: %v", err)
	}
	psd, _ := data["providerSpecificData"].(map[string]any)
	if psd == nil {
		t.Fatalf("providerSpecificData missing from %v", data)
	}
	return psd
}

// The ported fix: a live usage read reporting a higher tier than the row holds
// rewrites the row, so a free account that upgraded to plus unlocks the paid
// models without a manual reconnect.
func TestCodexUsageSyncsUpgradedPlanToConnection(t *testing.T) {
	handler, repo, connID := newCodexPlanSyncConn(t, map[string]any{"chatgptPlanType": "free"}, "plus")

	req := httptest.NewRequest(http.MethodGet, "/api/usage/"+connID, nil)
	rec := httptest.NewRecorder()
	setupTestRouterWith(repo, handler).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("usage status = %d, body = %s", rec.Code, rec.Body.String())
	}

	psd := readStoredCodexPSD(t, repo, connID)
	if got := psd["chatgptPlanType"]; got != "plus" {
		t.Errorf("stored chatgptPlanType = %v, want plus", got)
	}
	// The account id rides in the same blob; the sync must not drop the rest.
	if got := psd["chatgptAccountId"]; got != "acct-1" {
		t.Errorf("stored chatgptAccountId = %v, want acct-1", got)
	}

	// The connection's data blob still carries its token fields: only
	// providerSpecificData may change.
	conn, err := repo.GetProviderConnectionByID(connID)
	if err != nil {
		t.Fatalf("re-read connection: %v", err)
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(conn.Data), &data); err != nil {
		t.Fatalf("unmarshal connection data: %v", err)
	}
	if got := data["refreshToken"]; got != "rt" {
		t.Errorf("refreshToken = %v, want rt", got)
	}
}

// A stored value that already matches the live one must not rewrite the row:
// the quota panel refreshes on a tick, so a rewrite on every read would mean a
// write per account per tick.
func TestCodexUsageLeavesUnchangedPlanAlone(t *testing.T) {
	handler, repo, connID := newCodexPlanSyncConn(t, map[string]any{"chatgptPlanType": "plus"}, "plus")
	before, err := repo.GetProviderConnectionByID(connID)
	if err != nil {
		t.Fatalf("read connection: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/usage/"+connID, nil)
	rec := httptest.NewRecorder()
	setupTestRouterWith(repo, handler).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("usage status = %d, body = %s", rec.Code, rec.Body.String())
	}

	if got := readStoredCodexPSD(t, repo, connID)["chatgptPlanType"]; got != "plus" {
		t.Errorf("stored chatgptPlanType = %v, want plus", got)
	}
	after, err := repo.GetProviderConnectionByID(connID)
	if err != nil {
		t.Fatalf("re-read connection: %v", err)
	}
	if after.UpdatedAt != before.UpdatedAt {
		t.Errorf("updatedAt moved on an unchanged plan: %q -> %q", before.UpdatedAt, after.UpdatedAt)
	}
}

// An auth-expired Codex read reports no plan at all, and the sync only runs on
// a successful read: a quota failure must not be able to blank a stored tier.
func TestCodexUsageKeepsStoredPlanOnFailedRead(t *testing.T) {
	handler, repo, connID := newCodexPlanSyncConn(t, map[string]any{"chatgptPlanType": "free"}, "free")
	before, err := repo.GetProviderConnectionByID(connID)
	if err != nil {
		t.Fatalf("read connection: %v", err)
	}
	withCodexUsageServer(t, http.StatusServiceUnavailable, `{}`)

	req := httptest.NewRequest(http.MethodGet, "/api/usage/"+connID, nil)
	rec := httptest.NewRecorder()
	setupTestRouterWith(repo, handler).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("usage status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "temporarily unavailable") {
		t.Errorf("body = %s, want the quota failure message", rec.Body.String())
	}

	if got := readStoredCodexPSD(t, repo, connID)["chatgptPlanType"]; got != "free" {
		t.Errorf("stored chatgptPlanType = %v, want free after a failed read", got)
	}
	after, err := repo.GetProviderConnectionByID(connID)
	if err != nil {
		t.Fatalf("re-read connection: %v", err)
	}
	if after.UpdatedAt != before.UpdatedAt {
		t.Errorf("updatedAt moved on a failed read: %q -> %q", before.UpdatedAt, after.UpdatedAt)
	}
}

// An account that had no plan stored (pre-OAuth imports, older rows) gets the
// live tier written rather than staying empty, which is what a codexPlanType
// lookup falls back to.
func TestCodexUsageWritesMissingPlanToConnection(t *testing.T) {
	handler, repo, connID := newCodexPlanSyncConn(t, map[string]any{}, "pro")

	req := httptest.NewRequest(http.MethodGet, "/api/usage/"+connID, nil)
	rec := httptest.NewRecorder()
	setupTestRouterWith(repo, handler).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("usage status = %d, body = %s", rec.Code, rec.Body.String())
	}

	if got := readStoredCodexPSD(t, repo, connID)["chatgptPlanType"]; got != "pro" {
		t.Errorf("stored chatgptPlanType = %v, want pro", got)
	}
}

// The sync is Codex-only: another provider's row carrying the same field must
// not be rewritten while this request is being served.
func TestCodexPlanSyncLeavesOtherProvidersAlone(t *testing.T) {
	handler, repo, connID := newCodexPlanSyncConn(t, map[string]any{"chatgptPlanType": "free"}, "pro")

	other := map[string]any{"apiKey": "sk-x", "providerSpecificData": map[string]any{"chatgptPlanType": "free"}}
	encoded, err := json.Marshal(other)
	if err != nil {
		t.Fatalf("marshal other data: %v", err)
	}
	if err := repo.CreateProviderConnectionFull(context.Background(), "other-1", "openai", "apikey", "Other", nil, string(encoded)); err != nil {
		t.Fatalf("create other connection: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/usage/"+connID, nil)
	rec := httptest.NewRecorder()
	setupTestRouterWith(repo, handler).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("usage status = %d, body = %s", rec.Code, rec.Body.String())
	}

	if got := readStoredCodexPSD(t, repo, connID)["chatgptPlanType"]; got != "pro" {
		t.Errorf("codex chatgptPlanType = %v, want pro", got)
	}
	if got := readStoredCodexPSD(t, repo, "other-1")["chatgptPlanType"]; got != "free" {
		t.Errorf("other provider chatgptPlanType = %v, want free untouched", got)
	}
}

// A live read reporting a tier outside validCodexPlans ("unknown", an
// unrecognized alias) must never clobber a stored tier: an upgraded account
// whose read glitched would otherwise be silently downgraded.
func TestCodexUsageUnknownPlanKeepsStoredTier(t *testing.T) {
	for _, stored := range []string{"free", "plus"} {
		t.Run(stored, func(t *testing.T) {
			handler, repo, connID := newCodexPlanSyncConn(t, map[string]any{"chatgptPlanType": stored}, "unknown")

			req := httptest.NewRequest(http.MethodGet, "/api/usage/"+connID, nil)
			rec := httptest.NewRecorder()
			setupTestRouterWith(repo, handler).ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("usage status = %d, body = %s", rec.Code, rec.Body.String())
			}

			if got := readStoredCodexPSD(t, repo, connID)["chatgptPlanType"]; got != stored {
				t.Errorf("stored chatgptPlanType = %v, want %s preserved against an unknown plan", got, stored)
			}
		})
	}
}

// An empty live plan (a quota read that reported no tier) must likewise leave a
// stored tier alone rather than blanking it.
func TestCodexUsageEmptyPlanKeepsStoredTier(t *testing.T) {
	handler, repo, connID := newCodexPlanSyncConn(t, map[string]any{"chatgptPlanType": "plus"}, "")

	req := httptest.NewRequest(http.MethodGet, "/api/usage/"+connID, nil)
	rec := httptest.NewRecorder()
	setupTestRouterWith(repo, handler).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("usage status = %d, body = %s", rec.Code, rec.Body.String())
	}

	if got := readStoredCodexPSD(t, repo, connID)["chatgptPlanType"]; got != "plus" {
		t.Errorf("stored chatgptPlanType = %v, want plus preserved against an empty plan", got)
	}
}

// The sync is Codex-only at the helper level: a non-codex connection carrying
// the same field is never passed to or written by syncCodexPlanFromUsage, even
// when invoked directly with a live plan.
func TestCodexPlanSyncNeverTouchesNonCodexConnection(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)

	other := map[string]any{"apiKey": "sk-ant", "providerSpecificData": map[string]any{"chatgptPlanType": "free"}}
	encoded, err := json.Marshal(other)
	if err != nil {
		t.Fatalf("marshal other data: %v", err)
	}
	if err := repo.CreateProviderConnectionFull(context.Background(), "claude-1", "claude", "apikey", "Claude", nil, string(encoded)); err != nil {
		t.Fatalf("create claude connection: %v", err)
	}

	handler := NewDashboardHandler(repo)
	conn, err := repo.GetProviderConnectionByID("claude-1")
	if err != nil {
		t.Fatalf("read claude connection: %v", err)
	}
	if conn == nil {
		t.Fatalf("claude connection missing")
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(conn.Data), &data); err != nil {
		t.Fatalf("unmarshal claude data: %v", err)
	}

	handler.syncCodexPlanFromUsage(context.Background(), conn, data, "pro")

	if got := readStoredCodexPSD(t, repo, "claude-1")["chatgptPlanType"]; got != "free" {
		t.Errorf("claude chatgptPlanType = %v, want free untouched by plan sync", got)
	}
}
