package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy/executor"
)

func TestForwardAzureRequest_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("api-key") != "sk-azure" {
			t.Errorf("expected api-key header, got %q", r.Header.Get("api-key"))
		}
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

	cfg := &providers.ProviderConfig{}
	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()

	t.Setenv("AZURE_ENDPOINT", srv.URL)
	t.Setenv("AZURE_DEPLOYMENT", "gpt-4")
	t.Setenv("AZURE_API_VERSION", "2024-10-01-preview")

	err := executor.ForwardAzure(rec, &executor.Request{
		Client:   srv.Client(),
		Config:   cfg,
		APIKey:   "sk-azure",
		Body:     body,
		IsStream: false,
	})
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

	cfg := &providers.ProviderConfig{}
	body := []byte(`{"model":"gpt-4","messages":[]}`)
	rec := httptest.NewRecorder()

	t.Setenv("AZURE_ENDPOINT", srv.URL)

	err := executor.ForwardAzure(rec, &executor.Request{
		Client:   srv.Client(),
		Config:   cfg,
		APIKey:   "sk-azure",
		Body:     body,
		IsStream: true,
	})
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

	cfg := &providers.ProviderConfig{}
	body := []byte(`{"model":"x","messages":[]}`)
	rec := httptest.NewRecorder()

	t.Setenv("AZURE_ENDPOINT", srv.URL)

	err := executor.ForwardAzure(rec, &executor.Request{
		Client:   srv.Client(),
		Config:   cfg,
		APIKey:   "bad-key",
		Body:     body,
		IsStream: false,
	})
	if err == nil {
		t.Fatal("expected error for 401")
	}
	var ue *upstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *upstreamError, got %T", err)
	}
	if ue.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", ue.StatusCode)
	}
}
