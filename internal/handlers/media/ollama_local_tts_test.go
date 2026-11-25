package media

import (
	"database/sql"
	"encoding/json"
	"net"
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

	// The registry default is what this subtest pins, and the request carries
	// no injected TTSURL or loopback marker: the media lane itself must mark a
	// local-by-design provider's upstream loopback-safe, exactly as the chat
	// lane does, or the dial guard refuses every attempt under
	// ALLOW_PRIVATE_NETWORKING=false.
	t.Run("registry default TTSURL", func(t *testing.T) {
		const wantTTSURL = "http://localhost:11434/v1/audio/speech"
		cfg, ok := providers.KnownProviders["ollama-local"]
		if !ok {
			t.Fatal(`providers.KnownProviders["ollama-local"] is missing`)
		}
		if cfg.TTSURL != wantTTSURL {
			t.Fatalf("KnownProviders[\"ollama-local\"].TTSURL = %q, want %q", cfg.TTSURL, wantTTSURL)
		}

		database, cleanup := setupMultimodalTestDB(t)
		defer cleanup()
		seedOllamaLocalConn(t, database, `{"apiKey":"none"}`)

		handler := newTestMediaHandler(db.NewRepo(database))

		// The default is a loopback URL, so the request needs a listener on
		// that exact port. Skip when the port is taken: nothing to assert then.
		ln, err := net.Listen("tcp", "127.0.0.1:11434")
		if err != nil {
			t.Skipf("cannot bind the default Ollama host 127.0.0.1:11434: %v", err)
		}
		upstream := &httptest.Server{
			Listener: ln,
			Config: &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/audio/speech" {
					t.Errorf("upstream path = %q, want /v1/audio/speech", r.URL.Path)
				}
				var reqBody struct {
					Model string `json:"model"`
				}
				_ = json.NewDecoder(r.Body).Decode(&reqBody)
				// The tagged request forwards its resolved id verbatim
				// (provider/model is what the branch strips).
				if reqBody.Model != "phonon:en" {
					t.Errorf("upstream model = %q, want phonon:en", reqBody.Model)
				}
				w.Header().Set("Content-Type", "audio/wav")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("RIFFdefault"))
			})},
		}
		upstream.Start()
		defer upstream.Close()

		body := `{"model":"ollama-local/phonon:en","input":"hello"}`
		// No loopback marker is injected: the media lane marks local-by-design
		// providers loopback-safe itself (media.go forwardMediaRequest). The
		// environment is pinned off so the dial guard is at its strictest —
		// a lane that forgot the marker answers 502 here, not on the
		// operator's box.
		t.Setenv("ALLOW_PRIVATE_NETWORKING", "false")
		req := httptest.NewRequest("POST", "/v1/audio/speech", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		handler.HandleAudioSpeech(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		if rec.Body.String() != "RIFFdefault" {
			t.Errorf("body = %q, want raw audio passthrough", rec.Body.String())
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
