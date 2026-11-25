package media

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
)

func TestForwardMediaRequest_ConnectionRotation(t *testing.T) {
	const okImagePayload = `{"created":1,"data":[{"url":"https://example.com/cat.png"}]}`

	postMedia := func(fn func(http.ResponseWriter, *http.Request), path, body string, headers map[string]string, probe bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		if probe {
			req = req.WithContext(handlerutil.WithProbeContext(req.Context()))
		}
		rec := httptest.NewRecorder()
		fn(rec, req)
		return rec
	}

	t.Run("rotates_on_429_and_locks_failed_connection", func(t *testing.T) {
		var conn1Hits, conn2Hits atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.Header.Get("Authorization") {
			case "Bearer sk-img-1":
				conn1Hits.Add(1)
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":{"message":"rate limit exceeded"}}`))
			case "Bearer sk-img-2":
				conn2Hits.Add(1)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(okImagePayload))
			default:
				t.Errorf("unexpected auth header: %q", r.Header.Get("Authorization"))
				w.WriteHeader(http.StatusUnauthorized)
			}
		}))
		defer upstream.Close()

		database, cleanup := setupEmbeddingsTestDB(t)
		defer cleanup()
		seedJSONMediaConn(t, database, "conn-img-1", "openai", "sk-img-1", upstream.URL, 1)
		seedJSONMediaConn(t, database, "conn-img-2", "openai", "sk-img-2", upstream.URL, 2)
		handler := newTestMediaHandler(db.NewRepo(database))

		const imgBody = `{"model":"openai/dall-e-3","prompt":"cat"}`
		rec := postMedia(handler.HandleImages, "/v1/images/generations", imgBody, nil, false)
		if rec.Code != http.StatusOK || rec.Body.String() != okImagePayload {
			t.Fatalf("first response = (%d, %s), want (200, %s)", rec.Code, rec.Body.String(), okImagePayload)
		}
		if conn1Hits.Load() != 1 || conn2Hits.Load() != 1 {
			t.Fatalf("first hits = (%d, %d), want (1, 1)", conn1Hits.Load(), conn2Hits.Load())
		}

		// Cooled conn-img-1 must be skipped on the follow-up request.
		rec2 := postMedia(handler.HandleImages, "/v1/images/generations", imgBody, nil, false)
		if rec2.Code != http.StatusOK || rec2.Body.String() != okImagePayload {
			t.Fatalf("second response = (%d, %s), want (200, %s)", rec2.Code, rec2.Body.String(), okImagePayload)
		}
		if conn1Hits.Load() != 1 || conn2Hits.Load() != 2 {
			t.Errorf("second hits = (%d, %d), want (1, 2)", conn1Hits.Load(), conn2Hits.Load())
		}
	})

	t.Run("rotates_on_401_invalid_key", func(t *testing.T) {
		okAudio := []byte("ID3-mock-mp3-audio-bytes")
		var conn1Hits, conn2Hits atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Header.Get("Authorization") {
			case "Bearer sk-tts-1":
				conn1Hits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
			case "Bearer sk-tts-2":
				conn2Hits.Add(1)
				w.Header().Set("Content-Type", "audio/mpeg")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(okAudio)
			default:
				t.Errorf("unexpected auth header: %q", r.Header.Get("Authorization"))
				w.WriteHeader(http.StatusUnauthorized)
			}
		}))
		defer upstream.Close()

		database, cleanup := setupEmbeddingsTestDB(t)
		defer cleanup()
		seedJSONMediaConn(t, database, "conn-tts-1", "openai", "sk-tts-1", upstream.URL, 1)
		seedJSONMediaConn(t, database, "conn-tts-2", "openai", "sk-tts-2", upstream.URL, 2)
		handler := newTestMediaHandler(db.NewRepo(database))

		const ttsBody = `{"model":"openai/tts-1","input":"hello","voice":"alloy"}`
		rec := postMedia(handler.HandleAudioSpeech, "/v1/audio/speech", ttsBody, nil, false)
		if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), okAudio) {
			t.Fatalf("first response = (%d, %q), want (200, %q)", rec.Code, rec.Body.Bytes(), okAudio)
		}
		if conn1Hits.Load() != 1 || conn2Hits.Load() != 1 {
			t.Fatalf("first hits = (%d, %d), want (1, 1)", conn1Hits.Load(), conn2Hits.Load())
		}

		// Locked conn-tts-1 stays skipped on follow-up request.
		rec2 := postMedia(handler.HandleAudioSpeech, "/v1/audio/speech", ttsBody, nil, false)
		if rec2.Code != http.StatusOK || !bytes.Equal(rec2.Body.Bytes(), okAudio) {
			t.Fatalf("second response = (%d, %q), want (200, %q)", rec2.Code, rec2.Body.Bytes(), okAudio)
		}
		if conn1Hits.Load() != 1 || conn2Hits.Load() != 2 {
			t.Errorf("second hits = (%d, %d), want (1, 2)", conn1Hits.Load(), conn2Hits.Load())
		}
	})

	t.Run("does_not_rotate_or_lock_on_non_retryable_400", func(t *testing.T) {
		const clientErr = `{"error":{"message":"invalid prompt"}}`
		var conn1Hits, conn2Hits atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.Header.Get("Authorization") {
			case "Bearer sk-img-1":
				conn1Hits.Add(1)
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(clientErr))
			case "Bearer sk-img-2":
				conn2Hits.Add(1)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(okImagePayload))
			}
		}))
		defer upstream.Close()

		database, cleanup := setupEmbeddingsTestDB(t)
		defer cleanup()
		seedJSONMediaConn(t, database, "conn-img-1", "openai", "sk-img-1", upstream.URL, 1)
		seedJSONMediaConn(t, database, "conn-img-2", "openai", "sk-img-2", upstream.URL, 2)
		handler := newTestMediaHandler(db.NewRepo(database))

		const imgBody = `{"model":"openai/dall-e-3","prompt":"cat"}`
		rec := postMedia(handler.HandleImages, "/v1/images/generations", imgBody, nil, false)
		if rec.Code != http.StatusBadRequest || rec.Body.String() != clientErr {
			t.Fatalf("first response = (%d, %s), want (400, %s)", rec.Code, rec.Body.String(), clientErr)
		}
		if conn1Hits.Load() != 1 || conn2Hits.Load() != 0 {
			t.Fatalf("first hits = (%d, %d), want (1, 0)", conn1Hits.Load(), conn2Hits.Load())
		}

		// Non-retryable 400 must not lock conn-img-1.
		rec2 := postMedia(handler.HandleImages, "/v1/images/generations", imgBody, nil, false)
		if rec2.Code != http.StatusBadRequest {
			t.Fatalf("second status = %d, want 400", rec2.Code)
		}
		if conn1Hits.Load() != 2 || conn2Hits.Load() != 0 {
			t.Errorf("second hits = (%d, %d), want (2, 0)", conn1Hits.Load(), conn2Hits.Load())
		}
	})

	t.Run("video_create_rotates_on_429_but_does_not_rotate_on_500", func(t *testing.T) {
		const videoBody = `{"model":"openai/sora-2","prompt":"wave"}`
		const videoOK = `{"id":"vid_123","status":"queued"}`

		t.Run("rotates_on_429", func(t *testing.T) {
			var conn1Hits, conn2Hits atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.Header.Get("Authorization") {
				case "Bearer sk-vid-1":
					conn1Hits.Add(1)
					w.WriteHeader(http.StatusTooManyRequests)
					_, _ = w.Write([]byte(`{"error":{"message":"rate limit exceeded"}}`))
				case "Bearer sk-vid-2":
					conn2Hits.Add(1)
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(videoOK))
				}
			}))
			defer upstream.Close()

			database, cleanup := setupEmbeddingsTestDB(t)
			defer cleanup()
			seedJSONMediaConn(t, database, "conn-vid-1", "openai", "sk-vid-1", upstream.URL, 1)
			seedJSONMediaConn(t, database, "conn-vid-2", "openai", "sk-vid-2", upstream.URL, 2)
			handler := newTestMediaHandler(db.NewRepo(database))

			rec := postMedia(handler.HandleVideoGenerations, "/v1/videos/generations", videoBody, nil, false)
			if rec.Code != http.StatusOK || rec.Body.String() != videoOK {
				t.Fatalf("response = (%d, %s), want (200, %s)", rec.Code, rec.Body.String(), videoOK)
			}
			if conn1Hits.Load() != 1 || conn2Hits.Load() != 1 {
				t.Errorf("hits = (%d, %d), want (1, 1)", conn1Hits.Load(), conn2Hits.Load())
			}
		})

		t.Run("does_not_rotate_on_500", func(t *testing.T) {
			const serverErr = `{"error":{"message":"upstream internal error"}}`
			var conn1Hits, conn2Hits atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.Header.Get("Authorization") {
				case "Bearer sk-vid-1":
					conn1Hits.Add(1)
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = w.Write([]byte(serverErr))
				case "Bearer sk-vid-2":
					conn2Hits.Add(1)
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(videoOK))
				}
			}))
			defer upstream.Close()

			database, cleanup := setupEmbeddingsTestDB(t)
			defer cleanup()
			seedJSONMediaConn(t, database, "conn-vid-1", "openai", "sk-vid-1", upstream.URL, 1)
			seedJSONMediaConn(t, database, "conn-vid-2", "openai", "sk-vid-2", upstream.URL, 2)
			handler := newTestMediaHandler(db.NewRepo(database))

			rec := postMedia(handler.HandleVideoGenerations, "/v1/videos/generations", videoBody, nil, false)
			if rec.Code != http.StatusInternalServerError || rec.Body.String() != serverErr {
				t.Fatalf("response = (%d, %s), want (500, %s)", rec.Code, rec.Body.String(), serverErr)
			}
			if conn1Hits.Load() != 1 || conn2Hits.Load() != 0 {
				t.Fatalf("hits = (%d, %d), want (1, 0)", conn1Hits.Load(), conn2Hits.Load())
			}

			// 500 on billable video creation must not lock conn-vid-1 either.
			rec2 := postMedia(handler.HandleVideoGenerations, "/v1/videos/generations", videoBody, nil, false)
			if rec2.Code != http.StatusInternalServerError {
				t.Fatalf("second status = %d, want 500", rec2.Code)
			}
			if conn1Hits.Load() != 2 || conn2Hits.Load() != 0 {
				t.Errorf("second hits = (%d, %d), want (2, 0)", conn1Hits.Load(), conn2Hits.Load())
			}
		})
	})

	t.Run("respects_pinned_x_connection_id_header", func(t *testing.T) {
		var conn1Hits, conn2Hits atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.Header.Get("Authorization") {
			case "Bearer sk-img-1":
				conn1Hits.Add(1)
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
			case "Bearer sk-img-2":
				conn2Hits.Add(1)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(okImagePayload))
			}
		}))
		defer upstream.Close()

		database, cleanup := setupEmbeddingsTestDB(t)
		defer cleanup()
		seedJSONMediaConn(t, database, "conn-img-1", "openai", "sk-img-1", upstream.URL, 1)
		seedJSONMediaConn(t, database, "conn-img-2", "openai", "sk-img-2", upstream.URL, 2)
		handler := newTestMediaHandler(db.NewRepo(database))

		rec := postMedia(
			handler.HandleImages,
			"/v1/images/generations",
			`{"model":"openai/dall-e-3","prompt":"cat"}`,
			map[string]string{"x-connection-id": "conn-img-1"},
			false,
		)
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("status = %d, want 429: %s", rec.Code, rec.Body.String())
		}
		if conn1Hits.Load() != 1 || conn2Hits.Load() != 0 {
			t.Errorf("pinned hits = (%d, %d), want (1, 0)", conn1Hits.Load(), conn2Hits.Load())
		}
	})

	t.Run("returns_last_upstream_error_when_all_connections_exhausted", func(t *testing.T) {
		const quotaErr = `{"error":{"message":"quota exceeded"}}`
		var conn1Hits, conn2Hits atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Header.Get("Authorization") {
			case "Bearer sk-img-1":
				conn1Hits.Add(1)
			case "Bearer sk-img-2":
				conn2Hits.Add(1)
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Upstream-Err", "retry-later")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(quotaErr))
		}))
		defer upstream.Close()

		database, cleanup := setupEmbeddingsTestDB(t)
		defer cleanup()
		seedJSONMediaConn(t, database, "conn-img-1", "openai", "sk-img-1", upstream.URL, 1)
		seedJSONMediaConn(t, database, "conn-img-2", "openai", "sk-img-2", upstream.URL, 2)
		handler := newTestMediaHandler(db.NewRepo(database))

		rec := postMedia(handler.HandleImages, "/v1/images/generations", `{"model":"openai/dall-e-3","prompt":"cat"}`, nil, false)
		if rec.Code != http.StatusTooManyRequests || rec.Body.String() != quotaErr {
			t.Fatalf("exhausted response = (%d, %s), want (429, %s)", rec.Code, rec.Body.String(), quotaErr)
		}
		if got := rec.Header().Get("X-Upstream-Err"); got != "retry-later" {
			t.Errorf("X-Upstream-Err = %q, want %q", got, "retry-later")
		}
		if conn1Hits.Load() != 1 || conn2Hits.Load() != 1 {
			t.Errorf("exhausted hits = (%d, %d), want (1, 1)", conn1Hits.Load(), conn2Hits.Load())
		}
	})

	t.Run("probe_context_rotates_without_persisting_lock", func(t *testing.T) {
		var conn1Hits, conn2Hits atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.Header.Get("Authorization") {
			case "Bearer sk-img-1":
				conn1Hits.Add(1)
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":{"message":"quota exceeded"}}`))
			case "Bearer sk-img-2":
				conn2Hits.Add(1)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(okImagePayload))
			}
		}))
		defer upstream.Close()

		database, cleanup := setupEmbeddingsTestDB(t)
		defer cleanup()
		seedJSONMediaConn(t, database, "conn-img-1", "openai", "sk-img-1", upstream.URL, 1)
		seedJSONMediaConn(t, database, "conn-img-2", "openai", "sk-img-2", upstream.URL, 2)
		handler := newTestMediaHandler(db.NewRepo(database))

		const imgBody = `{"model":"openai/dall-e-3","prompt":"cat"}`
		rec := postMedia(handler.HandleImages, "/v1/images/generations", imgBody, nil, true)
		if rec.Code != http.StatusOK || rec.Body.String() != okImagePayload {
			t.Fatalf("probe response = (%d, %s), want (200, %s)", rec.Code, rec.Body.String(), okImagePayload)
		}
		if conn1Hits.Load() != 1 || conn2Hits.Load() != 1 {
			t.Fatalf("probe hits = (%d, %d), want (1, 1)", conn1Hits.Load(), conn2Hits.Load())
		}

		// Follow-up non-probe request must still try conn-img-1 first.
		rec2 := postMedia(handler.HandleImages, "/v1/images/generations", imgBody, nil, false)
		if rec2.Code != http.StatusOK {
			t.Fatalf("follow-up status = %d, want 200", rec2.Code)
		}
		if conn1Hits.Load() != 2 || conn2Hits.Load() != 2 {
			t.Errorf("follow-up hits = (%d, %d), want (2, 2)", conn1Hits.Load(), conn2Hits.Load())
		}
	})
}

