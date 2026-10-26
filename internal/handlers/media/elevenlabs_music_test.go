package media

import (
	"bytes"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
)

// TestHandleAudioMusic_Success tests successful ElevenLabs music generation.
func TestHandleAudioMusic_Success(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/v1/music" {
			t.Errorf("expected path /v1/music, got %s", r.URL.Path)
		}
		if auth := r.Header.Get("xi-api-key"); auth != "sk-el-TEST" {
			t.Errorf("expected xi-api-key sk-el-TEST, got %q", auth)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected Content-Type application/json, got %q", ct)
		}

		var reqBody struct {
			Prompt          string   `json:"prompt"`
			DurationSeconds *float64 `json:"duration_seconds,omitempty"`
			Model           string   `json:"model,omitempty"`
		}
		if err := json.UnmarshalRead(r.Body, &reqBody); err != nil {
			t.Errorf("decode request body: %v", err)
		}

		if reqBody.Prompt != "upbeat jazz track" {
			t.Errorf("expected prompt 'upbeat jazz track', got %q", reqBody.Prompt)
		}
		if reqBody.DurationSeconds == nil || *reqBody.DurationSeconds != 30.0 {
			t.Errorf("expected duration_seconds 30, got %v", reqBody.DurationSeconds)
		}
		if reqBody.Model != "elevenlabs/music-v1" {
			t.Errorf("expected model 'elevenlabs/music-v1', got %q", reqBody.Model)
		}

		w.Header().Set("Content-Type", "audio/mpeg")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("fake-mp3-data"))
	}))
	defer upstream.Close()

	// Override elevenlabs provider config
	orig := providers.KnownProviders["elevenlabs"]
	providers.KnownProviders["elevenlabs"] = providers.ProviderConfig{
		BaseURL:    upstream.URL,
		AuthHeader: "xi-api-key",
		AuthScheme: "raw",
	}
	defer func() {
		providers.KnownProviders["elevenlabs"] = orig
	}()

	database, cleanup := setupEmbeddingsTestDB(t)
	defer cleanup()

	connData, _ := json.Marshal(map[string]any{"apiKey": "sk-el-TEST"})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-el-music', 'elevenlabs', 'apikey', 'ElevenLabs Music Test', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(connData)); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	body := `{"prompt":"upbeat jazz track","duration_seconds":30.0,"model":"elevenlabs/music-v1"}`
	req := httptest.NewRequest("POST", "/v1/audio/music", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleAudioMusic(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "audio/mpeg" {
		t.Errorf("expected Content-Type audio/mpeg, got %q", ct)
	}
	if rec.Body.String() != "fake-mp3-data" {
		t.Errorf("expected body 'fake-mp3-data', got %q", rec.Body.String())
	}
}

// TestHandleAudioMusic_EmptyPrompt tests that empty prompt returns 400.
func TestHandleAudioMusic_EmptyPrompt(t *testing.T) {
	database, cleanup := setupEmbeddingsTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	body := `{"prompt":"","duration_seconds":30.0,"model":"elevenlabs/music-v1"}`
	req := httptest.NewRequest("POST", "/v1/audio/music", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleAudioMusic(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	var errResp struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("parse error response: %v", err)
	}
	if errResp.Error.Message != "missing required field: prompt" {
		t.Errorf("expected error 'missing required field: prompt', got %v", errResp.Error.Message)
	}
}

// TestHandleAudioMusic_NoConnection tests that missing connection returns 503.
func TestHandleAudioMusic_NoConnection(t *testing.T) {
	database, cleanup := setupEmbeddingsTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	body := `{"prompt":"upbeat jazz track","duration_seconds":30.0,"model":"elevenlabs/music-v1"}`
	req := httptest.NewRequest("POST", "/v1/audio/music", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleAudioMusic(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", rec.Code, rec.Body.String())
	}
	var errResp struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("parse error response: %v", err)
	}
	if errResp.Error.Message != "no active connection for elevenlabs" {
		t.Errorf("expected error 'no active connection for elevenlabs', got %v", errResp.Error.Message)
	}
}

// TestHandleAudioMusic_UpstreamError tests that upstream errors are propagated.
func TestHandleAudioMusic_UpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"detail":"Invalid API key"}`))
	}))
	defer upstream.Close()

	orig := providers.KnownProviders["elevenlabs"]
	providers.KnownProviders["elevenlabs"] = providers.ProviderConfig{
		BaseURL:    upstream.URL,
		AuthHeader: "xi-api-key",
		AuthScheme: "raw",
	}
	defer func() {
		providers.KnownProviders["elevenlabs"] = orig
	}()

	database, cleanup := setupEmbeddingsTestDB(t)
	defer cleanup()

	connData, _ := json.Marshal(map[string]any{"apiKey": "sk-el-TEST"})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-el-music', 'elevenlabs', 'apikey', 'ElevenLabs Music Test', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(connData)); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	body := `{"prompt":"upbeat jazz track","duration_seconds":30.0,"model":"elevenlabs/music-v1"}`
	req := httptest.NewRequest("POST", "/v1/audio/music", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleAudioMusic(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", rec.Code, rec.Body.String())
	}
	var errResp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("parse error response: %v", err)
	}
	if errResp["error"] == nil {
		t.Error("expected error in response")
	}
}
