package dashboard

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
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
		Quotas map[string]jsontext.Value `json:"quotas"`
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

// TestRunUsageRefresh_SpendsTheStoredRefreshToken is the whole point of the
// re-read upstream added in 0bc7f86e4. The caller's snapshot still holds the
// token a concurrent writer has already rotated; OpenAI revokes the entire
// session when a rotated refresh token is presented again, so spending the
// snapshot logs the account out rather than renewing it.
func TestRunUsageRefresh_SpendsTheStoredRefreshToken(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)

	var spent string
	registerStubRefresher(t, "ollama", func(_ context.Context, p *oauth.Params) (*oauth.TokenResult, error) {
		spent = p.RefreshToken
		return &oauth.TokenResult{AccessToken: "at", RefreshToken: "rotated-again", ExpiresIn: 3600}, nil
	})

	// The row holds the rotated token; the snapshot the caller carries was
	// taken before that write and is now the stale one.
	expired := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	stored := `{"accessToken":"stale","apiKey":"stale","refreshToken":"rt-rotated","expiresAt":"` + expired + `"}`
	if err := repo.CreateProviderConnectionFull("conn-reuse", "ollama", "oauth", "Reuse", nil, stored); err != nil {
		t.Fatalf("create connection: %v", err)
	}
	snapshot := map[string]any{"accessToken": "stale", "apiKey": "stale", "refreshToken": "rt-stale", "expiresAt": expired}
	conn, err := repo.GetProviderConnectionByID("conn-reuse")
	if err != nil || conn == nil {
		t.Fatalf("read connection: %v", err)
	}

	h := &DashboardHandler{Repo: repo}
	outcome := h.runUsageRefresh(t.Context(), conn, usageCredentials{raw: snapshot, provider: "ollama"})

	if outcome.err != nil {
		t.Fatalf("runUsageRefresh: %v", outcome.err)
	}
	if !outcome.attempted {
		t.Fatal("refresh was not attempted")
	}
	if spent != "rt-rotated" {
		t.Errorf("refresh spent %q, want the token stored on the row", spent)
	}
}

// TestRunUsageRefresh_KeepsSnapshotWhenRowUnchanged pins the other direction:
// with nothing newer stored, the snapshot is what gets spent, so the re-read
// cannot start refusing tokens it has no reason to doubt.
func TestRunUsageRefresh_KeepsSnapshotWhenRowUnchanged(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)

	var spent string
	registerStubRefresher(t, "ollama", func(_ context.Context, p *oauth.Params) (*oauth.TokenResult, error) {
		spent = p.RefreshToken
		return &oauth.TokenResult{AccessToken: "at", ExpiresIn: 3600}, nil
	})

	expired := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	stored := `{"accessToken":"stale","apiKey":"stale","refreshToken":"rt-same","expiresAt":"` + expired + `"}`
	if err := repo.CreateProviderConnectionFull("conn-same", "ollama", "oauth", "Same", nil, stored); err != nil {
		t.Fatalf("create connection: %v", err)
	}
	conn, err := repo.GetProviderConnectionByID("conn-same")
	if err != nil || conn == nil {
		t.Fatalf("read connection: %v", err)
	}

	h := &DashboardHandler{Repo: repo}
	snapshot := map[string]any{"accessToken": "stale", "apiKey": "stale", "refreshToken": "rt-same", "expiresAt": expired}
	h.runUsageRefresh(t.Context(), conn, usageCredentials{raw: snapshot, provider: "ollama"})

	if spent != "rt-same" {
		t.Errorf("refresh spent %q, want the snapshot token when the row agrees", spent)
	}
}

// TestRunUsageRefresh_FallsBackToSnapshotWhenRowIsUnreadable: a row that cannot
// be read back (gone, undecodable blob) must not stop the refresh — it falls
// back to the snapshot rather than presenting an empty token.
func TestRunUsageRefresh_FallsBackToSnapshotWhenRowIsUnreadable(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)

	var spent string
	registerStubRefresher(t, "ollama", func(_ context.Context, p *oauth.Params) (*oauth.TokenResult, error) {
		spent = p.RefreshToken
		return &oauth.TokenResult{AccessToken: "at", ExpiresIn: 3600}, nil
	})

	expired := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	if err := repo.CreateProviderConnectionFull("conn-gone", "ollama", "oauth", "Gone", nil, `{"refreshToken":"rt-only-on-row"}`); err != nil {
		t.Fatalf("create connection: %v", err)
	}
	conn := &models.ProviderConnection{ID: "conn-does-not-exist", Provider: "ollama", AuthType: "oauth"}

	h := &DashboardHandler{Repo: repo}
	outcome := h.runUsageRefresh(t.Context(), conn, usageCredentials{
		raw:      map[string]any{"accessToken": "a", "refreshToken": "rt-snapshot", "expiresAt": expired},
		provider: "ollama",
	})

	if outcome.err != nil || !outcome.attempted {
		t.Fatalf("outcome = %+v, want an attempted refresh", outcome)
	}
	if spent != "rt-snapshot" {
		t.Errorf("refresh spent %q, want the snapshot token when the row is unreadable", spent)
	}
}

