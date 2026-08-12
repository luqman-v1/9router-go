package chat

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/tokensaver"
)

// seedConnDB inserts a single active connection for the given provider pointing at upstream.
func seedConnDB(t *testing.T, database *sql.DB, provider, connID, apiKey, baseURL string) {
	t.Helper()
	data, _ := json.Marshal(map[string]interface{}{"apiKey": apiKey, "baseUrl": baseURL})
	q := `INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES (?, ?, 'apikey', 'Test', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`
	if _, err := database.Exec(q, connID, provider, string(data)); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
}

func TestApplyTokenSavers_AllOff(t *testing.T) {
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()

	body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	got := h.applyTokenSavers(body)
	if string(got) != string(body) {
		t.Errorf("expected unchanged body when all token savers off")
	}
}

func TestApplyTokenSavers_RTKOnly(t *testing.T) {
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()
	h.TokenSaver.SetRTK(true)

	// RTK compresses tool messages with large content. Build via json.Marshal
	// so newlines are properly escaped (raw newlines are invalid JSON).
	var sb strings.Builder
	for i := 0; i < 300; i++ {
		sb.WriteString("unique log line number ")
		sb.WriteString(strconv.Itoa(i))
		sb.WriteString("\n")
	}
	body, err := json.Marshal(map[string]any{
		"messages": []any{
			map[string]any{"role": "tool", "content": sb.String()},
		},
	})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	got := h.applyTokenSavers(body)
	if string(got) == string(body) {
		t.Errorf("expected RTK to modify body")
	}
}

func TestApplyTokenSavers_CavemanInjects(t *testing.T) {
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()
	h.TokenSaver.SetCaveman(true)

	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	got := h.applyTokenSavers(body)
	// Caveman prompt text should now appear in the system message.
	if !strings.Contains(string(got), "terse") && !strings.Contains(string(got), "caveman") {
		t.Errorf("expected caveman prompt injected, got %s", got)
	}
}

func TestApplyTokenSavers_PonytailInjects(t *testing.T) {
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()
	h.TokenSaver.SetPonytail(true)

	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	got := h.applyTokenSavers(body)
	if !strings.Contains(string(got), tokensaver.PonytailPrompt[:20]) {
		t.Errorf("expected ponytail prompt injected, got %s", got)
	}
}

func TestTryForwardWithConnection_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"ok","choices":[{"message":{"content":"done"}}],"usage":{"prompt_tokens":2,"completion_tokens":2}}`))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	seedConnDB(t, database, "deepseek", "conn-try", "sk-try", srv.URL)

	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	body := []byte(`{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	err := h.tryForwardWithConnection(context.Background(), rec, "deepseek", "deepseek-chat", "conn-try", &ConnectionData{APIKey: "sk-try", BaseURL: srv.URL}, body, false, false, "/v1/chat/completions")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestTryForwardWithConnection_NoAPIKey(t *testing.T) {
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	err := h.tryForwardWithConnection(context.Background(), rec, "deepseek", "deepseek-chat", "conn-x", &ConnectionData{}, []byte(`{}`), false, false, "/v1/chat/completions")
	if err == nil {
		t.Fatal("expected error when API key missing")
	}
	var ue *upstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *upstreamError, got %T", err)
	}
	if ue.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", ue.StatusCode)
	}
}

