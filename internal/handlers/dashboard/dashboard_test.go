package dashboard

import (
	"bytes"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"9router/proxy/internal/db"
	"9router/proxy/internal/models"
)

func setupTestDB(t *testing.T) (*db.Repo, func()) {
	t.Helper()
	tmpFile, err := os.CreateTemp("", "test_dashboard_handlers_*.sqlite")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	tmpFile.Close()

	database, err := db.OpenDatabase(tmpFile.Name())
	if err != nil {
		os.Remove(tmpFile.Name())
		t.Fatalf("OpenDatabase failed: %v", err)
	}

	cleanup := func() {
		database.Close()
		os.Remove(tmpFile.Name())
	}

	schema := []string{
		`CREATE TABLE apiKeys (
			id TEXT PRIMARY KEY,
			key TEXT UNIQUE NOT NULL,
			name TEXT,
			machineId TEXT,
			isActive INTEGER DEFAULT 1,
			createdAt TEXT NOT NULL
		);`,
		`CREATE TABLE providerConnections (
			id TEXT PRIMARY KEY,
			provider TEXT NOT NULL,
			authType TEXT NOT NULL,
			name TEXT,
			email TEXT,
			priority INTEGER,
			isActive INTEGER DEFAULT 1,
			data TEXT NOT NULL,
			lastUsedAt TEXT,
			consecutiveUseCount INTEGER DEFAULT 0,
			createdAt TEXT NOT NULL,
			updatedAt TEXT NOT NULL
		);`,
		`CREATE TABLE combos (
			id TEXT PRIMARY KEY,
			name TEXT UNIQUE NOT NULL,
			kind TEXT,
			models TEXT NOT NULL,
			strategy TEXT DEFAULT 'fallback',
			createdAt TEXT NOT NULL,
			updatedAt TEXT NOT NULL
		);`,
		`CREATE TABLE kv (
			scope TEXT NOT NULL,
			key TEXT NOT NULL,
			value TEXT NOT NULL,
			PRIMARY KEY (scope, key)
		);`,
		`CREATE TABLE settings (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			data TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS providerNodes (
			id TEXT PRIMARY KEY,
			type TEXT,
			name TEXT,
			data TEXT NOT NULL,
			createdAt TEXT NOT NULL,
			updatedAt TEXT NOT NULL
		);`,
	}

	for _, query := range schema {
		if _, err := database.Exec(query); err != nil {
			cleanup()
			t.Fatalf("failed to create table: %v", err)
		}
	}

	// Production applies the Go-only columns on every startup; without them a
	// fixture rejects any query that reads a newer column.
	if err := db.EnsureKeiRouterTables(database); err != nil {
		cleanup()
		t.Fatalf("failed to create keirouter tables: %v", err)
	}
	if err := db.EnsureAdditiveColumns(database); err != nil {
		cleanup()
		t.Fatalf("failed to apply additive columns: %v", err)
	}

	return db.NewRepo(database), cleanup
}

