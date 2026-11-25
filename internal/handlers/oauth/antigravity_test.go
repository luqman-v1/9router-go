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

func TestHandleAntigravityAuthorize(t *testing.T) {
	handler := NewOAuthHandler(nil)
	req := httptest.NewRequest(
		http.MethodGet,
		"/api/oauth/antigravity/authorize?state=custom_state_123&redirect_uri=http%3A%2F%2Flocalhost%3A20130%2Fcallback",
		nil,
	)
	rec := httptest.NewRecorder()
	handler.HandleAntigravityAuthorize(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var res struct {
		URL         string `json:"url"`
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
	if res.RedirectURI != "http://localhost:20130/callback" {
		t.Errorf("response redirectUri = %q", res.RedirectURI)
	}
	if authURL.Query().Get("state") != "custom_state_123" {
		t.Errorf("state = %q", authURL.Query().Get("state"))
	}
	if authURL.Query().Get("access_type") != "offline" {
		t.Errorf("access_type = %q", authURL.Query().Get("access_type"))
	}
	expectedScopes := []string{
		"https://www.googleapis.com/auth/cloud-platform",
		"https://www.googleapis.com/auth/userinfo.email",
		"https://www.googleapis.com/auth/userinfo.profile",
		"https://www.googleapis.com/auth/cclog",
		"https://www.googleapis.com/auth/experimentsandconfigs",
	}
	if got := authURL.Query().Get("scope"); got != strings.Join(expectedScopes, " ") {
		t.Errorf("scope = %q", got)
	}

	reqRedirect := httptest.NewRequest(http.MethodGet, "/api/oauth/antigravity/authorize?redirect=true", nil)
	recRedirect := httptest.NewRecorder()
	handler.HandleAntigravityAuthorize(recRedirect, reqRedirect)
	if recRedirect.Code != http.StatusFound {
		t.Errorf("expected 302 redirect, got %d", recRedirect.Code)
	}
}

func TestGetAntigravityRedirectURI(t *testing.T) {
	t.Setenv("PORT", "20130")
	tests := []struct {
		name     string
		request  *http.Request
		expected string
	}{
		{
			name:     "explicit loopback callback wins",
			request:  httptest.NewRequest(http.MethodGet, "/?redirect_uri=http%3A%2F%2Flocalhost%3A20130%2Fcallback", nil),
			expected: "http://localhost:20130/callback",
		},
		{
			name:     "reverse proxy pins callback to the gateway listener",
			request:  httptest.NewRequest(http.MethodGet, "/?proxy=1&redirect_uri=http%3A%2F%2Flocalhost%3A80%2Fcallback", nil),
			expected: "http://localhost:20130/callback",
		},
		{
			name:     "invalid callback falls back upstream default",
			request:  httptest.NewRequest(http.MethodGet, "/", nil),
			expected: "http://localhost:8080/callback",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := getAntigravityRedirectURI(test.request); got != test.expected {
				t.Fatalf("getAntigravityRedirectURI() = %q, expected %q", got, test.expected)
			}
		})
	}
}

func TestHandleAntigravityAuthorizeProxyRedirect(t *testing.T) {
	t.Setenv("PORT", "20130")
	handler := NewOAuthHandler(nil)
	req := httptest.NewRequest(http.MethodGet, "/api/oauth/antigravity/authorize?proxy=1&redirect_uri=http%3A%2F%2Flocalhost%3A80%2Fcallback&state=proxy-state", nil)
	rec := httptest.NewRecorder()
	handler.HandleAntigravityAuthorize(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		AuthURL     string `json:"authUrl"`
		RedirectURI string `json:"redirectUri"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.RedirectURI != "http://localhost:20130/callback" {
		t.Fatalf("redirectUri = %q", body.RedirectURI)
	}
	authURL, err := url.Parse(body.AuthURL)
	if err != nil {
		t.Fatalf("parse auth URL: %v", err)
	}
	if got := authURL.Query().Get("redirect_uri"); got != "http://localhost:20130/callback" {
		t.Fatalf("auth redirect_uri = %q", got)
	}
}

func TestHandleAntigravityExchangeErrors(t *testing.T) {
	handler := NewOAuthHandler(nil)

	tests := []struct {
		name         string
		method       string
		body         string
		expectedCode int
	}{
		{name: "method must be post", method: http.MethodGet, expectedCode: http.StatusMethodNotAllowed},
		{name: "missing code", method: http.MethodPost, body: `{"redirectUri":"http://localhost:20130/callback"}`, expectedCode: http.StatusBadRequest},
		{name: "missing redirect uri", method: http.MethodPost, body: `{"code":"code-123"}`, expectedCode: http.StatusBadRequest},
		{name: "invalid redirect uri", method: http.MethodPost, body: `{"code":"code-123","redirectUri":"javascript:alert(1)"}`, expectedCode: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(test.method, "/api/oauth/antigravity/exchange", strings.NewReader(test.body))
			rec := httptest.NewRecorder()
			handler.HandleAntigravityExchange(rec, req)
			if rec.Code != test.expectedCode {
				t.Fatalf("status = %d, expected %d: %s", rec.Code, test.expectedCode, rec.Body.String())
			}
		})
	}
}

func TestHandleAntigravityExchangeSuccess(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	const redirectURI = "http://localhost:20130/callback"

	mockGoogleServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		if got := r.Form.Get("redirect_uri"); got != redirectURI {
			http.Error(w, "redirect mismatch", http.StatusBadRequest)
			return
		}
		if got := r.Form.Get("code"); got != "valid_google_code_123" {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}

		// Simple mock JWT id_token with claims: {"email":"antigravity-user@gmail.com"}
		// Header: {"alg":"none"} -> eyJhbGciOiJub25lIn0
		// Payload: {"email":"antigravity-user@gmail.com"} -> eyJlbWFpbCI6ImFudGlncmF2aXR5LXVzZXJAZ21haWwuY29tIn0
		mockIDToken := "eyJhbGciOiJub25lIn0.eyJlbWFpbCI6ImFudGlncmF2aXR5LXVzZXJAZ21haWwuY29tIn0."

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"access_token": "ya29.mock_access_token_xyz",
			"refresh_token": "1//mock_refresh_token_uvw",
			"expires_in": 3600,
			"token_type": "Bearer",
			"id_token": "` + mockIDToken + `",
			"scope": "https://www.googleapis.com/auth/cloud-platform"
		}`))
	}))
	defer mockGoogleServer.Close()

	oldTokenURL := googleOAuthTokenURL
	googleOAuthTokenURL = mockGoogleServer.URL + "/token"
	defer func() { googleOAuthTokenURL = oldTokenURL }()

	handler := NewOAuthHandler(repo)
	body := `{"code":"valid_google_code_123","redirectUri":"` + redirectURI + `","state":"state-123"}`
	req := httptest.NewRequest(http.MethodPost, "/api/oauth/antigravity/exchange", strings.NewReader(body))
	rec := httptest.NewRecorder()

	handler.HandleAntigravityExchange(rec, req)

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
	if !res.Success {
		t.Error("expected success response")
	}
	if res.Connection.Provider != "antigravity" {
		t.Errorf("expected provider 'antigravity', got %q", res.Connection.Provider)
	}
	if res.Connection.Email != "antigravity-user@gmail.com" {
		t.Errorf("expected email 'antigravity-user@gmail.com', got %q", res.Connection.Email)
	}
	connID := res.Connection.ID
	if connID == "" {
		t.Fatalf("missing connection id in response: %s", rec.Body.String())
	}

	// Verify DB record
	var provider, authType, name, data string
	var isActive int
	err := database.QueryRow(
		"SELECT provider, authType, name, isActive, data FROM providerConnections WHERE id = ?",
		connID,
	).Scan(&provider, &authType, &name, &isActive, &data)
	if err != nil {
		t.Fatalf("failed to query providerConnections: %v", err)
	}

	if provider != "antigravity" {
		t.Errorf("expected provider 'antigravity', got %s", provider)
	}
	if authType != "oauth" {
		t.Errorf("expected authType 'oauth', got %s", authType)
	}
	if !strings.Contains(name, "antigravity-user@gmail.com") {
		t.Errorf("expected name to contain email, got %s", name)
	}
	if isActive != 1 {
		t.Errorf("expected isActive 1, got %d", isActive)
	}

	var dataMap map[string]any
	if err := json.Unmarshal([]byte(data), &dataMap); err != nil {
		t.Fatalf("failed to unmarshal stored data: %v", err)
	}
	if dataMap["accessToken"] != "ya29.mock_access_token_xyz" {
		t.Errorf("expected accessToken, got %v", dataMap["accessToken"])
	}
	if dataMap["refreshToken"] != "1//mock_refresh_token_uvw" {
		t.Errorf("expected refreshToken, got %v", dataMap["refreshToken"])
	}
	if dataMap["email"] != "antigravity-user@gmail.com" {
		t.Errorf("expected email 'antigravity-user@gmail.com', got %v", dataMap["email"])
	}
	if dataMap["projectId"] != "aicode-consumers" {
		t.Errorf("expected projectId 'aicode-consumers', got %v", dataMap["projectId"])
	}

	// Update the existing connection with a custom project ID
	dataMap["projectId"] = "my-custom-project"
	customDataBytes, _ := json.Marshal(dataMap)
	_, err = database.Exec("UPDATE providerConnections SET data = ? WHERE id = ?", string(customDataBytes), connID)
	if err != nil {
		t.Fatalf("failed to update DB with custom projectId: %v", err)
	}

	// Exchange again for the same account — custom project ID should be preserved
	req2 := httptest.NewRequest(http.MethodPost, "/api/oauth/antigravity/exchange", strings.NewReader(body))
	rec2 := httptest.NewRecorder()
	handler.HandleAntigravityExchange(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200 on second exchange, got %d: %s", rec2.Code, rec2.Body.String())
	}

	err = database.QueryRow("SELECT data FROM providerConnections WHERE id = ?", connID).Scan(&data)
	if err != nil {
		t.Fatalf("failed to re-query providerConnections: %v", err)
	}
	var dataMap2 map[string]any
	if err := json.Unmarshal([]byte(data), &dataMap2); err != nil {
		t.Fatalf("failed to unmarshal updated data: %v", err)
	}
	if dataMap2["projectId"] != "my-custom-project" {
		t.Errorf("expected custom projectId 'my-custom-project' to be preserved, got %v", dataMap2["projectId"])
	}
}
