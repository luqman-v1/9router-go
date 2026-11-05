package media

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers/chat"
)

func TestIsCloudflareFlux2Model(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    string
		want     bool
	}{
		{"flux-2-dev cf", "cf", "@cf/black-forest-labs/flux-2-dev", true},
		{"flux-2-dev cloudflare-ai", "cloudflare-ai", "@cf/black-forest-labs/flux-2-dev", true},
		{"flux-1-schnell NOT flux-2", "cloudflare-ai", "@cf/black-forest-labs/flux-1-schnell", false},
		{"other provider flux-2", "openai", "@cf/black-forest-labs/flux-2-dev", false},
		{"empty model", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isCloudflareFlux2Model(&chat.ModelInfo{Provider: tc.provider, Model: tc.model})
			if got != tc.want {
				t.Errorf("isCloudflareFlux2Model(%q, %q) = %v, want %v", tc.provider, tc.model, got, tc.want)
			}
		})
	}
}

func TestFlux2ReferenceImage(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{"image present", `{"image":"data:image/png;base64,abc"}`, true},
		{"images[0] present", `{"images":["data:image/png;base64,abc"]}`, true},
		{"empty image", `{"image":""}`, false},
		{"no image", `{"prompt":"hi"}`, false},
		{"empty images[0]", `{"images":[""]}`, false},
		{"invalid json", `{not json}`, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := flux2ReferenceImage([]byte(tc.body)); got != tc.want {
				t.Errorf("flux2ReferenceImage(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

func TestForwardCloudflareFlux2ImageReferenceDownload(t *testing.T) {
	var fetchedURL string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetchedURL = r.URL.String()
		w.Header().Set("Content-Type", "image/png")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("PNG-DATA"))
	}))
	defer upstream.Close()

	var forwardBody map[string]any
	var forwardAuth string
	fwd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &forwardBody)
		forwardAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"created":1,"data":[{"url":"https://example.com/flux.png"}]}`))
	}))
	defer fwd.Close()

	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	data, _ := json.Marshal(map[string]any{"apiKey": "cf-token", "baseUrl": fwd.URL})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-cf', 'cloudflare-ai', 'apikey', 'Cloudflare AI Test', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(data)); err != nil {
		t.Fatalf("seed cloudflare connection: %v", err)
	}

	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	body := `{"model":"cf/@cf/black-forest-labs/flux-2-dev","prompt":"a red fox","image":"` + upstream.URL + `/ref.png"}`
	req := httptest.NewRequest("POST", "/v1/images/generations", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleImages(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}
	if fetchedURL == "" {
		t.Errorf("expected reference image to be downloaded")
	}
	if forwardAuth != "Bearer cf-token" {
		t.Errorf("expected Authorization Bearer cf-token, got %q", forwardAuth)
	}
	if forwardBody["prompt"] != "a red fox" {
		t.Errorf("expected prompt preserved, got %v", forwardBody["prompt"])
	}
	if forwardBody["image"] == "" {
		t.Errorf("expected resolved base64 image in payload")
	}
}

func TestHandleImages_Flux2NoReferenceNotSpecialCased(t *testing.T) {
	// flux-2 without a reference image should fall through to normal handling
	// (not the reference-image lane), so dimensions are preserved like the
	// existing flux-2-dev behavior.
	var captured map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"created":1,"data":[{"url":"https://example.com/flux.png"}]}`))
	}))
	defer upstream.Close()

	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	data, _ := json.Marshal(map[string]any{"apiKey": "cf-token", "baseUrl": upstream.URL})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-cf', 'cloudflare-ai', 'apikey', 'Cloudflare AI Test', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(data)); err != nil {
		t.Fatalf("seed cloudflare connection: %v", err)
	}

	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	body := `{"model":"cf/@cf/black-forest-labs/flux-2-dev","prompt":"a red fox","size":"1024x1024","width":1024,"height":1024}`
	req := httptest.NewRequest("POST", "/v1/images/generations", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleImages(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, hasWidth := captured["width"]; !hasWidth {
		t.Errorf("expected width preserved for flux-2-dev without reference image")
	}
}