func TestClaudeAutoPingCatchup_MissedTickCatchupTable(t *testing.T) {
	// Table-driven unit test verifying that due deadline that fell between scheduler ticks
	// is caught up on next tick, and newly observed reset is retained for the next cycle.
	tests := []struct {
		name                 string
		secondsLate          int
		cachedReset          string
		lastPingedKey        string
		newlyObservedResetAt string
		tickTime             time.Time
		wantPing             bool
		wantResetAt          string
		wantResetKey         string
		wantCatchingUp       bool
	}{
		{
			name:                 "catches Claude reset when tick is 10 seconds late",
			secondsLate:          10,
			cachedReset:          "2026-01-01T12:00:00.000Z",
			lastPingedKey:        "",
			newlyObservedResetAt: "2026-01-01T17:00:00.000Z",
			tickTime:             time.Date(2026, 1, 1, 12, 0, 10, 0, time.UTC),
			wantPing:             true,
			wantResetAt:          "2026-01-01T12:00:00.000Z",
			wantResetKey:         "2026-01-01T12:00:00Z",
			wantCatchingUp:       true,
		},
		{
			name:                 "catches Claude reset when tick is 30 seconds late",
			secondsLate:          30,
			cachedReset:          "2026-01-01T12:00:00.000Z",
			lastPingedKey:        "",
			newlyObservedResetAt: "2026-01-01T17:00:00.000Z",
			tickTime:             time.Date(2026, 1, 1, 12, 0, 30, 0, time.UTC),
			wantPing:             true,
			wantResetAt:          "2026-01-01T12:00:00.000Z",
			wantResetKey:         "2026-01-01T12:00:00Z",
			wantCatchingUp:       true,
		},
		{
			name:                 "catches Claude reset when tick is 54 seconds late",
			secondsLate:          54,
			cachedReset:          "2026-01-01T12:00:00.000Z",
			lastPingedKey:        "",
			newlyObservedResetAt: "2026-01-01T17:00:00.000Z",
			tickTime:             time.Date(2026, 1, 1, 12, 0, 54, 0, time.UTC),
			wantPing:             true,
			wantResetAt:          "2026-01-01T12:00:00.000Z",
			wantResetKey:         "2026-01-01T12:00:00Z",
			wantCatchingUp:       true,
		},
		{
			name:                 "does not catch up if already pinged for that deadline",
			secondsLate:          30,
			cachedReset:          "2026-01-01T12:00:00.000Z",
			lastPingedKey:        "2026-01-01T12:00:00Z",
			newlyObservedResetAt: "2026-01-01T17:00:00.000Z",
			tickTime:             time.Date(2026, 1, 1, 12, 0, 30, 0, time.UTC),
			wantPing:             false,
		},
		{
			name:                 "does not ping if deadline not yet due",
			secondsLate:          -10,
			cachedReset:          "2026-01-01T12:00:00.000Z",
			lastPingedKey:        "",
			newlyObservedResetAt: "2026-01-01T12:00:00.000Z",
			tickTime:             time.Date(2026, 1, 1, 11, 59, 50, 0, time.UTC),
			wantPing:             false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := NewClaudeAutoPingCatchupState()
			connID := "claude-test-conn"
			key := "claude:" + connID
			if tt.cachedReset != "" {
				state.ResetCache[key] = tt.cachedReset
			}

			plan := state.PlanClaudeAutoPing(connID, tt.lastPingedKey, tt.newlyObservedResetAt, tt.tickTime)
			if plan.ShouldPing != tt.wantPing {
				t.Fatalf("ShouldPing = %v, want %v", plan.ShouldPing, tt.wantPing)
			}
			if !tt.wantPing {
				return
			}

			if plan.EffectiveResetAt != tt.wantResetAt {
				t.Errorf("EffectiveResetAt = %q, want %q", plan.EffectiveResetAt, tt.wantResetAt)
			}
			if plan.EffectiveResetKey != tt.wantResetKey {
				t.Errorf("EffectiveResetKey = %q, want %q", plan.EffectiveResetKey, tt.wantResetKey)
			}
			if plan.CatchingUpReset != tt.wantCatchingUp {
				t.Errorf("CatchingUpReset = %v, want %v", plan.CatchingUpReset, tt.wantCatchingUp)
			}

			// Simulate successful ping and verify resetCache retains newly observed reset for next cycle
			state.RecordPingSuccess(plan)
			if tt.wantCatchingUp {
				if got := state.ResetCache[key]; got != tt.newlyObservedResetAt {
					t.Errorf("after success, resetCache = %q, want newly observed %q", got, tt.newlyObservedResetAt)
				}
			}
		})
	}
}

