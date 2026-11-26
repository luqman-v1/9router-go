package media

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/handlers/chat"
)

// The image kind listing must emit ids the generation endpoint can resolve.
// A bare provider alias is not one: POST /v1/images/generations answers 400
// "could not resolve model: xai" for it, so discovery output could not be fed
// back into the endpoint it advertises. Every emitted id must therefore carry
// the "{provider}/{model}" shape, and resolve through ResolveModel.
func TestHandleModelsByKind_ImageEmitsResolvableIDs(t *testing.T) {
	handler := chat.NewChatHandler(nil)
	req := httptest.NewRequest("GET", "/v1/models/image", nil)
	req.SetPathValue("kind", "image")
	rec := httptest.NewRecorder()

	handler.HandleModelsByKind(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data []struct {
			ID         string `json:"id"`
			Kind       string `json:"kind"`
			Endpoint   string `json:"endpoint"`
			Capability string `json:"capability"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(resp.Data) == 0 {
		t.Fatal("expected at least one image model")
	}

	checked := 0
	for _, entry := range resp.Data {
		if !strings.Contains(entry.ID, "/") {
			t.Errorf("image listing emitted %q, which has no {provider}/{model} "+
				"shape and the generation endpoint cannot resolve it", entry.ID)
			continue
		}
		if entry.Kind != "image" {
			t.Errorf("%q: kind = %q, want image", entry.ID, entry.Kind)
		}
		if entry.Endpoint != "/v1/images/generations" {
			t.Errorf("%q: endpoint = %q, want /v1/images/generations", entry.ID, entry.Endpoint)
		}
		// The generation endpoint resolves with ResolveModel before dispatching,
		// so an id that does not resolve is unusable however it is listed.
		if _, err := handler.ResolveModel(entry.ID); err != nil {
			t.Errorf("%q is listed but does not resolve: %v", entry.ID, err)
			continue
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no emitted id resolved; the listing advertises unusable ids")
	}
}

// Non-image kinds keep their provider-level shape: a tts entry is addressed by
// provider, since /v1/audio/speech takes the model in the body and the provider
// from the connection. Only image generation needs "{provider}/{model}".
func TestHandleModelsByKind_TTSKeepsProviderIDs(t *testing.T) {
	handler := chat.NewChatHandler(nil)
	req := httptest.NewRequest("GET", "/v1/models/tts", nil)
	req.SetPathValue("kind", "tts")
	rec := httptest.NewRecorder()

	handler.HandleModelsByKind(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data []struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(resp.Data) == 0 {
		t.Fatal("expected at least one tts provider")
	}
	for _, entry := range resp.Data {
		if strings.Contains(entry.ID, "/") {
			t.Errorf("tts entry %q unexpectedly carries a model segment; the "+
				"provider-level shape is what /v1/audio/speech expects", entry.ID)
		}
	}
}
