package media

import (
	"database/sql"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
)

func TestResolveGeminiImageInput(t *testing.T) {
	dataURI := "data:image/webp;base64,QUJD"
	part := resolveGeminiImageInput(dataURI)
	if part == nil {
		t.Fatal("data URI rejected")
	}
	inline := part["inlineData"].(map[string]string)
	if inline["mimeType"] != "image/webp" || inline["data"] != "QUJD" {
		t.Errorf("inlineData = %v", inline)
	}

	bare := strings.Repeat("QUJDREVG", 20)
	part = resolveGeminiImageInput(bare)
	if part == nil {
		t.Fatal("bare base64 rejected")
	}
	if part["inlineData"].(map[string]string)["mimeType"] != "image/png" {
		t.Errorf("bare base64 mime = %v", part)
	}

	for _, rejected := range []string{"", "https://example.com/x.png", "short"} {
		if resolveGeminiImageInput(rejected) != nil {
			t.Errorf("input %q should not become a part", rejected)
		}
	}
}

func TestGeminiQuotaResetSeconds(t *testing.T) {
	future := time.Now().Add(90 * time.Second).UTC().Format(time.RFC3339)
	body := []byte(`{"error":{"code":429,"details":[{"metadata":{"quotaResetTimeStamp":"` + future + `"}}]}}`)
	sec, ok := geminiQuotaResetSeconds(body)
	if !ok || sec < 60 || sec > 95 {
		t.Errorf("quota reset = %d/%v, want ~90s", sec, ok)
	}

	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	if _, ok := geminiQuotaResetSeconds([]byte(`{"error":{"details":[{"metadata":{"quotaResetTimeStamp":"` + past + `"}}]}}`)); ok {
		t.Error("past reset accepted")
	}
	if _, ok := geminiQuotaResetSeconds([]byte(`{"error":{}}`)); ok {
		t.Error("missing reset accepted")
	}
}

func TestGeminiImageResponseBody(t *testing.T) {
	raw := []byte(`{"candidates":[{"content":{"parts":[
		{"text":"here you go"},
		{"inlineData":{"mimeType":"image/png","data":"QUJD"}}
	]}}]}`)
	out, err := geminiImageResponseBody(raw, "a cat")
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("output not JSON: %v", err)
	}
	data := m["data"].([]any)
	if len(data) != 1 || data[0].(map[string]any)["b64_json"] != "QUJD" {
		t.Errorf("images = %v", data)
	}

	if _, err := geminiImageResponseBody([]byte(`{"candidates":[{"content":{"parts":[{"text":"no image"}]}}]}`), "p"); err == nil {
		t.Error("image-less response accepted")
	}
}

func seedGeminiConn(t *testing.T, rawDB *sql.DB, id, apiKey, baseURL string, priority int) {
	t.Helper()
	data, _ := json.Marshal(map[string]any{"apiKey": apiKey, "baseUrl": baseURL})
	if _, err := rawDB.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		(?, 'gemini', 'apikey', ?, ?, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, id, "Gemini "+id, priority, string(data)); err != nil {
		t.Fatalf("seed gemini connection %s: %v", id, err)
	}
}

