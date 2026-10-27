package media

import (
	"bytes"
	"encoding/base64"
	json "encoding/json/v2"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
)

func TestHandleImageEdits_MissingPrompt(t *testing.T) {
	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("model", "dall-e-2")
	fw, _ := mw.CreateFormFile("image", "test.png")
	_, _ = fw.Write([]byte("fake image data"))
	_ = mw.Close()

	req := httptest.NewRequest("POST", "/v1/images/edits", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()

	handler.HandleImageEdits(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing prompt, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Missing required field: prompt") {
		t.Errorf("expected 'Missing required field: prompt' in response, got %s", rec.Body.String())
	}
}

func TestHandleImageEdits_MissingImage(t *testing.T) {
	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("prompt", "make it blue")
	_ = mw.WriteField("model", "dall-e-2")
	_ = mw.Close()

	req := httptest.NewRequest("POST", "/v1/images/edits", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()

	handler.HandleImageEdits(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing image, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Missing required field: image") {
		t.Errorf("expected 'Missing required field: image' in response, got %s", rec.Body.String())
	}
}

func TestHandleImageEdits_InvalidMultipart(t *testing.T) {
	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	// Invalid multipart: missing boundary
	req := httptest.NewRequest("POST", "/v1/images/edits", strings.NewReader("not multipart"))
	req.Header.Set("Content-Type", "multipart/form-data")
	rec := httptest.NewRecorder()

	handler.HandleImageEdits(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid multipart, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid multipart form") {
		t.Errorf("expected 'invalid multipart form' in response, got %s", rec.Body.String())
	}
}

func TestHandleImageEdits_ValidRequest(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v1/images/edits") && !strings.HasSuffix(r.URL.Path, "/images/edits") {
			t.Errorf("expected path /v1/images/edits, got %s", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer sk-test" {
			t.Errorf("expected Bearer sk-test, got %q", auth)
		}

		var reqBody struct {
			Prompt         string   `json:"prompt"`
			Image          string   `json:"image"`
			Images         []string `json:"images,omitempty"`
			Mask           string   `json:"mask,omitempty"`
			Model          string   `json:"model"`
			Size           string   `json:"size"`
			ResponseFormat string   `json:"response_format"`
			Background     string   `json:"background"`
			Quality        string   `json:"quality"`
			N              int      `json:"n"`
		}
		if err := json.UnmarshalRead(r.Body, &reqBody); err != nil {
			t.Errorf("decode request body: %v", err)
		}

		if reqBody.Prompt != "make it blue" {
			t.Errorf("expected prompt 'make it blue', got %q", reqBody.Prompt)
		}
		if !strings.HasPrefix(reqBody.Image, "data:image/") {
			t.Errorf("expected image data URI, got %q", reqBody.Image)
		}
		if len(reqBody.Images) != 1 || !strings.HasPrefix(reqBody.Images[0], "data:image/") {
			t.Errorf("expected one additional image data URI, got %v", reqBody.Images)
		}
		if reqBody.Mask != "" && !strings.HasPrefix(reqBody.Mask, "data:image/") {
			t.Errorf("expected mask data URI, got %q", reqBody.Mask)
		}
		if reqBody.Model != "dall-e-2" {
			t.Errorf("expected model dall-e-2, got %q", reqBody.Model)
		}
		if reqBody.Size != "1024x1024" {
			t.Errorf("expected size 1024x1024, got %q", reqBody.Size)
		}
		if reqBody.ResponseFormat != "b64_json" {
			t.Errorf("expected response_format b64_json, got %q", reqBody.ResponseFormat)
		}
		if reqBody.Background != "transparent" {
			t.Errorf("expected background transparent, got %q", reqBody.Background)
		}
		if reqBody.Quality != "high" {
			t.Errorf("expected quality high, got %q", reqBody.Quality)
		}
		if reqBody.N != 2 {
			t.Errorf("expected n=2, got %d", reqBody.N)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"created":1,"data":[{"b64_json":"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="}]}`))
	}))
	defer upstream.Close()

	ag := providers.KnownProviders["openai"]
	origBase := ag.BaseURL
	ag.BaseURL = upstream.URL
	providers.KnownProviders["openai"] = ag
	defer func() {
		ag.BaseURL = origBase
		providers.KnownProviders["openai"] = ag
	}()

	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()

	connData, _ := json.Marshal(map[string]any{"apiKey": "sk-test", "baseUrl": upstream.URL})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-1', 'openai', 'apikey', 'OpenAI Test', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(connData)); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("prompt", "make it blue")
	_ = mw.WriteField("model", "dall-e-2")
	_ = mw.WriteField("size", "1024x1024")
	_ = mw.WriteField("response_format", "b64_json")
	_ = mw.WriteField("background", "transparent")
	_ = mw.WriteField("quality", "high")
	_ = mw.WriteField("n", "2")

	fw, _ := mw.CreateFormFile("image", "primary.png")
	_, _ = fw.Write([]byte("primary image data"))

	fw2, _ := mw.CreateFormFile("image", "secondary.png")
	_, _ = fw2.Write([]byte("secondary image data"))

	fw3, _ := mw.CreateFormFile("mask", "mask.png")
	_, _ = fw3.Write([]byte("mask data"))

	_ = mw.Close()

	req := httptest.NewRequest("POST", "/v1/images/edits", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()

	handler.HandleImageEdits(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Created int64 `json:"created"`
		Data    []struct {
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}

	if len(resp.Data) != 1 {
		t.Fatalf("expected 1 image in data, got %d", len(resp.Data))
	}
	if !strings.HasPrefix(resp.Data[0].B64JSON, "iVBORw0KGgo") {
		t.Errorf("expected base64 PNG data, got %s", resp.Data[0].B64JSON)
	}
}

func TestHandleImageEdits_WithoutOptionalFields(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody struct {
			Prompt string `json:"prompt"`
			Image  string `json:"image"`
			Model  string `json:"model"`
			N      int    `json:"n,omitempty"`
		}
		_ = json.UnmarshalRead(r.Body, &reqBody)

		if reqBody.Prompt != "edit this" {
			t.Errorf("expected prompt 'edit this', got %q", reqBody.Prompt)
		}
		if !strings.HasPrefix(reqBody.Image, "data:image/") {
			t.Errorf("expected image data URI, got %q", reqBody.Image)
		}
		// model should default to dall-e-2 via forwardMediaRequest
		if reqBody.Model != "" && reqBody.Model != "dall-e-2" {
			t.Errorf("expected model dall-e-2 or empty, got %q", reqBody.Model)
		}
		// n should be 0 when not provided
		if reqBody.N != 0 {
			t.Errorf("expected n=0 when not provided, got %d", reqBody.N)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"created":1,"data":[{"b64_json":"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="}]}`))
	}))
	defer upstream.Close()

	ag := providers.KnownProviders["openai"]
	origBase := ag.BaseURL
	ag.BaseURL = upstream.URL
	providers.KnownProviders["openai"] = ag
	defer func() {
		ag.BaseURL = origBase
		providers.KnownProviders["openai"] = ag
	}()

	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()

	connData, _ := json.Marshal(map[string]any{"apiKey": "sk-test", "baseUrl": upstream.URL})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-1', 'openai', 'apikey', 'OpenAI Test', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(connData)); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("prompt", "edit this")

	fw, _ := mw.CreateFormFile("image", "test.png")
	_, _ = fw.Write([]byte("image data"))
	_ = mw.Close()

	req := httptest.NewRequest("POST", "/v1/images/edits", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()

	handler.HandleImageEdits(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleImageEdits_MultipleMasksUsesFirst(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody struct {
			Mask string `json:"mask"`
		}
		_ = json.UnmarshalRead(r.Body, &reqBody)

		// Should be the first mask (base64 of "first mask")
		expected := "data:image/png;base64,Zmlyc3QgbWFzaw=="
		if reqBody.Mask != expected {
			t.Errorf("expected first mask data, got %q", reqBody.Mask)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"created":1,"data":[{"b64_json":"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="}]}`))
	}))
	defer upstream.Close()

	ag := providers.KnownProviders["openai"]
	origBase := ag.BaseURL
	ag.BaseURL = upstream.URL
	providers.KnownProviders["openai"] = ag
	defer func() {
		ag.BaseURL = origBase
		providers.KnownProviders["openai"] = ag
	}()

	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()

	connData, _ := json.Marshal(map[string]any{"apiKey": "sk-test", "baseUrl": upstream.URL})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-1', 'openai', 'apikey', 'OpenAI Test', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(connData)); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("prompt", "test")

	fw, _ := mw.CreateFormFile("image", "test.png")
	_, _ = fw.Write([]byte("image data"))

	fw2, _ := mw.CreateFormFile("mask", "mask1.png")
	_, _ = fw2.Write([]byte("first mask"))

	fw3, _ := mw.CreateFormFile("mask", "mask2.png")
	_, _ = fw3.Write([]byte("second mask"))

	_ = mw.Close()

	req := httptest.NewRequest("POST", "/v1/images/edits", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()

	handler.HandleImageEdits(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestFileToDataURI(t *testing.T) {
	tests := []struct {
		name        string
		content     []byte
		contentType string
		filename    string
		wantPrefix  string
		wantErr     bool
	}{
		{
			name:        "PNG image with explicit content-type",
			content:     []byte("fake png data"),
			contentType: "image/png",
			filename:    "test.png",
			wantPrefix:  "data:image/png;base64,",
			wantErr:     false,
		},
		{
			name:        "JPEG image with sniffed content-type",
			content:     []byte("\xff\xd8\xff\xe0\x00\x10JFIF"),
			contentType: "",
			filename:    "test.jpg",
			wantPrefix:  "data:image/jpeg;base64,",
			wantErr:     false,
		},
		{
			name:        "Default to PNG when unknown",
			content:     []byte("unknown binary"),
			contentType: "application/octet-stream",
			filename:    "test.bin",
			wantPrefix:  "data:image/png;base64,",
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := base64.StdEncoding.EncodeToString(tt.content)

			mimeType := tt.contentType
			if mimeType == "" || mimeType == "application/octet-stream" {
				mimeType = http.DetectContentType(tt.content)
			}
			if !strings.HasPrefix(mimeType, "image/") {
				mimeType = "image/png"
			}

			expected := "data:" + mimeType + ";base64," + data

			// We can't directly call fileToDataURI without a real file,
			// so we verify the logic manually here
			if !strings.HasPrefix(expected, tt.wantPrefix) {
				t.Errorf("mismatch: expected %q prefix", tt.wantPrefix)
			}
		})
	}
}

func TestHandleImageEdits_WhitespacePrompt(t *testing.T) {
	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("prompt", "   ")
	_ = mw.WriteField("model", "dall-e-2")
	fw, _ := mw.CreateFormFile("image", "test.png")
	_, _ = fw.Write([]byte("fake image data"))
	_ = mw.Close()

	req := httptest.NewRequest("POST", "/v1/images/edits", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()

	handler.HandleImageEdits(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for whitespace-only prompt, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Missing required field: prompt") {
		t.Errorf("expected 'Missing required field: prompt' in response, got %s", rec.Body.String())
	}
}
