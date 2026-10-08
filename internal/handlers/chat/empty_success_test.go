package chat

import (
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"9router/proxy/internal/db"
)

// An upstream that answers HTTP 200 with nothing usable is a failure, not a
// completion. Before this, the router read the empty 200 as success, served
// it to the client, cleared the connection's cooldown, and stayed on that
// model for every following turn — the "round-robin keeps returning the same
// model" report. The observable contract is that the next combo member
// answers instead.
func TestHandleChatCompletions_ComboMovesOnFromEmpty200(t *testing.T) {
	emptyHits := new(atomic.Int64)

	// Model A: 200 OK with a blank body.
	emptyUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		emptyHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	defer emptyUpstream.Close()

	// Model B: a normal completion.
	servingUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		if req["model"] != "qwen3-32b" {
			t.Errorf("expected the router to have moved to groq/qwen3-32b, upstream got model %v", req["model"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp-recovered","choices":[{"index":0,"message":{"role":"assistant","content":"answered by the second model"},"finish_reason":"stop"}]}`))
	}))
	defer servingUpstream.Close()

	repo := seedComboWithTwoUpstreams(t, "combo-empty200", emptyUpstream.URL, servingUpstream.URL, "empty-200-test")

	rec := httptest.NewRecorder()
	NewChatHandler(repo).HandleChatCompletions(rec,
		httptest.NewRequest("POST", "/chat/completions",
			strings.NewReader(`{"model":"empty-200-test","messages":[{"role":"user","content":"hello"}],"stream":false}`)))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected the second combo model to answer, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "answered by the second model") {
		t.Errorf("body came from the empty model: %s", rec.Body.String())
	}
	if emptyHits.Load() == 0 {
		t.Error("expected the empty upstream to be tried first")
	}
}

// The same trap on a streaming request: the upstream returned an HTML error
// page with 200 and no event-stream Content-Type. Piping it through the SSE
// scanner produced zero frames, so the client got a clean [DONE] and the
// router never failed over.
func TestHandleChatCompletions_ComboMovesOnFromHTML200WhileStreaming(t *testing.T) {
	emptyUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<html><body>502 Bad Gateway</body></html>"))
	}))
	defer emptyUpstream.Close()

	servingUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"streamed answer\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer servingUpstream.Close()

	repo := seedComboWithTwoUpstreams(t, "combo-html200", emptyUpstream.URL, servingUpstream.URL, "html-200-test")

	rec := httptest.NewRecorder()
	NewChatHandler(repo).HandleChatCompletions(rec,
		httptest.NewRequest("POST", "/chat/completions",
			strings.NewReader(`{"model":"html-200-test","messages":[{"role":"user","content":"hello"}],"stream":true}`)))

	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("expected an event stream, got %q: %s", ct, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "streamed answer") {
		t.Errorf("body came from the failing model: %s", rec.Body.String())
	}
}

// Two 200s that carry no answer must not be served as a completed turn: the
// client gets a retryable gateway error, not two empty completions.
func TestHandleChatCompletions_ComboAllEmpty200Fails(t *testing.T) {
	emptyUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"stop"}]}`))
	}))
	defer emptyUpstream.Close()

	repo := seedComboWithTwoUpstreams(t, "combo-allempty", emptyUpstream.URL, emptyUpstream.URL, "all-empty-200-test")

	rec := httptest.NewRecorder()
	NewChatHandler(repo).HandleChatCompletions(rec,
		httptest.NewRequest("POST", "/chat/completions",
			strings.NewReader(`{"model":"all-empty-200-test","messages":[{"role":"user","content":"hello"}],"stream":false}`)))

	if rec.Code == http.StatusOK {
		t.Fatalf("a 200 with empty content must not be served as a completion: %s", rec.Body.String())
	}
	if rec.Code != http.StatusBadGateway {
		t.Errorf("expected 502 so the client can retry, got %d: %s", rec.Code, rec.Body.String())
	}
}

// seedComboWithTwoUpstreams inserts one deepseek and one groq connection,
// each pointed at its own mock, plus a two-model combo over them.
func seedComboWithTwoUpstreams(t *testing.T, comboID, deepseekURL, groqURL, comboName string) *db.Repo {
	t.Helper()
	database, cleanup := setupChatTestDB(t)
	t.Cleanup(cleanup)

	// Only the mock connections may be picked.
	if _, err := database.Exec(`DELETE FROM providerConnections WHERE id IN ('conn-1', 'conn-2')`); err != nil {
		t.Fatalf("clear seeded connections: %v", err)
	}

	insert := func(id, provider, name, baseURL string) {
		t.Helper()
		data, _ := json.Marshal(map[string]any{"apiKey": "sk-mock", "baseUrl": baseURL})
		if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
			(?, ?, 'apikey', ?, 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
			id, provider, name, string(data)); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	insert("mock-a", "deepseek", "Mock A", deepseekURL)
	insert("mock-b", "groq", "Mock B", groqURL)

	models, _ := json.Marshal([]string{"deepseek/deepseek-chat", "groq/qwen3-32b"})
	if _, err := database.Exec(`INSERT INTO combos (id, name, models, createdAt, updatedAt) VALUES
		(?, ?, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, comboID, comboName, string(models)); err != nil {
		t.Fatalf("insert combo: %v", err)
	}
	return db.NewRepo(database)
}
