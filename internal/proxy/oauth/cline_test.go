package oauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/providers"
)

func TestRefreshCline(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected Content-Type: application/json")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
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
	if res.ExpiresIn != 7200 {
		t.Errorf("expected expiresIn 7200, got: %d", res.ExpiresIn)
	}
}
