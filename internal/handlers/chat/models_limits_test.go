package chat

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/db"
)

func TestHandleModels_IncludesTokenLimits(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	if _, err := database.Exec(`INSERT INTO kv (scope, key, value) VALUES ('modelAliases', 'claude-sonnet-4-6', '"anthropic/claude-sonnet-4-6"')`); err != nil {
		t.Fatalf("insert model alias: %v", err)
	}

	h := NewChatHandler(repo)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/models", nil)

	h.HandleModels(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp struct {
		Object string `json:"object"`
		Data   []struct {
			ID                  string `json:"id"`
			ContextLength       *int   `json:"context_length"`
			MaxCompletionTokens *int   `json:"max_completion_tokens"`
		} `json:"data"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if len(resp.Data) == 0 {
		t.Fatal("expected at least 1 model in /v1/models")
	}

	found := false
	for _, m := range resp.Data {
		if m.ID == "claude-sonnet-4-6" {
			found = true
			if m.ContextLength == nil || *m.ContextLength <= 0 {
				t.Errorf("expected positive context_length for claude-sonnet-4-6, got %v", m.ContextLength)
			}
		}
	}
	if !found {
		t.Error("claude-sonnet-4-6 not found in /v1/models response")
	}
}
