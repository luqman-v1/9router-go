package oauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"9router/proxy/internal/providers"
)

// pinMCodeSite points a provider's token endpoint at a fixture for the life of
// the test and restores it afterwards. Without it a refresh would reach the real
// account host — a live request against a provider, with a made-up grant.
func pinMCodeSite(t *testing.T, provider, tokenURL string) {
	t.Helper()
	prev, existed := miniMaxCodeSites[provider]
	miniMaxCodeSites[provider] = tokenURL
	t.Cleanup(func() {
		if existed {
			miniMaxCodeSites[provider] = prev
			return
		}
		delete(miniMaxCodeSites, provider)
	})
}

// newMCodeServer starts a token endpoint whose replies the test controls and
// counts how many times it was hit. The grant itself is asserted here, once,
// rather than re-read in every case.
func newMCodeServer(t *testing.T, provider string, hits *atomic.Int64, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		_ = r.ParseForm()
		for field, want := range map[string]string{
			"client_id":  miniMaxCodeClientID,
			"grant_type": "refresh_token",
			"scope":      miniMaxCodeScope,
			"audience":   miniMaxCodeAudience,
		} {
			if got := r.Form.Get(field); got != want {
				t.Errorf("%s = %q, want %q", field, got, want)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	pinMCodeSite(t, provider, srv.URL)
	return srv
}

// mcodeParams is a refresh against the pinned fixture.
func mcodeParams(server *httptest.Server, provider, refreshToken string) *Params {
	return &Params{Client: server.Client(), Provider: provider, RefreshToken: refreshToken}
}

// TestRefreshMiniMaxCode_RotatesRefreshToken pins the contract that matters
// most for this provider: the answer's refresh token replaces the one that was
// spent, so the next refresh does not resend it.
func TestRefreshMiniMaxCode_RotatesRefreshToken(t *testing.T) {
	var hits atomic.Int64
	server := newMCodeServer(t, "minimax-code", &hits, http.StatusOK,
		`{"access_token":"a2","refresh_token":"r2","expires_in":3600}`)

	result, err := refreshMiniMaxCode(context.Background(), mcodeParams(server, "minimax-code", "r1"))
	if err != nil {
		t.Fatalf("refreshMiniMaxCode: %v", err)
	}
	if result.AccessToken != "a2" {
		t.Errorf("AccessToken = %q, want a2", result.AccessToken)
	}
	if result.RefreshToken != "r2" {
		t.Errorf("RefreshToken = %q, want the rotated r2", result.RefreshToken)
	}
	if result.ExpiresIn != 3600 {
		t.Errorf("ExpiresIn = %d, want 3600", result.ExpiresIn)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("token endpoint called %d times, want 1", n)
	}
}

// A body that omits the rotated token keeps the old one, so a partial answer
// never signs the account out on the next refresh.
func TestRefreshMiniMaxCode_KeepsRefreshTokenWhenOmitted(t *testing.T) {
	server := newMCodeServer(t, "minimax-code", nil, http.StatusOK, `{"access_token":"a2","expires_in":60}`)

	result, err := refreshMiniMaxCode(context.Background(), mcodeParams(server, "minimax-code", "r1"))
	if err != nil {
		t.Fatalf("refreshMiniMaxCode: %v", err)
	}
	if result.RefreshToken != "r1" {
		t.Errorf("RefreshToken = %q, want the unchanged r1", result.RefreshToken)
	}
	if result.ExpiresIn != 60 {
		t.Errorf("ExpiresIn = %d, want 60", result.ExpiresIn)
	}
}

// An answer without expires_in must not be stored as already-expired, which
// would drive a refresh on every single request.
func TestRefreshMiniMaxCode_DefaultsExpiry(t *testing.T) {
	server := newMCodeServer(t, "minimax-code", nil, http.StatusOK, `{"access_token":"a2","refresh_token":"r2"}`)

	result, err := refreshMiniMaxCode(context.Background(), mcodeParams(server, "minimax-code", "r1"))
	if err != nil {
		t.Fatalf("refreshMiniMaxCode: %v", err)
	}
	if result.ExpiresIn != miniMaxCodeDefaultExpiresIn {
		t.Errorf("ExpiresIn = %d, want the %d default", result.ExpiresIn, miniMaxCodeDefaultExpiresIn)
	}
}

// The two sites are distinct accounts, so neither may borrow the other's host.
func TestRefreshMiniMaxCode_SitesAreDistinct(t *testing.T) {
	for provider, host := range miniMaxCodeSites {
		if host == "" {
			t.Errorf("no token endpoint registered for %s", provider)
		}
	}
	if miniMaxCodeSites["minimax-code"] == miniMaxCodeSites["minimax-code-global"] {
		t.Fatal("both mcode sites share one token endpoint; sign-ins are per site")
	}
}

// A spent refresh token is the only answer that means the sign-in is gone, and
// it must reach the router as a terminal one.
func TestRefreshMiniMaxCode_SpentTokenIsGrantDead(t *testing.T) {
	server := newMCodeServer(t, "minimax-code", nil, http.StatusBadRequest, `{"error":"invalid_grant"}`)

	_, err := refreshMiniMaxCode(context.Background(), mcodeParams(server, "minimax-code", "spent"))
	if err == nil {
		t.Fatal("a spent refresh token returned no error")
	}
	if !providers.IsRefreshGrantDead(err) {
		t.Errorf("IsRefreshGrantDead(%v) = false, want true", err)
	}
}

// A blip is not a dead sign-in: the account must keep serving.
func TestRefreshMiniMaxCode_TransientFailureIsNotGrantDead(t *testing.T) {
	// A 401 is deliberately absent: IsRefreshGrantDead already treats it as a
	// rejected grant for every provider, and exempting this lane would make it
	// the only one where a 401 keeps a dead credential alive.
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"token endpoint down", http.StatusBadGateway, `boom`},
		{"rate limited", http.StatusTooManyRequests, `{"error":"slow_down"}`},
		{"server error with a body", http.StatusInternalServerError, `{"error":"internal"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newMCodeServer(t, "minimax-code", nil, tt.status, tt.body)

			_, err := refreshMiniMaxCode(context.Background(), mcodeParams(server, "minimax-code", "r1"))
			if err == nil {
				t.Fatalf("a %d answer returned no error", tt.status)
			}
			if providers.IsRefreshGrantDead(err) {
				t.Errorf("IsRefreshGrantDead(%v) = true, want false", err)
			}
		})
	}
}

// The single-use token is the reason the flight exists: two concurrent refreshes
// of one token must produce exactly one upstream send.
func TestRefreshMiniMaxCode_DedupesConcurrentRefreshes(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"a2","refresh_token":"r2","expires_in":3600}`))
	}))
	defer srv.Close()
	pinMCodeSite(t, "minimax-code", srv.URL)

	params := &Params{Client: srv.Client(), Provider: "minimax-code", RefreshToken: "shared-token"}
	var wg sync.WaitGroup
	results := make([]*TokenResult, 2)
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = refreshMiniMaxCode(context.Background(), params)
		}(i)
	}
	wg.Wait()

	for i := range 2 {
		if errs[i] != nil {
			t.Fatalf("concurrent refresh %d: %v", i, errs[i])
		}
		if results[i].AccessToken != results[1-i].AccessToken || results[i].RefreshToken != results[1-i].RefreshToken {
			t.Errorf("concurrent refresh %d got %+v, want the same answer as %d", i, results[i], 1-i)
		}
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("token endpoint called %d times, want 1 (a single-use token cannot be spent twice)", n)
	}
}

// A provider with no registered site must be refused before any request goes
// out, or the grant would be posted to the wrong host.
func TestRefreshMiniMaxCode_RejectsUnknownProvider(t *testing.T) {
	if _, err := refreshMiniMaxCode(context.Background(),
		&Params{Provider: "minimax", RefreshToken: "r1"}); err == nil {
		t.Fatal("the API-key minimax provider was accepted by the mcode refresher")
	}
}

// The refresher has to be reachable through the registry the chat path uses.
func TestRefreshMiniMaxCode_Registered(t *testing.T) {
	for _, provider := range []string{"minimax-code", "minimax-code-global"} {
		if Get(provider) == nil {
			t.Errorf("no refresher registered for %s", provider)
		}
	}
}
