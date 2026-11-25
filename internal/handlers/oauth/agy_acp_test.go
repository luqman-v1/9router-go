package oauth

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

func TestHandleAGYACPAuthorize(t *testing.T) {
	handler := NewOAuthHandler(nil)
	req := httptest.NewRequest(
		http.MethodGet,
		"/api/oauth/agy-acp/authorize?state=acp_state_1&redirect_uri=http%3A%2F%2Flocalhost%3A20130%2Fcallback",
		nil,
	)
	rec := httptest.NewRecorder()
	handler.HandleAGYACPAuthorize(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var res struct {
		URL         string `json:"url"`
		State       string `json:"state"`
		RedirectURI string `json:"redirectUri"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("failed to unmarshal JSON: %v", err)
	}
	authURL, err := url.Parse(res.URL)
	if err != nil {
		t.Fatalf("parse auth URL: %v", err)
	}
	if authURL.Host != "accounts.google.com" {
		t.Errorf("auth URL host = %q", authURL.Host)
	}
	if got := authURL.Query().Get("redirect_uri"); got != "http://localhost:20130/callback" {
		t.Errorf("redirect_uri = %q", got)
	}
	if res.State != "acp_state_1" {
		t.Errorf("state = %q", res.State)
	}
	if authURL.Query().Get("access_type") != "offline" {
		t.Errorf("access_type = %q", authURL.Query().Get("access_type"))
	}
}

func TestHandleAGYACPExchangeStoresOwnConnection(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	const redirectURI = "http://localhost:20130/callback"

	mockGoogle := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		if got := r.Form.Get("code"); got != "acp_code_123" {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		if got := r.Form.Get("redirect_uri"); got != redirectURI {
			http.Error(w, "redirect mismatch", http.StatusBadRequest)
			return
		}
		// {"email":"acp-user@gmail.com"} JWT payload, alg none.
		mockIDToken := "eyJhbGciOiJub25lIn0.eyJlbWFpbCI6ImFjcC11c2VyQGdtYWlsLmNvbSJ9."
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"access_token": "ya29.acp_access",
			"refresh_token": "1//acp_refresh",
			"expires_in": 3600,
			"token_type": "Bearer",
			"id_token": "` + mockIDToken + `"
		}`))
	}))
	defer mockGoogle.Close()

	oldTokenURL := googleOAuthTokenURL
	googleOAuthTokenURL = mockGoogle.URL + "/token"
	defer func() { googleOAuthTokenURL = oldTokenURL }()

	handler := NewOAuthHandler(repo)
	body := `{"code":"acp_code_123","redirectUri":"` + redirectURI + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/oauth/agy-acp/exchange", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.HandleAGYACPExchange(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Success    bool `json:"success"`
		Connection struct {
			ID       string `json:"id"`
			Provider string `json:"provider"`
			Email    string `json:"email"`
		} `json:"connection"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("failed to parse response JSON: %v", err)
	}
	if !res.Success || res.Connection.Provider != "agy-acp" {
		t.Fatalf("unexpected response: %s", rec.Body.String())
	}
	if res.Connection.Email != "acp-user@gmail.com" {
		t.Errorf("email = %q", res.Connection.Email)
	}
	if !strings.HasPrefix(res.Connection.ID, "agcpy-") {
		t.Errorf("connection id = %q, want agcpy- prefix", res.Connection.ID)
	}

	// The row must be agy-acp's own, never an antigravity row.
	var provider, authType, data string
	err := database.QueryRow(
		"SELECT provider, authType, data FROM providerConnections WHERE id = ?",
		res.Connection.ID,
	).Scan(&provider, &authType, &data)
	if err != nil {
		t.Fatalf("query connection: %v", err)
	}
	if provider != "agy-acp" || authType != "oauth" {
		t.Errorf("provider=%q authType=%q", provider, authType)
	}
	var dataMap map[string]any
	if err := json.Unmarshal([]byte(data), &dataMap); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if dataMap["accessToken"] != "ya29.acp_access" || dataMap["refreshToken"] != "1//acp_refresh" {
		t.Errorf("stored tokens wrong: %v", dataMap)
	}
	if dataMap["expiresAt"] == nil || dataMap["expiresAt"] == "" {
		t.Errorf("missing expiresAt: %v", dataMap)
	}
	var agCount int
	if err := database.QueryRow("SELECT COUNT(*) FROM providerConnections WHERE provider = 'antigravity'").Scan(&agCount); err != nil {
		t.Fatalf("count antigravity rows: %v", err)
	}
	if agCount != 0 {
		t.Errorf("agy-acp login wrote %d antigravity rows, want 0", agCount)
	}

	// Re-login for the same account updates the same row, not a duplicate.
	req2 := httptest.NewRequest(http.MethodPost, "/api/oauth/agy-acp/exchange", strings.NewReader(body))
	rec2 := httptest.NewRecorder()
	handler.HandleAGYACPExchange(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second exchange: %d: %s", rec2.Code, rec2.Body.String())
	}
	var total int
	if err := database.QueryRow("SELECT COUNT(*) FROM providerConnections WHERE provider = 'agy-acp'").Scan(&total); err != nil {
		t.Fatalf("count agy-acp rows: %v", err)
	}
	if total != 1 {
		t.Errorf("re-login created %d agy-acp rows, want 1", total)
	}
}

func TestHandleAGYACPExchangeErrors(t *testing.T) {
	handler := NewOAuthHandler(nil)
	tests := []struct {
		name string
		body string
		want int
	}{
		{"missing code", `{"redirectUri":"http://localhost:20130/callback"}`, http.StatusBadRequest},
		{"missing redirect", `{"code":"abc"}`, http.StatusBadRequest},
		{"invalid redirect", `{"code":"abc","redirectUri":"not-a-url"}`, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/oauth/agy-acp/exchange", strings.NewReader(tt.body))
			rec := httptest.NewRecorder()
			handler.HandleAGYACPExchange(rec, req)
			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}
