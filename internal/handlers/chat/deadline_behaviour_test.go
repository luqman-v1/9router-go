package chat

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"9router/proxy/internal/db"
)

// A hung upstream must be cut and reported as 504, not left to hold the client
// until the socket dies, and not reported as a 502 "connection lost".
func TestTotalRequestTimeoutCutsHungUpstream(t *testing.T) {
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

	start := time.Now()
	h.HandleChatCompletions(w, req)
	elapsed := time.Since(start)

	if elapsed > 10*time.Second {
		t.Fatalf("request held for %v, the 300ms bound never fired", elapsed)
	}
	if w.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504 (body: %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "total request timeout") {
		t.Fatalf("body = %s, want the deadline message", w.Body.String())
	}
}

// The bound exists to reclaim a hung upstream, not to cut a slow one: a stream
// that keeps producing bytes must be served in full even when it outlives the
// bound's equivalent in small steps.
func TestTotalRequestTimeoutLeavesPromptAnswerAlone(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"answered in time"}}]}`))
	}))
	defer upstream.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	if _, err := database.Exec(`DELETE FROM providerConnections WHERE id IN ('conn-1', 'conn-2')`); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"apiKey": "sk-test", "baseUrl": upstream.URL})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-1', 'deepseek', 'apikey', 'Fast', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(data)); err != nil {
		t.Fatalf("insert providerConnections: %v", err)
	}

	repo := db.NewRepo(database)
	if err := repo.UpdateSettingsRaw(map[string]any{TotalRequestTimeoutSetting: 30000}); err != nil {
		t.Fatal(err)
	}

	h := NewChatHandler(repo)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"deepseek/deepseek-chat","messages":[{"role":"user","content":"hello"}]}`))
	h.HandleChatCompletions(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "answered in time") {
		t.Fatalf("body = %s, want the upstream answer", w.Body.String())
	}
}

// An unset bound must not change today's behaviour: a request to a hung
// upstream is left alone (the test releases it rather than waiting for a cut).
func TestTotalRequestTimeoutUnsetDoesNotCut(t *testing.T) {
	var answered atomic.Bool
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1200 * time.Millisecond)
		answered.Store(true)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"slow but complete"}}]}`))
	}))
	defer slow.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	if _, err := database.Exec(`DELETE FROM providerConnections WHERE id IN ('conn-1', 'conn-2')`); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"apiKey": "sk-test", "baseUrl": slow.URL})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-1', 'deepseek', 'apikey', 'Slow', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(data)); err != nil {
		t.Fatalf("insert providerConnections: %v", err)
	}

	h := NewChatHandler(db.NewRepo(database))
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"deepseek/deepseek-chat","messages":[{"role":"user","content":"hello"}]}`))
	h.HandleChatCompletions(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	if !answered.Load() {
		t.Fatal("the upstream answer never arrived, yet the request returned")
	}
	if !strings.Contains(w.Body.String(), "slow but complete") {
		t.Fatalf("body = %s, want the upstream answer", w.Body.String())
	}
}
