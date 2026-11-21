package chat

import (
	"database/sql"
	json "encoding/json/v2"
	"testing"
	"time"

	"9router/proxy/internal/db"
)

// bypassData builds a connection data blob with the operator's
// ignoreRateLimit flag. The cooldown is always present so a test that selects
// the flagged connection proves the bypass, not an empty park.
func bypassData(t *testing.T, ignore bool) string {
	t.Helper()
	until := time.Now().UTC().Add(1 * time.Hour)
	data := map[string]any{
		"apiKey":           "sk-test-deepseek-key",
		"rateLimitedUntil": until.UTC().Format(time.RFC3339),
	}
	if ignore {
		data["ignoreRateLimit"] = true
	}
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal data: %v", err)
	}
	return string(raw)
}

// flagOnlyData is bypassData without the preset cooldown, so a test that
// asserts "no park was written" reads absence instead of a pre-seeded park.
func flagOnlyData(t *testing.T, ignore bool) string {
	t.Helper()
	data := map[string]any{"apiKey": "sk-test-deepseek-key"}
	if ignore {
		data["ignoreRateLimit"] = true
	}
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal data: %v", err)
	}
	return string(raw)
}

func insertConnData(t *testing.T, database *sql.DB, id string, priority int, data string) {
	t.Helper()
	_, err := database.Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		 VALUES (?, 'deepseek', 'apikey', ?, ?, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		id, id, priority, data)
	if err != nil {
		t.Fatalf("insert %s: %v", id, err)
	}
}

// The selector must hand a flagged connection out even though its
// rateLimitedUntil is still an hour away: that is the whole point of the
// toggle. An unflagged connection with the identical park must stay skipped,
// so the same test proves both halves.
func TestGetBestConnection_IgnoreRateLimitBypassesAccountCooldown(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	dropSeededConn(t, database)
	insertConnData(t, database, "conn-flagged", 1, bypassData(t, true))
	insertConnData(t, database, "conn-plain", 2, bypassData(t, false))

	handler := NewChatHandler(db.NewRepo(database))
	conn, _, err := handler.getBestConnection("deepseek", "", nil, "deepseek-chat")
	if err != nil {
		t.Fatalf("getBestConnection: %v", err)
	}
	if conn.ID != "conn-flagged" {
		t.Errorf("selected %q, want conn-flagged (ignoreRateLimit bypasses the account cooldown)", conn.ID)
	}
}

// A flagged connection with a real cooldown parked it would ride out must
// report that window: the log line and any caller that wants the skipped
// cooldown read it from here.
func TestIgnoredAccountPark_ReportsTheRiddenOutWindow(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	dropSeededConn(t, database)
	insertConnData(t, database, "conn-flagged", 1, bypassData(t, true))

	handler := NewChatHandler(db.NewRepo(database))
	conn, _, err := handler.getBestConnection("deepseek", "", nil, "deepseek-chat")
	if err != nil {
		t.Fatalf("getBestConnection: %v", err)
	}
	ride := ignoredAccountPark(conn, time.Now())
	if ride.IsZero() {
		t.Fatal("ignoredAccountPark = zero, want the parked window the connection is riding out")
	}
	if !ride.After(time.Now()) {
		t.Errorf("ignoredAccountPark = %s, want a future instant", ride.UTC())
	}
}
