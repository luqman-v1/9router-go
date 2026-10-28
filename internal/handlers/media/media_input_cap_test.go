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
)

// newCountingUpstream returns an STT-shaped fake that records how many times an
// upstream call actually arrived. The media contract requires an oversized
// request to be refused BEFORE any upstream call, so the count is the point.
func newCountingUpstream(t *testing.T, hits *int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"text":"ok"}`))
	}))
}

// lowerMediaCaps shrinks the caps for one test so no test has to allocate a
// real 64MB part. The caps are package vars for exactly this reason.
func lowerMediaCaps(t *testing.T, items int, itemBytes int64) {
	t.Helper()
	prevItems, prevBytes := MediaMaxItems, MediaMaxItemBytes
	MediaMaxItems, MediaMaxItemBytes = items, itemBytes
	t.Cleanup(func() { MediaMaxItems, MediaMaxItemBytes = prevItems, prevBytes })
}

// sttMultipart builds a /v1/audio/transcriptions multipart body with one file
// part per entry, preserving order.
func sttMultipart(t *testing.T, model string, files [][]byte) (*bytes.Buffer, string) {
	t.Helper()
	return mediaMultipart(t, model, "file", "clip", ".mp3", files)
}

// imageMultipart is sttMultipart with image parts, which is what the per-item
// cap applies to: audio and video are exempt, so a fixture named *.mp3 cannot
// test that cap at all.
func imageMultipart(t *testing.T, model string, files [][]byte) (*bytes.Buffer, string) {
	t.Helper()
	return mediaMultipart(t, model, "image", "frame", ".png", files)
}

func mediaMultipart(t *testing.T, model, field, stem, ext string, files [][]byte) (*bytes.Buffer, string) {
	t.Helper()
	buf := &bytes.Buffer{}
	mw := multipart.NewWriter(buf)
	if err := mw.WriteField("model", model); err != nil {
		t.Fatalf("write model field: %v", err)
	}
	for i, data := range files {
		fw, err := mw.CreateFormFile(field, stem+string(rune('a'+i))+ext)
		if err != nil {
			t.Fatalf("create form file: %v", err)
		}
		if _, err := fw.Write(data); err != nil {
			t.Fatalf("write form file: %v", err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	return buf, mw.FormDataContentType()
}

func runSTT(t *testing.T, upstreamURL string, body *bytes.Buffer, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	database, cleanup := setupMultimodalTestDB(t)
	t.Cleanup(cleanup)
	seedOpenAIConn(t, database, upstreamURL)
	handler := newTestMediaHandler(db.NewRepo(database))

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	handler.HandleAudioTranscriptions(rec, req)
	return rec
}

// TestMediaInputCap_RefusesTooManyItems is red before the cap exists: five
// tiny file parts are forwarded upstream, so the request is billed instead of
// refused.
func TestMediaInputCap_RefusesTooManyItems(t *testing.T) {
	lowerMediaCaps(t, 4, 1<<20)
	var hits int32
	upstream := newCountingUpstream(t, &hits)
	defer upstream.Close()

	files := make([][]byte, 5)
	for i := range files {
		files[i] = []byte("tiny")
	}
	body, ct := sttMultipart(t, "whisper-1", files)

	rec := runSTT(t, upstream.URL, body, ct)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("5 file parts with cap 4: expected 413, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Errorf("oversized request reached upstream %d time(s); the cap must be enforced before any upstream call", got)
	}
	if !strings.Contains(rec.Body.String(), "too large") {
		t.Errorf("413 body does not name the cap: %s", rec.Body.String())
	}
}

// TestMediaInputCap_RefusesOversizedSingleItem is red before the cap exists: a
// single part over the per-item ceiling is passed through.
//
// The fixture is an image because that is what the per-item cap bounds. Audio
// and video are exempt — a 30MB recording is the documented STT input, not
// abuse — so testing this cap with a .mp3 part tested nothing.
func TestMediaInputCap_RefusesOversizedSingleItem(t *testing.T) {
	lowerMediaCaps(t, 16, 4096)
	var hits int32
	upstream := newCountingUpstream(t, &hits)
	defer upstream.Close()

	body, ct := imageMultipart(t, "dall-e-3", [][]byte{bytes.Repeat([]byte("a"), 8192)})

	rec := runSTT(t, upstream.URL, body, ct)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("8KB item with 4KB cap: expected 413, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Errorf("oversized request reached upstream %d time(s); the cap must be enforced before any upstream call", got)
	}
}

// TestMediaUsage_ReportsItemsAndBytes is red before the accounting exists: the
// response carries no request-level usage at all. These lanes pass the upstream
// body through verbatim, so the accounting rides on response headers — the same
// shape X-Token-Quota-* already uses.
func TestMediaUsage_ReportsItemsAndBytes(t *testing.T) {
	var hits int32
	upstream := newCountingUpstream(t, &hits)
	defer upstream.Close()

	payload := bytes.Repeat([]byte("b"), 1024)
	body, ct := sttMultipart(t, "whisper-1", [][]byte{payload})

	rec := runSTT(t, upstream.URL, body, ct)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Media-Items"); got != "1" {
		t.Errorf("X-Media-Items = %q, want 1", got)
	}
	if got := rec.Header().Get("X-Media-Bytes"); got != "1024" {
		t.Errorf("X-Media-Bytes = %q, want 1024", got)
	}
}

// TestMediaUsage_EnvelopeIsNotCorrupted guards the pass-through contract: the
// accounting must not reshape the upstream body the media lanes return.
func TestMediaUsage_EnvelopeIsNotCorrupted(t *testing.T) {
	var hits int32
	upstream := newCountingUpstream(t, &hits)
	defer upstream.Close()

	body, ct := sttMultipart(t, "whisper-1", [][]byte{[]byte("abc")})
	rec := runSTT(t, upstream.URL, body, ct)

	var resp struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response body is no longer JSON: %v (%s)", err, rec.Body.String())
	}
	if resp.Text != "ok" {
		t.Errorf("upstream body was reshaped: %s", rec.Body.String())
	}
}
