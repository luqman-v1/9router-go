package dashboard

import (
	"bytes"
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/auth"
	"9router/proxy/internal/db"
)

func insertClaudeResetConn(t *testing.T, repo *db.Repo, id, provider, authType, data string) {
	t.Helper()
	_, err := repo.RawDB().Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, email, priority, isActive, data, createdAt, updatedAt)
		 VALUES (?, ?, ?, ?, '', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		id, provider, authType, id, data)
	if err != nil {
		t.Fatalf("seed connection %s: %v", id, err)
	}
}

func claudeOAuthData(token, orgUUID string) string {
	raw, _ := json.Marshal(map[string]any{
		"accessToken": token,
		"providerSpecificData": map[string]any{
			"organizationUuid": orgUUID,
		},
	})
	return string(raw)
}

func TestClaudeResetCreditListRoute(t *testing.T) {
	repo, cleanup := setupSettingsTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	prevUsage := claudeUsageURL
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"five_hour": {"utilization": 0.0},
			"seven_day": {"utilization": 16.0},
			"cedar_ember": {
				"eligible": true,
				"at_limit": false,
				"grants": [
					{
						"id": "launch-fable-1",
						"label": "Fable Launch Reset",
						"type": "full",
						"resets_left": 1,
						"usable_now": true,
						"starts_at": "2026-11-09T18:00:00Z",
						"ends_at": "2026-11-17T08:00:00Z",
						"clears": ["five_hour", "seven_day"]
					}
				]
			}
		}`))
	}))
	defer srv.Close()
	claudeUsageURL = srv.URL
	defer func() { claudeUsageURL = prevUsage }()

	insertClaudeResetConn(t, repo, "claude-1", "claude", "oauth", claudeOAuthData("tok-1", "org-123"))

	req := httptest.NewRequest(http.MethodGet, "/api/usage/claude-1/claude-reset-credits", nil)
	req.Header.Set(cliTokenHeader, auth.CLIToken())
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("list reset credits: %d %s", rec.Code, rec.Body.String())
	}

	var res struct {
		AvailableCount int `json:"availableCount"`
		Credits        []struct {
			SelectionToken string `json:"selectionToken"`
			Title          string `json:"title"`
			Description    string `json:"description"`
		} `json:"credits"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.AvailableCount != 1 {
		t.Errorf("availableCount = %d, want 1", res.AvailableCount)
	}
	if len(res.Credits) != 1 || res.Credits[0].SelectionToken != "launch-fable-1" {
		t.Errorf("credits = %+v, want launch-fable-1", res.Credits)
	}
}

func TestClaudeResetCreditConsumeRoute(t *testing.T) {
	repo, cleanup := setupSettingsTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	var receivedPath string
	var receivedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &receivedBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"result":"reset","resets_left":0,"cleared":["five_hour"]}`))
	}))
	defer srv.Close()

	prevProfile := claudeProfileURL
	prevClaim := claudeResetClaimBase
	claudeProfileURL = srv.URL + "/api/oauth/profile"
	claudeResetClaimBase = srv.URL
	defer func() {
		claudeProfileURL = prevProfile
		claudeResetClaimBase = prevClaim
	}()

	insertClaudeResetConn(t, repo, "claude-1", "claude", "oauth", claudeOAuthData("tok-1", "org-test-uuid"))

	body := []byte(`{"selectionToken":"launch-fable-1","idempotencyKey":"idem-123"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/usage/claude-1/claude-reset-credits/consume", bytes.NewReader(body))
	req.Header.Set(cliTokenHeader, auth.CLIToken())
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("consume reset credit: %d %s", rec.Code, rec.Body.String())
	}
	if receivedPath != "/api/organizations/org-test-uuid/reset_rate_limits" {
		t.Errorf("claim path = %q, want /api/organizations/org-test-uuid/reset_rate_limits", receivedPath)
	}
	if receivedBody["grant_id"] != "launch-fable-1" || receivedBody["program"] != "cedar_ember" {
		t.Errorf("receivedBody = %+v", receivedBody)
	}

	// Testing validation error for wrong provider
	insertClaudeResetConn(t, repo, "wrong-prov", "openai", "apikey", `{}`)
	reqWrong := httptest.NewRequest(http.MethodPost, "/api/usage/wrong-prov/claude-reset-credits/consume", bytes.NewReader(body))
	reqWrong.Header.Set(cliTokenHeader, auth.CLIToken())
	recWrong := httptest.NewRecorder()
	router.ServeHTTP(recWrong, reqWrong)
	if recWrong.Code != http.StatusBadRequest {
		t.Errorf("wrong provider status = %d, want 400", recWrong.Code)
	}
}