func setupTestRouter(repo *db.Repo) chi.Router {
	r := chi.NewRouter()
	h := NewDashboardHandler(repo)
	RegisterRoutes(r, h)
	return r
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestConnectionsEndpoints(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	// 1. GET /api/connections (empty)
	req := httptest.NewRequest(http.MethodGet, "/api/connections", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var conns []*models.ProviderConnection
	if err := json.Unmarshal(rec.Body.Bytes(), &conns); err != nil {
		t.Fatalf("failed to unmarshal connections: %v", err)
	}
	if len(conns) != 0 {
		t.Fatalf("expected 0 connections, got %d", len(conns))
	}

	// 2. POST /api/connections
	createBody := map[string]any{
		"id":       "conn-1",
		"provider": "openai",
		"authType": "apikey",
		"name":     "OpenAI Primary",
		"apiKey":   "sk-test-123",
		"data":     map[string]any{"custom": "value"},
	}
	bodyBytes, _ := json.Marshal(createBody)
	req = httptest.NewRequest(http.MethodPost, "/api/connections", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("create connection expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// 3b. GET /api/connections must never leak secret blobs (credential-dump
	// hardening): data.apiKey/accessToken must be absent from the payload.
	req = httptest.NewRequest(http.MethodGet, "/api/connections", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %s", rec.Body.String())
	}
	var rawConns []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &rawConns); err != nil {
		t.Fatalf("failed to unmarshal connections: %v", err)
	}
	if len(rawConns) != 1 {
		t.Fatalf("expected 1 connection, got %d", len(rawConns))
	}
	if _, ok := rawConns[0]["data"]; ok {
		t.Errorf("GET /api/connections must not expose raw data blob, got keys %v", keysOf(rawConns[0]))
	}
	body3 := rec.Body.String()
	for _, leak := range []string{"sk-test-123", `"accessToken"`, `"refreshToken"`, `"authToken"`, `"apiKey"`} {
		if strings.Contains(body3, leak) {
			t.Errorf("GET /api/connections leaks %q", leak)
		}
	}

	// 3c. GET /api/keys must mask secrets (full value shown once at creation).
	req = httptest.NewRequest(http.MethodGet, "/api/keys", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %s", rec.Body.String())
	}
	var rawKeys []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &rawKeys); err != nil {
		t.Fatalf("failed to unmarshal keys: %v", err)
	}
	for _, k := range rawKeys {
		if kv, ok := k["key"].(string); ok && len(kv) > 16 && !strings.Contains(kv, "…") {
			t.Errorf("GET /api/keys exposes full secret %q", kv)
		}
	}

	// 3. GET /api/connections (now with 1 connection)
	req = httptest.NewRequest(http.MethodGet, "/api/connections", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &conns); err != nil {
		t.Fatalf("failed to unmarshal connections: %v", err)
	}
	if len(conns) != 1 {
		t.Fatalf("expected 1 connection, got %d", len(conns))
	}
	if conns[0].ID != "conn-1" || conns[0].Provider != "openai" {
		t.Fatalf("unexpected connection details: %+v", conns[0])
	}

	// 4. PUT /api/connections/conn-1 (status update)
	inactive := false
	statusUpdate := map[string]any{
		"isActive": inactive,
	}
	bodyBytes, _ = json.Marshal(statusUpdate)
	req = httptest.NewRequest(http.MethodPut, "/api/connections/conn-1", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("update connection status expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify status updated in DB
	c, err := repo.GetProviderConnectionByID("conn-1")
	if err != nil || c == nil {
		t.Fatalf("expected conn-1 to exist, got err: %v", err)
	}
	if c.IsActive != 0 {
		t.Fatalf("expected isActive=0, got %d", c.IsActive)
	}

	// 5. PUT /api/connections/conn-1 (full update)
	active := true
	priority := 5
	fullUpdate := map[string]any{
		"name":     "OpenAI Renamed",
		"priority": priority,
		"isActive": active,
		"data":     `{"new":"data"}`,
	}
	bodyBytes, _ = json.Marshal(fullUpdate)
	req = httptest.NewRequest(http.MethodPut, "/api/connections/conn-1", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("full update connection expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	c, _ = repo.GetProviderConnectionByID("conn-1")
	if c == nil || c.Name == nil || *c.Name != "OpenAI Renamed" || c.Priority == nil || *c.Priority != 5 || c.IsActive != 1 {
		t.Fatalf("unexpected conn-1 state after full update: %+v", c)
	}

	// 6. DELETE /api/connections/conn-1
	req = httptest.NewRequest(http.MethodDelete, "/api/connections/conn-1", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("delete connection expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	c, _ = repo.GetProviderConnectionByID("conn-1")
	if c != nil {
		t.Fatalf("expected conn-1 to be deleted, got: %+v", c)
	}
}

func TestCombosEndpoints(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	// 1. GET /api/combos (empty)
	req := httptest.NewRequest(http.MethodGet, "/api/combos", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var combos []*models.Combo
	if err := json.Unmarshal(rec.Body.Bytes(), &combos); err != nil {
		t.Fatalf("failed to unmarshal combos: %v", err)
	}
	if len(combos) != 0 {
		t.Fatalf("expected 0 combos, got %d", len(combos))
	}

	// 2. POST /api/combos
	createBody := map[string]any{
		"id":       "combo-1",
		"name":     "fast-route",
		"kind":     "chat",
		"models":   []string{"openai/gpt-4o-mini", "anthropic/claude-3-haiku"},
		"strategy": "fallback",
	}
	bodyBytes, _ := json.Marshal(createBody)
	req = httptest.NewRequest(http.MethodPost, "/api/combos", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("create combo expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// 3. GET /api/combos (with 1 combo)
	req = httptest.NewRequest(http.MethodGet, "/api/combos", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &combos); err != nil {
		t.Fatalf("failed to unmarshal combos: %v", err)
	}
	if len(combos) != 1 {
		t.Fatalf("expected 1 combo, got %d", len(combos))
	}
	if combos[0].Name != "fast-route" {
		t.Fatalf("expected fast-route, got %s", combos[0].Name)
	}

	// 4. PUT /api/combos/combo-1
	updateBody := map[string]any{
		"name":     "fast-route-v2",
		"strategy": "round-robin",
	}
	bodyBytes, _ = json.Marshal(updateBody)
	req = httptest.NewRequest(http.MethodPut, "/api/combos/combo-1", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("update combo expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	combo, err := repo.GetComboById("combo-1")
	if err != nil || combo == nil {
		t.Fatalf("expected combo-1, err: %v", err)
	}
	if combo.Name != "fast-route-v2" {
		t.Fatalf("expected updated name fast-route-v2, got %s", combo.Name)
	}

	// 5. DELETE /api/combos/combo-1
	req = httptest.NewRequest(http.MethodDelete, "/api/combos/combo-1", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("delete combo expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	combo, _ = repo.GetComboById("combo-1")
	if combo != nil {
		t.Fatalf("expected combo to be deleted, got: %+v", combo)
	}
}

func TestApiKeysEndpoints(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	// 1. GET /api/keys (empty)
	req := httptest.NewRequest(http.MethodGet, "/api/keys", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var keys []*models.APIKey
	if err := json.Unmarshal(rec.Body.Bytes(), &keys); err != nil {
		t.Fatalf("failed to unmarshal keys: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("expected 0 keys, got %d", len(keys))
	}

	// 2. POST /api/keys (with empty key and id to verify auto-generation)
	createBody := map[string]any{
		"name": "Generated Key",
	}
	bodyBytes, _ := json.Marshal(createBody)
	req = httptest.NewRequest(http.MethodPost, "/api/keys", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("create apiKey expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var createResp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("unmarshal create response: %v", err)
	}
	genID, okID := createResp["id"].(string)
	genKey, okKey := createResp["key"].(string)
	if !okID || genID == "" || !okKey || len(genKey) < 3 || genKey[:3] != "sk-" {
		t.Fatalf("expected auto-generated id and sk-... key, got id=%s, key=%s", genID, genKey)
	}

	// 3. PUT /api/keys/{id}/toggle (toggle status)
	req = httptest.NewRequest(http.MethodPut, "/api/keys/"+genID+"/toggle", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("toggle apiKey expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var toggleResp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &toggleResp)
	if toggleResp["isActive"] != false {
		t.Fatalf("expected isActive to toggle to false, got %v", toggleResp["isActive"])
	}

	// 4. DELETE /api/keys/{id}
	req = httptest.NewRequest(http.MethodDelete, "/api/keys/"+genID, nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("delete apiKey expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	allKeys, _ := repo.GetApiKeys()
	if len(allKeys) != 0 {
		t.Fatalf("expected 0 keys after delete, got %d", len(allKeys))
	}
}

func TestCustomModelsEndpoints(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	// 1. GET /api/models/custom (empty)
	req := httptest.NewRequest(http.MethodGet, "/api/models/custom", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// 2. POST /api/models/custom
	modelData := map[string]any{
		"providerAlias": "cc",
		"id":            "my-custom-model",
		"type":          "llm",
		"name":          "My Custom Model",
		"caps":          map[string]bool{"vision": true, "reasoning": true},
	}
	bodyBytes, _ := json.Marshal(modelData)
	req = httptest.NewRequest(http.MethodPost, "/api/models/custom", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("save custom model expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// 3. GET /api/models/custom
	req = httptest.NewRequest(http.MethodGet, "/api/models/custom", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("get custom models expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var customResp struct {
		Models []any `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &customResp); err != nil {
		t.Fatalf("unmarshal custom models: %v", err)
	}
	if len(customResp.Models) != 1 {
		t.Fatalf("expected 1 custom model, got %d", len(customResp.Models))
	}

	// 4. DELETE /api/models/custom/{key}
	expectedKey := "cc|my-custom-model|llm"
	req = httptest.NewRequest(http.MethodDelete, "/api/models/custom/"+expectedKey, nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("delete custom model expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	kvScope, _ := repo.GetKVScope("customModels")
	if len(kvScope) != 0 {
		t.Fatalf("expected 0 custom models after delete, got %d", len(kvScope))
	}
}

func TestDisabledModelsEndpoints(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	// 1. GET /api/models/disabled (empty)
	req := httptest.NewRequest(http.MethodGet, "/api/models/disabled", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// 2. PUT /api/models/disabled/openai
	disabledList := []string{"gpt-3.5-turbo", "text-embedding-ada-002"}
	bodyBytes, _ := json.Marshal(disabledList)
	req = httptest.NewRequest(http.MethodPut, "/api/models/disabled/openai", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("save disabled models expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// 3. GET /api/models/disabled
	req = httptest.NewRequest(http.MethodGet, "/api/models/disabled", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("get disabled models expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var disabledResp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &disabledResp); err != nil {
		t.Fatalf("unmarshal disabled models: %v", err)
	}
	if _, ok := disabledResp["openai"]; !ok {
		t.Fatalf("expected openai entry in disabled models, got: %+v", disabledResp)
	}
}

func TestSettingsEndpoints(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	// 1. GET /api/settings (empty)
	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// 2. PUT /api/settings
	updates := map[string]any{
		"requireApiKey": true,
		"rtkEnabled":    true,
		"theme":         "dark",
	}
	bodyBytes, _ := json.Marshal(updates)
	req = httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("update settings expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// 3. GET /api/settings (verify merged)
	req = httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("get settings expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var settingsResp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &settingsResp); err != nil {
		t.Fatalf("unmarshal settings: %v", err)
	}
	if settingsResp["requireApiKey"] != true || settingsResp["theme"] != "dark" {
		t.Fatalf("settings not correctly updated: %+v", settingsResp)
	}
}
func TestEdgeAndErrorCases(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	// 1. Connection errors
	// Missing provider
	req := httptest.NewRequest(http.MethodPost, "/api/connections", bytes.NewReader([]byte(`{"name":"test"}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing provider, got %d", rec.Code)
	}

	// Update non-existent connection
	req = httptest.NewRequest(http.MethodPut, "/api/connections/non-existent", bytes.NewReader([]byte(`{"name":"foo"}`)))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for non-existent connection, got %d", rec.Code)
	}

	// 2. Combo errors
	// Missing combo name
	req = httptest.NewRequest(http.MethodPost, "/api/combos", bytes.NewReader([]byte(`{"strategy":"fallback"}`)))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing combo name, got %d", rec.Code)
	}

	// Update non-existent combo
	req = httptest.NewRequest(http.MethodPut, "/api/combos/non-existent", bytes.NewReader([]byte(`{"name":"foo"}`)))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for non-existent combo, got %d", rec.Code)
	}

	// 3. ApiKey errors
	// Toggle non-existent
	req = httptest.NewRequest(http.MethodPut, "/api/keys/non-existent/toggle", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for non-existent apiKey toggle, got %d", rec.Code)
	}

	// Toggle with explicit isActive
	// First create a key
	req = httptest.NewRequest(http.MethodPost, "/api/keys", bytes.NewReader([]byte(`{"id":"key-toggle-test","key":"sk-toggle-test"}`)))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("create key expected 200, got %d", rec.Code)
	}

	// Set explicit false
	explicitFalse := []byte(`{"isActive":false}`)
	req = httptest.NewRequest(http.MethodPut, "/api/keys/key-toggle-test/toggle", bytes.NewReader(explicitFalse))
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("toggle with explicit false expected 200, got %d", rec.Code)
	}

	// 4. Custom Model errors & direct key/value save
	// Missing key and id
	req = httptest.NewRequest(http.MethodPost, "/api/models/custom", bytes.NewReader([]byte(`{"unrelated":"field"}`)))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing custom model key/id, got %d", rec.Code)
	}

	// Save with explicit key and value
	explicitKV := []byte(`{"key":"my-custom-key","value":{"name":"Custom Model 2"}}`)
	req = httptest.NewRequest(http.MethodPost, "/api/models/custom", bytes.NewReader(explicitKV))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("save custom model with explicit key/value expected 200, got %d", rec.Code)
	}

	// 5. Disabled models with object wrapper formats
	wrapObj := []byte(`{"models":["claude-instant-1"]}`)
	req = httptest.NewRequest(http.MethodPut, "/api/models/disabled/anthropic", bytes.NewReader(wrapObj))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("save disabled models with object format expected 200, got %d", rec.Code)
	}

	wrapObj2 := []byte(`{"disabledModels":["command-light"]}`)
	req = httptest.NewRequest(http.MethodPut, "/api/models/disabled/cohere", bytes.NewReader(wrapObj2))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("save disabled models with disabledModels format expected 200, got %d", rec.Code)
	}
}
