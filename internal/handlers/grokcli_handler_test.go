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

func TestForwardGrokCLIRequest_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-grok-client-identifier") != "grok-cli-go" {
			t.Errorf("expected x-grok-client-identifier header, got %q", r.Header.Get("x-grok-client-identifier"))
		}
		if r.Header.Get("x-grok-client-version") != "0.1.0" {
			t.Errorf("expected x-grok-client-version header, got %q", r.Header.Get("x-grok-client-version"))
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("expected Authorization: Bearer test-key, got %q", r.Header.Get("Authorization"))
		}

		var reqBody map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("parse body: %v", err)
		}
		if reqBody["stream"] != true {
			t.Errorf("expected stream=true")
		}
		if reqBody["store"] != false {
			t.Errorf("expected store=false")
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(
			"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"grok response\"}\n\n" +
				"event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n" +
				"data: [DONE]\n\n",
		))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	cfg := &providers.ProviderConfig{
		BaseURL: srv.URL,
	}
	body := []byte(`{"model":"grok-build","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	err := h.forwardGrokCLIRequest(rec, cfg, "test-key", body, true, false, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(rec.Body.String(), "grok response") {
		t.Errorf("expected response content, got %s", rec.Body.String())
	}
}

func TestForwardGrokCLIRequest_UpstreamError(t *testing.T) {
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
	err := h.forwardGrokCLIRequest(rec, cfg, "bad-key", body, true, false, nil)
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
