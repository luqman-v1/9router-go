package chat

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

// The review of #1527 asked whether a request cut by the gateway's own deadline
// should park the connection for a cooldown. It must not.
//
// The account was never exercised: the gateway stopped waiting. A cooldown
// would take a healthy credential out of rotation for a delay the operator
// configured, which is the same class of mistake UpstreamError.Local already
// prevents for a full local queue.
func TestRequestDeadlineDoesNotLockTheConnection(t *testing.T) {
	release := make(chan struct{})
	hung := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer hung.Close()
	defer close(release)

	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	if _, err := database.Exec(`DELETE FROM providerConnections WHERE id IN ('conn-1', 'conn-2')`); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"apiKey": "sk-test", "baseUrl": hung.URL})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-1', 'deepseek', 'apikey', 'Hung', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(data)); err != nil {
		t.Fatalf("insert providerConnections: %v", err)
	}

	repo := db.NewRepo(database)
	if err := repo.UpdateSettingsRaw(map[string]any{TotalRequestTimeoutSetting: 300}); err != nil {
		t.Fatal(err)
	}

	h := NewChatHandler(repo)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"deepseek/deepseek-chat","messages":[{"role":"user","content":"hello"}]}`))
	h.HandleChatCompletions(w, req)

	if w.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504 (body: %s)", w.Code, w.Body.String())
	}

	var raw string
	if err := database.QueryRow(`SELECT data FROM providerConnections WHERE id = 'conn-1'`).Scan(&raw); err != nil {
		t.Fatalf("read connection: %v", err)
	}
	if db.ConnectionModelLockedData(raw, "deepseek-chat") {
		t.Fatalf("the deadline parked a connection it never dialled to completion:\n%s", raw)
	}
}
