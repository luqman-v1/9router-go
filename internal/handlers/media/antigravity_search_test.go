package media

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
)

func TestHandleSearch_Antigravity(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v1internal:generateContent") {
			t.Errorf("expected path /v1internal:generateContent, got %s", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer ag-token-123" {
			t.Errorf("expected Bearer ag-token-123, got %q", auth)
		}

		var reqBody struct {
			Project     string `json:"project"`
			Model       string `json:"model"`
			RequestType string `json:"requestType"`
			Request     struct {
				Tools []map[string]any `json:"tools"`
			} `json:"request"`
		}
		if err := json.UnmarshalRead(r.Body, &reqBody); err != nil {
			t.Errorf("decode request body: %v", err)
		}

		if reqBody.Project != "test-project-id" {
			t.Errorf("expected project test-project-id, got %s", reqBody.Project)
		}
		if reqBody.RequestType != "search" {
			t.Errorf("expected requestType search, got %s", reqBody.RequestType)
		}
		if len(reqBody.Request.Tools) == 0 || reqBody.Request.Tools[0]["googleSearch"] == nil {
			t.Errorf("expected googleSearch tool, got %v", reqBody.Request.Tools)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"response": {
				"candidates": [{
					"content": {
						"parts": [{"text": "AI models are evolving rapidly in 2026."}]
					},
					"groundingMetadata": {
						"groundingChunks": [
							{"web": {"uri": "https://example.com/ai-news", "title": "AI News 2026"}}
						],
						"groundingSupports": [{
							"segment": {"text": "AI models are evolving rapidly in 2026.", "startIndex": 0, "endIndex": 39},
							"groundingChunkIndices": [0]
						}]
					}
				}]
			}
		}`))
	}))
	defer upstream.Close()

	ag := providers.KnownProviders["antigravity"]
	origBase := ag.BaseURL
	ag.BaseURL = upstream.URL
	providers.KnownProviders["antigravity"] = ag
	defer func() {
		ag.BaseURL = origBase
		providers.KnownProviders["antigravity"] = ag
	}()

	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()

	connData, _ := json.Marshal(map[string]any{
		"apiKey":    "ag-token-123",
		"projectId": "test-project-id",
	})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-ag', 'antigravity', 'oauth', 'Antigravity Test', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(connData)); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	body := `{"model":"antigravity/search","query":"latest AI news"}`
	req := httptest.NewRequest("POST", "/v1/search", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleSearch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Provider string `json:"provider"`
		Results  []struct {
			Title    string `json:"title"`
			URL      string `json:"url"`
			Snippet  string `json:"snippet"`
			Position int    `json:"position"`
			Citation struct {
				Provider    string `json:"provider"`
				RetrievedAt string `json:"retrieved_at"`
				Rank        int    `json:"rank"`
			} `json:"citation"`
		} `json:"results"`
		Answer struct {
			Source string `json:"source"`
			Text   string `json:"text"`
			Model  string `json:"model"`
		} `json:"answer"`
		Query string `json:"query"`
		Usage struct {
			QueriesUsed int `json:"queries_used"`
			LLMTokens   int `json:"llm_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}

	if len(resp.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(resp.Results))
	}
	if resp.Results[0].URL != "https://example.com/ai-news" {
		t.Errorf("expected URL https://example.com/ai-news, got %s", resp.Results[0].URL)
	}
	if resp.Results[0].Position != 1 {
		t.Errorf("expected position 1, got %d", resp.Results[0].Position)
	}
	if resp.Results[0].Citation.Provider != "antigravity" {
		t.Errorf("expected citation provider antigravity, got %s", resp.Results[0].Citation.Provider)
	}
	if resp.Results[0].Citation.RetrievedAt == "" {
		t.Errorf("expected non-empty retrieved_at")
	}
	if !strings.Contains(resp.Answer.Text, "evolving rapidly") {
		t.Errorf("expected answer text, got %s", resp.Answer.Text)
	}
	if resp.Answer.Source != "antigravity" {
		t.Errorf("expected answer source antigravity, got %s", resp.Answer.Source)
	}
}
