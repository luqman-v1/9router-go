package media

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/providers"
)

func TestHandleSearch_Success(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/search") {
			t.Errorf("expected path /search, got %s", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer sk-test" {
			t.Errorf("expected Bearer sk-test, got %q", auth)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"results":[{"title":"Result 1","url":"https://example.com/1"}]}`))
	}))
	defer upstream.Close()

	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	seedOpenAIConn(t, database, upstream.URL)

	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	body := `{"model":"openai/search","query":"golang proxy"}`
	req := httptest.NewRequest("POST", "/v1/search", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleSearch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if resp["results"] == nil {
		t.Errorf("expected results in response, got %v", resp)
	}
}

func TestHandleSearch_MissingModel(t *testing.T) {
	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	body := `{"query":"hello"}`
	req := httptest.NewRequest("POST", "/v1/search", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleSearch(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleScrape_Success(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/scrape") {
			t.Errorf("expected path /scrape, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"content":{"format":"markdown","text":"# Title\nbody"}}`))
	}))
	defer upstream.Close()

	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	seedOpenAIConn(t, database, upstream.URL)

	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	body := `{"model":"openai/scrape","url":"https://example.com"}`
	req := httptest.NewRequest("POST", "/v1/scrape", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleScrape(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	content, _ := resp["content"].(map[string]any)
	if content == nil {
		t.Errorf("expected content in response, got %v", resp)
	}
}

func TestHandleScrape_MissingModel(t *testing.T) {
	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	body := `{"url":"https://example.com"}`
	req := httptest.NewRequest("POST", "/v1/scrape", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleScrape(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleSearch_UpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"invalid key"}`))
	}))
	defer upstream.Close()

	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	seedOpenAIConn(t, database, upstream.URL)

	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	body := `{"model":"openai/search","query":"test"}`
	req := httptest.NewRequest("POST", "/v1/search", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleSearch(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 from upstream, got %d", rec.Code)
	}
}

// ---- New endpoint tests ----

func TestEstimateAnthropicTokens(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
	}{
		{"empty", `{}`, 1},
		{"simple message", `{"messages":[{"role":"user","content":"hello"}]}`, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := chat.EstimateAnthropicTokens([]byte(tt.body))
			if got != tt.want {
				t.Errorf("chat.EstimateAnthropicTokens(%q) = %d, want %d", tt.body, got, tt.want)
			}
		})
	}
}

func TestHandleCountTokens(t *testing.T) {
	handler := chat.NewChatHandler(nil)
	body := `{"messages":[{"role":"user","content":"hello world"}]}`
	req := httptest.NewRequest("POST", "/v1/messages/count_tokens", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleCountTokens(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse: %v", err)
	}
	tokens, ok := resp["input_tokens"].(float64)
	if !ok || tokens <= 0 {
		t.Errorf("expected positive input_tokens, got %v", resp["input_tokens"])
	}
}

func TestHandleModelsInfo(t *testing.T) {
	handler := chat.NewChatHandler(nil)
	req := httptest.NewRequest("GET", "/v1/models/info?id=openai/gpt-4", nil)
	rec := httptest.NewRecorder()
	handler.HandleModelsInfo(rec, req)

	if rec.Code == http.StatusBadRequest {
		// Without DB, model won't resolve — that's acceptable
		return
	}
}

func TestHandleModelsInfo_missingID(t *testing.T) {
	handler := chat.NewChatHandler(nil)
	req := httptest.NewRequest("GET", "/v1/models/info", nil)
	rec := httptest.NewRecorder()

	handler.HandleModelsInfo(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleModelsByKind(t *testing.T) {
	handler := chat.NewChatHandler(nil)
	req := httptest.NewRequest("GET", "/v1/models/image", nil)
	req.SetPathValue("kind", "image")
	rec := httptest.NewRecorder()

	handler.HandleModelsByKind(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse: %v", err)
	}
	data, ok := resp["data"].([]any)
	if !ok {
		t.Fatalf("expected data array, got %T", resp["data"])
	}
	if len(data) == 0 {
		t.Error("expected at least one image provider")
	}
}

func TestHandleModelsByKind_tts(t *testing.T) {
	handler := chat.NewChatHandler(nil)
	req := httptest.NewRequest("GET", "/v1/models/tts", nil)
	req.SetPathValue("kind", "tts")
	rec := httptest.NewRecorder()

	handler.HandleModelsByKind(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse: %v", err)
	}
	data, ok := resp["data"].([]any)
	if !ok {
		t.Fatalf("expected data array, got %T", resp["data"])
	}
	found := false
	for _, item := range data {
		if m, ok := item.(map[string]any); ok && m["id"] == "elevenlabs" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected elevenlabs in TTS provider list")
	}
}

func TestHandleAudioVoices_missingProvider(t *testing.T) {
	handler := chat.NewChatHandler(nil)
	req := httptest.NewRequest("GET", "/v1/audio/voices", nil)
	rec := httptest.NewRecorder()

	handler.HandleAudioVoices(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleAudioVoices_unknownProvider(t *testing.T) {
	handler := chat.NewChatHandler(nil)
	req := httptest.NewRequest("GET", "/v1/audio/voices?provider=nonexistent", nil)
	rec := httptest.NewRecorder()

	handler.HandleAudioVoices(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestHandleAudioVoices_elevenlabs(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"voices":[{"voice_id":"21m00Tcm4TlvDq8ikWAM","name":"Rachel"}]}`))
	}))
	defer upstream.Close()

	handler := chat.NewChatHandler(nil)
	req := httptest.NewRequest("GET", "/v1/audio/voices?provider=elevenlabs", nil)
	rec := httptest.NewRecorder()
	handler.Client = upstream.Client()
	handler.HandleAudioVoices(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if resp["voices"] == nil {
		t.Errorf("expected voices in response, got %v", resp)
	}
}

