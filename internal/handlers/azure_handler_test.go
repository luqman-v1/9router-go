package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
)

func TestForwardAzureRequest_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify Azure-specific headers
		if r.Header.Get("api-key") != "sk-azure" {
			t.Errorf("expected api-key header, got %q", r.Header.Get("api-key"))
		}
		// Verify URL contains expected Azure params
		if !strings.Contains(r.URL.String(), "deployments/gpt-4/chat/completions") {
			t.Errorf("expected deployments path, got %s", r.URL.String())
		}
		if !strings.Contains(r.URL.String(), "api-version=2024-10-01-preview") {
			t.Errorf("expected api-version, got %s", r.URL.String())
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"test","choices":[{"message":{"content":"azure ok"}}],"usage":{}}`))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	cfg := &providers.ProviderConfig{}
	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()

	// Use override env to test Azure URL construction
	t.Setenv("AZURE_ENDPOINT", srv.URL)
	t.Setenv("AZURE_DEPLOYMENT", "gpt-4")
	t.Setenv("AZURE_API_VERSION", "2024-10-01-preview")

	err := h.forwardAzureRequest(rec, cfg, "sk-azure", body, false, false, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(rec.Body.String(), "azure ok") {
		t.Errorf("expected content in response, got %s", rec.Body.String())
	}
}

func TestForwardAzureRequest_Stream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("expected SSE accept header")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	cfg := &providers.ProviderConfig{}
	body := []byte(`{"model":"gpt-4","messages":[]}`)
	rec := httptest.NewRecorder()

	t.Setenv("AZURE_ENDPOINT", srv.URL)

	err := h.forwardAzureRequest(rec, cfg, "sk-azure", body, true, false, &streamMetrics{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(rec.Body.String(), "hi") {
		t.Errorf("expected stream content, got %s", rec.Body.String())
	}
}

func TestForwardAzureRequest_UpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	cfg := &providers.ProviderConfig{}
	body := []byte(`{"model":"x","messages":[]}`)
	rec := httptest.NewRecorder()

	t.Setenv("AZURE_ENDPOINT", srv.URL)

	err := h.forwardAzureRequest(rec, cfg, "bad-key", body, false, false, nil)
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

func TestForwardAzureRequest_DefaultEndpoint(t *testing.T) {
	// With no AZURE_ENDPOINT set, should use default https://api.openai.com
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()

	cfg := &providers.ProviderConfig{}
	body := []byte(`{"model":"gpt-4","messages":[]}`)
	rec := httptest.NewRecorder()

	// Clear env
	t.Setenv("AZURE_ENDPOINT", "")

	// This should fail with connection refused (not DNS error) since api.openai.com is not local
	err := h.forwardAzureRequest(rec, cfg, "sk-key", body, false, false, nil)
	if err == nil {
		t.Fatal("expected error when default endpoint unreachable")
	}
}
