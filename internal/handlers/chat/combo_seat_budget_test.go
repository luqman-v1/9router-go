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

func TestComboSeatAttemptBudgetAdvancesToNextMember(t *testing.T) {
	var stalledCalls atomic.Int32
	stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stalledCalls.Add(1)
		time.Sleep(300 * time.Millisecond)
	}))
	defer stalled.Close()

	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"healthy fallback response"}}]}`))
	}))
	defer healthy.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	if _, err := database.Exec(`DELETE FROM providerConnections WHERE id IN ('conn-ds', 'conn-gq')`); err != nil {
		t.Fatal(err)
	}

	dsData, _ := json.Marshal(map[string]any{"apiKey": "sk-test", "baseUrl": stalled.URL})
	gqData, _ := json.Marshal(map[string]any{"apiKey": "sk-test", "baseUrl": healthy.URL})

	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-ds', 'deepseek', 'apikey', 'Stalled', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z'),
		('conn-gq', 'groq', 'apikey', 'Healthy', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(dsData), string(gqData)); err != nil {
		t.Fatalf("insert providerConnections: %v", err)
	}

	comboModels, _ := json.Marshal([]string{"deepseek/deepseek-chat", "groq/qwen3-32b"})
	if _, err := database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES
		('c-budget', 'budget-combo', 'llm', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(comboModels)); err != nil {
		t.Fatalf("seed combo: %v", err)
	}

	repo := db.NewRepo(database)
	if err := repo.UpdateSettingsRaw(map[string]any{"seatAttemptBudgetMs": 40}); err != nil {
		t.Fatal(err)
	}

	h := NewChatHandler(repo)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"budget-combo","messages":[{"role":"user","content":"hello"}]}`))
	h.HandleChatCompletions(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "healthy fallback response") {
		t.Fatalf("body = %s, want healthy fallback response", w.Body.String())
	}
}
