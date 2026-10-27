package chat

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	json "encoding/json/v2"

	"9router/proxy/internal/db"
)

const customJSONProvider = "openai-compatible-custom-json-test"

func seedCustomJSONConn(t *testing.T, database *sql.DB, connID, apiKey, baseURL string, customJSON map[string]any) {
	t.Helper()
	data, _ := json.Marshal(map[string]any{
		"apiKey": apiKey,
		"providerSpecificData": map[string]any{
			"apiType": "custom-json", "baseUrl": baseURL, "prefix": "custom", "customJson": customJSON,
		},
	})
	q := `INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES (?, ?, 'apikey', ?, 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`
	if _, err := database.Exec(q, connID, customJSONProvider, connID, string(data)); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
}

func newCustomJSONHandler(t *testing.T, status int, reply string) (*ChatHandler, *sql.DB, *atomic.Int32, string) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	database, cleanup := setupChatTestDB(t)
	t.Cleanup(cleanup)
	if _, err := database.Exec(`DELETE FROM providerConnections`); err != nil {
		t.Fatalf("clear seeded connections: %v", err)
	}
	return NewChatHandler(db.NewRepo(database)), database, &calls, srv.URL + "/business"
}

// A business endpoint that fails may already have performed its operation, so
// the router must not replay it on the provider's next account.
func TestCustomJSON_FailedAttemptIsNotReplayedOnAnotherAccount(t *testing.T) {
	h, database, calls, endpoint := newCustomJSONHandler(t, http.StatusBadGateway, `{"error":"business failure"}`)
	cfg := map[string]any{"responsePath": "answer"}
	seedCustomJSONConn(t, database, "conn-a", "key-a", endpoint, cfg)
	seedCustomJSONConn(t, database, "conn-b", "key-b", endpoint, cfg)

	body := []byte(`{"model":"custom-model","messages":[{"role":"user","content":"hi"}]}`)
	err := h.handleAccountFallback(context.Background(), httptest.NewRecorder(), customJSONProvider, "custom-model", "", body, false, false, "/v1/chat/completions")
	var ue *upstreamError
	if !errors.As(err, &ue) || ue.StatusCode != http.StatusBadGateway {
		t.Fatalf("err = %v, want the upstream 502", err)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("business endpoint called %d times, want exactly 1", n)
	}
}

func TestCustomJSON_NoAuthConnectionServesWithoutKey(t *testing.T) {
	h, database, calls, endpoint := newCustomJSONHandler(t, http.StatusOK, `{"answer":"pong"}`)
	seedCustomJSONConn(t, database, "conn-a", "", endpoint, map[string]any{"authType": "none", "responsePath": "answer"})

	rec := httptest.NewRecorder()
	body := []byte(`{"model":"custom-model","messages":[{"role":"user","content":"ping"}]}`)
	if err := h.handleAccountFallback(context.Background(), rec, customJSONProvider, "custom-model", "", body, false, false, "/v1/chat/completions"); err != nil {
		t.Fatalf("handleAccountFallback: %v", err)
	}
	if calls.Load() != 1 || !strings.Contains(rec.Body.String(), `"content":"pong"`) {
		t.Errorf("calls=%d body=%s", calls.Load(), rec.Body.String())
	}
}