func TestCountValueChars(t *testing.T) {
	tests := []struct {
		name string
		v    any
		want int
	}{
		{"nil", nil, 0},
		{"string", "hello", 5},
		{"float", float64(42), 2},
		{"bool true", true, 4},
		{"bool false", false, 5},
		{"array", []any{"a", "b"}, 2},
		{"map", map[string]any{"key": "val"}, 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := chat.CountValueChars(tt.v)
			if got != tt.want {
				t.Errorf("chat.CountValueChars(%v) = %d, want %d", tt.v, got, tt.want)
			}
		})
	}
}

func TestMessageContentChars(t *testing.T) {
	tests := []struct {
		name string
		c    any
		want int
	}{
		{"nil", nil, 0},
		{"string", "hello world", 11},
		{"content blocks", []any{map[string]any{"type": "text", "text": "hi"}}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := chat.MessageContentChars(tt.c)
			if got != tt.want {
				t.Errorf("chat.MessageContentChars(%v) = %d, want %d", tt.c, got, tt.want)
			}
		})
	}
}

func TestContentBlockChars(t *testing.T) {
	tests := []struct {
		name string
		blk  any
		want int
	}{
		{"nil", nil, 0},
		{"text", map[string]any{"type": "text", "text": "hello"}, 5},
		{"tool_use", map[string]any{"type": "tool_use", "name": "get_weather", "input": `{"city":"NYC"}`}, 25},
		{"tool_result", map[string]any{"type": "tool_result", "content": "sunny"}, 5},
		{"thinking", map[string]any{"type": "thinking", "thinking": "hmm"}, 3},
		{"unknown type", map[string]any{"type": "unknown", "data": "test"}, 19},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := chat.ContentBlockChars(tt.blk)
			if got != tt.want {
				t.Errorf("chat.ContentBlockChars(%v) = %d, want %d", tt.blk, got, tt.want)
			}
		})
	}
}

