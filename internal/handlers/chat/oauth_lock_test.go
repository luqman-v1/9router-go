package chat

import (
	"context"
	"database/sql"
	json "encoding/json/v2"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/models"
	"9router/proxy/internal/proxy/oauth"
	"9router/proxy/internal/providers"
)

// oauthLockTestProvider is a provider id no built-in refresher claims, so the
// tests decide what the token endpoint answers.
const oauthLockTestProvider = "oauth-lock-test"

type oauthRefresherStub struct {
	status int // 0 = succeed
	calls  atomic.Int32
}

func (s *oauthRefresherStub) refresher() oauth.Refresher {
	return func(context.Context, *oauth.Params) (*oauth.TokenResult, error) {
		s.calls.Add(1)
		if s.status == 0 {
			return &oauth.TokenResult{AccessToken: "fresh-token", RefreshToken: "fresh-refresh", ExpiresIn: 3600}, nil
		}
		return nil, &providers.OAuthRefreshError{Status: s.status, Body: "invalid_grant"}
	}
}

func installRefresher(t *testing.T, stub *oauthRefresherStub) {
	t.Helper()
	previous := oauth.Get(oauthLockTestProvider)
	oauth.Register(oauthLockTestProvider, stub.refresher())
	t.Cleanup(func() { oauth.Register(oauthLockTestProvider, previous) })
}

func insertOAuthConn(t *testing.T, database *sql.DB, id string, priority int, data string) {
	t.Helper()
	if _, err := database.Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		 VALUES (?, ?, 'oauth', ?, ?, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		id, oauthLockTestProvider, id, priority, data); err != nil {
		t.Fatalf("insert %s: %v", id, err)
	}
}

func expiredOAuthData(t *testing.T) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"accessToken":  "stale-token",
		"refreshToken": "revoked-grant",
		"expiresAt":    time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
		"projectId":    "project-1",
	})
	if err != nil {
		t.Fatalf("marshal connection data: %v", err)
	}
	return string(raw)
}

// parkAccount drives a connection through a rejected refresh and reports
// whether the account was left parked.
func parkAccount(t *testing.T, database *sql.DB, status int) *oauthRefresherStub {
	t.Helper()
	stub := &oauthRefresherStub{status: status}
	installRefresher(t, stub)
	insertOAuthConn(t, database, "conn-broken", 1, expiredOAuthData(t))
	insertOAuthConn(t, database, "conn-healthy", 2, expiredOAuthData(t))

	handler := NewChatHandler(db.NewRepo(database))
	if _, _, err := handler.forceRefreshOAuthToken("conn-broken"); err == nil {
		t.Fatal("expected the rejected refresh to report an error")
	}
	return stub
}

// A grant the provider rejected cannot be fixed by asking again, and the old
// behaviour asked on every request until the token endpoint rate limited the
// whole IP. Both refresh entry points have to leave the account parked; a
// failure with any other status proves nothing about the grant and must not
// take the account out of rotation.
func TestRejectedOAuthRefreshParksAccount(t *testing.T) {
	entryPoints := []struct {
		name    string
		refresh func(h *ChatHandler, connectionID string) error
	}{
		{
			name: "forced refresh",
			refresh: func(h *ChatHandler, connectionID string) error {
				_, _, err := h.forceRefreshOAuthToken(connectionID)
				return err
			},
		},
		{
			name: "expiry refresh",
			refresh: func(h *ChatHandler, connectionID string) error {
				_, _, err := h.refreshOAuthTokenIfExpired(connectionID, "stale-token")
				return err
			},
		},
	}

	tests := []struct {
		name       string
		status     int
		wantParked bool
	}{
		{"revoked grant is parked", http.StatusUnauthorized, true},
		{"invalid request is not parked", http.StatusBadRequest, false},
		{"token endpoint down is not parked", http.StatusInternalServerError, false},
	}

	for _, entry := range entryPoints {
		for _, tt := range tests {
			t.Run(entry.name+"/"+tt.name, func(t *testing.T) {
				database, cleanup := setupChatTestDB(t)
				defer cleanup()
				dropSeededConn(t, database)
				installRefresher(t, &oauthRefresherStub{status: tt.status})
				insertOAuthConn(t, database, "conn-broken", 1, expiredOAuthData(t))

				handler := NewChatHandler(db.NewRepo(database))
				if err := entry.refresh(handler, "conn-broken"); err == nil {
					t.Fatal("expected the rejected refresh to report an error")
				}

				_, parked := db.ConnectionOAuthLockUntil(string(mustConnData(t, database, "conn-broken")))
				if parked != tt.wantParked {
					t.Errorf("account parked = %v, want %v", parked, tt.wantParked)
				}
			})
		}
	}
}

