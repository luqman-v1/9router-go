package dashboard

import (
	"io"
	"time"

	"9router/proxy/internal/db"
	"bytes"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/auth"
	"9router/proxy/internal/codexquota"
	"9router/proxy/internal/handlers/chat"
)

// insertResetCreditConn seeds a provider connection the reset-credit handlers
// can resolve.
func insertResetCreditConn(t *testing.T, repo *db.Repo, id, provider, authType, data string) {
	t.Helper()
	_, err := repo.RawDB().Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, email, priority, isActive, data, createdAt, updatedAt)
		 VALUES (?, ?, ?, ?, '', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		id, provider, authType, id, data)
	if err != nil {
		t.Fatalf("seed connection %s: %v", id, err)
	}
}

// pointCodexResetURLs redirects the wham endpoints at a test server.
func pointCodexResetURLs(t *testing.T, base string) {
	t.Helper()
	oldCredits, oldConsume := codexquota.ResetCreditsURL, codexquota.ConsumeResetCreditsURL
	codexquota.ResetCreditsURL = base + "/credits"
	codexquota.ConsumeResetCreditsURL = base + "/credits/consume"
	t.Cleanup(func() {
		codexquota.ResetCreditsURL, codexquota.ConsumeResetCreditsURL = oldCredits, oldConsume
	})
}

func codexOAuthData(token, accountID string) string {
	raw, _ := json.Marshal(map[string]any{
		"accessToken": token,
		"providerSpecificData": map[string]any{
			"chatgptAccountId": accountID,
		},
	})
	return string(raw)
}

// The reset-credit routes must be reachable and must not be swallowed by the
// /usage/{connectionId} parameter route sitting one segment above them.
func TestCodexResetCreditListRoute(t *testing.T) {
	repo, cleanup := setupSettingsTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	var seenAccount string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAccount = r.Header.Get("chatgpt-account-id")
		w.Write([]byte(`{"credits":[{"credit_id":"c1","title":"Weekly reset"}],"available_count":1}`))
	}))
	defer srv.Close()
	pointCodexResetURLs(t, srv.URL)
	insertResetCreditConn(t, repo, "codex-1", "codex", "oauth", codexOAuthData("tok-1", "acct-1"))

	req := httptest.NewRequest(http.MethodGet, "/api/usage/codex-1/reset-credits", nil)
	req.Header.Set(cliTokenHeader, auth.CLIToken())
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list reset credits: %d %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Credits []struct {
			SelectionToken string `json:"selectionToken"`
			Title          string `json:"title"`
		} `json:"credits"`
		AvailableCount int `json:"availableCount"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if len(body.Credits) != 1 || body.Credits[0].SelectionToken != "c1" {
		t.Errorf("credits = %+v, want one with selectionToken c1", body.Credits)
	}
	if body.AvailableCount != 1 {
		t.Errorf("availableCount = %d, want 1", body.AvailableCount)
	}
	// The wham endpoint scopes credits per Codex account, so a missing header
	// would list the wrong account's credits.
	if seenAccount != "acct-1" {
		t.Errorf("chatgpt-account-id = %q, want acct-1", seenAccount)
	}
}

func TestCodexResetCreditConsumeRoute(t *testing.T) {
	repo, cleanup := setupSettingsTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	var consumeBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/credits/consume" {
			raw, _ := io.ReadAll(r.Body)
			json.Unmarshal(raw, &consumeBody)
			w.Write([]byte(`{"outcome":"reset"}`))
			return
		}
		w.Write([]byte(`{"credits":[{"credit_id":"c1"}]}`))
	}))
	defer srv.Close()
	pointCodexResetURLs(t, srv.URL)
	insertResetCreditConn(t, repo, "codex-1", "codex", "oauth", codexOAuthData("tok-1", "acct-1"))
	chat.BlockCodexConnectionUntil("codex-1", time.Now().UTC().Add(time.Hour))
	repo.LockConnectionModel("codex-1", "gpt-6.1-sol", 3600, 1)
	if !chat.IsCodexConnectionExhausted("codex-1") {
		t.Fatal("precondition: expected codex-1 to be blocked in quota cache")
	}

	body, _ := json.Marshal(map[string]string{"selectionToken": "c1", "idempotencyKey": "idem-1"})
	req := httptest.NewRequest(http.MethodPost, "/api/usage/codex-1/reset-credits/consume", bytes.NewReader(body))
	req.Header.Set(cliTokenHeader, auth.CLIToken())
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("consume: %d %s", rec.Code, rec.Body.String())
	}

	if chat.IsCodexConnectionExhausted("codex-1") {
		t.Error("expected reset-credit consume to unblock quota cache")
	}
	if locked, _ := repo.IsConnectionModelLocked("codex-1", "gpt-6.1-sol"); locked {
		t.Error("expected reset-credit consume to clear model lock")
	}

	var out struct {
		Outcome string `json:"outcome"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if out.Outcome != "reset" {
		t.Errorf("outcome = %q, want reset", out.Outcome)
	}
	if consumeBody["credit_id"] != "c1" || consumeBody["redeem_request_id"] != "idem-1" {
		t.Errorf("consume payload = %+v, want credit_id c1 and redeem_request_id idem-1", consumeBody)
	}
}