func TestHandleGeminiImage_ConnectionRotation(t *testing.T) {
	const successBody = `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"Q09OTjI="}}]}}]}`
	futureReset := time.Now().Add(120 * time.Second).UTC().Format(time.RFC3339)
	quota429Body := `{"error":{"code":429,"message":"quota exhausted","details":[{"metadata":{"quotaResetTimeStamp":"` + futureReset + `"}}]}}`

	postGeminiImage := func(handler *MediaHandler, headers map[string]string, probe bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"gemini/gemini-3.1-flash-image","prompt":"a sunset"}`))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		if probe {
			req = req.WithContext(handlerutil.WithProbeContext(req.Context()))
		}
		rec := httptest.NewRecorder()
		handler.HandleImages(rec, req)
		return rec
	}

	assertConn2ImageOK := func(t *testing.T, rec *httptest.ResponseRecorder) {
		t.Helper()
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		var out struct {
			Data []struct {
				B64JSON string `json:"b64_json"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(out.Data) != 1 || out.Data[0].B64JSON != "Q09OTjI=" {
			t.Fatalf("data = %+v, want single b64_json Q09OTjI=", out.Data)
		}
	}

	t.Run("rotates_on_quota_429_and_locks_with_reset_timestamp", func(t *testing.T) {
		var conn1Hits, conn2Hits atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.Header.Get("Authorization") {
			case "Bearer key-1":
				conn1Hits.Add(1)
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(quota429Body))
			case "Bearer key-2":
				conn2Hits.Add(1)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(successBody))
			default:
				t.Errorf("unexpected auth header: %q", r.Header.Get("Authorization"))
				w.WriteHeader(http.StatusUnauthorized)
			}
		}))
		defer upstream.Close()

		database, cleanup := setupMultimodalTestDB(t)
		defer cleanup()
		repo := db.NewRepo(database)
		seedGeminiConn(t, database, "conn-gm-1", "key-1", upstream.URL+"/v1beta/openai/chat/completions", 1)
		seedGeminiConn(t, database, "conn-gm-2", "key-2", upstream.URL+"/v1beta/openai/chat/completions", 2)
		handler := newTestMediaHandler(repo)

		assertConn2ImageOK(t, postGeminiImage(handler, nil, false))
		if conn1Hits.Load() != 1 || conn2Hits.Load() != 1 {
			t.Fatalf("first request hits = (%d, %d), want (1, 1)", conn1Hits.Load(), conn2Hits.Load())
		}

		// Cooled conn-gm-1 must be skipped on the follow-up request.
		assertConn2ImageOK(t, postGeminiImage(handler, nil, false))
		if conn1Hits.Load() != 1 || conn2Hits.Load() != 2 {
			t.Errorf("second request hits = (%d, %d), want (1, 2) with conn-gm-1 locked", conn1Hits.Load(), conn2Hits.Load())
		}
	})

	t.Run("rotates_on_401_and_429_without_reset_timestamp", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			status int
			body   string
		}{
			{"401_unauthorized", http.StatusUnauthorized, `{"error":{"code":401,"message":"API key invalid","status":"UNAUTHENTICATED"}}`},
			{"429_no_reset_timestamp", http.StatusTooManyRequests, `{"error":{"code":429,"message":"Too Many Requests","status":"RESOURCE_EXHAUSTED"}}`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				var conn1Hits, conn2Hits atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					switch r.Header.Get("Authorization") {
					case "Bearer key-1":
						conn1Hits.Add(1)
						w.WriteHeader(tc.status)
						_, _ = w.Write([]byte(tc.body))
					case "Bearer key-2":
						conn2Hits.Add(1)
						w.WriteHeader(http.StatusOK)
						_, _ = w.Write([]byte(successBody))
					}
				}))
				defer upstream.Close()

				database, cleanup := setupMultimodalTestDB(t)
				defer cleanup()
				repo := db.NewRepo(database)
				seedGeminiConn(t, database, "conn-gm-1", "key-1", upstream.URL, 1)
				seedGeminiConn(t, database, "conn-gm-2", "key-2", upstream.URL, 2)
				handler := newTestMediaHandler(repo)

				assertConn2ImageOK(t, postGeminiImage(handler, nil, false))
				if conn1Hits.Load() != 1 || conn2Hits.Load() != 1 {
					t.Fatalf("first request hits = (%d, %d), want (1, 1)", conn1Hits.Load(), conn2Hits.Load())
				}

				assertConn2ImageOK(t, postGeminiImage(handler, nil, false))
				if conn1Hits.Load() != 1 || conn2Hits.Load() != 2 {
					t.Errorf("second request hits = (%d, %d), want (1, 2)", conn1Hits.Load(), conn2Hits.Load())
				}
			})
		}
	})

	t.Run("does_not_rotate_or_lock_on_non_retryable_400", func(t *testing.T) {
		var conn1Hits, conn2Hits atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.Header.Get("Authorization") {
			case "Bearer key-1":
				conn1Hits.Add(1)
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"code":400,"message":"Invalid argument: prompt too long","status":"INVALID_ARGUMENT"}}`))
			case "Bearer key-2":
				conn2Hits.Add(1)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(successBody))
			}
		}))
		defer upstream.Close()

		database, cleanup := setupMultimodalTestDB(t)
		defer cleanup()
		repo := db.NewRepo(database)
		seedGeminiConn(t, database, "conn-gm-1", "key-1", upstream.URL, 1)
		seedGeminiConn(t, database, "conn-gm-2", "key-2", upstream.URL, 2)
		handler := newTestMediaHandler(repo)

		rec := postGeminiImage(handler, nil, false)
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502: %s", rec.Code, rec.Body.String())
		}
		if conn1Hits.Load() != 1 || conn2Hits.Load() != 0 {
			t.Fatalf("first request hits = (%d, %d), want (1, 0)", conn1Hits.Load(), conn2Hits.Load())
		}

		// Non-retryable 400 must leave conn-gm-1 unlocked for subsequent requests.
		rec2 := postGeminiImage(handler, nil, false)
		if rec2.Code != http.StatusBadGateway {
			t.Fatalf("second status = %d, want 502", rec2.Code)
		}
		if conn1Hits.Load() != 2 || conn2Hits.Load() != 0 {
			t.Errorf("second request hits = (%d, %d), want (2, 0)", conn1Hits.Load(), conn2Hits.Load())
		}
	})

	t.Run("respects_pinned_x_connection_id_header", func(t *testing.T) {
		var conn1Hits, conn2Hits atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.Header.Get("Authorization") {
			case "Bearer key-1":
				conn1Hits.Add(1)
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(quota429Body))
			case "Bearer key-2":
				conn2Hits.Add(1)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(successBody))
			}
		}))
		defer upstream.Close()

		database, cleanup := setupMultimodalTestDB(t)
		defer cleanup()
		repo := db.NewRepo(database)
		seedGeminiConn(t, database, "conn-gm-1", "key-1", upstream.URL, 1)
		seedGeminiConn(t, database, "conn-gm-2", "key-2", upstream.URL, 2)
		handler := newTestMediaHandler(repo)

		rec := postGeminiImage(handler, map[string]string{"x-connection-id": "conn-gm-1"}, false)
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
			switch r.Header.Get("Authorization") {
			case "Bearer key-1":
				conn1Hits.Add(1)
			case "Bearer key-2":
				conn2Hits.Add(1)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(quota429Body))
		}))
		defer upstream.Close()

		database, cleanup := setupMultimodalTestDB(t)
		defer cleanup()
		repo := db.NewRepo(database)
		seedGeminiConn(t, database, "conn-gm-1", "key-1", upstream.URL, 1)
		seedGeminiConn(t, database, "conn-gm-2", "key-2", upstream.URL, 2)
		handler := newTestMediaHandler(repo)

		rec := postGeminiImage(handler, nil, false)
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
			w.Header().Set("Content-Type", "application/json")
			switch r.Header.Get("Authorization") {
			case "Bearer key-1":
				conn1Hits.Add(1)
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(quota429Body))
			case "Bearer key-2":
				conn2Hits.Add(1)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(successBody))
			}
		}))
		defer upstream.Close()

		database, cleanup := setupMultimodalTestDB(t)
		defer cleanup()
		repo := db.NewRepo(database)
		seedGeminiConn(t, database, "conn-gm-1", "key-1", upstream.URL, 1)
		seedGeminiConn(t, database, "conn-gm-2", "key-2", upstream.URL, 2)
		handler := newTestMediaHandler(repo)

		assertConn2ImageOK(t, postGeminiImage(handler, nil, true))
		if conn1Hits.Load() != 1 || conn2Hits.Load() != 1 {
			t.Fatalf("probe hits = (%d, %d), want (1, 1)", conn1Hits.Load(), conn2Hits.Load())
		}

		// Follow-up non-probe request must still try conn-gm-1 first because probe did not persist a lock.
		assertConn2ImageOK(t, postGeminiImage(handler, nil, false))
		if conn1Hits.Load() != 2 || conn2Hits.Load() != 2 {
			t.Errorf("follow-up hits = (%d, %d), want (2, 2)", conn1Hits.Load(), conn2Hits.Load())
		}
	})
}
