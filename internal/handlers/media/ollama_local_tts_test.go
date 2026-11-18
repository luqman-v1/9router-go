package media

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
)

// ollama-local/phonon is speech synthesis on the self-hosted Ollama daemon.
// The request must reach the OpenAI-compatible /v1/audio/speech endpoint with
// the model rewritten to the bare phonon id, and the audio body passed
// through verbatim (upstream ollama-local.js ttsConfig, commit 31a9d262).
func TestHandleAudioSpeech_OllamaLocalPhonon(t *testing.T) {
	t.Run("custom base URL", func(t *testing.T) {
		var gotPath, gotModel string
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			var reqBody struct {
				Model string `json:"model"`
			}
			_ = json.NewDecoder(r.Body).Decode(&reqBody)
			gotModel = reqBody.Model
			w.Header().Set("Content-Type", "audio/wav")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("RIFFfakephonon"))
		}))
		defer upstream.Close()

		database, cleanup := setupMultimodalTestDB(t)
		defer cleanup()
		seedOllamaLocalConn(t, database, `{"apiKey":"none","baseUrl":"`+upstream.URL+`"}`)

		handler := newTestMediaHandler(db.NewRepo(database))
		body := `{"model":"ollama-local/phonon","input":"hello","voice":"alloy"}`
		req := httptest.NewRequest("POST", "/v1/audio/speech", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		handler.HandleAudioSpeech(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		if gotPath != "/v1/audio/speech" {
			t.Errorf("upstream path = %q, want /v1/audio/speech", gotPath)
		}
		if gotModel != "phonon" {
			t.Errorf("upstream model = %q, want phonon", gotModel)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "audio/wav" {
			t.Errorf("content-type = %q, want audio/wav passthrough", ct)
		}
		if rec.Body.String() != "RIFFfakephonon" {
			t.Errorf("body = %q, want raw audio passthrough", rec.Body.String())
		}
	})

	t.Run("default TTSURL", func(t *testing.T) {
		var gotPath string
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			w.Header().Set("Content-Type", "audio/wav")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("RIFFdefault"))
		}))
		defer upstream.Close()

		// No custom base URL: the provider's TTSURL decides the target.
		orig := providers.KnownProviders["ollama-local"]
		cfg := orig
		cfg.TTSURL = upstream.URL + "/v1/audio/speech"
		providers.KnownProviders["ollama-local"] = cfg
		defer func() { providers.KnownProviders["ollama-local"] = orig }()

		database, cleanup := setupMultimodalTestDB(t)
		defer cleanup()
		seedOllamaLocalConn(t, database, `{"apiKey":"none"}`)

		handler := newTestMediaHandler(db.NewRepo(database))
		body := `{"model":"ollama-local/phonon:en","input":"hello"}`
		req := httptest.NewRequest("POST", "/v1/audio/speech", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		handler.HandleAudioSpeech(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		if gotPath != "/v1/audio/speech" {
			t.Errorf("upstream path = %q, want /v1/audio/speech", gotPath)
		}
	})
}

func seedOllamaLocalConn(t *testing.T, database *sql.DB, data string) {
	t.Helper()
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-ollama-local-tts', 'ollama-local', 'none', 'Ollama Local TTS', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, data); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
}