func mustConnData(t *testing.T, database *sql.DB, id string) []byte {
	t.Helper()
	var raw string
	if err := database.QueryRow("SELECT data FROM providerConnections WHERE id = ?", id).Scan(&raw); err != nil {
		t.Fatalf("read connection data: %v", err)
	}
	return []byte(raw)
}

// The park only pays off if routing honours it: the account must not be handed
// out again, which is what keeps the refresh from being retried.
func TestParkedAccountIsSkippedByRouting(t *testing.T) {
	tests := []struct {
		name string
		pick func(h *ChatHandler) (*models.ProviderConnection, error)
	}{
		{
			name: "rotation",
			pick: func(h *ChatHandler) (*models.ProviderConnection, error) {
				conn, _, err := h.getBestConnection(oauthLockTestProvider, "", nil, "model-1")
				return conn, err
			},
		},
		{
			name: "client pin",
			pick: func(h *ChatHandler) (*models.ProviderConnection, error) {
				conn, _, err := h.getBestConnection(oauthLockTestProvider, "conn-broken", nil, "model-1")
				return conn, err
			},
		},
		{
			name: "request without a model",
			pick: func(h *ChatHandler) (*models.ProviderConnection, error) {
				conn, _, err := h.getBestConnection(oauthLockTestProvider, "", nil, "")
				return conn, err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database, cleanup := setupChatTestDB(t)
			defer cleanup()
			dropSeededConn(t, database)
			stub := parkAccount(t, database, http.StatusUnauthorized)

			handler := NewChatHandler(db.NewRepo(database))
			// Three selections stand in for three client requests: each one
			// that picked the parked account would have cost a token-endpoint
			// call before this fix.
			for range 3 {
				conn, err := tt.pick(handler)
				if err != nil {
					t.Fatalf("select connection: %v", err)
				}
				if conn.ID == "conn-broken" {
					t.Fatal("routing handed out an account whose OAuth grant was rejected")
				}
			}
			if got := stub.calls.Load(); got != 1 {
				t.Errorf("token endpoint was called %d times, want 1 (the rejection that parked it)", got)
			}
		})
	}
}

// The park is a cooldown, not a disable: once it runs out the account is a
// normal candidate again.
func TestParkedAccountReturnsToRotationAfterCooldown(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	dropSeededConn(t, database)
	parkAccount(t, database, http.StatusUnauthorized)

	if _, err := database.Exec(
		`UPDATE providerConnections SET data = json_set(data, '$.oauthLockedUntil', ?) WHERE id = 'conn-broken'`,
		time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("expire the park: %v", err)
	}

	handler := NewChatHandler(db.NewRepo(database))
	conn, _, err := handler.getBestConnection(oauthLockTestProvider, "", nil, "model-1")
	if err != nil {
		t.Fatalf("select connection: %v", err)
	}
	if conn.ID != "conn-broken" {
		t.Errorf("selected %q, want the account whose cooldown has expired", conn.ID)
	}
}

// With no account left to serve, the caller is told when one comes back rather
// than handed a bare "all excluded".
func TestAllAccountsParkedReportsEarliestReset(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	dropSeededConn(t, database)
	parkAccount(t, database, http.StatusUnauthorized)
	if _, err := database.Exec(`DELETE FROM providerConnections WHERE id = 'conn-healthy'`); err != nil {
		t.Fatalf("drop the healthy connection: %v", err)
	}

	handler := NewChatHandler(db.NewRepo(database))
	_, _, err := handler.getBestConnection(oauthLockTestProvider, "", nil, "model-1")
	if err == nil {
		t.Fatal("expected an error when every account is parked")
	}
	until, ok := db.ConnectionOAuthLockUntil(string(mustConnData(t, database, "conn-broken")))
	if !ok {
		t.Fatal("expected the connection to carry a park")
	}
	if !containsAll(err.Error(), "cooldown", until.UTC().Format(time.RFC3339)) {
		t.Errorf("error should name the earliest reset, got: %v", err)
	}
}

