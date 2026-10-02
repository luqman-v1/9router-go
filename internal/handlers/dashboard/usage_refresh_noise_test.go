package dashboard

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy/oauth"
)

// The three regressions the merged PR #83 produced on a real account set:
// a provider with no refresher (qoder), a revoked grant (grok-cli invalid_grant)
// refreshing twice per request, and a dead grant nothing ever recorded.

func TestIsRefreshGrantDead(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "401 revoked grant",
			err:  &providers.OAuthRefreshError{Status: http.StatusUnauthorized},
			want: true,
		},
		{
			// xAI answers a revoked refresh token with 400, not 401 — the shape
			// behind the grok-cli invalid_grant warnings.
			name: "400 invalid_grant",
			err:  &providers.OAuthRefreshError{Status: http.StatusBadRequest, Body: `{"error":"invalid_grant","error_description":"Invalid or unknown refresh token"}`},
			want: true,
		},
		{
			name: "400 refresh_token_reused",
			err:  &providers.OAuthRefreshError{Status: http.StatusBadRequest, Body: `{"error":"refresh_token_reused"}`},
			want: true,
		},
		{
			// A 400 that is a malformed request, not a dead grant, must not park
			// an account that a single fix would recover.
			name: "400 unrelated body",
			err:  &providers.OAuthRefreshError{Status: http.StatusBadRequest, Body: `{"error":"unsupported_grant_type"}`},
			want: false,
		},
		{
			name: "500 is a provider blip, not proof of a dead grant",
			err:  &providers.OAuthRefreshError{Status: http.StatusInternalServerError, Body: "upstream down"},
			want: false,
		},
		{
			name: "transport error is not a rejection",
			err:  errors.New("POST: dial tcp: timeout"),
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := providers.IsRefreshGrantDead(tt.err); got != tt.want {
				t.Errorf("IsRefreshGrantDead() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsRefreshGrantDead_SurvivesWrapping(t *testing.T) {
	// The handler wraps the provider error with the provider name, so the
	// classification has to survive that layer.
	inner := &providers.OAuthRefreshError{Status: http.StatusBadRequest, Body: `{"error":"invalid_grant"}`}
	wrapped := fmt.Errorf("refresh grok-cli credentials: %w", inner)

	if !providers.IsRefreshGrantDead(wrapped) {
		t.Error("IsRefreshGrantDead() = false for a wrapped invalid_grant, want true")
	}
}

func TestCanAttemptRefresh_SkipsProvidersWithoutRefresher(t *testing.T) {
	const withRefresher = "can-refresh-yes"
	const withoutRefresher = "can-refresh-no"
	registerStubRefresher(t, withRefresher, func(context.Context, *oauth.Params) (*oauth.TokenResult, error) {
		return &oauth.TokenResult{AccessToken: "tok"}, nil
	})

	tests := []struct {
		name     string
		provider string
		want     bool
	}{
		{name: "registered refresher is worth a call", provider: withRefresher, want: true},
		{
			// qoder has no refresher and upstream's executor answers null, so
			// every attempt was a guaranteed failure that logged each time.
			name: "no refresher registered is skipped", provider: withoutRefresher, want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn := &models.ProviderConnection{ID: "t", Provider: tt.provider, AuthType: "oauth"}
			raw := map[string]any{"accessToken": "a", "refreshToken": "r"}
			if got := canAttemptRefresh(conn, raw); got != tt.want {
				t.Errorf("canAttemptRefresh(%q) = %v, want %v", tt.provider, got, tt.want)
			}
		})
	}
}

func TestCanAttemptRefresh_RequiresOAuthAndRefreshToken(t *testing.T) {
	const provider = "can-refresh-guard"
	registerStubRefresher(t, provider, func(context.Context, *oauth.Params) (*oauth.TokenResult, error) {
		return &oauth.TokenResult{AccessToken: "tok"}, nil
	})

	tests := []struct {
		name     string
		authType string
		raw      map[string]any
		want     bool
	}{
		{name: "oauth with refresh token", authType: "oauth", raw: map[string]any{"refreshToken": "r"}, want: true},
		{name: "oauth without refresh token", authType: "oauth", raw: map[string]any{}, want: false},
		{name: "api key has nothing to exchange", authType: "api_key", raw: map[string]any{"refreshToken": "r"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn := &models.ProviderConnection{ID: "t", Provider: provider, AuthType: tt.authType}
			if got := canAttemptRefresh(conn, tt.raw); got != tt.want {
				t.Errorf("canAttemptRefresh() = %v, want %v", got, tt.want)
			}
		})
	}
}

// A revoked grant must refresh exactly once per request. The merged code called
// twice — a pre-read refresh that failed, then the forced retry — which is where
// the paired warnings per account came from.
func TestHandleGetConnectionUsage_RefreshesAtMostOnceWhenGrantIsDead(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)

	const provider = "usage-dead-grant-provider"
	var attempts int
	registerStubRefresher(t, provider, func(_ context.Context, _ *oauth.Params) (*oauth.TokenResult, error) {
		attempts++
		return nil, &providers.OAuthRefreshError{
			Status: http.StatusBadRequest,
			Body:   `{"error":"invalid_grant","error_description":"Invalid or unknown refresh token"}`,
		}
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"token expired"}`))
	}))
	t.Cleanup(srv.Close)
	stubOllamaUsage(t, srv.URL+"/api/usage", srv.URL+"/api/me")

	expired := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)
	payload := `{"accessToken":"dead","apiKey":"dead","refreshToken":"rt","expiresAt":"` + expired + `"}`
	if err := repo.CreateProviderConnectionFull("conn-dead", provider, "oauth", "Dead", nil, payload); err != nil {
		t.Fatalf("create connection: %v", err)
	}

	rec := httptest.NewRecorder()
	setupTestRouter(repo).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/usage/conn-dead", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if attempts != 1 {
		t.Errorf("refresh attempts = %d, want 1: a rejected exchange cannot succeed on a retry", attempts)
	}

	// The grant is dead and only a re-login fixes it, so the row has to say so:
	// before this, a revoked account looked identical to a healthy one.
	stored := readConnectionData(t, repo, "conn-dead")
	lockedUntil, _ := stored["oauthLockedUntil"].(string)
	if lockedUntil == "" {
		t.Errorf("dead grant not recorded on the connection: %v", stored)
	}
	lastErr, _ := stored["lastError"].(map[string]any)
	if lastErr == nil || lastErr["message"] == "" {
		t.Errorf("expected a recorded lastError for the dead grant, got %v", stored["lastError"])
	}
}

// A provider with no refresher must cost zero refresh calls, not two guaranteed
// failures per panel open.
func TestHandleGetConnectionUsage_NoRefreshCallWithoutRefresher(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)

	// A provider absent from providers.KnownOAuthConfigs gets no refresher at
	// all — qoder's shape. ollama would look right here but does have one.
	if oauth.Get("ollama") != nil {
		t.Fatal("ollama unexpectedly has a registered refresher")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"limits":{"session":{"usage":0.1}}}`))
	}))
	t.Cleanup(srv.Close)
	stubOllamaUsage(t, srv.URL+"/api/usage", srv.URL+"/api/me")

	expired := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)
	payload := `{"accessToken":"tok","apiKey":"tok","refreshToken":"rt","expiresAt":"` + expired + `"}`
	if err := repo.CreateProviderConnectionFull("conn-noref", "ollama", "oauth", "NoRef", nil, payload); err != nil {
		t.Fatalf("create connection: %v", err)
	}

	rec := httptest.NewRecorder()
	setupTestRouter(repo).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/usage/conn-noref", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if !hasQuotaRow(rec.Body.String(), "Session (5h)") {
		t.Errorf("expected the quota reading to survive the skip, got %s", rec.Body.String())
	}
	stored := readConnectionData(t, repo, "conn-noref")
	if locked, _ := stored["oauthLockedUntil"].(string); locked != "" {
		t.Errorf("a provider with no refresher must not park the account: %v", stored)
	}
}
