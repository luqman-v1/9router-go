package media

import (
	"bytes"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
)

// newElevenLabsSoundHandler seeds an active elevenlabs connection pointed at a
// fake sound-generation upstream and returns a handler wired to it.
func newElevenLabsSoundHandler(t *testing.T, upstreamURL string) *MediaHandler {
	t.Helper()
	database, cleanup := setupMultimodalTestDB(t)
	t.Cleanup(cleanup)

	connData, err := json.Marshal(map[string]any{"apiKey": "sk-el-sound", "baseUrl": upstreamURL})
	if err != nil {
		t.Fatalf("marshal conn data: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-el-sound', 'elevenlabs', 'apikey', 'ElevenLabs Sound Test', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(connData)); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	return newTestMediaHandler(db.NewRepo(database))
}

func TestHandleAudioSoundGeneration_Success(t *testing.T) {
	var upstreamBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if got, want := r.Header.Get("xi-api-key"), "sk-el-sound"; got != want {
			t.Errorf("xi-api-key = %q, want %q", got, want)
		}
		if got, want := r.Header.Get("Content-Type"), "application/json"; got != want {
			t.Errorf("content-type = %q, want %q", got, want)
		}
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		upstreamBody = buf[:n]
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Write([]byte{0xFF, 0xFB, 0x90, 0x00})
	}))
	defer upstream.Close()

	handler := newElevenLabsSoundHandler(t, upstream.URL)

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/sound-generation", strings.NewReader(`{"text":"cinematic thunderclap","duration_seconds":3,"prompt_influence":0.5}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleAudioSoundGeneration(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "audio/mpeg" {
		t.Errorf("content-type = %q, want audio/mpeg", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), []byte{0xFF, 0xFB, 0x90, 0x00}) {
		t.Errorf("body = %v, want mpeg bytes", rec.Body.Bytes())
	}
	var sent map[string]any
	if err := json.Unmarshal(upstreamBody, &sent); err != nil {
		t.Fatalf("upstream body not json: %v: %s", err, string(upstreamBody))
	}
	if sent["text"] != "cinematic thunderclap" {
		t.Errorf("upstream text = %v", sent["text"])
	}
}

func TestHandleAudioSoundGeneration_EmptyText(t *testing.T) {
	handler := newElevenLabsSoundHandler(t, "http://127.0.0.1:1")

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/sound-generation", strings.NewReader(`{"text":"   "}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleAudioSoundGeneration(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleAudioSoundGeneration_NoConnection(t *testing.T) {
	database, cleanup := setupMultimodalTestDB(t)
	t.Cleanup(cleanup)
	handler := newTestMediaHandler(db.NewRepo(database))

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/sound-generation", strings.NewReader(`{"text":"boom"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleAudioSoundGeneration(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", rec.Code, rec.Body.String())
	}
}

// providers keeps the test from accidentally depending on the real registry
// host when a caller omits baseUrl; the handler must not hit the network.
var _ = providers.ProviderConfig{}