func TestForwardSystemoneRequest_ConnectionRotation(t *testing.T) {
	const okSysPayload = `{"model":"gpt-4o","answers":{"q":{"type":"noul","noul":0.5}}}`
	const sysBody = `{"model":"openai/gpt-4o","state":"ok","questions":{"q":{"type":"noul"}}}`

	postSys := func(handler *MediaHandler, probe bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/systemone", strings.NewReader(sysBody))
		req.Header.Set("Content-Type", "application/json")
		if probe {
			req = req.WithContext(handlerutil.WithProbeContext(req.Context()))
		}
		rec := httptest.NewRecorder()
		handler.HandleSystemone(rec, req)
		return rec
	}

	t.Run("rotates_on_429_and_locks_failed_connection", func(t *testing.T) {
		var conn1Hits, conn2Hits atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.Header.Get("Authorization") {
			case "Bearer sk-sys-1":
				conn1Hits.Add(1)
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":{"message":"rate limit exceeded"}}`))
			case "Bearer sk-sys-2":
				conn2Hits.Add(1)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(okSysPayload))
			default:
				w.WriteHeader(http.StatusUnauthorized)
			}
		}))
		defer upstream.Close()

		database, cleanup := setupEmbeddingsTestDB(t)
		defer cleanup()
		seedJSONMediaConn(t, database, "conn-sys-1", "openai", "sk-sys-1", upstream.URL, 1)
		seedJSONMediaConn(t, database, "conn-sys-2", "openai", "sk-sys-2", upstream.URL, 2)
		handler := newTestMediaHandler(db.NewRepo(database))

		rec := postSys(handler, false)
		if rec.Code != http.StatusOK || rec.Body.String() != okSysPayload {
			t.Fatalf("first response = (%d, %s), want (200, %s)", rec.Code, rec.Body.String(), okSysPayload)
		}
		if conn1Hits.Load() != 1 || conn2Hits.Load() != 1 {
			t.Fatalf("first hits = (%d, %d), want (1, 1)", conn1Hits.Load(), conn2Hits.Load())
		}

		rec2 := postSys(handler, false)
		if rec2.Code != http.StatusOK || rec2.Body.String() != okSysPayload {
			t.Fatalf("second response = (%d, %s), want (200, %s)", rec2.Code, rec2.Body.String(), okSysPayload)
		}
		if conn1Hits.Load() != 1 || conn2Hits.Load() != 2 {
			t.Errorf("second hits = (%d, %d), want (1, 2)", conn1Hits.Load(), conn2Hits.Load())
		}
	})

	t.Run("does_not_rotate_or_lock_on_non_retryable_400", func(t *testing.T) {
		const clientErr = `{"error":{"message":"bad state"}}`
		var conn1Hits, conn2Hits atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.Header.Get("Authorization") {
			case "Bearer sk-sys-1":
				conn1Hits.Add(1)
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(clientErr))
			case "Bearer sk-sys-2":
				conn2Hits.Add(1)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(okSysPayload))
			}
		}))
		defer upstream.Close()

		database, cleanup := setupEmbeddingsTestDB(t)
		defer cleanup()
		seedJSONMediaConn(t, database, "conn-sys-1", "openai", "sk-sys-1", upstream.URL, 1)
		seedJSONMediaConn(t, database, "conn-sys-2", "openai", "sk-sys-2", upstream.URL, 2)
		handler := newTestMediaHandler(db.NewRepo(database))

		rec := postSys(handler, false)
		if rec.Code != http.StatusBadRequest || rec.Body.String() != clientErr {
			t.Fatalf("first response = (%d, %s), want (400, %s)", rec.Code, rec.Body.String(), clientErr)
		}
		if conn1Hits.Load() != 1 || conn2Hits.Load() != 0 {
			t.Fatalf("first hits = (%d, %d), want (1, 0)", conn1Hits.Load(), conn2Hits.Load())
		}

		rec2 := postSys(handler, false)
		if rec2.Code != http.StatusBadRequest {
			t.Fatalf("second status = %d, want 400", rec2.Code)
		}
		if conn1Hits.Load() != 2 || conn2Hits.Load() != 0 {
			t.Errorf("second hits = (%d, %d), want (2, 0)", conn1Hits.Load(), conn2Hits.Load())
		}
	})

	t.Run("probe_context_rotates_without_mutating_cooldown_locks", func(t *testing.T) {
		var conn1Hits, conn2Hits atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.Header.Get("Authorization") {
			case "Bearer sk-sys-1":
				conn1Hits.Add(1)
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":{"message":"quota exceeded"}}`))
			case "Bearer sk-sys-2":
				conn2Hits.Add(1)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(okSysPayload))
			}
		}))
		defer upstream.Close()

		database, cleanup := setupEmbeddingsTestDB(t)
		defer cleanup()
		seedJSONMediaConn(t, database, "conn-sys-1", "openai", "sk-sys-1", upstream.URL, 1)
		seedJSONMediaConn(t, database, "conn-sys-2", "openai", "sk-sys-2", upstream.URL, 2)
		handler := newTestMediaHandler(db.NewRepo(database))

		rec := postSys(handler, true)
		if rec.Code != http.StatusOK {
			t.Fatalf("probe status = %d, body = %s", rec.Code, rec.Body.String())
		}
		if conn1Hits.Load() != 1 || conn2Hits.Load() != 1 {
			t.Fatalf("probe hits = (%d, %d), want (1, 1)", conn1Hits.Load(), conn2Hits.Load())
		}

		rec2 := postSys(handler, false)
		if rec2.Code != http.StatusOK {
			t.Fatalf("follow-up status = %d, body = %s", rec2.Code, rec2.Body.String())
		}
		if conn1Hits.Load() != 2 || conn2Hits.Load() != 2 {
			t.Errorf("follow-up hits = (%d, %d), want (2, 2)", conn1Hits.Load(), conn2Hits.Load())
		}
	})
}