func TestHandleAccountFallback_RetryableLocksModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	seedConnDB(t, database, "deepseek", "conn-429", "sk-429", srv.URL)

	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	body := []byte(`{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	err := h.handleAccountFallback(context.Background(), rec, "deepseek", "deepseek-chat", "", body, false, false, "/v1/chat/completions")
	if err == nil {
		t.Fatal("expected error after exhausting connections")
	}

	locked, lerr := repo.IsConnectionModelLocked("conn-429", "deepseek-chat")
	if lerr != nil {
		t.Fatalf("IsConnectionModelLocked failed: %v", lerr)
	}
	if !locked {
		t.Error("expected conn-429 per-connection lock after 429 on all connections")
	}
}

func TestHandleMessagesComboFallback_429LocksAndExcludesConnection(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	// Remove the helper's pre-seeded deepseek/groq connections so conn-combo is
	// the only deepseek connection (they're priority 1 and would shadow it).
	if _, err := database.Exec(`DELETE FROM providerConnections WHERE id IN ('conn-1', 'conn-2')`); err != nil {
		t.Fatalf("clear seeded connections: %v", err)
	}
	seedConnDB(t, database, "deepseek", "conn-combo", "sk-combo", srv.URL)

	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	// Two models on the SAME provider+connection: after the first 429, the
	// connection is locked AND excluded so the second model must not re-hit it.
	comboModels := []string{"deepseek/deepseek-chat", "deepseek/deepseek-reasoner"}
	modelsJSON, _ := json.Marshal(comboModels)
	if _, err := database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES ('combo-1', 'combo-test', 'fallback', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(modelsJSON)); err != nil {
		t.Fatalf("seed combo: %v", err)
	}

	translatedReq := map[string]any{
		"model":      "deepseek-chat",
		"max_tokens": 100,
		"messages":   []map[string]any{{"role": "user", "content": "hi"}},
	}
	rec := httptest.NewRecorder()
	h.handleMessagesComboFallback(context.Background(), rec, translatedReq, comboModels, "fallback", false, "combo-test", 0)

	if got := hits.Load(); got != 1 {
		t.Errorf("expected 1 upstream hit (second combo model excluded), got %d", got)
	}

	locked, lerr := repo.IsConnectionModelLocked("conn-combo", "deepseek-chat")
	if lerr != nil {
		t.Fatalf("IsConnectionModelLocked failed: %v", lerr)
	}
	if !locked {
		t.Error("expected conn-combo locked for deepseek-chat after 429")
	}
}

func TestHandleMessagesComboFallback_RetriesOnceOnBoundedRetryAfter(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if n == 1 {
			// Upstream says "wait ~4s" (RFC3339). The 429 connection lock is ~2s,
			// so the retry pass after the wait finds it unlocked again.
			ra := time.Now().Add(4 * time.Second).Format(time.RFC3339)
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":{"retryAfter":"` + ra + `"}}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","created":0,"model":"deepseek-chat","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`DELETE FROM providerConnections WHERE id IN ('conn-1', 'conn-2')`); err != nil {
		t.Fatalf("clear seeded connections: %v", err)
	}
	seedConnDB(t, database, "deepseek", "conn-combo", "sk-combo", srv.URL)

	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	comboModels := []string{"deepseek/deepseek-chat"}
	modelsJSON, _ := json.Marshal(comboModels)
	if _, err := database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES ('combo-r', 'combo-retry', 'fallback', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(modelsJSON)); err != nil {
		t.Fatalf("seed combo: %v", err)
	}

	translatedReq := map[string]any{
		"model":      "deepseek-chat",
		"max_tokens": 100,
		"messages":   []map[string]any{{"role": "user", "content": "hi"}},
	}
	rec := httptest.NewRecorder()
	h.handleMessagesComboFallback(context.Background(), rec, translatedReq, comboModels, "fallback", false, "combo-retry", 0)

	if got := hits.Load(); got != 2 {
		t.Errorf("expected 2 upstream hits (1 failure + 1 retry), got %d", got)
	}
}

func TestHandleAccountFallback_NoConnections(t *testing.T) {
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()

	body := []byte(`{"model":"deepseek-chat","messages":[]}`)
	rec := httptest.NewRecorder()
	err := h.handleAccountFallback(context.Background(), rec, "nonexistent-provider", "model", "", body, false, false, "/v1/chat/completions")
	if err == nil {
		t.Fatal("expected error when provider has no connections")
	}
}
