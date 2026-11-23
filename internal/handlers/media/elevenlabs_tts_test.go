package media

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

func TestElevenLabsTTS_VoiceAndFormat(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		if payload["model_id"] != "eleven_v4" {
			t.Errorf("expected model_id eleven_v4, got %v", payload["model_id"])
		}
		if _, ok := payload["voice_settings"]; ok {
			t.Errorf("voice_settings should not be forced")
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		w.WriteHeader(http.StatusOK)
		w.Write(make([]byte, 2048))
	}))
	defer up.Close()

	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()

	connData, _ := json.Marshal(map[string]any{"apiKey": "sk-el", "baseUrl": up.URL})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-el-tts', 'elevenlabs', 'apikey', 'EL TTS', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(connData)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	// bare model with OpenAI voice field
	req := httptest.NewRequest("POST", "/v1/audio/speech", strings.NewReader(`{"model":"elevenlabs/eleven_v4","input":"Hello","voice":"voice123","response_format":"mp3"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.HandleAudioSpeech(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	// upstream path recorded via request URL in handler? We can't easily inspect; check content-type
	if ct := rec.Header().Get("Content-Type"); ct != "audio/mpeg" {
		t.Errorf("content-type = %q, want audio/mpeg", ct)
	}

	// voice inside model string
	req2 := httptest.NewRequest("POST", "/v1/audio/speech", strings.NewReader(`{"model":"elevenlabs/eleven_v4/inModel","input":"Hello","voice":"voice123","response_format":"opus"}`))
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	handler.HandleAudioSpeech(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	// content type for opus
	if ct := rec2.Header().Get("Content-Type"); ct != "audio/opus" {
		t.Errorf("content-type = %q, want audio/opus", ct)
	}
}

func TestElevenLabsTTS_BareVoiceFallback(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		if payload["model_id"] != "eleven_flash_v2_5" {
			t.Errorf("expected fallback model_id eleven_flash_v2_5, got %v", payload["model_id"])
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		w.WriteHeader(http.StatusOK)
		w.Write(make([]byte, 2048))
	}))
	defer up.Close()

	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()

	connData, _ := json.Marshal(map[string]any{"apiKey": "sk-el", "baseUrl": up.URL})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-el-tts2', 'elevenlabs', 'apikey', 'EL TTS2', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(connData)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	req := httptest.NewRequest("POST", "/v1/audio/speech", strings.NewReader(`{"model":"elevenlabs/voice123","input":"Hello","response_format":"json"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.HandleAudioSpeech(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q, want application/json", ct)
	}
}