func TestForwardMiMoSpeech_ConnectionRotation(t *testing.T) {
	const mimoBody = `{"model":"xiaomi-mimo/mimo-v2.5-tts","input":"hello"}`
	const okMiMoJSON = `{"choices":[{"message":{"audio":{"data":"aGVsbG8=","format":"wav"}}}]}`

	postMiMo := func(handler *MediaHandler, headers map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(mimoBody))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		handler.HandleAudioSpeech(rec, req)
		return rec
	}

	t.Run("rotates_on_429_and_locks_failed_connection", func(t *testing.T) {
		var conn1Hits, conn2Hits atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.Header.Get("Authorization") {
			case "Bearer sk-mimo-1":
				conn1Hits.Add(1)
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":{"message":"quota exceeded"}}`))
			case "Bearer sk-mimo-2":
				conn2Hits.Add(1)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(okMiMoJSON))
			default:
				w.WriteHeader(http.StatusUnauthorized)
			}
		}))
		defer upstream.Close()

		database, cleanup := setupEmbeddingsTestDB(t)
		defer cleanup()
		seedJSONMediaConn(t, database, "conn-mimo-1", "xiaomi-mimo", "sk-mimo-1", upstream.URL+"/v1/chat/completions", 1)
		seedJSONMediaConn(t, database, "conn-mimo-2", "xiaomi-mimo", "sk-mimo-2", upstream.URL+"/v1/chat/completions", 2)
		handler := newTestMediaHandler(db.NewRepo(database))

		rec := postMiMo(handler, nil)
		if rec.Code != http.StatusOK || rec.Body.String() != "hello" {
			t.Fatalf("first response = (%d, %q), want (200, \"hello\")", rec.Code, rec.Body.String())
		}
		if conn1Hits.Load() != 1 || conn2Hits.Load() != 1 {
			t.Fatalf("first hits = (%d, %d), want (1, 1)", conn1Hits.Load(), conn2Hits.Load())
		}

		rec2 := postMiMo(handler, nil)
		if rec2.Code != http.StatusOK || rec2.Body.String() != "hello" {
			t.Fatalf("second response = (%d, %q), want (200, \"hello\")", rec2.Code, rec2.Body.String())
		}
		if conn1Hits.Load() != 1 || conn2Hits.Load() != 2 {
			t.Errorf("second hits = (%d, %d), want (1, 2)", conn1Hits.Load(), conn2Hits.Load())
		}
	})

	t.Run("rotates_on_401_and_respects_pinned_connection", func(t *testing.T) {
		var conn1Hits, conn2Hits atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.Header.Get("Authorization") {
			case "Bearer sk-mimo-1":
				conn1Hits.Add(1)
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
			case "Bearer sk-mimo-2":
				conn2Hits.Add(1)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(okMiMoJSON))
			}
		}))
		defer upstream.Close()

		database, cleanup := setupEmbeddingsTestDB(t)
		defer cleanup()
		seedJSONMediaConn(t, database, "conn-mimo-1", "xiaomi-mimo", "sk-mimo-1", upstream.URL+"/v1/chat/completions", 1)
		seedJSONMediaConn(t, database, "conn-mimo-2", "xiaomi-mimo", "sk-mimo-2", upstream.URL+"/v1/chat/completions", 2)
		handler := newTestMediaHandler(db.NewRepo(database))

		rec := postMiMo(handler, nil)
		if rec.Code != http.StatusOK || rec.Body.String() != "hello" {
			t.Fatalf("unpinned response = (%d, %q), want (200, \"hello\")", rec.Code, rec.Body.String())
		}
		if conn1Hits.Load() != 1 || conn2Hits.Load() != 1 {
			t.Fatalf("unpinned hits = (%d, %d), want (1, 1)", conn1Hits.Load(), conn2Hits.Load())
		}

		// Pinned connection on a fresh DB must not rotate on 401.
		conn1Hits.Store(0)
		conn2Hits.Store(0)
		database2, cleanup2 := setupEmbeddingsTestDB(t)
		defer cleanup2()
		seedJSONMediaConn(t, database2, "conn-mimo-1", "xiaomi-mimo", "sk-mimo-1", upstream.URL+"/v1/chat/completions", 1)
		seedJSONMediaConn(t, database2, "conn-mimo-2", "xiaomi-mimo", "sk-mimo-2", upstream.URL+"/v1/chat/completions", 2)
		handler2 := newTestMediaHandler(db.NewRepo(database2))

		recPinned := postMiMo(handler2, map[string]string{"X-Connection-ID": "conn-mimo-1"})
		if recPinned.Code != http.StatusBadGateway {
			t.Fatalf("pinned status = %d, want 502: %s", recPinned.Code, recPinned.Body.String())
		}
		if conn1Hits.Load() != 1 || conn2Hits.Load() != 0 {
			t.Errorf("pinned hits = (%d, %d), want (1, 0)", conn1Hits.Load(), conn2Hits.Load())
		}
	})
}