func TestClaudeAutoPingCatchup_RetainsNewlyObservedResetForNextCycle(t *testing.T) {
	state := NewClaudeAutoPingCatchupState()
	connID := "claude-cycle-conn"
	key := "claude:" + connID

	// Tick 1: observed 12:00 reset deadline before lead window
	t1 := time.Date(2026, 1, 1, 11, 59, 10, 0, time.UTC)
	plan1 := state.PlanClaudeAutoPing(connID, "", "2026-01-01T12:00:00.000Z", t1)
	if plan1.ShouldPing {
		t.Fatal("tick 1 should not ping before 5s lead window")
	}

	// Tick 2 (60s later): 12:00:10. Next usage poll reports 17:00:00.
	t2 := time.Date(2026, 1, 1, 12, 0, 10, 0, time.UTC)
	plan2 := state.PlanClaudeAutoPing(connID, "", "2026-01-01T17:00:00.000Z", t2)
	if !plan2.ShouldPing {
		t.Fatal("tick 2 must catch up missed 12:00 deadline")
	}
	if plan2.EffectiveResetAt != "2026-01-01T12:00:00.000Z" {
		t.Errorf("EffectiveResetAt = %q, want 12:00", plan2.EffectiveResetAt)
	}
	if !plan2.CatchingUpReset {
		t.Error("CatchingUpReset must be true")
	}

	// Ping succeeds. Marked 12:00 as pinged.
	lastPingedKey := plan2.EffectiveResetKey
	state.RecordPingSuccess(plan2)

	// Verify resetCache retained 17:00 for the next cycle
	if state.ResetCache[key] != "2026-01-01T17:00:00.000Z" {
		t.Fatalf("resetCache = %q, want 17:00 retained for next cycle", state.ResetCache[key])
	}

	// Tick 3: 12:01:10 (inside same cycle). Should NOT ping again.
	t3 := time.Date(2026, 1, 1, 12, 1, 10, 0, time.UTC)
	plan3 := state.PlanClaudeAutoPing(connID, lastPingedKey, "2026-01-01T17:00:00.000Z", t3)
	if plan3.ShouldPing {
		t.Fatal("tick 3 must not ping duplicate in same cycle")
	}

	// Tick 4: 17:00:30 (next cycle arrived, tick is 30s late). Must catch up 17:00!
	t4 := time.Date(2026, 1, 1, 17, 0, 30, 0, time.UTC)
	plan4 := state.PlanClaudeAutoPing(connID, lastPingedKey, "2026-01-01T22:00:00.000Z", t4)
	if !plan4.ShouldPing {
		t.Fatal("tick 4 must ping next cycle (17:00)")
	}
	if plan4.EffectiveResetAt != "2026-01-01T17:00:00.000Z" {
		t.Errorf("tick 4 EffectiveResetAt = %q, want 17:00", plan4.EffectiveResetAt)
	}
}

func TestClaudeAutoPingCatchup_FailureRetainsDeadlineAndRespectsCooldown(t *testing.T) {
	state := NewClaudeAutoPingCatchupState()
	connID := "claude-fail-conn"
	key := "claude:" + connID

	state.ResetCache[key] = "2026-01-01T12:00:00.000Z"
	t1 := time.Date(2026, 1, 1, 12, 0, 30, 0, time.UTC)
	plan1 := state.PlanClaudeAutoPing(connID, "", "2026-01-01T17:00:00.000Z", t1)
	if !plan1.ShouldPing || !plan1.CatchingUpReset {
		t.Fatal("plan1 should ping catching up")
	}

	// Ping fails (HTTP error or transport error)
	state.RecordPingFailure(plan1, t1)

	// Deadline must remain in resetCache
	if state.ResetCache[key] != "2026-01-01T12:00:00.000Z" {
		t.Errorf("after failure, resetCache = %q, want 12:00 retained", state.ResetCache[key])
	}

	// Tick 1 min later (during cooldown) -> must NOT ping
	t2 := time.Date(2026, 1, 1, 12, 1, 30, 0, time.UTC)
	plan2 := state.PlanClaudeAutoPing(connID, "", "2026-01-01T17:00:00.000Z", t2)
	if plan2.ShouldPing {
		t.Fatal("must skip during failure cooldown")
	}

	// Tick 16 min later (after 15 min cooldown) -> retries missed deadline
	t3 := time.Date(2026, 1, 1, 12, 16, 0, 0, time.UTC)
	plan3 := state.PlanClaudeAutoPing(connID, "", "2026-01-01T17:00:00.000Z", t3)
	if !plan3.ShouldPing {
		t.Fatal("after cooldown expired, must retry missed deadline")
	}
	if plan3.EffectiveResetAt != "2026-01-01T12:00:00.000Z" {
		t.Errorf("EffectiveResetAt = %q, want 12:00", plan3.EffectiveResetAt)
	}

	// Successful retry clears cooldown and updates resetCache to newly observed reset
	state.RecordPingSuccess(plan3)
	if state.ResetCache[key] != "2026-01-01T17:00:00.000Z" {
		t.Errorf("after successful retry, resetCache = %q, want 17:00", state.ResetCache[key])
	}
	if state.ShouldSkipAfterFailure(key, t3) {
		t.Error("failureCache must be cleared after success")
	}
}
