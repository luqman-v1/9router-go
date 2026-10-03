package chat

import (
	"context"
	"database/sql"
	json "encoding/json/v2"
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
	data, _ := json.Marshal(map[string]any{"apiKey": apiKey, "baseUrl": baseURL})
	q := `INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES (?, ?, 'apikey', 'Test', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`
	if _, err := database.Exec(q, connID, provider, string(data)); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
}

func TestApplyTokenSavers_AllOff(t *testing.T) {
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()

	body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	got, origTokens, savedTokens, savedPct := h.applyTokenSavers(body, false)
	if string(got) != string(body) {
		t.Errorf("expected unchanged body when all token savers off")
	}
	if origTokens != 0 || savedTokens != 0 || savedPct != 0 {
		t.Errorf("expected 0 token metrics when off, got orig=%d, saved=%d, pct=%d", origTokens, savedTokens, savedPct)
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
	got, origTokens, savedTokens, savedPct := h.applyTokenSavers(body, false)
	if string(got) == string(body) {
		t.Errorf("expected RTK to modify body")
	}
	if origTokens <= 0 {
		t.Errorf("expected origTokens > 0, got %d", origTokens)
	}
	if savedTokens <= 0 {
		t.Errorf("expected savedTokens > 0, got %d", savedTokens)
	}
	if savedPct <= 0 {
		t.Errorf("expected savedPct > 0, got %d", savedPct)
	}
}

func TestApplyTokenSavers_CavemanInjects(t *testing.T) {
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()
	h.TokenSaver.SetCaveman(true)

	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	got, _, _, _ := h.applyTokenSavers(body, false)
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
	got, _, _, _ := h.applyTokenSavers(body, false)
	if !strings.Contains(string(got), tokensaver.PonytailPrompt[:20]) {
		t.Errorf("expected ponytail prompt injected, got %s", got)
	}
}

func TestApplyTokenSavers_ADHDInjects(t *testing.T) {
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()
	h.TokenSaver.SetADHD(true)

	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	got, _, _, _ := h.applyTokenSavers(body, false)
	if !strings.Contains(string(got), "I have ADHD — action-first output") {
		t.Errorf("expected ADHD full prompt injected, got %s", got)
	}

	// Test lite mode
	h.TokenSaver.SetADHD(true, "lite")
	bodyLite := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	gotLite, _, _, _ := h.applyTokenSavers(bodyLite, false)
	if !strings.Contains(string(gotLite), "I have ADHD (lite)") {
		t.Errorf("expected ADHD lite prompt injected, got %s", gotLite)
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
	err := h.tryForwardWithConnection(forwardRequestParams{
		Ctx: context.Background(), W: rec, Provider: "deepseek", Model: "deepseek-chat",
		ConnectionID: "conn-try", ConnData: &ConnectionData{APIKey: "sk-try", BaseURL: srv.URL}, Body: body,
		IsStream: false, TranslateResponse: false, Endpoint: "/v1/chat/completions",
	})
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
	err := h.tryForwardWithConnection(forwardRequestParams{
		Ctx: context.Background(), W: rec, Provider: "deepseek", Model: "deepseek-chat",
		ConnectionID: "conn-x", ConnData: &ConnectionData{}, Body: []byte(`{}`),
		IsStream: false, TranslateResponse: false, Endpoint: "/v1/chat/completions",
	})
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
	if _, err := database.Exec(`DELETE FROM providerConnections WHERE id IN ('conn-1', 'conn-2')`); err != nil {
		t.Fatalf("clear seeded connections: %v", err)
	}
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
			// Upstream says "wait ~1s" (RFC3339). A 429 is only worth retrying
			// inside brief429RetryTolerance — past that the account is out of
			// quota and re-hitting it deepens the upstream's backoff — so the
			// bound, not the old 8s cap, is what this covers.
			ra := time.Now().Add(time.Second).Format(time.RFC3339)
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

func TestHandleAccountFallback_503CapacityLocksCanonicalModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{
			"error": {
				"code": 503,
				"message": "No capacity available for model gemini-3.8-flash-tiered on the server",
				"status": "UNAVAILABLE",
				"details": [{"reason": "MODEL_CAPACITY_EXHAUSTED"}]
			}
		}`))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	agData, _ := json.Marshal(map[string]any{
		"apiKey":      "tok-ag",
		"accessToken": "tok-ag",
		"baseUrl":     srv.URL,
		"projectId":   "test-proj-503",
		"providerSpecificData": map[string]any{
			"projectId": "test-proj-503",
		},
	})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES ('conn-ag-cap', 'antigravity', 'oauth', 'AG Cap Test', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(agData)); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	body := []byte(`{"model":"gemini-3.8-flash-low","messages":[{"role":"user","content":"ping"}]}`)
	rec := httptest.NewRecorder()
	err := h.handleAccountFallback(context.Background(), rec, "antigravity", "gemini-3.8-flash-low", "", body, false, false, "/v1/chat/completions")
	if err == nil {
		t.Fatal("expected error after 503 capacity exhaustion")
	}

	// Canonical model "gemini-3.8-flash-tiered" must be locked
	lockedCanonical, err := repo.IsConnectionModelLocked("conn-ag-cap", "gemini-3.8-flash-tiered")
	if err != nil {
		t.Fatalf("check canonical lock: %v", err)
	}
	if !lockedCanonical {
		t.Error("expected canonical model gemini-3.8-flash-tiered to be locked on 503 capacity error")
	}

	// Best connection for gemini-3.8-flash-high must now skip this connection because canonical tiered is locked!
	conn, _, _ := h.GetBestConnection("antigravity", "", nil, "gemini-3.8-flash-high")
	if conn != nil {
		t.Errorf("expected no connection available for high tier when canonical model is locked, got %s", conn.ID)
	}
}

func TestExtractErrorText_CloudflareHTML(t *testing.T) {
	htmlBody := []byte(`<!DOCTYPE html>
<html class="no-js" lang="en-US">
<head>
<title>Attention Required! | Cloudflare</title>
<meta charset="UTF-8" />
</head>
<body>
<h1>Attention Required!</h1>
</body>
</html>`)

	got := extractErrorText(htmlBody)
	if !strings.Contains(got, "Cloudflare WAF challenge") {
		t.Errorf("expected Cloudflare WAF challenge in error text, got %q", got)
	}
}