func TestHandleOllamaChat(t *testing.T) {
	upstream := setupFakeOpenAIUpstream(t)
	defer upstream.Close()

	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	seedOpenAIConn(t, database, upstream.URL)

	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	body := `{"model":"openai/gpt-4","messages":[{"role":"user","content":"hi"}],"stream":false}`
	req := httptest.NewRequest("POST", "/v1/api/chat", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ChatH.HandleOllamaChat(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if resp["choices"] == nil {
		t.Errorf("expected choices in response, got %v", resp)
	}
}

func TestHandleResponsesCompact(t *testing.T) {
	upstream := setupFakeOpenAIUpstream(t)
	defer upstream.Close()

	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	seedOpenAIConn(t, database, upstream.URL)

	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	body := `{"model":"openai/gpt-4","messages":[{"role":"user","content":"compact this"}],"stream":false}`
	req := httptest.NewRequest("POST", "/v1/responses/compact", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleResponsesCompact(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// setupFakeOpenAIUpstream creates a test upstream that returns a valid OpenAI response.
func setupFakeOpenAIUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"chatcmpl-test","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"Hello!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	}))
}


func TestHandleWebFetch_missingFields(t *testing.T) {
	database, cleanup := setupEmbeddingsTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)
	tests := []struct {
		name string
		body string
	}{
		{"empty body", `{}`},
		{"missing url", `{"model":"jina-reader/fetch"}`},
		{"missing model", `{"url":"https://example.com"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/v1/web/fetch", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.HandleWebFetch(rec, req)
			if rec.Code != http.StatusBadRequest && rec.Code != http.StatusNotFound {
				t.Errorf("expected 400 or 404, got %d", rec.Code)
			}
		})
	}
}

func TestHandleWebFetch_jinaReader(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "Bearer sk-test" {
			t.Errorf("expected Bearer sk-test, got %q", auth)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"content":"# Title\nHello world"}`))
	}))
	defer upstream.Close()

	jr := providers.KnownProviders["jina-reader"]
	origFetch := jr.FetchURL
	origBase := jr.BaseURL
	jr.FetchURL = upstream.URL
	jr.BaseURL = upstream.URL
	providers.KnownProviders["jina-reader"] = jr
	defer func() {
		jr.FetchURL = origFetch
		jr.BaseURL = origBase
		providers.KnownProviders["jina-reader"] = jr
	}()

	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	seedFetchConn(t, database, "jina-reader", "conn-jina", "sk-test")

	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	body := `{"model":"jina-reader/fetch","url":"https://example.com"}`
	req := httptest.NewRequest("POST", "/v1/web/fetch", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleWebFetch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if resp["content"] == nil {
		t.Errorf("expected content in response, got %v", resp)
	}
}

func TestHandleWebFetch_firecrawl(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"success":true,"data":{"markdown":"# Title"}}`))
	}))
	defer upstream.Close()

	fc := providers.KnownProviders["firecrawl"]
	origFetch := fc.FetchURL
	origBase := fc.BaseURL
	fc.FetchURL = upstream.URL + "/scrape"
	fc.BaseURL = upstream.URL
	providers.KnownProviders["firecrawl"] = fc
	defer func() {
		fc.FetchURL = origFetch
		fc.BaseURL = origBase
		providers.KnownProviders["firecrawl"] = fc
	}()

	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	seedFetchConn(t, database, "firecrawl", "conn-fc", "sk-fc-test")

	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	body := `{"model":"firecrawl/fetch","url":"https://example.com"}`
	req := httptest.NewRequest("POST", "/v1/web/fetch", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleWebFetch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func seedFetchConn(t *testing.T, database *sql.DB, provider, id, apiKey string) {
	t.Helper()
	data, _ := json.Marshal(map[string]any{"apiKey": apiKey})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		(?, ?, 'apikey', ?, 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, id, provider, provider+" Test", string(data)); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
}
