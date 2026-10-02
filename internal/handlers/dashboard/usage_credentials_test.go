package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"9router/proxy/internal/models"
	"9router/proxy/internal/proxy/oauth"
)

// errRefreshRefused stands in for a refresh token the provider will not honour.
var errRefreshRefused = errors.New("refresh token revoked")

// stubOllamaUsage repoints the ollama quota endpoints at a test server. ollama
// is the one usage fetcher with both endpoints behind package vars, so it is
// the cheapest seam for exercising the generic refresh-then-read handler.
func stubOllamaUsage(t *testing.T, usageURL, meURL string) {
	t.Helper()
	previousUsage, previousMe := ollamaUsageURL, ollamaMeURL
	ollamaUsageURL, ollamaMeURL = usageURL, meURL
	t.Cleanup(func() { ollamaUsageURL, ollamaMeURL = previousUsage, previousMe })
}

// hasQuotaRow reports whether a usage response carries a named quota row, so a
// test asserts on the reading instead of on a raw JSON substring.
func hasQuotaRow(body, name string) bool {
	var payload struct {
		Quotas map[string]json.RawMessage `json:"quotas"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return false
	}
	_, ok := payload.Quotas[name]
	return ok
}

// registerStubRefresher installs a process-wide refresher for a synthetic
// provider and restores the previous registration afterwards.
func registerStubRefresher(t *testing.T, provider string, fn oauth.Refresher) {
	previous := oauth.Get(provider)
	oauth.Register(provider, fn)
	t.Cleanup(func() {
		// A leaked stub keeps answering after the test, which would make later
		// code believe this provider has a refresher it does not have.
		if previous != nil {
			oauth.Register(provider, previous)
			return
		}
		oauth.Unregister(provider)
	})
}

func TestUsageCredentialsNeedsRefresh(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
		want bool
	}{
		{
			name: "expiry long past counts as expired",
			raw:  map[string]any{"expiresAt": time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)},
			want: true,
		},
		{
			// The parse must stay strict: a timestamp Go cannot read must not
			// count as expired, or every panel open burns a refresh token.
			name: "unparseable expiry is not treated as expired",
			raw:  map[string]any{"expiresAt": "2026-10-02 07:12:32 +0000 UTC"},
			want: false,
		},
		{
			// Fractional seconds do parse, and this one really is in the past.
			name: "fractional-second expiry in the past counts as expired",
			raw:  map[string]any{"expiresAt": "2026-10-01T00:00:00.000Z"},
			want: true,
		},
		{
			name: "absent expiry is not treated as expired",
			raw:  map[string]any{},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			creds := usageCredentials{raw: tt.raw, provider: "kiro"}
			if got := creds.needsRefresh(); got != tt.want {
				t.Errorf("needsRefresh() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestUsageCredentialsAccessTokenFallsBackToAPIKey(t *testing.T) {
	creds := usageCredentials{raw: map[string]any{"apiKey": "kc-key"}}
	if got := creds.accessToken(); got != "kc-key" {
		t.Errorf("accessToken() = %q, want the apiKey fallback", got)
	}
}

func TestIsAuthExpiredUsageMessage(t *testing.T) {
	tests := []struct {
		name string
		res  usageResult
		want bool
	}{
		{
			name: "kiro social auth-expired message",
			res:  usageResult{message: "Kiro quota API authentication expired. Chat may still work."},
			want: true,
		},
		{
			// Upstream matches on wording only ("expired", "401", …), and its own
			// IDC-session message contains none of them, so upstream leaves that
			// message unretried. Mirrored rather than "improved", so the retry
			// fires for exactly the cases upstream fires it for.
			name: "kiro idc session message is not retried",
			res:  usageResult{message: "Kiro quota API is unavailable for the current AWS IAM Identity Center session."},
			want: false,
		},
		{
			name: "transient failure is not an auth failure",
			res:  usageResult{message: "Unable to fetch Kiro usage right now. (q-get:503)"},
			want: false,
		},
		{
			name: "a live reading has no message",
			res:  usageResult{plan: "Kiro", quotas: map[string]any{"agenticrequest": map[string]any{}}},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isAuthExpiredUsageMessage(tt.res); got != tt.want {
				t.Errorf("isAuthExpiredUsageMessage() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsUsageRefreshable(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		authType string
		raw      map[string]any
		want     bool
	}{
		{name: "oauth with a refresh token", provider: "kiro", authType: "oauth", raw: map[string]any{"refreshToken": "rt"}, want: true},
		{name: "oauth without a refresh token", provider: "kiro", authType: "oauth", raw: map[string]any{}, want: false},
		{name: "oauth with a blank refresh token", provider: "kiro", authType: "oauth", raw: map[string]any{"refreshToken": "  "}, want: false},
		{name: "api key carries nothing to exchange", provider: "kiro", authType: "api_key", raw: map[string]any{"refreshToken": "rt"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn := &models.ProviderConnection{ID: "t", Provider: tt.provider, AuthType: tt.authType}
			if got := isUsageRefreshable(conn, tt.raw); got != tt.want {
				t.Errorf("isUsageRefreshable() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHandleGetConnectionUsage_RefreshesExpiredTokenBeforeReading(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)

	// The connection is a real ollama one: fetchProviderUsage dispatches on
	// provider id, so a synthetic id would never reach a fetcher at all.
	registerStubRefresher(t, "ollama", func(_ context.Context, _ *oauth.Params) (*oauth.TokenResult, error) {
		return &oauth.TokenResult{AccessToken: "fresh-token", RefreshToken: "fresh-refresh", ExpiresIn: 3600}, nil
	})

	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"limits":{"session":{"usage":0.1}}}`))
	}))
	t.Cleanup(srv.Close)
	stubOllamaUsage(t, srv.URL+"/api/usage", srv.URL+"/api/me")

	expired := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	// Kiro writes its OAuth token into both accessToken and apiKey on login, so
	// the blob mirrors that shape: a refresh that rotated one field but not the
	// other would leave the stale copy in place for the fetcher to send.
	payload := `{"accessToken":"stale","apiKey":"stale","refreshToken":"rt","expiresAt":"` + expired + `"}`
	if err := repo.CreateProviderConnectionFull("conn-refresh", "ollama", "oauth", "Refresh", nil, payload); err != nil {
		t.Fatalf("create connection: %v", err)
	}

	rec := httptest.NewRecorder()
	setupTestRouter(repo).ServeHTTP(rec, httptest.NewRequest("GET", "/api/usage/conn-refresh", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	// The point of #78 item 4: the quota read must carry the refreshed token,
	// not the stale one that kept chat working while quota refused.
	if seen != "Bearer fresh-token" {
		t.Errorf("quota read carried %q, want the refreshed access token", seen)
	}

	stored := readConnectionData(t, repo, "conn-refresh")
	if stored["accessToken"] != "fresh-token" || stored["refreshToken"] != "fresh-refresh" {
		t.Errorf("rotated credentials not persisted: %v", stored)
	}
	if stored["expiresAt"] == expired {
		t.Error("expiresAt must move forward after a refresh")
	}
}

func TestHandleGetConnectionUsage_RetriesOnceWhenProviderReportsAuthExpired(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)

	var refreshes int
	registerStubRefresher(t, "ollama", func(_ context.Context, _ *oauth.Params) (*oauth.TokenResult, error) {
		refreshes++
		return &oauth.TokenResult{AccessToken: "second-try-token", ExpiresIn: 3600}, nil
	})

	// The provider rejects the stale token, then accepts the refreshed one: the
	// shape upstream retries on exactly once.
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer second-try-token" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"token expired"}`))
			return
		}
		_, _ = w.Write([]byte(`{"limits":{"session":{"usage":0.25}}}`))
	}))
	t.Cleanup(srv.Close)
	stubOllamaUsage(t, srv.URL+"/api/usage", srv.URL+"/api/me")

	// A live expiresAt keeps the pre-read refresh out of the way, so this
	// exercises the retry half on its own.
	payload := `{"accessToken":"stale","apiKey":"stale","refreshToken":"rt","expiresAt":"2099-01-01T00:00:00Z"}`
	if err := repo.CreateProviderConnectionFull("conn-retry", "ollama", "oauth", "Retry", nil, payload); err != nil {
		t.Fatalf("create connection: %v", err)
	}

	rec := httptest.NewRecorder()
	setupTestRouter(repo).ServeHTTP(rec, httptest.NewRequest("GET", "/api/usage/conn-retry", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if refreshes != 1 {
		t.Errorf("refresh attempts = %d, want exactly 1 retry", refreshes)
	}
	if calls < 2 {
		t.Errorf("provider reads = %d, want the read retried with the new token", calls)
	}
	if !hasQuotaRow(rec.Body.String(), "Session (5h)") {
		t.Errorf("expected a live quota row after the retry, got %s", rec.Body.String())
	}
}

func TestHandleGetConnectionUsage_FailedRefreshStillReadsWithStoredToken(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)

	registerStubRefresher(t, "ollama", func(_ context.Context, _ *oauth.Params) (*oauth.TokenResult, error) {
		return nil, errRefreshRefused
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"limits":{"session":{"usage":0.5}}}`))
	}))
	t.Cleanup(srv.Close)
	stubOllamaUsage(t, srv.URL+"/api/usage", srv.URL+"/api/me")

	expired := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	payload := `{"accessToken":"still-valid","apiKey":"still-valid","refreshToken":"rt","expiresAt":"` + expired + `"}`
	if err := repo.CreateProviderConnectionFull("conn-fail", "ollama", "oauth", "Fail", nil, payload); err != nil {
		t.Fatalf("create connection: %v", err)
	}

	rec := httptest.NewRecorder()
	setupTestRouter(repo).ServeHTTP(rec, httptest.NewRequest("GET", "/api/usage/conn-fail", nil))

	// A refused refresh must not blank the panel: upstream falls back to the
	// stored token whenever one is available, and so does this.
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if !hasQuotaRow(rec.Body.String(), "Session (5h)") {
		t.Errorf("expected a quota row from the stored token, got %s", rec.Body.String())
	}
}
