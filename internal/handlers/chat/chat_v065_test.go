package chat

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
)

func TestHandleModelLookup_Kind(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	// Test kind: image should return list with at least one (if provider has ImageURL)
	req := httptest.NewRequest("GET", "/v1/models/image", nil)
	w := httptest.NewRecorder()
	handler.HandleModelLookup(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for kind image, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["object"] != "list" {
		t.Errorf("expected list, got %v", resp["object"])
	}
}

func TestHandleModelLookup_ProviderModel(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	// Seed an alias for lookup
	if _, err := database.Exec(`INSERT INTO kv (scope, key, value) VALUES ('modelAliases', 'cc/claude-sonnet-4-6', '"cc/claude-sonnet-4-6"')`); err != nil {
		t.Fatalf("seed alias: %v", err)
	}
	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	req := httptest.NewRequest("GET", "/v1/models/cc/claude-sonnet-4-6", nil)
	w := httptest.NewRecorder()
	handler.HandleModelLookup(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for provider/model, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["id"] != "cc/claude-sonnet-4-6" {
		t.Errorf("expected id cc/claude-sonnet-4-6, got %v", resp["id"])
	}
	// Test encoded slash
	req2 := httptest.NewRequest("GET", "/v1/models/cc%2Fclaude-sonnet-4-6", nil)
	w2 := httptest.NewRecorder()
	handler.HandleModelLookup(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200 for encoded, got %d: %s", w2.Code, w2.Body.String())
	}
}

func TestHandleModelLookup_NotFound(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	req := httptest.NewRequest("GET", "/v1/models/cc/missing-model-xyz", nil)
	w := httptest.NewRecorder()
	handler.HandleModelLookup(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if errObj, ok := resp["error"].(map[string]any); ok {
		if errObj["code"] != "model_not_found" {
			t.Errorf("expected model_not_found, got %v", errObj["code"])
		}
	} else {
		t.Error("expected error object")
	}
}

func TestHandleModels_CustomModels(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	// Seed custom model with vision caps
	customJSON := `{"providerAlias":"cc","id":"my-custom-vision","type":"llm","name":"my-custom-vision","caps":{"vision":true,"reasoning":true}}`
	if _, err := database.Exec(`INSERT INTO kv (scope, key, value) VALUES ('customModels', 'cc/my-custom-vision/llm', ?)`, customJSON); err != nil {
		t.Fatalf("seed custom: %v", err)
	}
	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	req := httptest.NewRequest("GET", "/models", nil)
	w := httptest.NewRecorder()
	handler.HandleModels(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !bytes.Contains([]byte(body), []byte("cc/my-custom-vision")) {
		t.Errorf("expected custom model cc/my-custom-vision in list, got %s", body)
	}
	// Check caps applied
	caps := providers.GetCapabilitiesForModel("cc", "my-custom-vision")
	if !caps.Vision || !caps.Reasoning {
		t.Errorf("custom caps should be Vision+Reasoning, got %+v", caps)
	}
	// Cleanup
	providers.ClearCustomModelCaps()
}

func TestAntigravityQuota_StrikeReassert(t *testing.T) {
	ClearAntigravityQuotaCache()
	connID := "test-conn-strike"
	model := "gemini-3.8-flash-high"
	ClearAntigravityStrikes(connID, model)

	// Mock server that returns optimistic 90% remaining
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"models":{"` + model + `":{"quotaInfo":{"remainingFraction":0.9,"resetTime":"2099-01-01T00:00:00Z"}}}}`))
	}))
	defer srv.Close()

	// Temporarily override URL
	oldURL := antigravityQuotaBaseURL
	antigravityQuotaBaseURL = srv.URL
	defer func() { antigravityQuotaBaseURL = oldURL }()

	// 3 consecutive 429s with optimistic quota should trigger strike block
	for i := 0; i < 3; i++ {
		res := HandleAntigravityQuotaError(context.Background(), srv.Client(), connID, 429, model, "token", "proj")
		t.Logf("strike %d: res=%v", i+1, res)
		if i < 2 && res != nil {
			t.Fatalf("expected nil for first 2 strikes, got %v", *res)
		}
		if i == 2 && res == nil {
			// Debug: check agStrikes
			t.Logf("agStrikes for %s: %v", connID+"|"+model, agStrikes[connID+"|"+model])
			t.Fatalf("expected block on 3rd strike")
		}
	}

	t.Logf("after 3 strikes, IsBlocked=%v", IsAntigravityModelBlocked(connID, model))
	for k, v := range agStrikes {
		t.Logf("agStrikes[%q] = %v len=%d", k, v, len(v))
	}
	for k, v := range agStrikeBlocks {
		t.Logf("agStrikeBlocks[%q] = %v", k, v)
	}
	// Also check quota cache
	if quotas, err := RefreshAntigravityQuota(context.Background(), srv.Client(), connID, "token", "proj"); err == nil {
		t.Logf("quotas after refresh: %v", quotas)
		for mk, q := range quotas {
			t.Logf("quota %q: %+v", mk, q)
		}
	}
	if !IsAntigravityModelBlocked(connID, model) {
		t.Error("should be blocked after 3 strikes")
	}

	// Refresh should re-assert the block even though upstream says 90%
	_, err := RefreshAntigravityQuota(nil, srv.Client(), connID, "token", "proj")
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if !IsAntigravityModelBlocked(connID, model) {
		t.Error("block should be re-asserted after optimistic refresh")
	}

	ClearAntigravityStrikes(connID, model)
	ClearAntigravityQuotaCache()
	if IsAntigravityModelBlocked(connID, model) {
		t.Error("should not be blocked after clear")
	}
}
