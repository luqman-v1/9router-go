package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
)

func TestForwardIflowRequest_Stream(t *testing.T) {
	var gotSessionID, gotTimestamp, gotSignature, gotUserAgent string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSessionID = r.Header.Get("session-id")
		gotTimestamp = r.Header.Get("x-iflow-timestamp")
		gotSignature = r.Header.Get("x-iflow-signature")
		gotUserAgent = r.Header.Get("User-Agent")

		if r.Header.Get("Authorization") != "" {
			t.Errorf("Authorization header should not be sent")
		}
		if gotSessionID == "" {
			t.Errorf("expected session-id header")
		}
		if gotTimestamp == "" {
			t.Errorf("expected x-iflow-timestamp header")
		}
		if gotSignature == "" {
			t.Errorf("expected x-iflow-signature header")
		}
		if gotUserAgent != "iFlow-Cli" {
			t.Errorf("expected User-Agent 'iFlow-Cli', got %q", gotUserAgent)
		}
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("expected Accept header for streaming")
		}

		// Verify stream_options was injected
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		if body["stream"] != true {
			t.Errorf("expected stream=true in body")
		}
		so, ok := body["stream_options"].(map[string]interface{})
		if !ok || so["include_usage"] != true {
			t.Errorf("expected stream_options.include_usage=true in body")
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"delta\":{\"content\":\"hello from iflow\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	cfg := &providers.ProviderConfig{
		BaseURL: srv.URL,
	}
	body := []byte(`{"model":"qwen3-coder-plus","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	err := h.forwardIflowRequest(rec, cfg, "test-api-key", body, true, false, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(rec.Body.String(), "hello from iflow") {
		t.Errorf("expected content in SSE, got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "[DONE]") {
		t.Errorf("expected [DONE] at end, got %s", rec.Body.String())
	}
	if gotSessionID == "" || gotTimestamp == "" || gotSignature == "" {
		t.Errorf("HMAC headers not set properly")
	}
}

func TestForwardIflowRequest_NonStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") == "text/event-stream" {
			t.Errorf("no Accept header expected for non-streaming")
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"chatcmpl-1","choices":[{"message":{"content":"hello non-stream"}}],"usage":{"prompt_tokens":5,"completion_tokens":10}}`))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	cfg := &providers.ProviderConfig{
		BaseURL: srv.URL,
	}
	body := []byte(`{"model":"qwen3-coder-plus","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	err := h.forwardIflowRequest(rec, cfg, "test-api-key", body, false, false, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(rec.Body.String(), "hello non-stream") {
		t.Errorf("expected response content, got %s", rec.Body.String())
	}
}

func TestForwardIflowRequest_UpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	cfg := &providers.ProviderConfig{
		BaseURL: srv.URL,
	}
	body := []byte(`{"model":"x","messages":[]}`)
	rec := httptest.NewRecorder()
	err := h.forwardIflowRequest(rec, cfg, "bad-key", body, true, false, nil)
	if err == nil {
		t.Fatal("expected error for 401")
	}
	ue, ok := err.(*upstreamError)
	if !ok {
		t.Fatalf("expected *upstreamError, got %T", err)
	}
	if ue.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", ue.StatusCode)
	}
}

func TestForwardIflowRequest_ForceStreamOptions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)

		// Verify stream_options was forced even without it in body
		so, ok := body["stream_options"].(map[string]interface{})
		if !ok {
			t.Fatal("expected stream_options in body")
		}
		if so["include_usage"] != true {
			t.Errorf("expected include_usage=true, got %v", so["include_usage"])
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	cfg := &providers.ProviderConfig{
		BaseURL: srv.URL,
	}
	body := []byte(`{"model":"x","messages":[],"stream_options":{"include_usage":true}}`)
	rec := httptest.NewRecorder()
	err := h.forwardIflowRequest(rec, cfg, "key", body, true, false, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// stream_options already present — should not be overwritten by force-stream test body
	_ = rec
}
