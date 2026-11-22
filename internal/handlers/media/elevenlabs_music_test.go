package media

import (
	"bytes"
	"database/sql"
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

func seedElevenLabsConn(t *testing.T, rawDB *sql.DB, id, apiKey, baseURL string, priority int) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"apiKey": apiKey, "baseUrl": baseURL})
	if err != nil {
		t.Fatalf("marshal elevenlabs conn %s: %v", id, err)
	}
	if _, err := rawDB.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		(?, 'elevenlabs', 'apikey', ?, ?, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, id, "ElevenLabs "+id, priority, string(data)); err != nil {
		t.Fatalf("seed elevenlabs connection %s: %v", id, err)
	}
}

func TestHandleAudioMusic_ConnectionRotation(t *testing.T) {
	postMusic := func(handler *MediaHandler, headers map[string]string, probe bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/audio/music", strings.NewReader(`{"prompt":"upbeat jazz","model":"elevenlabs/music-v1"}`))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		if probe {
			req = req.WithContext(handlerutil.WithProbeContext(req.Context()))
		}
		rec := httptest.NewRecorder()
		handler.HandleAudioMusic(rec, req)
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
				_, _ = w.Write([]byte(`{"detail":"quota exhausted"}`))
			case "sk-el-2":
				conn2Hits.Add(1)
				w.Header().Set("Content-Type", "audio/mpeg")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("mp3-from-2"))
			default:
				t.Errorf("unexpected api key: %q", r.Header.Get("xi-api-key"))
				w.WriteHeader(http.StatusUnauthorized)
			}
		}))
		defer upstream.Close()

		database, cleanup := setupEmbeddingsTestDB(t)
		defer cleanup()
		seedElevenLabsConn(t, database, "conn-el-1", "sk-el-1", upstream.URL, 1)
		seedElevenLabsConn(t, database, "conn-el-2", "sk-el-2", upstream.URL, 2)
		handler := newTestMediaHandler(db.NewRepo(database))

		rec := postMusic(handler, nil, false)
		if rec.Code != http.StatusOK || rec.Body.String() != "mp3-from-2" {
			t.Fatalf("first response = (%d, %q), want (200, mp3-from-2)", rec.Code, rec.Body.String())
		}
		if conn1Hits.Load() != 1 || conn2Hits.Load() != 1 {
			t.Fatalf("first hits = (%d, %d), want (1, 1)", conn1Hits.Load(), conn2Hits.Load())
		}

		// Cooled conn-el-1 must be skipped on the follow-up request.
		rec2 := postMusic(handler, nil, false)
		if rec2.Code != http.StatusOK || rec2.Body.String() != "mp3-from-2" {
			t.Fatalf("second response = (%d, %q), want (200, mp3-from-2)", rec2.Code, rec2.Body.String())
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
				_, _ = w.Write([]byte("mp3-from-2"))
			}
		}))
		defer upstream.Close()

		database, cleanup := setupEmbeddingsTestDB(t)
		defer cleanup()
		seedElevenLabsConn(t, database, "conn-el-1", "sk-el-1", upstream.URL, 1)
		seedElevenLabsConn(t, database, "conn-el-2", "sk-el-2", upstream.URL, 2)
		handler := newTestMediaHandler(db.NewRepo(database))

		rec := postMusic(handler, nil, false)
		if rec.Code != http.StatusOK || rec.Body.String() != "mp3-from-2" {
			t.Fatalf("first response = (%d, %q), want (200, mp3-from-2)", rec.Code, rec.Body.String())
		}
		if conn1Hits.Load() != 1 || conn2Hits.Load() != 1 {
			t.Fatalf("first hits = (%d, %d), want (1, 1)", conn1Hits.Load(), conn2Hits.Load())
		}

		rec2 := postMusic(handler, nil, false)
		if rec2.Code != http.StatusOK {
			t.Fatalf("second status = %d, want 200", rec2.Code)
		}
		if conn1Hits.Load() != 1 || conn2Hits.Load() != 2 {
			t.Errorf("second hits = (%d, %d), want (1, 2)", conn1Hits.Load(), conn2Hits.Load())
		}
	})

	t.Run("does_not_rotate_or_lock_on_non_retryable_400", func(t *testing.T) {
		var conn1Hits, conn2Hits atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Header.Get("xi-api-key") {
			case "sk-el-1":
				conn1Hits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"detail":"invalid prompt"}`))
			case "sk-el-2":
				conn2Hits.Add(1)
				w.Header().Set("Content-Type", "audio/mpeg")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("mp3-from-2"))
			}
		}))
		defer upstream.Close()

		database, cleanup := setupEmbeddingsTestDB(t)
		defer cleanup()
		seedElevenLabsConn(t, database, "conn-el-1", "sk-el-1", upstream.URL, 1)
		seedElevenLabsConn(t, database, "conn-el-2", "sk-el-2", upstream.URL, 2)
		handler := newTestMediaHandler(db.NewRepo(database))

		rec := postMusic(handler, nil, false)
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("first status = %d, want 502: %s", rec.Code, rec.Body.String())
		}
		if conn1Hits.Load() != 1 || conn2Hits.Load() != 0 {
			t.Fatalf("first hits = (%d, %d), want (1, 0)", conn1Hits.Load(), conn2Hits.Load())
		}

		rec2 := postMusic(handler, nil, false)
		if rec2.Code != http.StatusBadGateway {
			t.Fatalf("second status = %d, want 502", rec2.Code)
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

		database, cleanup := setupEmbeddingsTestDB(t)
		defer cleanup()
		seedElevenLabsConn(t, database, "conn-el-1", "sk-el-1", upstream.URL, 1)
		seedElevenLabsConn(t, database, "conn-el-2", "sk-el-2", upstream.URL, 2)
		handler := newTestMediaHandler(db.NewRepo(database))

		rec := postMusic(handler, map[string]string{"x-connection-id": "conn-el-1"}, false)
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502: %s", rec.Code, rec.Body.String())
		}
		if conn1Hits.Load() != 1 || conn2Hits.Load() != 0 {
			t.Errorf("pinned hits = (%d, %d), want (1, 0)", conn1Hits.Load(), conn2Hits.Load())
		}
	})

	t.Run("returns_error_when_all_connections_exhausted", func(t *testing.T) {
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

		database, cleanup := setupEmbeddingsTestDB(t)
		defer cleanup()
		seedElevenLabsConn(t, database, "conn-el-1", "sk-el-1", upstream.URL, 1)
		seedElevenLabsConn(t, database, "conn-el-2", "sk-el-2", upstream.URL, 2)
		handler := newTestMediaHandler(db.NewRepo(database))

		rec := postMusic(handler, nil, false)
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502: %s", rec.Code, rec.Body.String())
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
				_, _ = w.Write([]byte("mp3-from-2"))
			}
		}))
		defer upstream.Close()

		database, cleanup := setupEmbeddingsTestDB(t)
		defer cleanup()
		seedElevenLabsConn(t, database, "conn-el-1", "sk-el-1", upstream.URL, 1)
		seedElevenLabsConn(t, database, "conn-el-2", "sk-el-2", upstream.URL, 2)
		handler := newTestMediaHandler(db.NewRepo(database))

		rec := postMusic(handler, nil, true)
		if rec.Code != http.StatusOK {
			t.Fatalf("probe status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		if conn1Hits.Load() != 1 || conn2Hits.Load() != 1 {
			t.Fatalf("probe hits = (%d, %d), want (1, 1)", conn1Hits.Load(), conn2Hits.Load())
		}

		// Follow-up non-probe request must still try conn-el-1 first because probe did not persist lock.
		rec2 := postMusic(handler, nil, false)
		if rec2.Code != http.StatusOK {
			t.Fatalf("follow-up status = %d, want 200: %s", rec2.Code, rec2.Body.String())
		}
		if conn1Hits.Load() != 2 || conn2Hits.Load() != 2 {
			t.Errorf("follow-up hits = (%d, %d), want (2, 2)", conn1Hits.Load(), conn2Hits.Load())
		}
	})

	t.Run("model_less_request_succeeds_with_strict_model_assignment", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("xi-api-key") != "sk-el-strict" {
				t.Errorf("unexpected api key: %q", r.Header.Get("xi-api-key"))
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "audio/mpeg")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("mp3-strict"))
		}))
		defer upstream.Close()

		database, cleanup := setupEmbeddingsTestDB(t)
		defer cleanup()
		if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS settings (id INTEGER PRIMARY KEY, data TEXT NOT NULL)`); err != nil {
			t.Fatalf("create settings table: %v", err)
		}
		if _, err := database.Exec(`INSERT INTO settings (id, data) VALUES (1, ?)`, `{"providerStrategies":{"elevenlabs":{"strictModelAssignment":true}}}`); err != nil {
			t.Fatalf("seed settings: %v", err)
		}
		connData, err := json.Marshal(map[string]any{
			"apiKey":        "sk-el-strict",
			"baseUrl":       upstream.URL,
			"assignedModel": "elevenlabs/music-v1",
		})
		if err != nil {
			t.Fatalf("marshal conn data: %v", err)
		}
		if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
			('conn-el-strict', 'elevenlabs', 'apikey', 'ElevenLabs Strict', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(connData)); err != nil {
			t.Fatalf("seed strict connection: %v", err)
		}

		handler := newTestMediaHandler(db.NewRepo(database))
		req := httptest.NewRequest(http.MethodPost, "/v1/audio/music", strings.NewReader(`{"prompt":"jazz"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.HandleAudioMusic(rec, req)

		if rec.Code != http.StatusOK || rec.Body.String() != "mp3-strict" {
			t.Fatalf("response = (%d, %q), want (200, mp3-strict)", rec.Code, rec.Body.String())
		}
	})
}
