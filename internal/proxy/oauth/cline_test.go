package oauth

import (
	"9router/proxy/internal/providers"
	"9router/proxy/internal/updater"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRefreshCline(t *testing.T) {
	var capturedUA, capturedClientType string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected Content-Type: application/json")
		}
		capturedUA = r.Header.Get("User-Agent")
		capturedClientType = r.Header.Get("X-CLIENT-TYPE")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"success": true,
			"data": {
				"accessToken": "new-cline-access-token",
				"refreshToken": "new-cline-refresh-token",
				"expiresIn": 7200
			}
		}`))
	}))
	defer ts.Close()

	providers.KnownOAuthConfigs["cline"] = providers.OAuthClientConfig{
		TokenURL: ts.URL,
	}
	providers.KnownOAuthConfigs["clinepass"] = providers.OAuthClientConfig{
		TokenURL: ts.URL,
	}

	// Test cline
	p := &Params{
		Client:       ts.Client(),
		Provider:     "cline",
		RefreshToken: "old-refresh-token",
	}

	res, err := RefreshCline(context.Background(), p)
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	if res.AccessToken != "new-cline-access-token" {
		t.Errorf("expected accessToken 'new-cline-access-token', got: %s", res.AccessToken)
	}
	if res.RefreshToken != "new-cline-refresh-token" {
		t.Errorf("expected refreshToken 'new-cline-refresh-token', got: %s", res.RefreshToken)
	}
	if res.ExpiresIn != 7200 {
		t.Errorf("expected expiresIn 7200, got: %d", res.ExpiresIn)
	}
	if capturedUA != "9Router/"+updater.CurrentVersion {
		t.Errorf("expected User-Agent '9Router/%s', got: %s", updater.CurrentVersion, capturedUA)
	}
	if capturedClientType != "9router" {
		t.Errorf("expected X-CLIENT-TYPE '9router', got: %s", capturedClientType)
	}

	// Verify clinepass is registered in oauth registry
	refresher := Get("clinepass")
	if refresher == nil {
		t.Fatalf("expected clinepass to be registered in oauth registry")
	}

	pPass := &Params{
		Client:       ts.Client(),
		Provider:     "clinepass",
		RefreshToken: "old-refresh-token",
	}
	resPass, err := refresher(context.Background(), pPass)
	if err != nil {
		t.Fatalf("expected clinepass refresh success, got: %v", err)
	}
	if resPass.AccessToken != "new-cline-access-token" {
		t.Errorf("expected accessToken 'new-cline-access-token', got: %s", resPass.AccessToken)
	}
	if resPass.RefreshToken != "new-cline-refresh-token" {
		t.Errorf("expected refreshToken 'new-cline-refresh-token', got: %s", resPass.RefreshToken)
	}
}

func TestRefreshCline_ErrorHandling(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"success": false,
			"error": "failed to refresh token: invalid_grant",
			"data": ""
		}`))
	}))
	defer ts.Close()

	p := &Params{
		Client:       ts.Client(),
		Provider:     "clinepass",
		RefreshToken: "bad-token",
	}
	providers.KnownOAuthConfigs["clinepass"] = providers.OAuthClientConfig{
		TokenURL: ts.URL,
	}

	_, err := RefreshCline(context.Background(), p)
	if err == nil {
		t.Fatalf("expected error from failed refresh, got nil")
	}
}
