package chat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

func TestComboFallbackChain_Success(t *testing.T) {
	primaryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error":{"message":"primary unavailable"}}`))
	}))
	defer primaryServer.Close()

	fallbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"fallback answered"}}]}`))
	}))
	defer fallbackServer.Close()

	database, cleanup := setupChatTestDB(t)
	t.Cleanup(cleanup)

	database.Exec(`DELETE FROM providerConnections`)
	database.Exec(`DELETE FROM combos`)

	dataPrimary, _ := json.Marshal(map[string]any{"apiKey": "sk-mock", "baseUrl": primaryServer.URL})
	database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES ('conn-p', 'openai', 'apikey', 'pri', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(dataPrimary))

	dataFallback, _ := json.Marshal(map[string]any{"apiKey": "sk-mock", "baseUrl": fallbackServer.URL})
	database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES ('conn-f', 'deepseek', 'apikey', 'fb', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(dataFallback))

	modelsPrimary, _ := json.Marshal([]string{"openai/gpt-4o"})
	modelsFallback, _ := json.Marshal([]string{"deepseek/deepseek-chat"})
	database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES ('c10', 'primary-combo', 'fallback', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(modelsPrimary))
	database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES ('c20', 'fallback-combo', 'fallback', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(modelsFallback))

	repo := db.NewRepo(database)
	_ = repo.SetComboStrategy("primary-combo", db.ComboStrategy{
		Strategy:       "fallback",
		FallbackCombos: []string{"fallback-combo"},
	})

	h := NewChatHandler(repo)
	req := httptest.NewRequest(http.MethodPost, "/chat/completions", strings.NewReader(`{"model":"primary-combo","messages":[{"role":"user","content":"hi"}],"stream":false}`))
	rec := httptest.NewRecorder()
	h.HandleChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from fallback combo, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "fallback answered") {
		t.Errorf("unexpected body: %s", rec.Body.String())
	}
	if comboHeader := rec.Header().Get("x-9router-combo"); comboHeader != "fallback-combo" {
		t.Errorf("expected x-9router-combo header to be fallback-combo, got %q", comboHeader)
	}
}

func TestComboFallbackChain_NonRetryable400(t *testing.T) {
	primaryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"bad request model not supported"}}`))
	}))
	defer primaryServer.Close()

	fallbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("fallback combo should not be called on 400 bad request")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"should not happen"}}]}`))
	}))
	defer fallbackServer.Close()

	database, cleanup := setupChatTestDB(t)
	t.Cleanup(cleanup)

	database.Exec(`DELETE FROM providerConnections`)
	database.Exec(`DELETE FROM combos`)

	dataPrimary, _ := json.Marshal(map[string]any{"apiKey": "sk-mock", "baseUrl": primaryServer.URL})
	database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES ('conn-p', 'openai', 'apikey', 'pri', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(dataPrimary))

	modelsPrimary, _ := json.Marshal([]string{"openai/gpt-4o"})
	modelsFallback, _ := json.Marshal([]string{"deepseek/deepseek-chat"})
	database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES ('c10', 'primary-combo', 'fallback', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(modelsPrimary))
	database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES ('c20', 'fallback-combo', 'fallback', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(modelsFallback))

	repo := db.NewRepo(database)
	_ = repo.SetComboStrategy("primary-combo", db.ComboStrategy{
		Strategy:       "fallback",
		FallbackCombos: []string{"fallback-combo"},
	})

	h := NewChatHandler(repo)
	req := httptest.NewRequest(http.MethodPost, "/chat/completions", strings.NewReader(`{"model":"primary-combo","messages":[{"role":"user","content":"hi"}],"stream":false}`))
	rec := httptest.NewRecorder()
	h.HandleChatCompletions(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestNestedComboExpansion_AndCyclePrevention(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	t.Cleanup(cleanup)

	database.Exec(`DELETE FROM combos`)

	mRoot, _ := json.Marshal([]string{"sub1"})
	mSub1, _ := json.Marshal([]string{"sub2"})
	mSub2, _ := json.Marshal([]string{"openai/gpt-4o"})

	database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES ('r', 'root-combo', 'fallback', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(mRoot))
	database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES ('s1', 'sub1', 'fallback', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(mSub1))
	database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES ('s2', 'sub2', 'fallback', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(mSub2))

	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	info, err := h.ResolveModel("root-combo")
	if err != nil {
		t.Fatalf("failed to resolve root-combo: %v", err)
	}
	if info.Model != "gpt-4o" || info.Provider != "openai" {
		t.Errorf("unexpected resolved info: %+v", info)
	}

	mA, _ := json.Marshal([]string{"comboB"})
	mB, _ := json.Marshal([]string{"comboA"})
	database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES ('ca', 'comboA', 'fallback', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(mA))
	database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES ('cb', 'comboB', 'fallback', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(mB))

	_, err = h.ResolveModel("comboA")
	if err == nil {
		t.Error("expected error or failure on cyclic combo reference, got nil")
	}
}

func TestComboFallbackChain_MultiFallback_A_to_B_to_C(t *testing.T) {
	serverA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error":{"message":"server A unavailable"}}`))
	}))
	defer serverA.Close()

	serverB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error":{"message":"server B unavailable"}}`))
	}))
	defer serverB.Close()

	serverC := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"answered by combo C"}}]}`))
	}))
	defer serverC.Close()

	database, cleanup := setupChatTestDB(t)
	t.Cleanup(cleanup)

	database.Exec(`DELETE FROM providerConnections`)
	database.Exec(`DELETE FROM combos`)

	dataA, _ := json.Marshal(map[string]any{"apiKey": "sk-mock", "baseUrl": serverA.URL})
	database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES ('conn-a', 'openai', 'apikey', 'a', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(dataA))

	dataB, _ := json.Marshal(map[string]any{"apiKey": "sk-mock", "baseUrl": serverB.URL})
	database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES ('conn-b', 'groq', 'apikey', 'b', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(dataB))

	dataC, _ := json.Marshal(map[string]any{"apiKey": "sk-mock", "baseUrl": serverC.URL})
	database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES ('conn-c', 'deepseek', 'apikey', 'c', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(dataC))

	mA, _ := json.Marshal([]string{"openai/gpt-4o"})
	mB, _ := json.Marshal([]string{"groq/qwen"})
	mC, _ := json.Marshal([]string{"deepseek/deepseek-chat"})

	database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES ('ca', 'combo-a', 'fallback', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(mA))
	database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES ('cb', 'combo-b', 'fallback', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(mB))
	database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES ('cc', 'combo-c', 'fallback', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(mC))

	repo := db.NewRepo(database)
	// combo-a has fallbackCombos: ["combo-b", "combo-c"]
	// combo-b has NO fallbacks of its own (terminal failure inside combo-b)
	_ = repo.SetComboStrategy("combo-a", db.ComboStrategy{
		Strategy:       "fallback",
		FallbackCombos: []string{"combo-b", "combo-c"},
	})

	h := NewChatHandler(repo)
	req := httptest.NewRequest(http.MethodPost, "/chat/completions", strings.NewReader(`{"model":"combo-a","messages":[{"role":"user","content":"hi"}],"stream":false}`))
	rec := httptest.NewRecorder()
	h.HandleChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from combo-c after combo-b failed, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "answered by combo C") {
		t.Errorf("unexpected body: %s", rec.Body.String())
	}
	if comboHeader := rec.Header().Get("x-9router-combo"); comboHeader != "combo-c" {
		t.Errorf("expected x-9router-combo header to be combo-c, got %q", comboHeader)
	}
}
