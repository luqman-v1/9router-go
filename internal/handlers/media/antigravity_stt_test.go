package media

import (
	"bytes"
	json "encoding/json/v2"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
)

func TestHandleSTT_Antigravity(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v1internal:generateContent") {
			t.Errorf("expected path /v1internal:generateContent, got %s", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer ag-token-stt-123" {
			t.Errorf("expected Bearer ag-token-stt-123, got %q", auth)
		}
		// Same identity pair as the image lane: the UA says cli/1.5.9, so
		// X-Client-Version must carry that version too.
		if v := r.Header.Get("X-Client-Version"); v != providers.AntigravityCLIVersion {
			t.Errorf("X-Client-Version = %q, want %q", v, providers.AntigravityCLIVersion)
		}
		if ua := r.Header.Get("User-Agent"); ua != providers.AntigravityCLIUserAgent {
			t.Errorf("User-Agent = %q, want %q", ua, providers.AntigravityCLIUserAgent)
		}

		var reqBody struct {
			Project string `json:"project"`
			Model   string `json:"model"`
			Request struct {
				Contents []struct {
					Role  string `json:"role"`
					Parts []struct {
						Text       string `json:"text"`
						InlineData struct {
							MimeType string `json:"mimeType"`
							Data     string `json:"data"`
						} `json:"inlineData"`
					} `json:"parts"`
				} `json:"contents"`
			} `json:"request"`
		}
		if err := json.UnmarshalRead(r.Body, &reqBody); err != nil {
			t.Errorf("decode request body: %v", err)
		}

		if reqBody.Project != "test-stt-proj" {
			t.Errorf("expected project test-stt-proj, got %s", reqBody.Project)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"response": {
				"candidates": [{
					"content": {
						"parts": [{
							"text": "Hello, this is a test audio transcription."
						}]
					}
				}]
			}
		}`))
	}))
	defer upstream.Close()

	ag := providers.KnownProviders["antigravity"]
	origBase := ag.BaseURL
	ag.BaseURL = upstream.URL
	providers.KnownProviders["antigravity"] = ag
	defer func() {
		ag.BaseURL = origBase
		providers.KnownProviders["antigravity"] = ag
	}()

	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()

	connData, _ := json.Marshal(map[string]any{
		"apiKey":    "ag-token-stt-123",
		"projectId": "test-stt-proj",
	})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-ag-stt', 'antigravity', 'oauth', 'Antigravity STT Test', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(connData)); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	// Create multipart body
	bodyBuf := &bytes.Buffer{}
	mpWriter := multipart.NewWriter(bodyBuf)
	filePart, err := mpWriter.CreateFormFile("file", "test.mp3")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	filePart.Write([]byte("dummy mp3 audio content"))
	_ = mpWriter.WriteField("model", "antigravity/gemini-2.5-flash")
	_ = mpWriter.WriteField("response_format", "json")
	mpWriter.Close()

	req := httptest.NewRequest("POST", "/v1/audio/transcriptions", bodyBuf)
	req.Header.Set("Content-Type", mpWriter.FormDataContentType())
	rec := httptest.NewRecorder()

	handler.HandleAudioTranscriptions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}

	if resp.Text != "Hello, this is a test audio transcription." {
		t.Errorf("expected transcribed text, got %q", resp.Text)
	}
}

func TestHandleSTT_AntigravityStopsOnNonRetryable400AndRotatesOn429(t *testing.T) {
	const okSTT = `{"response":{"candidates":[{"content":{"parts":[{"text":"transcribed"}]}}]}}`

	makeMultipartReq := func(t *testing.T) *http.Request {
		t.Helper()
		bodyBuf := &bytes.Buffer{}
		mpWriter := multipart.NewWriter(bodyBuf)
		filePart, err := mpWriter.CreateFormFile("file", "test.mp3")
		if err != nil {
			t.Fatalf("create form file: %v", err)
		}
		_, _ = filePart.Write([]byte("dummy mp3 audio content"))
		_ = mpWriter.WriteField("model", "antigravity/gemini-2.5-flash")
		_ = mpWriter.WriteField("response_format", "json")
		_ = mpWriter.Close()

		req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", bodyBuf)
		req.Header.Set("Content-Type", mpWriter.FormDataContentType())
		return req
	}

	seedTwo := func(t *testing.T, baseURL string) *MediaHandler {
		t.Helper()
		ag := providers.KnownProviders["antigravity"]
		origBase := ag.BaseURL
		ag.BaseURL = baseURL
		providers.KnownProviders["antigravity"] = ag
		t.Cleanup(func() {
			ag.BaseURL = origBase
			providers.KnownProviders["antigravity"] = ag
		})

		database, cleanup := setupMultimodalTestDB(t)
		t.Cleanup(cleanup)
		for i, id := range []string{"conn-ag-stt-1", "conn-ag-stt-2"} {
			connData, _ := json.Marshal(map[string]any{"apiKey": "ag-tok-" + id, "projectId": "test-stt-proj"})
			if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
				(?, 'antigravity', 'oauth', 'AG', ?, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, id, i+1, string(connData)); err != nil {
				t.Fatalf("seed connection: %v", err)
			}
		}
		return newTestMediaHandler(db.NewRepo(database))
	}

	t.Run("stops_on_non_retryable_400", func(t *testing.T) {
		var conn1Hits, conn2Hits atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.Header.Get("Authorization") {
			case "Bearer ag-tok-conn-ag-stt-1":
				conn1Hits.Add(1)
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"code":400,"message":"unsupported audio"}}`))
			case "Bearer ag-tok-conn-ag-stt-2":
				conn2Hits.Add(1)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(okSTT))
			}
		}))
		defer upstream.Close()

		handler := seedTwo(t, upstream.URL)
		rec := httptest.NewRecorder()
		handler.HandleAudioTranscriptions(rec, makeMultipartReq(t))

		if rec.Code == http.StatusOK {
			t.Fatalf("expected non-200 on 400 client error, got 200")
		}
		if conn1Hits.Load() != 1 || conn2Hits.Load() != 0 {
			t.Fatalf("hits = (%d, %d), want (1, 0)", conn1Hits.Load(), conn2Hits.Load())
		}
	})

	t.Run("rotates_on_retryable_429", func(t *testing.T) {
		var conn1Hits, conn2Hits atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.Header.Get("Authorization") {
			case "Bearer ag-tok-conn-ag-stt-1":
				conn1Hits.Add(1)
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":{"code":429,"message":"quota exceeded"}}`))
			case "Bearer ag-tok-conn-ag-stt-2":
				conn2Hits.Add(1)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(okSTT))
			}
		}))
		defer upstream.Close()

		handler := seedTwo(t, upstream.URL)
		rec := httptest.NewRecorder()
		handler.HandleAudioTranscriptions(rec, makeMultipartReq(t))

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 after 429 failover, got %d: %s", rec.Code, rec.Body.String())
		}
		if conn1Hits.Load() != 1 || conn2Hits.Load() != 1 {
			t.Fatalf("hits = (%d, %d), want (1, 1)", conn1Hits.Load(), conn2Hits.Load())
		}
	})
}
