package chat

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"9router/proxy/internal/semanticcache"
	"9router/proxy/internal/translator"
)

func TestChatHandler_SemanticCacheHit(t *testing.T) {
	cache := semanticcache.New(semanticcache.Config{
		Enabled:    true,
		TTL:        time.Hour,
		MaxEntries: 100,
	}, nil)

	handler := &ChatHandler{
		SemanticCache: cache,
	}

	reqBody := `{"model":"gpt-4o","messages":[{"role":"user","content":"Ping 123"}],"stream":false}`

	// Populate cache directly
	openAIReq := &translator.OpenAIRequest{
		Model: "gpt-4o",
		Messages: []translator.OpenAIMessage{
			{Role: "user", Content: "Ping 123"},
		},
	}
	cachedResp := []byte(`{"id":"chatcmpl-cached","choices":[{"message":{"role":"assistant","content":"Pong 123"}}]}`)
	_ = cache.Store(t.Context(), openAIReq, cachedResp, "application/json")

	// Now send request to HandleChatCompletions
	rec := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(reqBody))
	httpReq.Header.Set("Content-Type", "application/json")

	handler.HandleChatCompletions(rec, httpReq)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if rec.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("expected X-Cache: HIT, got %s", rec.Header().Get("X-Cache"))
	}
	if rec.Header().Get("X-Semantic-Similarity") == "" {
		t.Fatalf("expected X-Semantic-Similarity header to be present")
	}
	if rec.Body.String() != string(cachedResp) {
		t.Fatalf("expected response body %s, got %s", cachedResp, rec.Body.String())
	}
}

func TestChatHandler_SemanticCacheDisabledByDefault(t *testing.T) {
	// Cache initialized with Enabled: false
	cache := semanticcache.New(semanticcache.Config{
		Enabled:    false,
		TTL:        time.Hour,
		MaxEntries: 100,
	}, nil)

	handler := &ChatHandler{
		SemanticCache: cache,
	}

	reqBody := `{"model":"gpt-4o","messages":[{"role":"user","content":"Ping 123"}],"stream":false}`

	rec := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(reqBody))
	httpReq.Header.Set("Content-Type", "application/json")

	handler.HandleChatCompletions(rec, httpReq)

	// Since cache is disabled and no upstream is configured, it should fail model resolution / 400 Bad Request, NOT hit cache
	if rec.Header().Get("X-Cache") == "HIT" {
		t.Fatal("cache hit occurred while cache was disabled")
	}
}

func TestChatHandler_CaptureWriterStoresCache(t *testing.T) {
	cache := semanticcache.New(semanticcache.Config{
		Enabled:    true,
		TTL:        time.Hour,
		MaxEntries: 100,
	}, nil)

	req := &translator.OpenAIRequest{
		Model: "gpt-4o",
		Messages: []translator.OpenAIMessage{
			{Role: "user", Content: "Store me"},
		},
	}

	rec := httptest.NewRecorder()
	capture := &cacheCaptureWriter{ResponseWriter: rec}
	capture.WriteHeader(http.StatusOK)
	_, _ = capture.Write([]byte(`{"result":"ok"}`))

	if capture.statusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", capture.statusCode)
	}
	if capture.body.String() != `{"result":"ok"}` {
		t.Fatalf("expected recorded body, got %s", capture.body.String())
	}

	err := cache.Store(t.Context(), req, capture.body.Bytes(), "application/json")
	if err != nil {
		t.Fatalf("failed to store: %v", err)
	}

	entry, score, hit := cache.Lookup(t.Context(), req)
	if !hit || score != 1.0 {
		t.Fatalf("expected hit, got %v (score %f)", hit, score)
	}
	if string(entry.ResponseBody) != `{"result":"ok"}` {
		t.Fatalf("expected cached body, got %s", string(entry.ResponseBody))
	}
}
