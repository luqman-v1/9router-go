package oauth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/vault"
)

func TestHandleClineExchange_SealsTokensAndPreservesRefreshOnRelogin(t *testing.T) {
	const accessToken = "cline-at-secret"
	const refreshToken = "cline-rt-secret"

	callCount := 0
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/auth/token" {
			http.NotFound(w, r)
			return
		}
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if callCount == 1 {
			_, _ = w.Write([]byte(`{"success":true,"data":{"accessToken":"` + accessToken + `","refreshToken":"` + refreshToken + `","expiresIn":3600}}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{"accessToken":"` + accessToken + `","expiresIn":3600}}`))
	}))
	defer mock.Close()

	old := clineOAuthTokenURL
	clineOAuthTokenURL = mock.URL + "/api/v1/auth/token"
	defer func() { clineOAuthTokenURL = old }()

	database, cleanup := setupTestDB(t)
	defer cleanup()

	v, err := vault.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatalf("build vault: %v", err)
	}
	repo := db.NewRepo(database)
	repo.SetVault(v)

	handler := NewOAuthHandler(repo)

	req1 := httptest.NewRequest(http.MethodPost, "/api/oauth/cline/exchange",
		strings.NewReader(`{"provider":"clinepass","code":"code-1","codeVerifier":"v","redirectUri":"http://127.0.0.1:0/callback","name":"Initial Cline"}`))
	req1.Header.Set("Content-Type", "application/json")
	rec1 := httptest.NewRecorder()
	handler.HandleClineExchange(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first exchange status = %d: %s", rec1.Code, rec1.Body.String())
	}

	connID := "clinepass-" + shortHash(accessToken)
	var raw string
	if err := repo.RawDB().QueryRow(`SELECT data FROM providerConnections WHERE id = ?`, connID).Scan(&raw); err != nil {
		t.Fatalf("read raw data after create: %v", err)
	}
	if strings.Contains(raw, accessToken) {
		t.Fatalf("accessToken stored plaintext at rest after create: %s", raw)
	}
	if strings.Contains(raw, refreshToken) {
		t.Fatalf("refreshToken stored plaintext at rest after create: %s", raw)
	}

	got, err := repo.GetProviderConnectionByID(connID)
	if err != nil {
		t.Fatalf("hydrate connection after create: %v", err)
	}
	if !strings.Contains(got.Data, accessToken) || !strings.Contains(got.Data, refreshToken) {
		t.Fatalf("reader did not hydrate tokens after create: %s", got.Data)
	}

	// Relogin without refreshToken in upstream payload: must stay sealed AND keep prior refreshToken.
	req2 := httptest.NewRequest(http.MethodPost, "/api/oauth/cline/exchange",
		strings.NewReader(`{"provider":"clinepass","code":"code-2","codeVerifier":"v","redirectUri":"http://127.0.0.1:0/callback","name":"Relogin Cline"}`))
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	handler.HandleClineExchange(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("relogin exchange status = %d: %s", rec2.Code, rec2.Body.String())
	}

	var rawAfterUpdate string
	if err := repo.RawDB().QueryRow(`SELECT data FROM providerConnections WHERE id = ?`, connID).Scan(&rawAfterUpdate); err != nil {
		t.Fatalf("read raw data after relogin: %v", err)
	}
	if strings.Contains(rawAfterUpdate, accessToken) {
		t.Fatalf("accessToken stored plaintext at rest after relogin: %s", rawAfterUpdate)
	}
	if strings.Contains(rawAfterUpdate, refreshToken) {
		t.Fatalf("refreshToken stored plaintext at rest after relogin: %s", rawAfterUpdate)
	}

	gotAfterUpdate, err := repo.GetProviderConnectionByID(connID)
	if err != nil {
		t.Fatalf("hydrate connection after relogin: %v", err)
	}
	if !strings.Contains(gotAfterUpdate.Data, refreshToken) {
		t.Fatalf("relogin dropped sealed refreshToken: %s", gotAfterUpdate.Data)
	}
	if gotAfterUpdate.Name == nil || *gotAfterUpdate.Name != "Relogin Cline" {
		t.Fatalf("expected updated name %q, got %v", "Relogin Cline", gotAfterUpdate.Name)
	}
}
