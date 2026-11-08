package db

import (
	json "encoding/json/v2"
	"errors"
	"testing"
	"time"

	"9router/proxy/internal/providers"
)

// The cooldown has to start at the OmniRoute floor and grow while nobody
// repairs the account, but never so far that a re-login is the only way back.
func TestOAuthLockDelay(t *testing.T) {
	tests := []struct {
		name     string
		failures int
		want     time.Duration
	}{
		{"first rejection", 1, 5 * time.Minute},
		{"second rejection", 2, 10 * time.Minute},
		{"third rejection", 3, 20 * time.Minute},
		{"fourth rejection", 4, 40 * time.Minute},
		{"eighth rejection", 8, 640 * time.Minute},
		{"ninth rejection", 9, 1280 * time.Minute},
		{"tenth rejection is capped", 10, 24 * time.Hour},
		{"negative counter is treated as the first", -3, 5 * time.Minute},
		{"zero counter is treated as the first", 0, 5 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := OAuthLockDelay(tt.failures); got != tt.want {
				t.Errorf("OAuthLockDelay(%d) = %s, want %s", tt.failures, got, tt.want)
			}
		})
	}
}

// A malformed or absent park must never take routing down: the selector
// treats "cannot tell" as "not parked".
func TestConnectionOAuthLockUntil_FailsOpen(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{"empty blob", "", false},
		{"key missing", `{"apiKey":"sk-x"}`, false},
		{"key null", `{"oauthLockedUntil":null}`, false},
		{"key empty string", `{"oauthLockedUntil":""}`, false},
		{"key whitespace", `{"oauthLockedUntil":"   "}`, false},
		{"unparseable timestamp", `{"oauthLockedUntil":"soon"}`, false},
		{"wrong type", `{"oauthLockedUntil":12345}`, false},
		{"blob is not json", `not json at all`, false},
		{"valid timestamp", `{"oauthLockedUntil":"2030-01-01T00:00:00Z"}`, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := ConnectionOAuthLockUntil(tc.raw); ok != tc.want {
				t.Errorf("ConnectionOAuthLockUntil(%s) ok = %v, want %v", tc.raw, ok, tc.want)
			}
		})
	}
}
func TestConnectionRequiresReauth(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{"reauth_required", `{"testStatus":"reauth_required"}`, true},
		{"healthy", `{"testStatus":"healthy"}`, false},
		{"missing", `{"apiKey":"sk-x"}`, false},
		{"empty", "", false},
		{"malformed", `not json`, false},
		{"wrong type", `{"testStatus":12345}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ConnectionRequiresReauth(tt.raw); got != tt.want {
				t.Errorf("ConnectionRequiresReauth(%s) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

// The selector asks one question, so the two account-scoped parks have to be
// answered together — earliest wins, and a half-readable blob still reads.
func TestConnectionBlockedUntil_TakesTheEarliestPark(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "no park",
			raw:  `{"apiKey":"sk-x"}`,
		},
		{
			name: "oauth park only",
			raw:  `{"oauthLockedUntil":"2030-01-01T00:00:00Z"}`,
			want: "2030-01-01T00:00:00Z",
		},
		{
			name: "quota cooldown only",
			raw:  `{"rateLimitedUntil":"2030-01-01T00:00:00Z"}`,
			want: "2030-01-01T00:00:00Z",
		},
		{
			name: "oauth park comes back first",
			raw:  `{"rateLimitedUntil":"2030-01-02T00:00:00Z","oauthLockedUntil":"2030-01-01T00:00:00Z"}`,
			want: "2030-01-01T00:00:00Z",
		},
		{
			name: "quota cooldown comes back first",
			raw:  `{"rateLimitedUntil":"2030-01-01T00:00:00Z","oauthLockedUntil":"2030-01-02T00:00:00Z"}`,
			want: "2030-01-01T00:00:00Z",
		},
		{
			name: "unreadable park does not hide a readable one",
			raw:  `{"rateLimitedUntil":"2030-01-01T00:00:00Z","oauthLockedUntil":"soon"}`,
			want: "2030-01-01T00:00:00Z",
		},
		{
			name: "both parks unreadable",
			raw:  `{"rateLimitedUntil":"soon","oauthLockedUntil":42}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			until, ok := ConnectionBlockedUntil(tt.raw)
			if ok != (tt.want != "") {
				t.Fatalf("ConnectionBlockedUntil(%s) ok = %v, want %v", tt.raw, ok, tt.want != "")
			}
			if !ok {
				return
			}
			if got := until.UTC().Format(time.RFC3339); got != tt.want {
				t.Errorf("earliest park = %s, want %s", got, tt.want)
			}
		})
	}
}

