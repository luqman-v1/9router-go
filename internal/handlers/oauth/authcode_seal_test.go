package oauth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/vault"
)

// TestHandleAuthCodeExchange_sealsTokensThroughVaultRepo drives the real
// HandleAuthCodeExchange write path against a vault-enabled Repo and asserts
// the tokens never sit plaintext in the data column at rest, and that the
// single-row reader hydrates them back.
//
// The db-level test only calls CreateProviderConnectionFull /
// ReplaceProviderConnectionPayload directly — APIs that already sealed before
// this change — so it passes unchanged on the pre-fix base and proves nothing
// about the handler. This one goes through the handler, which is the path the
// fix actually rewrote: before it, the exchange wrote the payload with
// RawDB().Exec and left the tokens in `data` in the clear.
func TestHandleAuthCodeExchange_sealsTokensThroughVaultRepo(t *testing.T) {
	const accessToken = "at-handler-level-secret"
	const refreshToken = "rt-handler-level-secret"

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"` + accessToken + `","refresh_token":"` + refreshToken + `","expires_in":3600}`))
	}))
	defer mock.Close()

	// No userInfoURL: the credential stays the access token, so the conn id
	// is derived exactly the way the handler derives it.
	old := authcodeProviders["iflow"]
	authcodeProviders["iflow"] = &authcodeConfig{
		providers: []string{"iflow"}, clientID: "10009311001", secret: "s",
		tokenURL: mock.URL + "/oauth/token", useBasic: true,
		connPrefix: "if-", display: "iFlow",
	}
	defer func() { authcodeProviders["iflow"] = old }()

	database, cleanup := setupTestDB(t)
	defer cleanup()

	v, err := vault.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatalf("build vault: %v", err)
	}
	repo := db.NewRepo(database)
	repo.SetVault(v)

	handler := NewOAuthHandler(repo)
	req := httptest.NewRequest("POST", "/api/oauth/authcode/exchange",
		strings.NewReader(`{"provider":"iflow","code":"c"}`))
	rec := httptest.NewRecorder()
	handler.HandleAuthCodeExchange(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	connID := "if-" + shortHash(accessToken)

	// Sealed at rest: raw SQL must not carry either token in the clear.
	var raw string
	if err := repo.RawDB().QueryRow(`SELECT data FROM providerConnections WHERE id = ?`, connID).Scan(&raw); err != nil {
		t.Fatalf("read raw data %s: %v", connID, err)
	}
	if strings.Contains(raw, accessToken) {
		t.Errorf("access token is plaintext in the data column at rest: %s", raw)
	}
	if strings.Contains(raw, refreshToken) {
		t.Errorf("refresh token is plaintext in the data column at rest: %s", raw)
	}

	// Hydrated back through the sealed reader.
	got, err := repo.GetProviderConnectionByID(connID)
	if err != nil {
		t.Fatalf("get connection by ID: %v", err)
	}
	if !strings.Contains(got.Data, accessToken) {
		t.Errorf("reader did not hydrate the access token back: %s", got.Data)
	}
}
