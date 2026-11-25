package chat

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"
)

// seedQoderConn inserts an active qoder OAuth connection. When hasExpiry is
// false the row carries a null expiresAt (the pre-fix shape); otherwise the
// given instant is persisted.
func seedQoderConn(t *testing.T, database *sql.DB, id string, expiresAt time.Time, hasExpiry bool) {
	t.Helper()
	data := map[string]any{
		"accessToken":  "tok-" + id,
		"refreshToken": "rt-" + id,
		"providerSpecificData": map[string]any{
			"userId":    "u-" + id,
			"machineId": "m-" + id,
		},
	}
	if hasExpiry {
		data["expiresAt"] = expiresAt.UTC().Format(time.RFC3339)
	}
	blob, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal qoder data: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO providerConnections
		(id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		VALUES (?, 'qoder', 'oauth', ?, 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		id, id, string(blob)); err != nil {
		t.Fatalf("seed qoder connection %s: %v", id, err)
	}
}

// An expired Qoder credential has no refresh grant, so it must be skipped at
// selection time rather than handed to the executor to fail upstream. With only
// an expired row present, selection must report no usable connection.
func TestQoderExpiredConnectionSkippedOnUse(t *testing.T) {
	repo := newTestRepo(t)
	seedQoderConn(t, repo.RawDB(), "qoder-expired", time.Now().Add(-time.Hour), true)

	h := &ChatHandler{Repo: repo}
	conn, _, err := h.GetBestConnection("qoder", "", nil, "qoder-model")
	if err == nil {
		t.Fatalf("expected the expired qoder credential to be skipped, but selection returned connection %q", conn.ID)
	}
}

// With one expired and one live row, selection must land on the live account.
func TestQoderExpiredConnectionSkippedInFavorOfLive(t *testing.T) {
	repo := newTestRepo(t)
	seedQoderConn(t, repo.RawDB(), "qoder-expired", time.Now().Add(-time.Hour), true)
	seedQoderConn(t, repo.RawDB(), "qoder-live", time.Now().Add(time.Hour), true)

	h := &ChatHandler{Repo: repo}
	conn, _, err := h.GetBestConnection("qoder", "", nil, "qoder-model")
	if err != nil {
		t.Fatalf("expected the live qoder credential to be selected, got error: %v", err)
	}
	if conn.ID != "qoder-live" {
		t.Errorf("selected %q, want the live connection qoder-live", conn.ID)
	}
}

// A legacy row with a null expiry must keep working: the skip only applies to a
// concrete expiry that has already passed.
func TestQoderNullExpiryConnectionStillUsable(t *testing.T) {
	repo := newTestRepo(t)
	seedQoderConn(t, repo.RawDB(), "qoder-legacy", time.Time{}, false)

	h := &ChatHandler{Repo: repo}
	conn, _, err := h.GetBestConnection("qoder", "", nil, "qoder-model")
	if err != nil {
		t.Fatalf("a null-expiry qoder credential must remain usable, got error: %v", err)
	}
	if conn.ID != "qoder-legacy" {
		t.Errorf("selected %q, want qoder-legacy", conn.ID)
	}
}