// A written park must be readable by the selector, grow while the account stays
// broken, and disappear once it is released.
func TestRecordConnectionOAuthFailure_PersistsGrowsAndClears(t *testing.T) {
	repo, cleanup := setupHealthConnTestDB(t)
	defer cleanup()
	insertTestConn(t, repo)

	until, failures, err := repo.RecordConnectionOAuthFailure("conn-1", 401, errors.New("token refresh rejected with status 401"))
	if err != nil {
		t.Fatalf("RecordConnectionOAuthFailure: %v", err)
	}
	if failures != 1 {
		t.Fatalf("failures = %d, want 1", failures)
	}
	if !until.After(time.Now()) {
		t.Fatalf("park until %s is not in the future", until)
	}
	if got := repo.GetConnectionOAuthFailures("conn-1"); got != 1 {
		t.Errorf("failure count = %d, want 1", got)
	}
	locked, ok := ConnectionOAuthLockUntil(readConnData(t, repo, "conn-1"))
	if !ok {
		t.Fatal("expected the connection to carry an OAuth park")
	}
	if !locked.After(time.Now()) {
		t.Errorf("park until %s is not in the future", locked)
	}

	// The same strike seen twice inside one window is one failure: a request
	// can discover a dead grant more than once.
	if _, failures, err = repo.RecordConnectionOAuthFailure("conn-1", 401, errors.New("still invalid_grant")); err != nil {
		t.Fatalf("RecordConnectionOAuthFailure (repeat): %v", err)
	}
	if failures != 1 {
		t.Errorf("failures after a repeat of the same window = %d, want 1", failures)
	}

	// Once the window has run out, the next rejection is a new failure and the
	// cooldown grows.
	if _, err := repo.RawDB().Exec(`UPDATE providerConnections SET data = json_set(data, '$.oauthLockedUntil', ?) WHERE id = ?`,
		time.Now().Add(-time.Minute).UTC().Format(time.RFC3339), "conn-1"); err != nil {
		t.Fatalf("expire the park: %v", err)
	}
	second, failures, err := repo.RecordConnectionOAuthFailure("conn-1", 401, errors.New("still invalid_grant"))
	if err != nil {
		t.Fatalf("RecordConnectionOAuthFailure (second): %v", err)
	}
	if failures != 2 {
		t.Fatalf("failures after the second window = %d, want 2", failures)
	}
	if !second.After(locked) {
		t.Errorf("second park until %s does not outlast the first (%s)", second, locked)
	}
	if got, want := second.Sub(time.Now().UTC()), OAuthLockDelay(2); got < want-30*time.Second || got > want+30*time.Second {
		t.Errorf("second park window = %s, want about %s", got, want)
	}

	if err := repo.ClearConnectionOAuthLock("conn-1"); err != nil {
		t.Fatalf("ClearConnectionOAuthLock: %v", err)
	}
	if _, ok := ConnectionOAuthLockUntil(readConnData(t, repo, "conn-1")); ok {
		t.Error("expected the park to be cleared so the account is selectable again")
	}
	if got := repo.GetConnectionOAuthFailures("conn-1"); got != 0 {
		t.Errorf("failure count after clearing = %d, want 0", got)
	}
}

// The reason the account is parked has to reach the dashboard's error panel,
// which reads lastError.
func TestRecordConnectionOAuthFailure_RecordsWhy(t *testing.T) {
	repo, cleanup := setupHealthConnTestDB(t)
	defer cleanup()
	insertTestConn(t, repo)

	if _, _, err := repo.RecordConnectionOAuthFailure("conn-1", 401, errors.New("invalid_grant")); err != nil {
		t.Fatalf("RecordConnectionOAuthFailure: %v", err)
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(readConnData(t, repo, "conn-1")), &data); err != nil {
		t.Fatalf("unmarshal connection data: %v", err)
	}
	lastError, ok := data["lastError"].(map[string]any)
	if !ok {
		t.Fatalf("lastError = %T, want an object", data["lastError"])
	}
	if message, _ := lastError["message"].(string); message != "invalid_grant" {
		t.Errorf("lastError.message = %q, want %q", message, "invalid_grant")
	}
	if status, _ := lastError["status"].(float64); status != 401 {
		t.Errorf("lastError.status = %v, want 401", lastError["status"])
	}
}

