package chat

import (
	json "encoding/json/v2"
	"net/http"
	"testing"
	"time"

	"9router/proxy/internal/db"
)

// A 429 on a flagged connection must not write the account-scoped park: the
// toggle exists so the endpoint keeps serving instead of parking for the
// announced window. The error is still recorded for the dashboard, and the
// unflagged twin keeps the old behaviour.
func TestComboLockRetryable_IgnoreRateLimitSkipsAccountPark(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	dropSeededConn(t, database)
	insertConnData(t, database, "conn-flagged", 1, flagOnlyData(t, true))
	insertConnData(t, database, "conn-plain", 2, flagOnlyData(t, false))

	handler := NewChatHandler(db.NewRepo(database))
	ue := &upstreamError{StatusCode: http.StatusTooManyRequests, Body: []byte(`{"error":{"message":"rate limited"}}`)}

	var excludeA, excludeB []string
	handler.comboLockRetryable(t.Context(), &excludeA, "conn-flagged", "deepseek", "deepseek-chat", ue)
	handler.comboLockRetryable(t.Context(), &excludeB, "conn-plain", "deepseek", "deepseek-chat", ue)

	for id, wantPark := range map[string]bool{"conn-flagged": false, "conn-plain": true} {
		conn, err := handler.Repo.GetProviderConnectionByID(id)
		if err != nil || conn == nil {
			t.Fatalf("fetch %s: %v", id, err)
		}
		_, parked := db.ConnectionCooldownUntil(conn.Data)
		if parked != wantPark {
			t.Errorf("%s parked = %v, want %v", id, parked, wantPark)
		}
	}

	// Both are excluded for the rest of the pass regardless: the request must
	// not re-hit the same exhausted account within this turn.
	if len(excludeA) != 1 || excludeA[0] != "conn-flagged" {
		t.Errorf("excludeA = %v, want [conn-flagged]", excludeA)
	}
	if len(excludeB) != 1 || excludeB[0] != "conn-plain" {
		t.Errorf("excludeB = %v, want [conn-plain]", excludeB)
	}
}

// A model lock on a flagged connection must not block selection: on a custom
// endpoint the model-scoped 429 is the same upstream window the toggle
// promises to ride out. The unflagged twin stays skipped.
func TestGetBestConnection_IgnoreRateLimitLiftsModelLock(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	dropSeededConn(t, database)
	flagged := map[string]any{"apiKey": "sk-test-deepseek-key", "ignoreRateLimit": true}
	plain := map[string]any{"apiKey": "sk-test-deepseek-key"}
	lockUntil := time.Now().UTC().Add(3 * time.Hour).Format(time.RFC3339)
	for id, data := range map[string]map[string]any{"conn-flagged": flagged, "conn-plain": plain} {
		data["modelLock_deepseek-chat"] = lockUntil
		raw, err := json.Marshal(data)
		if err != nil {
			t.Fatalf("marshal data: %v", err)
		}
		insertConnData(t, database, id, 1, string(raw))
	}

	handler := NewChatHandler(db.NewRepo(database))
	conn, _, err := handler.getBestConnection("deepseek", "", nil, "deepseek-chat")
	if err != nil {
		t.Fatalf("getBestConnection: %v", err)
	}
	if conn.ID != "conn-flagged" {
		t.Errorf("selected %q, want conn-flagged (ignoreRateLimit lifts the model lock)", conn.ID)
	}
}

// The provider health pre-check must see a locked-but-flagged connection as
// available, or a single-connection custom node fails every combo entry
// before a candidate is even chosen.
func TestIsProviderAvailable_IgnoreRateLimitCountsAsAvailable(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	dropSeededConn(t, database)
	flagged := map[string]any{"apiKey": "sk-test-deepseek-key", "ignoreRateLimit": true}
	plain := map[string]any{"apiKey": "sk-test-deepseek-key"}
	lockUntil := time.Now().UTC().Add(3 * time.Hour).Format(time.RFC3339)
	for id, data := range map[string]map[string]any{"conn-flagged": flagged, "conn-plain": plain} {
		data["modelLock_deepseek-chat"] = lockUntil
		raw, err := json.Marshal(data)
		if err != nil {
			t.Fatalf("marshal data: %v", err)
		}
		insertConnData(t, database, id, 1, string(raw))
	}

	repo := db.NewRepo(database)
	if !repo.IsProviderAvailable(t.Context(), "deepseek", "deepseek-chat") {
		t.Error("IsProviderAvailable = false, want true (the flagged connection lifts the pool)")
	}
}

// The selector must accept the flagged connection again on the next request
// even though the unflagged twin is parked for the same window.
func TestComboLockRetryable_FlaggedStaysSelectableNextRequest(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	dropSeededConn(t, database)
	insertConnData(t, database, "conn-flagged", 1, bypassData(t, true))
	insertConnData(t, database, "conn-plain", 2, bypassData(t, false))

	handler := NewChatHandler(db.NewRepo(database))
	var exclude []string
	handler.comboLockRetryable(t.Context(), &exclude, "conn-plain", "deepseek", "deepseek-chat",
		&upstreamError{StatusCode: http.StatusTooManyRequests, Body: []byte(`{"error":{"message":"rate limited"}}`)})

	conn, _, err := handler.getBestConnection("deepseek", "", exclude, "deepseek-chat")
	if err != nil {
		t.Fatalf("getBestConnection: %v", err)
	}
	if conn.ID != "conn-flagged" {
		t.Errorf("selected %q at %s, want conn-flagged to stay in rotation", conn.ID, time.Now().UTC().Format(time.RFC3339))
	}
}
