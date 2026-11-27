package media

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

// buildTranslationMultipart mirrors what an OpenAI SDK client sends to
// /v1/audio/translations: a binary `file` part plus text fields. modelField is
// omitted when empty, which is how a client relying on the gateway default
// behaves.
func buildTranslationMultipart(t *testing.T, filename string, audio []byte, modelField string) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	filePart, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := filePart.Write(audio); err != nil {
		t.Fatalf("write audio: %v", err)
	}
	if modelField != "" {
		if err := mw.WriteField("model", modelField); err != nil {
			t.Fatalf("write model field: %v", err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return body, mw.FormDataContentType()
}

// TestHandleAudioTranslations_ForwardsMultipart pins the whole wire contract:
// the request reaches <base>/v1/audio/translations, arrives as multipart, and
// carries the resolved model. Deleting the endpoint from the URL builder or
// from the route table changes the path this test observes.
func TestHandleAudioTranslations_ForwardsMultipart(t *testing.T) {
	var gotPath, gotContentType, gotModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		if mr, err := r.MultipartReader(); err == nil {
			for {
				p, err := mr.NextPart()
				if err != nil {
					break
				}
				if p.FormName() == "model" {
					b, _ := io.ReadAll(p)
					gotModel = strings.TrimSpace(string(b))
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"hello world"}`))
	}))
	defer upstream.Close()

	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	seedOpenAIConn(t, database, upstream.URL)
	handler := newTestMediaHandler(db.NewRepo(database))

	body, contentType := buildTranslationMultipart(t, "speech.mp3", []byte("dummy mp3 audio"), "openai/whisper-1")
	req := httptest.NewRequest("POST", "/v1/audio/translations", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()

	handler.HandleAudioTranslations(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if gotPath != "/v1/audio/translations" {
		t.Errorf("upstream path = %q, want /v1/audio/translations", gotPath)
	}
	if mt, _, err := mime.ParseMediaType(gotContentType); err != nil || mt != "multipart/form-data" {
		t.Errorf("upstream content-type = %q, want multipart/form-data", gotContentType)
	}
	// The provider prefix is stripped at the boundary; the upstream must see the
	// bare model id, not "openai/whisper-1".
	if gotModel != "whisper-1" {
		t.Errorf("upstream model part = %q, want whisper-1", gotModel)
	}
	if !strings.Contains(rec.Body.String(), "hello world") {
		t.Errorf("expected translation text in response, got: %s", rec.Body.String())
	}
}

// TestHandleAudioTranslations_DefaultModelResolves covers the client that sends
// no model field at all. The lane resolves whisper-1 as its default, so the
// request succeeds instead of failing with "could not resolve model". Replacing
// that default with "" turns this into a 400, which is what it locks.
func TestHandleAudioTranslations_DefaultModelResolves(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"ok"}`))
	}))
	defer upstream.Close()

	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	seedOpenAIConn(t, database, upstream.URL)
	handler := newTestMediaHandler(db.NewRepo(database))

	body, contentType := buildTranslationMultipart(t, "speech.mp3", []byte("dummy mp3 audio"), "")
	req := httptest.NewRequest("POST", "/v1/audio/translations", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()

	handler.HandleAudioTranslations(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from the whisper-1 default, got %d: %s", rec.Code, rec.Body.String())
	}
	if gotPath != "/v1/audio/translations" {
		t.Errorf("upstream path = %q, want /v1/audio/translations", gotPath)
	}
}

// TestHandleAudioTranslations_MalformedMultipartRefused asserts the guard, not
// an incidental status: a multipart body the gateway cannot measure is refused
// before any upstream call, and the error names the malformed body rather than
// a model that could not be resolved.
func TestHandleAudioTranslations_MalformedMultipartRefused(t *testing.T) {
	var upstreamCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"text":"should not be reached"}`))
	}))
	defer upstream.Close()

	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	seedOpenAIConn(t, database, upstream.URL)
	handler := newTestMediaHandler(db.NewRepo(database))

	// A declared boundary with a truncated body: no closed part, so the
	// multipart reader fails mid-stream.
	req := httptest.NewRequest("POST", "/v1/audio/translations", strings.NewReader("--boundary\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.mp3\"\r\n\r\npartial audio"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=boundary")
	rec := httptest.NewRecorder()

	handler.HandleAudioTranslations(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "malformed media request body") {
		t.Errorf("expected malformed-body message, got: %s", rec.Body.String())
	}
	if upstreamCalls != 0 {
		t.Errorf("malformed body reached upstream %d time(s), want 0", upstreamCalls)
	}
}
