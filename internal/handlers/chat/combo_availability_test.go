package chat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"9router/proxy/internal/db"
)

func seedAvailabilityConnection(t testing.TB, database *db.Repo, id, provider string, active int, baseURL string) {
	t.Helper()
	data, _ := json.Marshal(map[string]any{"apiKey": "sk-mock", "baseUrl": baseURL})
	if _, err := database.RawDB().Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		 VALUES (?, ?, 'apikey', ?, 0, ?, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		id, provider, id, active, string(data),
	); err != nil {
		t.Fatalf("seed connection %s: %v", id, err)
	}
}

func TestComboAvailability_DisabledModelsAndProvidersSkippedAndRestored(t *testing.T) {
	var firstHits atomic.Int32
	firstUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"from-first"}}]}`))
	}))
	defer firstUpstream.Close()

	var secondHits atomic.Int32
	secondUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"from-second"}}]}`))
	}))
	defer secondUpstream.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`DELETE FROM providerConnections`); err != nil {
		t.Fatalf("clear connections: %v", err)
	}
	if _, err := database.Exec(`DELETE FROM combos`); err != nil {
		t.Fatalf("clear combos: %v", err)
	}

	repo := db.NewRepo(database)
	seedAvailabilityConnection(t, repo, "conn-oai", "openai", 1, firstUpstream.URL)
	seedAvailabilityConnection(t, repo, "conn-ds", "deepseek", 1, secondUpstream.URL)

	comboModels, _ := json.Marshal([]string{"openai/gpt-4o", "deepseek/deepseek-chat"})
	if _, err := database.Exec(
		`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES ('c-avail', 'c-avail', 'fallback', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(comboModels),
	); err != nil {
		t.Fatalf("seed combo: %v", err)
	}
	_ = repo.SetComboStrategy("c-avail", db.ComboStrategy{Strategy: "fallback"})

	h := NewChatHandler(repo)

	callCombo := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.handleComboFallback(context.Background(), rec, []byte(`{"model":"c-avail","messages":[{"role":"user","content":"hi"}]}`), []string{"openai/gpt-4o", "deepseek/deepseek-chat"}, "fallback", false, false, "c-avail", 0)
		return rec
	}

	// 1. Disable openai/gpt-4o -> routes straight to deepseek without touching firstUpstream.
	if err := repo.SetKV("disabledModels", "openai", `["gpt-4o"]`); err != nil {
		t.Fatalf("SetKV disabledModels: %v", err)
	}
	rec := callCombo()
	if rec.Code != http.StatusOK {
		t.Fatalf("disabled model status = %d: %s", rec.Code, rec.Body.String())
	}
	if firstHits.Load() != 0 || secondHits.Load() != 1 {
		t.Fatalf("hits after model disable: first=%d second=%d, want 0/1", firstHits.Load(), secondHits.Load())
	}
	// Combo stored config must still hold both entries.
	if c, err := repo.GetComboByName("c-avail"); err != nil || len(c.ModelNames()) != 2 {
		t.Fatalf("combo config altered while disabled: combo=%+v err=%v", c, err)
	}

	// 2. Re-enable openai/gpt-4o -> resumes using firstUpstream automatically.
	if err := repo.SetKV("disabledModels", "openai", `[]`); err != nil {
		t.Fatalf("clear disabledModels: %v", err)
	}
	rec = callCombo()
	if rec.Code != http.StatusOK || firstHits.Load() != 1 {
		t.Fatalf("after re-enable: status=%d firstHits=%d body=%s", rec.Code, firstHits.Load(), rec.Body.String())
	}

	// 3. Disable provider connection (isActive = 0) -> skips openai without touching firstUpstream.
	if err := repo.SetConnectionStatus("conn-oai", false); err != nil {
		t.Fatalf("disable conn-oai: %v", err)
	}
	rec = callCombo()
	if rec.Code != http.StatusOK || firstHits.Load() != 1 || secondHits.Load() != 2 {
		t.Fatalf("after provider disable: status=%d first=%d second=%d", rec.Code, firstHits.Load(), secondHits.Load())
	}

	// 4. Re-enable provider connection -> resumes automatically.
	if err := repo.SetConnectionStatus("conn-oai", true); err != nil {
		t.Fatalf("enable conn-oai: %v", err)
	}
	rec = callCombo()
	if rec.Code != http.StatusOK || firstHits.Load() != 2 {
		t.Fatalf("after provider re-enable: status=%d first=%d", rec.Code, firstHits.Load())
	}
}

func TestComboAvailability_MessagesAndFusionAndCustomNodes(t *testing.T) {
	var disabledNodeHits atomic.Int32
	disabledNodeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		disabledNodeHits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer disabledNodeSrv.Close()

	var okNodeHits atomic.Int32
	okNodeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		okNodeHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"from-ok-node"}}]}`))
	}))
	defer okNodeSrv.Close()

	var claudeHits atomic.Int32
	claudeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claudeHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"ok-claude","content":[{"type":"text","text":"claude-ok"}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":3}}`))
	}))
	defer claudeSrv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`DELETE FROM providerConnections`); err != nil {
		t.Fatalf("clear connections: %v", err)
	}
	repo := db.NewRepo(database)

	if _, err := repo.CreateProviderNode("node-a", "openai-compatible", "Node A",
		`{"prefix":"nodea","apiType":"openai-compatible","baseUrl":"`+disabledNodeSrv.URL+`"}`); err != nil {
		t.Fatalf("CreateProviderNode node-a: %v", err)
	}
	if _, err := repo.CreateProviderNode("node-b", "openai-compatible", "Node B",
		`{"prefix":"nodeb","apiType":"openai-compatible","baseUrl":"`+okNodeSrv.URL+`"}`); err != nil {
		t.Fatalf("CreateProviderNode node-b: %v", err)
	}
	seedAvailabilityConnection(t, repo, "conn-na", "node-a", 1, disabledNodeSrv.URL)
	seedAvailabilityConnection(t, repo, "conn-nb", "node-b", 1, okNodeSrv.URL)
	seedAvailabilityConnection(t, repo, "conn-anth", "anthropic-compatible-acme", 1, claudeSrv.URL+"/proxy/v1")

	// Disable "shared-model" strictly on node-a (keyed by nodeID); nodeb/shared-model must stay available.
	if err := repo.SetKV("disabledModels", "node-a", `["shared-model"]`); err != nil {
		t.Fatalf("disable node-a model: %v", err)
	}

	h := NewChatHandler(repo)

	// /v1/messages combo path skips disabled nodea/shared-model and succeeds on Claude node.
	rec := httptest.NewRecorder()
	translatedReq := map[string]any{
		"model":      "combo-msg",
		"max_tokens": 50,
		"messages":   []map[string]any{{"role": "user", "content": "hi"}},
	}
	h.handleMessagesComboFallback(context.Background(), rec, translatedReq, []string{"nodea/shared-model", "anthropic-compatible-acme/ok-claude"}, "fallback", false, "combo-msg", 0)
	if rec.Code != http.StatusOK || disabledNodeHits.Load() != 0 || claudeHits.Load() != 1 {
		t.Fatalf("messages path: status=%d disabledNodeHits=%d claudeHits=%d body=%s", rec.Code, disabledNodeHits.Load(), claudeHits.Load(), rec.Body.String())
	}

	// Fusion degrades cleanly to the single enabled panel model ("nodeb/shared-model") when nodea is skipped.
	rec = httptest.NewRecorder()
	h.handleFusion(context.Background(), rec, []byte(`{"model":"combo-fusion","messages":[{"role":"user","content":"hi"}]}`), []string{"nodea/shared-model", "nodeb/shared-model"}, "fallback", false, false, "combo-fusion", 0, "nodea/shared-model")
	if rec.Code != http.StatusOK || disabledNodeHits.Load() != 0 || okNodeHits.Load() != 1 {
		t.Fatalf("fusion path: status=%d disabledNodeHits=%d okNodeHits=%d body=%s", rec.Code, disabledNodeHits.Load(), okNodeHits.Load(), rec.Body.String())
	}

	// Fusion returns 503 when every panel entry is disabled.
	if err := repo.SetConnectionStatus("conn-nb", false); err != nil {
		t.Fatalf("disable conn-nb: %v", err)
	}
	rec = httptest.NewRecorder()
	h.handleFusion(context.Background(), rec, []byte(`{"model":"combo-fusion","messages":[{"role":"user","content":"hi"}]}`), []string{"nodea/shared-model", "nodeb/shared-model"}, "fallback", false, false, "combo-fusion", 0, "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("all disabled fusion status = %d, want 503: %s", rec.Code, rec.Body.String())
	}
}

