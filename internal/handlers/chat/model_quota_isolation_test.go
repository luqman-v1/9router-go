package chat

import (
	"context"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"9router/proxy/internal/db"
)

func TestModelScoped429_DoesNotBlockUnrelatedModelsOnSameAccount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED","details":[{"reason":"QUOTA_EXHAUSTED"}]}}`))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	dropSeededConn(t, database)

	connData, _ := json.Marshal(map[string]any{
		"apiKey":      "tok-ag",
		"accessToken": "tok-ag",
		"baseUrl":     srv.URL,
		"projectId":   "test-proj-429",
		"providerSpecificData": map[string]any{
			"projectId": "test-proj-429",
		},
	})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES ('conn-multi', 'antigravity', 'oauth', 'Multi Model Conn', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(connData)); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	// Send request to claude-sonnet-4-6 which returns 429 QUOTA_EXHAUSTED
	body := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	_ = h.handleAccountFallback(context.Background(), rec, "antigravity", "claude-sonnet-4-6", "", body, false, false, "/v1/chat/completions")

	// claude-sonnet-4-6 must be locked on conn-multi
	locked, err := repo.IsConnectionModelLocked("conn-multi", "claude-sonnet-4-6")
	if err != nil || !locked {
		t.Errorf("expected claude-sonnet-4-6 to be locked, err=%v, locked=%v", err, locked)
	}

	// But rateLimitedUntil must NOT be set on conn-multi
	var rawData string
	_ = database.QueryRow(`SELECT data FROM providerConnections WHERE id = 'conn-multi'`).Scan(&rawData)
	if _, ok := db.ConnectionCooldownUntil(rawData); ok {
		t.Error("expected rateLimitedUntil to NOT be set for model-scoped 429")
	}

	// Next request for gemini-3.8-flash-high must be able to select conn-multi!
	conn, _, err := h.getBestConnection("antigravity", "", nil, "gemini-3.8-flash-high")
	if err != nil {
		t.Fatalf("getBestConnection for gemini failed: %v", err)
	}
	if conn == nil || conn.ID != "conn-multi" {
		t.Errorf("expected conn-multi to be selected for gemini, got: %v", conn)
	}
}

func TestAccountScoped401_BlocksAllModelsOnSameAccount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"Invalid OAuth credentials","status":"UNAUTHENTICATED"}}`))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	dropSeededConn(t, database)

	connData, _ := json.Marshal(map[string]any{
		"apiKey":      "tok-ag",
		"accessToken": "tok-ag",
		"baseUrl":     srv.URL,
		"projectId":   "test-proj-401",
		"providerSpecificData": map[string]any{
			"projectId": "test-proj-401",
		},
	})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES ('conn-multi', 'antigravity', 'oauth', 'Multi Model Conn', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(connData)); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	// Send request that returns 401
	body := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	_ = h.handleAccountFallback(context.Background(), rec, "antigravity", "claude-sonnet-4-6", "", body, false, false, "/v1/chat/completions")

	// rateLimitedUntil MUST be set on conn-multi
	var rawData string
	_ = database.QueryRow(`SELECT data FROM providerConnections WHERE id = 'conn-multi'`).Scan(&rawData)
	if until, ok := db.ConnectionCooldownUntil(rawData); !ok || until.Before(time.Now()) {
		t.Error("expected rateLimitedUntil to be set for account-scoped 401")
	}

	// getBestConnection for any other model must skip conn-multi
	_, _, err := h.getBestConnection("antigravity", "", nil, "gemini-3.8-flash-high")
	if err == nil {
		t.Error("expected getBestConnection to fail because account is cooling down")
	}
}
