package chat

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/db"
)

// insertNvidiaConn seeds one nvidia api-key connection aimed at testURL.
func insertNvidiaConn(t *testing.T, database *sql.DB, id, testURL string) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"apiKey": "nvapi-test", "baseUrl": testURL})
	if err != nil {
		t.Fatalf("marshal connection data: %v", err)
	}
	if _, err := database.Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		 VALUES (?, 'nvidia', 'apikey', ?, 1, 1, ?, '2026-11-20T00:00:00Z', '2026-11-20T00:00:00Z')`,
		id, id, string(data)); err != nil {
		t.Fatalf("insert %s: %v", id, err)
	}
}

// TestNVIDIModelScopedDegradedDoesNotLockAPIKey is the wire-level half of
// upstream decolua@2275bc0d: NIM answers 400 "degraded" for one unhealthy
// model cluster, and the connection's key still serves every other model. The
// verdict must lock the model and must NOT set the account-scoped cooldown —
// which is what makes the sibling model selectable on the same account.
func TestNVIDIModelScopedDegradedDoesNotLockAPIKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"DEGRADED function cannot be invoked","type":"invalid_request_error"}}`))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	dropSeededConn(t, database)
	insertNvidiaConn(t, database, "conn-nv-deg", srv.URL)

	h := NewChatHandler(db.NewRepo(database))
	body := []byte(`{"model":"nvidia/nemotron-3-ultra-550b-a55b","messages":[{"role":"user","content":"hi"}]}`)

	if err := h.handleAccountFallback(context.Background(), httptest.NewRecorder(),
		"nvidia", "nvidia/nemotron-3-ultra-550b-a55b", "", body, false, false, "/v1/chat/completions"); err == nil {
		t.Fatal("expected the degraded upstream error to surface")
	}

	if locked, err := h.Repo.IsConnectionModelLocked("conn-nv-deg", "nvidia/nemotron-3-ultra-550b-a55b"); err != nil || !locked {
		t.Errorf("degraded model must be locked, err=%v locked=%v", err, locked)
	}

	var rawData string
	if err := database.QueryRow(`SELECT data FROM providerConnections WHERE id = 'conn-nv-deg'`).Scan(&rawData); err != nil {
		t.Fatalf("read connection: %v", err)
	}
	if _, ok := db.ConnectionCooldownUntil(rawData); ok {
		t.Error("account-scoped cooldown must NOT be set: the API key still serves other models")
	}

	// A sibling model on the same key must still be selectable: the key is
	// healthy, only one cluster is degraded.
	conn, _, err := h.getBestConnection("nvidia", "", nil, "meta/llama-3.3-70b-instruct")
	if err != nil || conn == nil || conn.ID != "conn-nv-deg" {
		t.Errorf("sibling model selection = (%v, %v), want conn-nv-deg", conn, err)
	}
}

// TestNVIDIModelScopedDegradedInComboLocksModelOnly pins the combo-loop half
// of the same contract: comboLockRetryable takes the ModelScoped verdict from
// the classifier and records a scoped error instead of a rate-limit lock.
func TestNVIDIModelScopedDegradedInComboLocksModelOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"cluster is degraded","type":"invalid_request_error"}}`))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	dropSeededConn(t, database)
	insertNvidiaConn(t, database, "conn-nv-combo", srv.URL)

	repo := db.NewRepo(database)
	h := NewChatHandler(repo)
	excludeIDs := []string{}
	ue := &upstreamError{
		StatusCode: http.StatusBadRequest,
		Body:       []byte(`{"error":{"message":"cluster is degraded"}}`),
	}
	h.comboLockRetryable(context.Background(), &excludeIDs, "conn-nv-combo",
		"nvidia", "nvidia/nemotron-3-ultra-550b-a55b", ue)

	if locked, err := repo.IsConnectionModelLocked("conn-nv-combo", "nvidia/nemotron-3-ultra-550b-a55b"); err != nil || !locked {
		t.Errorf("degraded model must be locked, err=%v locked=%v", err, locked)
	}
	var rawData string
	if err := database.QueryRow(`SELECT data FROM providerConnections WHERE id = 'conn-nv-combo'`).Scan(&rawData); err != nil {
		t.Fatalf("read connection: %v", err)
	}
	if _, ok := db.ConnectionCooldownUntil(rawData); ok {
		t.Error("combo must not set the account-scoped cooldown for a model-scoped verdict")
	}
	if len(excludeIDs) != 1 || excludeIDs[0] != "conn-nv-combo" {
		t.Errorf("excludeIDs = %v, want the failed connection appended", excludeIDs)
	}
}