func TestComboAvailability_AllDisabledFallsBackOrReturns503Safely(t *testing.T) {
	var fbHits atomic.Int32
	fbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fbHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"from-fallback-combo"}}]}`))
	}))
	defer fbSrv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`DELETE FROM providerConnections`); err != nil {
		t.Fatalf("clear connections: %v", err)
	}
	repo := db.NewRepo(database)
	seedAvailabilityConnection(t, repo, "conn-oai", "openai", 0, "http://127.0.0.1:1")
	seedAvailabilityConnection(t, repo, "conn-ds", "deepseek", 1, fbSrv.URL)

	fbModels, _ := json.Marshal([]string{"deepseek/deepseek-chat"})
	if _, err := database.Exec(
		`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES ('fb-combo', 'fb-combo', 'fallback', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(fbModels),
	); err != nil {
		t.Fatalf("seed fb-combo: %v", err)
	}
	_ = repo.SetComboStrategy("primary-off", db.ComboStrategy{
		Strategy:       "fallback",
		FallbackCombos: []string{"fb-combo"},
	})

	h := NewChatHandler(repo)

	// All primary models disabled + image modality requested -> does not panic on empty slice and reaches fallbackCombo.
	rec := httptest.NewRecorder()
	imgBody := []byte(`{"model":"primary-off","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}`)
	h.handleComboFallback(context.Background(), rec, imgBody, []string{"openai/gpt-4o"}, "fallback", false, false, "primary-off", 0)
	if rec.Code != http.StatusOK || fbHits.Load() != 1 {
		t.Fatalf("fallback from all-disabled primary: status=%d fbHits=%d body=%s", rec.Code, fbHits.Load(), rec.Body.String())
	}

	// No fallback combo & all models disabled -> 503 Service Unavailable.
	rec = httptest.NewRecorder()
	h.handleComboFallback(context.Background(), rec, imgBody, []string{"openai/gpt-4o"}, "fallback", false, false, "standalone-off", 0)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("all disabled without fallback status = %d, want 503: %s", rec.Code, rec.Body.String())
	}
}