// A "no credit" refusal must reach the dashboard as a 409 with its code, not a
// generic 200 or a 500 — the modal tells the user which case it is.
func TestCodexResetCreditConsumeSurfacesTypedRefusal(t *testing.T) {
	repo, cleanup := setupSettingsTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/credits/consume" {
			w.WriteHeader(http.StatusConflict)
			w.Write([]byte(`{"outcome":"no_credit"}`))
			return
		}
		w.Write([]byte(`{"credits":[{"credit_id":"c1"}]}`))
	}))
	defer srv.Close()
	pointCodexResetURLs(t, srv.URL)
	insertResetCreditConn(t, repo, "codex-1", "codex", "oauth", codexOAuthData("tok-1", "acct-1"))

	req := httptest.NewRequest(http.MethodPost, "/api/usage/codex-1/reset-credits/consume",
		bytes.NewReader([]byte(`{"selectionToken":"c1"}`)))
	req.Header.Set(cliTokenHeader, auth.CLIToken())
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"code":"no_credit"`)) {
		t.Errorf("body should carry the typed code: %s", rec.Body.String())
	}
}

// Reset credits are a Codex OAuth feature. A different provider or an API-key
// connection must be refused before any upstream call.
func TestCodexResetCreditRejectsWrongConnectionShape(t *testing.T) {
	tests := []struct {
		name      string
		provider  string
		authType  string
		want      int
		wantWords string
	}{
		{name: "another provider", provider: "openai", authType: "apikey", want: http.StatusBadRequest, wantWords: "Codex"},
		{name: "codex on an api key", provider: "codex", authType: "apikey", want: http.StatusBadRequest, wantWords: "OAuth"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo, cleanup := setupSettingsTestDB(t)
			defer cleanup()
			router := setupTestRouter(repo)
			insertResetCreditConn(t, repo, "conn-x", tc.provider, tc.authType, `{"apiKey":"sk-x"}`)

			req := httptest.NewRequest(http.MethodGet, "/api/usage/conn-x/reset-credits", nil)
			req.Header.Set(cliTokenHeader, auth.CLIToken())
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.want, rec.Body.String())
			}
			if !bytes.Contains(rec.Body.Bytes(), []byte(tc.wantWords)) {
				t.Errorf("body should explain why: %s", rec.Body.String())
			}
		})
	}
}

func TestCodexResetCreditRejectsMissingConnection(t *testing.T) {
	repo, cleanup := setupSettingsTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/api/usage/nope/reset-credits", nil)
	req.Header.Set(cliTokenHeader, auth.CLIToken())
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
}

// Codex rejecting its own token must never reach the browser as a 401. The
// API client treats any 401 as an expired dashboard session and logs the user
// out (web/src/api/client.ts:476), so forwarding the upstream status verbatim
// bounced the whole dashboard to /login the instant the chooser was opened.
func TestCodexResetCreditUpstreamAuthFailureIsNotA401(t *testing.T) {
	for _, upstream := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(upstream), func(t *testing.T) {
			repo, cleanup := setupSettingsTestDB(t)
			defer cleanup()
			router := setupTestRouter(repo)

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(upstream)
			}))
			defer srv.Close()
			pointCodexResetURLs(t, srv.URL)
			insertResetCreditConn(t, repo, "codex-1", "codex", "oauth", codexOAuthData("stale", "acct-1"))

			req := httptest.NewRequest(http.MethodGet, "/api/usage/codex-1/reset-credits", nil)
			req.Header.Set(cliTokenHeader, auth.CLIToken())
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code == http.StatusUnauthorized {
				t.Fatalf("upstream %d surfaced as 401; the client would log the user out (%s)",
					upstream, rec.Body.String())
			}
			if rec.Code != http.StatusBadGateway {
				t.Errorf("upstream %d surfaced as %d, want 502", upstream, rec.Code)
			}
		})
	}
}