// A dead grant (invalid_grant / refresh_token_reused / revoked) is terminal,
// so the row is stamped with testStatus = reauth_required and quarantined from
// routing. Anything else (a 403, a 5xx) parks the account but leaves
// testStatus untouched: only a re-login fixes a dead grant.
func TestRecordConnectionOAuthFailure_SetsReauthRequiredForDeadGrant(t *testing.T) {
	deadCases := []struct {
		name   string
		err    error
		status int
	}{
		{"401 invalid_grant", &providers.OAuthRefreshError{Status: 401, Body: "invalid_grant"}, 401},
		{"400 invalid_grant", &providers.OAuthRefreshError{Status: 400, Body: "invalid_grant"}, 400},
		{"400 refresh_token_reused", &providers.OAuthRefreshError{Status: 400, Body: "refresh_token_reused"}, 400},
	}
	for _, tc := range deadCases {
		t.Run(tc.name, func(t *testing.T) {
			repo, cleanup := setupHealthConnTestDB(t)
			defer cleanup()
			insertTestConn(t, repo)
			if _, _, err := repo.RecordConnectionOAuthFailure("conn-1", tc.status, tc.err); err != nil {
				t.Fatalf("RecordConnectionOAuthFailure: %v", err)
			}
			if got := testStatusOf(t, repo, "conn-1"); got != providers.StatusReauthRequired {
				t.Errorf("dead grant testStatus = %q, want %q", got, providers.StatusReauthRequired)
			}
		})
	}

	// Non-dead failures park the account but leave testStatus empty.
	t.Run("403 forbidden leaves testStatus empty", func(t *testing.T) {
		repo, cleanup := setupHealthConnTestDB(t)
		defer cleanup()
		insertTestConn(t, repo)
		if _, _, err := repo.RecordConnectionOAuthFailure("conn-1", 403, errors.New("forbidden")); err != nil {
			t.Fatalf("RecordConnectionOAuthFailure: %v", err)
		}
		if got := testStatusOf(t, repo, "conn-1"); got != "" {
			t.Errorf("non-dead failure testStatus = %q, want empty", got)
		}
	})

	// A successful refresh sheds the quarantine marker.
	t.Run("clear removes reauth_required", func(t *testing.T) {
		repo, cleanup := setupHealthConnTestDB(t)
		defer cleanup()
		insertTestConn(t, repo)
		if _, _, err := repo.RecordConnectionOAuthFailure("conn-1", 401, &providers.OAuthRefreshError{Status: 401, Body: "invalid_grant"}); err != nil {
			t.Fatalf("RecordConnectionOAuthFailure: %v", err)
		}
		if err := repo.ClearConnectionOAuthLock("conn-1"); err != nil {
			t.Fatalf("ClearConnectionOAuthLock: %v", err)
		}
		if got := testStatusOf(t, repo, "conn-1"); got != "" {
			t.Errorf("after clear testStatus = %q, want empty", got)
		}
	})
}

func testStatusOf(t *testing.T, repo *Repo, connID string) string {
	t.Helper()
	var raw string
	if err := repo.RawDB().QueryRow("SELECT data FROM providerConnections WHERE id = ?", connID).Scan(&raw); err != nil {
		t.Fatalf("read connection data: %v", err)
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		t.Fatalf("unmarshal connection data: %v", err)
	}
	if v, ok := data["testStatus"].(string); ok {
		return v
	}
	return ""
}

// A counter that cannot be read must cost the account its longer backoff, not
// its routing.
func TestGetConnectionOAuthFailures_FailsOpen(t *testing.T) {
	repo, cleanup := setupHealthConnTestDB(t)
	defer cleanup()
	insertTestConn(t, repo)
	if _, err := repo.RawDB().Exec(`UPDATE providerConnections SET data = ? WHERE id = ?`, `{"oauthFailureCount":"many"}`, "conn-1"); err != nil {
		t.Fatalf("seed corrupt counter: %v", err)
	}
	if got := repo.GetConnectionOAuthFailures("conn-1"); got != 0 {
		t.Errorf("failure count = %d, want 0 for a corrupt counter", got)
	}
	if got := repo.GetConnectionOAuthFailures("missing-conn"); got != 0 {
		t.Errorf("failure count for an unknown connection = %d, want 0", got)
	}
}
