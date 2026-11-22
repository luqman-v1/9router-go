package media

import (
	"bytes"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
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

func TestHandleAudioSoundGeneration_ConnectionRotation(t *testing.T) {
	sfxBytes := []byte{0xFF, 0xFB, 0x90, 0x01}

	postSound := func(handler *MediaHandler, headers map[string]string, probe bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/audio/sound-generation", strings.NewReader(`{"text":"cinematic boom"}`))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		if probe {
			req = req.WithContext(handlerutil.WithProbeContext(req.Context()))
		}
		rec := httptest.NewRecorder()
		handler.HandleAudioSoundGeneration(rec, req)
		return rec
	}

	t.Run("rotates_on_429_and_locks_failed_connection", func(t *testing.T) {
		var conn1Hits, conn2Hits atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Header.Get("xi-api-key") {
			case "sk-el-1":
				conn1Hits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"detail":"quota exceeded"}`))
			case "sk-el-2":
				conn2Hits.Add(1)
				w.Header().Set("Content-Type", "audio/mpeg")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(sfxBytes)
			default:
				t.Errorf("unexpected api key: %q", r.Header.Get("xi-api-key"))
				w.WriteHeader(http.StatusUnauthorized)
			}
		}))
		defer upstream.Close()

		database, cleanup := setupMultimodalTestDB(t)
		defer cleanup()
		seedElevenLabsConn(t, database, "conn-el-1", "sk-el-1", upstream.URL, 1)
		seedElevenLabsConn(t, database, "conn-el-2", "sk-el-2", upstream.URL, 2)
		handler := newTestMediaHandler(db.NewRepo(database))

		rec := postSound(handler, nil, false)
		if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), sfxBytes) {
			t.Fatalf("first response = (%d, %v), want (200, %v)", rec.Code, rec.Body.Bytes(), sfxBytes)
		}
		if conn1Hits.Load() != 1 || conn2Hits.Load() != 1 {
			t.Fatalf("first hits = (%d, %d), want (1, 1)", conn1Hits.Load(), conn2Hits.Load())
		}

		// Cooled conn-el-1 must be skipped on the follow-up request.
		rec2 := postSound(handler, nil, false)
		if rec2.Code != http.StatusOK || !bytes.Equal(rec2.Body.Bytes(), sfxBytes) {
			t.Fatalf("second response = (%d, %v), want (200, %v)", rec2.Code, rec2.Body.Bytes(), sfxBytes)
		}
		if conn1Hits.Load() != 1 || conn2Hits.Load() != 2 {
			t.Errorf("second hits = (%d, %d), want (1, 2)", conn1Hits.Load(), conn2Hits.Load())
		}
	})

	t.Run("rotates_on_401_invalid_key", func(t *testing.T) {
		var conn1Hits, conn2Hits atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Header.Get("xi-api-key") {
			case "sk-el-1":
				conn1Hits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"detail":"Invalid API key"}`))
			case "sk-el-2":
				conn2Hits.Add(1)
				w.Header().Set("Content-Type", "audio/mpeg")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(sfxBytes)
			}
		}))
		defer upstream.Close()

		database, cleanup := setupMultimodalTestDB(t)
		defer cleanup()
		seedElevenLabsConn(t, database, "conn-el-1", "sk-el-1", upstream.URL, 1)
		seedElevenLabsConn(t, database, "conn-el-2", "sk-el-2", upstream.URL, 2)
		handler := newTestMediaHandler(db.NewRepo(database))

		rec := postSound(handler, nil, false)
		if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), sfxBytes) {
			t.Fatalf("first response = (%d, %v), want (200, %v)", rec.Code, rec.Body.Bytes(), sfxBytes)
		}
		if conn1Hits.Load() != 1 || conn2Hits.Load() != 1 {
			t.Fatalf("first hits = (%d, %d), want (1, 1)", conn1Hits.Load(), conn2Hits.Load())
		}

		rec2 := postSound(handler, nil, false)
		if rec2.Code != http.StatusOK {
			t.Fatalf("second status = %d, want 200", rec2.Code)
		}
		if conn1Hits.Load() != 1 || conn2Hits.Load() != 2 {
			t.Errorf("second hits = (%d, %d), want (1, 2)", conn1Hits.Load(), conn2Hits.Load())
		}
	})

	t.Run("does_not_rotate_or_lock_on_non_retryable_400", func(t *testing.T) {
		const badReqBody = `{"detail":"invalid text"}`
		var conn1Hits, conn2Hits atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Header.Get("xi-api-key") {
			case "sk-el-1":
				conn1Hits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(badReqBody))
			case "sk-el-2":
				conn2Hits.Add(1)
				w.Header().Set("Content-Type", "audio/mpeg")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(sfxBytes)
			}
		}))
		defer upstream.Close()

		database, cleanup := setupMultimodalTestDB(t)
		defer cleanup()
		seedElevenLabsConn(t, database, "conn-el-1", "sk-el-1", upstream.URL, 1)
		seedElevenLabsConn(t, database, "conn-el-2", "sk-el-2", upstream.URL, 2)
		handler := newTestMediaHandler(db.NewRepo(database))

		rec := postSound(handler, nil, false)
		if rec.Code != http.StatusBadRequest || rec.Body.String() != badReqBody {
			t.Fatalf("first response = (%d, %q), want (400, %q)", rec.Code, rec.Body.String(), badReqBody)
		}
		if conn1Hits.Load() != 1 || conn2Hits.Load() != 0 {
			t.Fatalf("first hits = (%d, %d), want (1, 0)", conn1Hits.Load(), conn2Hits.Load())
		}

		rec2 := postSound(handler, nil, false)
		if rec2.Code != http.StatusBadRequest {
			t.Fatalf("second status = %d, want 400", rec2.Code)
		}
		if conn1Hits.Load() != 2 || conn2Hits.Load() != 0 {
			t.Errorf("second hits = (%d, %d), want (2, 0)", conn1Hits.Load(), conn2Hits.Load())
		}
	})

	t.Run("respects_pinned_x_connection_id_header", func(t *testing.T) {
		var conn1Hits, conn2Hits atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Header.Get("xi-api-key") {
			case "sk-el-1":
				conn1Hits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"detail":"rate limited"}`))
			case "sk-el-2":
				conn2Hits.Add(1)
				w.WriteHeader(http.StatusOK)
			}
		}))
		defer upstream.Close()

		database, cleanup := setupMultimodalTestDB(t)
		defer cleanup()
		seedElevenLabsConn(t, database, "conn-el-1", "sk-el-1", upstream.URL, 1)
		seedElevenLabsConn(t, database, "conn-el-2", "sk-el-2", upstream.URL, 2)
		handler := newTestMediaHandler(db.NewRepo(database))

		rec := postSound(handler, map[string]string{"x-connection-id": "conn-el-1"}, false)
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("status = %d, want 429: %s", rec.Code, rec.Body.String())
		}
		if conn1Hits.Load() != 1 || conn2Hits.Load() != 0 {
			t.Errorf("pinned hits = (%d, %d), want (1, 0)", conn1Hits.Load(), conn2Hits.Load())
		}
	})

	t.Run("returns_last_upstream_error_when_all_connections_exhausted", func(t *testing.T) {
		var conn1Hits, conn2Hits atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Header.Get("xi-api-key") {
			case "sk-el-1":
				conn1Hits.Add(1)
			case "sk-el-2":
				conn2Hits.Add(1)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"detail":"quota exceeded"}`))
		}))
		defer upstream.Close()

		database, cleanup := setupMultimodalTestDB(t)
		defer cleanup()
		seedElevenLabsConn(t, database, "conn-el-1", "sk-el-1", upstream.URL, 1)
		seedElevenLabsConn(t, database, "conn-el-2", "sk-el-2", upstream.URL, 2)
		handler := newTestMediaHandler(db.NewRepo(database))

		rec := postSound(handler, nil, false)
		if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), "quota exceeded") {
			t.Fatalf("status/body = (%d, %q), want (429, quota exceeded)", rec.Code, rec.Body.String())
		}
		if conn1Hits.Load() != 1 || conn2Hits.Load() != 1 {
			t.Errorf("exhausted hits = (%d, %d), want (1, 1)", conn1Hits.Load(), conn2Hits.Load())
		}
	})

	t.Run("probe_context_rotates_without_persisting_lock", func(t *testing.T) {
		var conn1Hits, conn2Hits atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Header.Get("xi-api-key") {
			case "sk-el-1":
				conn1Hits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"detail":"quota exceeded"}`))
			case "sk-el-2":
				conn2Hits.Add(1)
				w.Header().Set("Content-Type", "audio/mpeg")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(sfxBytes)
			}
		}))
		defer upstream.Close()

		database, cleanup := setupMultimodalTestDB(t)
		defer cleanup()
		seedElevenLabsConn(t, database, "conn-el-1", "sk-el-1", upstream.URL, 1)
		seedElevenLabsConn(t, database, "conn-el-2", "sk-el-2", upstream.URL, 2)
		handler := newTestMediaHandler(db.NewRepo(database))

		rec := postSound(handler, nil, true)
		if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), sfxBytes) {
			t.Fatalf("probe response = (%d, %v), want (200, %v)", rec.Code, rec.Body.Bytes(), sfxBytes)
		}
		if conn1Hits.Load() != 1 || conn2Hits.Load() != 1 {
			t.Fatalf("probe hits = (%d, %d), want (1, 1)", conn1Hits.Load(), conn2Hits.Load())
		}

		// Follow-up non-probe request must still try conn-el-1 first because probe did not persist lock.
		rec2 := postSound(handler, nil, false)
		if rec2.Code != http.StatusOK {
			t.Fatalf("follow-up status = %d, want 200: %s", rec2.Code, rec2.Body.String())
		}
		if conn1Hits.Load() != 2 || conn2Hits.Load() != 2 {
			t.Errorf("follow-up hits = (%d, %d), want (2, 2)", conn1Hits.Load(), conn2Hits.Load())
		}
	})
}