// State nobody can read must not decide routing: the account stays selectable
// and the refresh is retried once the cooldown is over.
func TestUnreadableParkFailsOpen(t *testing.T) {
	tests := []struct {
		name  string
		patch string
	}{
		{"field missing", `json_remove(data, '$.oauthLockedUntil')`},
		{"field is null", `json_set(data, '$.oauthLockedUntil', NULL)`},
		{"field is not a timestamp", `json_set(data, '$.oauthLockedUntil', 'soon')`},
		{"field is the wrong type", `json_set(data, '$.oauthLockedUntil', 12345)`},
		// The counter only sizes the next cooldown, so it is the timestamp
		// that has to become unreadable for the account to come back.
		{"no readable timestamp and no readable counter", `json_set(data, '$.oauthLockedUntil', 'soon', '$.oauthFailureCount', 'many')`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database, cleanup := setupChatTestDB(t)
			defer cleanup()
			dropSeededConn(t, database)
			parkAccount(t, database, http.StatusUnauthorized)
			if _, err := database.Exec(
				`UPDATE providerConnections SET data = ` + tt.patch + ` WHERE id = 'conn-broken'`); err != nil {
				t.Fatalf("rewrite connection data: %v", err)
			}

			handler := NewChatHandler(db.NewRepo(database))
			conn, _, err := handler.getBestConnection(oauthLockTestProvider, "", nil, "model-1")
			if err != nil {
				t.Fatalf("select connection: %v", err)
			}
			if conn.ID != "conn-broken" {
				t.Errorf("selected %q, want the account whose park could not be read", conn.ID)
			}
		})
	}
}

// The cooldown grows with CONSECUTIVE failures only: an account that refreshes
// again is released and starts over at the base delay.
func TestSuccessfulRefreshReleasesThePark(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	dropSeededConn(t, database)
	parkAccount(t, database, http.StatusUnauthorized)
	installRefresher(t, &oauthRefresherStub{})

	handler := NewChatHandler(db.NewRepo(database))
	token, _, err := handler.forceRefreshOAuthToken("conn-broken")
	if err != nil {
		t.Fatalf("refresh after the account was repaired: %v", err)
	}
	if token != "fresh-token" {
		t.Fatalf("token = %q, want the refreshed one", token)
	}
	if _, parked := db.ConnectionOAuthLockUntil(string(mustConnData(t, database, "conn-broken"))); parked {
		t.Error("a successful refresh must release the park")
	}
	if got := db.NewRepo(database).GetConnectionOAuthFailures("conn-broken"); got != 0 {
		t.Errorf("failure count after a successful refresh = %d, want 0", got)
	}
}

func TestOAuthRefresh_SingleflightCollapsesConcurrentCalls(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	dropSeededConn(t, database)

	var calls atomic.Int32
	previous := oauth.Get(oauthLockTestProvider)
	oauth.Register(oauthLockTestProvider, func(_ context.Context, _ *oauth.Params) (*oauth.TokenResult, error) {
		calls.Add(1)
		time.Sleep(30 * time.Millisecond) // simulate remote refresh latency
		return &oauth.TokenResult{AccessToken: "fresh-token", RefreshToken: "fresh-refresh", ExpiresIn: 3600}, nil
	})
	t.Cleanup(func() { oauth.Register(oauthLockTestProvider, previous) })

	insertOAuthConn(t, database, "conn-concurrent", 1, expiredOAuthData(t))

	handler := NewChatHandler(db.NewRepo(database))

	const concurrentCount = 10
	var wg sync.WaitGroup
	results := make([]string, concurrentCount)
	errs := make([]error, concurrentCount)

	wg.Add(concurrentCount)
	for i := range concurrentCount {
		go func(idx int) {
			defer wg.Done()
			token, _, err := handler.RefreshOAuthTokenIfExpired("conn-concurrent", "stale-token")
			results[idx] = token
			errs[idx] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d failed: %v", i, err)
		}
		if results[i] != "fresh-token" {
			t.Errorf("goroutine %d got token %q, want fresh-token", i, results[i])
		}
	}

	if got := calls.Load(); got != 1 {
		t.Fatalf("refresher called %d times, want exactly 1 (singleflight collapse)", got)
	}
}
