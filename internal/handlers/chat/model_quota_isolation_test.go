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

	// RateLimitedUntil must NOT be set on conn-multi
	var rawData string
	_ = database.QueryRow(`SELECT data FROM providerConnections WHERE id = 'conn-multi'`).Scan(&rawData)
	if _, ok := db.ConnectionCooldownUntil(rawData); ok {
		t.Error("expected rateLimitedUntil to NOT be set for model-scoped 429")
	}

	// But lastError and status must still be recorded for dashboard visibility
	var parsedData map[string]any
	_ = json.Unmarshal([]byte(rawData), &parsedData)
	if parsedData["status"] != "error" {
		t.Errorf("status = %v, want error", parsedData["status"])
	}
	if parsedData["lastError"] == nil {
		t.Error("expected lastError to be recorded on connection for dashboard visibility")
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

func TestComboFallback_ModelScoped429_PinsLockAndPreservesUnrelatedModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"Resource has been exhausted","status":"RESOURCE_EXHAUSTED","details":[{"reason":"QUOTA_EXHAUSTED"}]}}`))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	dropSeededConn(t, database)

	connData, _ := json.Marshal(map[string]any{
		"apiKey":      "tok-ag",
		"accessToken": "tok-ag",
		"baseUrl":     srv.URL,
		"projectId":   "test-proj-combo",
		"providerSpecificData": map[string]any{
			"projectId": "test-proj-combo",
		},
	})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES ('conn-combo', 'antigravity', 'oauth', 'Combo AG Conn', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(connData)); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	comboModels := []string{"antigravity/claude-sonnet-4-6", "antigravity/gemini-3.8-flash-high"}
	modelsJSON, _ := json.Marshal(comboModels)
	if _, err := database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES ('combo-test', 'combo-test', 'fallback', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(modelsJSON)); err != nil {
		t.Fatalf("seed combo: %v", err)
	}

	var excludeIDs []string
	reqErr := &upstreamError{
		StatusCode: http.StatusTooManyRequests,
		Body:       []byte(`{"error":{"message":"Resource has been exhausted","details":[{"reason":"QUOTA_EXHAUSTED"}]}}`),
	}

	// Call comboLockRetryable directly
	h.comboLockRetryable(&excludeIDs, "conn-combo", "antigravity", "claude-sonnet-4-6", reqErr)

	// 1. claude-sonnet-4-6 must be locked
	locked, err := repo.IsConnectionModelLocked("conn-combo", "claude-sonnet-4-6")
	if err != nil || !locked {
		t.Errorf("expected claude-sonnet-4-6 to be locked, err=%v, locked=%v", err, locked)
	}

	// 2. rateLimitedUntil must NOT be set
	var rawData string
	_ = database.QueryRow(`SELECT data FROM providerConnections WHERE id = 'conn-combo'`).Scan(&rawData)
	if _, ok := db.ConnectionCooldownUntil(rawData); ok {
		t.Error("expected rateLimitedUntil to NOT be set in combo on model-scoped 429")
	}

	// 3. lastError must be recorded
	var parsedData map[string]any
	_ = json.Unmarshal([]byte(rawData), &parsedData)
	if parsedData["status"] != "error" || parsedData["lastError"] == nil {
		t.Error("expected status=error and lastError to be recorded in combo on model-scoped 429")
	}

	// 4. Next model gemini-3.8-flash-high must be able to select conn-combo!
	conn, _, err := h.getBestConnection("antigravity", "", nil, "gemini-3.8-flash-high")
	if err != nil {
		t.Fatalf("getBestConnection for gemini failed: %v", err)
	}
	if conn == nil || conn.ID != "conn-combo" {
		t.Errorf("expected conn-combo to be selected for gemini, got: %v", conn)
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

func TestModelScoped402_FundsRequiredDoesNotBlockFreeModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte(`{"error":{"message":"Insufficient account funds","type":"insufficient_funds"}}`))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	dropSeededConn(t, database)

	connData, _ := json.Marshal(map[string]any{
		"apiKey":  "sk-zen-key",
		"baseUrl": srv.URL,
	})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES ('conn-zen', 'opencode-zen', 'apikey', 'Zen Conn', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(connData)); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	// Request paid model that returns 402
	body := []byte(`{"model":"claude-3-7-sonnet","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	_ = h.handleAccountFallback(context.Background(), rec, "opencode-zen", "claude-3-7-sonnet", "", body, false, false, "/v1/chat/completions")

	// Paid model should be locked
	locked, err := repo.IsConnectionModelLocked("conn-zen", "claude-3-7-sonnet")
	if err != nil || !locked {
		t.Errorf("expected claude-3-7-sonnet to be locked, err=%v, locked=%v", err, locked)
	}

	// rateLimitedUntil must NOT be set on conn-zen
	var rawData string
	_ = database.QueryRow(`SELECT data FROM providerConnections WHERE id = 'conn-zen'`).Scan(&rawData)
	if _, ok := db.ConnectionCooldownUntil(rawData); ok {
		t.Error("expected rateLimitedUntil to NOT be set for model-scoped 402")
	}

	// Next request for free model space-bunny-free must still select conn-zen!
	conn, _, err := h.getBestConnection("opencode-zen", "", nil, "space-bunny-free")
	if err != nil {
		t.Fatalf("getBestConnection for free model failed: %v", err)
	}
	if conn == nil || conn.ID != "conn-zen" {
		t.Errorf("expected conn-zen to be selected for free model, got: %v", conn)
	}
}

func TestModelScoped401_UnsupportedModelDoesNotBlockAccount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"Model mimo-v2.5-free is not supported"}}`))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	dropSeededConn(t, database)

	connData, _ := json.Marshal(map[string]any{
		"apiKey":  "sk-zen-key",
		"baseUrl": srv.URL,
	})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES ('conn-zen', 'opencode-zen', 'apikey', 'Zen Conn', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(connData)); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	// Request unsupported model that returns 401 Model ... is not supported
	body := []byte(`{"model":"mimo-v2.5-free","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	_ = h.handleAccountFallback(context.Background(), rec, "opencode-zen", "mimo-v2.5-free", "", body, false, false, "/v1/chat/completions")

	// mimo-v2.5-free must be locked
	locked, err := repo.IsConnectionModelLocked("conn-zen", "mimo-v2.5-free")
	if err != nil || !locked {
		t.Errorf("expected mimo-v2.5-free to be locked, err=%v, locked=%v", err, locked)
	}

	// rateLimitedUntil must NOT be set on conn-zen
	var rawData string
	_ = database.QueryRow(`SELECT data FROM providerConnections WHERE id = 'conn-zen'`).Scan(&rawData)
	if _, ok := db.ConnectionCooldownUntil(rawData); ok {
		t.Error("expected rateLimitedUntil to NOT be set for model-scoped 401 unsupported")
	}

	// Next request for another model must still select conn-zen!
	conn, _, err := h.getBestConnection("opencode-zen", "", nil, "space-bunny-free")
	if err != nil {
		t.Fatalf("getBestConnection for another model failed: %v", err)
	}
	if conn == nil || conn.ID != "conn-zen" {
		t.Errorf("expected conn-zen to be selected, got: %v", conn)
	}
}
