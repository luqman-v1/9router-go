package handlers

import (
	"bytes"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleKiro_RemoveSystemPrompt(t *testing.T) {
	// Use proxyBase override to capture forwarded request
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(r.Body)
		capturedBody = buf.Bytes()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()
	oldBase := proxyBase
	proxyBase = srv.URL
	defer func() { proxyBase = oldBase }()

	body := []byte(`{"model":"test-model","systemPrompt":"should be removed","messages":[{"role":"user","content":"hi"}]}`)
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(string(body)))
	w := httptest.NewRecorder()

	HandleKiro(w, req, body)

	if len(capturedBody) == 0 {
		t.Fatal("expected captured body")
	}
	var parsed map[string]any
	if err := json.Unmarshal(capturedBody, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, has := parsed["systemPrompt"]; has {
		t.Error("systemPrompt should be removed")
	}
	if parsed["model"] != "kiro/test-model" {
		t.Errorf("expected kiro/test-model, got %v", parsed["model"])
	}
}

func TestHandleKiro_InlineImages(t *testing.T) {
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(r.Body)
		capturedBody = buf.Bytes()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()
	oldBase := proxyBase
	proxyBase = srv.URL
	defer func() { proxyBase = oldBase }()

	// Body with userInputMessage containing images
	body := []byte(`{"model":"test-model","userInputMessage":{"content":"hello","images":[{"format":"png","source":{"bytes":"iVBORw0KGgoAAAANSUhEUgAAAAEAAAAB"}}]}}`)
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(string(body)))
	w := httptest.NewRecorder()

	HandleKiro(w, req, body)

	if len(capturedBody) == 0 {
		t.Fatal("expected captured body")
	}
	var parsed map[string]any
	_ = json.Unmarshal(capturedBody, &parsed)

	// Check that messages contains image_url
	msgs, ok := parsed["messages"].([]any)
	if !ok || len(msgs) == 0 {
		t.Fatalf("expected messages with image, got %v", parsed["messages"])
	}
	first, _ := msgs[0].(map[string]any)
	content, _ := first["content"].([]any)
	foundImage := false
	for _, c := range content {
		if m, ok := c.(map[string]any); ok && m["type"] == "image_url" {
			if imgURL, ok := m["image_url"].(map[string]any); ok {
				if url, ok := imgURL["url"].(string); ok && strings.HasPrefix(url, "data:image/png;base64,") {
					foundImage = true
				}
			}
		}
	}
	if !foundImage {
		t.Errorf("expected image_url with base64, got %v", content)
	}
	// Original images should be removed from userInputMessage
	if uim, ok := parsed["userInputMessage"].(map[string]any); ok {
		if _, has := uim["images"]; has {
			t.Error("images should be removed from userInputMessage after conversion")
		}
	}
}

func TestHandleAntigravity_CatalogPreserve(t *testing.T) {
	var capturedBody []byte
	var capturedUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(r.Body)
		capturedBody = buf.Bytes()
		capturedUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()
	oldBase := proxyBase
	proxyBase = srv.URL
	defer func() { proxyBase = oldBase }()

	// Catalog request: fetchAvailableModels should preserve version
	body := []byte(`{"model":"test","metadata":{"ideVersion":"2.11.0"}}`)
	req := httptest.NewRequest("POST", "/v1internal:fetchAvailableModels", strings.NewReader(string(body)))
	req.Header.Set("User-Agent", "antigravity/2.11.0")
	w := httptest.NewRecorder()

	HandleAntigravity(w, req, body)

	if len(capturedBody) == 0 {
		t.Fatal("expected captured body")
	}
	var parsed map[string]any
	_ = json.Unmarshal(capturedBody, &parsed)
	// For catalog, should NOT override to 1.23.2
	if meta, ok := parsed["metadata"].(map[string]any); ok {
		if meta["ideVersion"] == "1.23.2" {
			t.Error("catalog request should preserve 2.11.0, not override to 1.23.2")
		}
	}
	if capturedUA == "antigravity/1.23.2" {
		t.Error("catalog should preserve User-Agent, not override")
	}

	// Generation request should override
	capturedBody = nil
	body2 := []byte(`{"model":"gemini-3.8-flash","metadata":{"ideVersion":"2.11.0"}}`)
	req2 := httptest.NewRequest("POST", "/v1internal:streamGenerateContent?alt=sse", strings.NewReader(string(body2)))
	req2.Header.Set("User-Agent", "antigravity/2.11.0")
	w2 := httptest.NewRecorder()

	HandleAntigravity(w2, req2, body2)

	if len(capturedBody) == 0 {
		t.Fatal("expected captured body for generation")
	}
	_ = json.Unmarshal(capturedBody, &parsed)
	if meta, ok := parsed["metadata"].(map[string]any); ok {
		if meta["ideVersion"] != "1.23.2" {
			t.Errorf("generation should override to 1.23.2, got %v", meta["ideVersion"])
		}
	}
	if capturedUA != "antigravity/1.23.2" {
		t.Errorf("generation should override User-Agent to 1.23.2, got %q", capturedUA)
	}
}
