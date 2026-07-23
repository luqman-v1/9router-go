package chat

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"9router/proxy/internal/db"
)

// TestE2E_Streaming_MetricsAndUsage verifies end-to-end SSE streaming request flow:
// 1. Client sends POST request
// 2. Upstream streams SSE chunks with realistic delay
// 3. Client receives full stream
// 4. DB persists accurate PromptTokens, CompletionTokens, TTFT (>0ms), and Total Latency.
func TestE2E_Streaming_MetricsAndUsage(t *testing.T) {
	// 1. Create mock upstream SSE server with artificial TTFT delay
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
			return
		}

		// Simulate TTFT delay (30ms before first chunk)
		time.Sleep(30 * time.Millisecond)
		w.Write([]byte("data: {\"id\":\"chatcmpl-stream-e2e\",\"choices\":[{\"delta\":{\"content\":\"Hello \"}}]}\n\n"))
		flusher.Flush()

		time.Sleep(10 * time.Millisecond)
		w.Write([]byte("data: {\"id\":\"chatcmpl-stream-e2e\",\"choices\":[{\"delta\":{\"content\":\"world!\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":25,\"completion_tokens\":12,\"prompt_tokens_details\":{\"cached_tokens\":5}}}\n\n"))
		flusher.Flush()

		time.Sleep(5 * time.Millisecond)
		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer upstream.Close()

	// 2. Setup SQLite DB and ChatHandler
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	seedConnDB(t, database, "bn", "conn-e2e-stream", "sk-e2e-secret", upstream.URL)
	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	// 3. Construct HTTP request
	reqBody := `{"model":"bn/claude-sonnet-4.5","messages":[{"role":"user","content":"Hello world, please generate a response"}],"stream":true}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader([]byte(reqBody)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	// 4. Execute request
	handler.HandleChatCompletions(rec, req)

	// 5. Assert HTTP Response
	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}
	respBody := rec.Body.String()
	if !bytes.Contains([]byte(respBody), []byte("Hello ")) || !bytes.Contains([]byte(respBody), []byte("world!")) {
		t.Errorf("expected SSE body to contain streamed chunks, got: %s", respBody)
	}

	// 6. Assert DB Usage History (End-to-End Metrics)
	var promptTokens, completionTokens int
	var tokensJSON, metaJSON string
	err := database.QueryRow(`
		SELECT promptTokens, completionTokens, tokens, meta 
		FROM usageHistory 
		WHERE provider = 'bn' AND connectionId = 'conn-e2e-stream'
		ORDER BY rowid DESC LIMIT 1
	`).Scan(&promptTokens, &completionTokens, &tokensJSON, &metaJSON)
	if err != nil {
		t.Fatalf("failed to query usageHistory: %v", err)
	}

	if promptTokens <= 0 {
		t.Errorf("expected PromptTokens > 0, got %d", promptTokens)
	}
	if completionTokens <= 0 {
		t.Errorf("expected CompletionTokens > 0, got %d", completionTokens)
	}

	// 7. Assert Request Details (TTFT > 0ms and Latency)
	var reqDetailData string
	err = database.QueryRow(`
		SELECT data FROM requestDetails 
		WHERE provider = 'bn' AND connectionId = 'conn-e2e-stream'
		ORDER BY rowid DESC LIMIT 1
	`).Scan(&reqDetailData)
	if err != nil {
		t.Fatalf("failed to query requestDetails: %v", err)
	}

	var detailMap struct {
		Latency struct {
			TTFT  int64 `json:"ttft"`
			Total int64 `json:"total"`
		} `json:"latency"`
		Tokens struct {
			Prompt     int `json:"prompt_tokens"`
			Completion int `json:"completion_tokens"`
			Cached     int `json:"cached_tokens"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal([]byte(reqDetailData), &detailMap); err != nil {
		t.Fatalf("failed to unmarshal requestDetails data: %v", err)
	}

	ttftMs := detailMap.Latency.TTFT
	latencyMs := detailMap.Latency.Total
	cachedTokens := detailMap.Tokens.Cached

	if ttftMs <= 0 {
		t.Errorf("expected TTFT > 0ms, got %dms", ttftMs)
	}
	if latencyMs < ttftMs {
		t.Errorf("expected Total Latency (%dms) >= TTFT (%dms)", latencyMs, ttftMs)
	}
	if cachedTokens < 0 {
		t.Errorf("expected CachedTokens >= 0, got %d", cachedTokens)
	}
}

// TestE2E_NonStreaming_PromptTokensDetails verifies end-to-end non-streaming JSON flow:
// 1. Upstream returns JSON response with prompt_tokens_details.cached_tokens
// 2. Proxy parses usage correctly and saves cached_tokens to DB.
func TestE2E_NonStreaming_PromptTokensDetails(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"id": "chatcmpl-nonstream",
			"object": "chat.completion",
			"choices": [{"message": {"role": "assistant", "content": "Non-stream answer"}}],
			"usage": {
				"prompt_tokens": 100,
				"completion_tokens": 40,
				"total_tokens": 140,
				"prompt_tokens_details": {
					"cached_tokens": 30
				}
			}
		}`))
	}))
	defer upstream.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	seedConnDB(t, database, "openai", "conn-e2e-json", "sk-json-secret", upstream.URL)
	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	reqBody := `{"model":"openai/gpt-4o","messages":[{"role":"user","content":"Hi"}],"stream":false}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader([]byte(reqBody)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var promptTokens, completionTokens int
	err := database.QueryRow(`
		SELECT promptTokens, completionTokens 
		FROM usageHistory 
		WHERE provider = 'openai' AND connectionId = 'conn-e2e-json'
		ORDER BY rowid DESC LIMIT 1
	`).Scan(&promptTokens, &completionTokens)
	if err != nil {
		t.Fatalf("query usageHistory: %v", err)
	}

	if promptTokens != 100 || completionTokens != 40 {
		t.Errorf("expected prompt 100 / completion 40, got prompt %d / completion %d", promptTokens, completionTokens)
	}

	var detailData string
	err = database.QueryRow(`
		SELECT data FROM requestDetails 
		WHERE provider = 'openai' AND connectionId = 'conn-e2e-json'
		ORDER BY rowid DESC LIMIT 1
	`).Scan(&detailData)
	if err != nil {
		t.Fatalf("query requestDetails: %v", err)
	}

	var detailMap struct {
		Tokens struct {
			Cached int `json:"cached_tokens"`
		} `json:"tokens"`
	}
	json.Unmarshal([]byte(detailData), &detailMap)
	if detailMap.Tokens.Cached != 30 {
		t.Errorf("expected cached_tokens = 30, got %d", detailMap.Tokens.Cached)
	}
}
