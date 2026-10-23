package media

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/middleware"
	"9router/proxy/internal/models"
)

// The tests here pin the ways the caps could still be walked around after the
// first pass, and the decisions that replaced caps which refused the workload
// this repository documents. Each is red before its fix.

// liveHeap returns the heap in use right now, after collecting garbage twice so
// what is left is reachable data rather than the previous measurement's litter.
func liveHeap(t *testing.T) uint64 {
	t.Helper()
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// malformedTailMultipart builds a body whose file part is well-formed and is
// followed by a part whose header block cannot be parsed. The media lanes break
// out of their own parse loop on exactly this error and forward the file they
// already read, so the guard has to refuse the body instead of measuring zero.
func malformedTailMultipart(t *testing.T, payload []byte) (*bytes.Buffer, string) {
	t.Helper()
	const boundary = "9routerTestBoundary"
	buf := &bytes.Buffer{}
	buf.WriteString("--" + boundary + "\r\n")
	buf.WriteString("Content-Disposition: form-data; name=\"model\"\r\n\r\nwhisper-1\r\n")
	buf.WriteString("--" + boundary + "\r\n")
	buf.WriteString("Content-Disposition: form-data; name=\"file\"; filename=\"frame.png\"\r\n")
	buf.WriteString("Content-Type: image/png\r\n\r\n")
	buf.Write(payload)
	buf.WriteString("\r\n")
	// A header line with no colon: multipart.Reader refuses it, but only after
	// the file part has already been read and handed to the lane.
	buf.WriteString("--" + boundary + "\r\n")
	buf.WriteString("this header line has no colon\r\n\r\n")
	buf.WriteString("--" + boundary + "--\r\n")
	return buf, "multipart/form-data; boundary=" + boundary
}

// TestMediaInputCap_MalformedMultipartIsRefused is BLOCKING-1: a parse error
// used to be read as "could not measure, so do not refuse", and the lanes then
// forwarded whatever they had already read. A large audio part behind one bad
// header reached upstream with items=0 and status 200.
func TestMediaInputCap_MalformedMultipartIsRefused(t *testing.T) {
	var hits int32
	upstream := newCountingUpstream(t, &hits)
	defer upstream.Close()

	body, ct := malformedTailMultipart(t, bytes.Repeat([]byte("a"), 4096))
	rec := runSTT(t, upstream.URL, body, ct)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed multipart tail: expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Errorf("unmeasurable body reached upstream %d time(s); the guard must fail closed", got)
	}
	if !strings.Contains(rec.Body.String(), "malformed") {
		t.Errorf("400 does not say the body is malformed: %s", rec.Body.String())
	}
}

// TestMediaInputCap_MalformedMultipartStillNamesAnExceededCap pins the half
// that must NOT change: when the partial measurement already exceeds a cap, the
// answer is the 413 that names it, not a generic 400. "Too large" is the
// actionable answer even when the body is also broken.
func TestMediaInputCap_MalformedMultipartStillNamesAnExceededCap(t *testing.T) {
	lowerMediaCaps(t, 16, 1024)
	var hits int32
	upstream := newCountingUpstream(t, &hits)
	defer upstream.Close()

	body, ct := malformedTailMultipart(t, bytes.Repeat([]byte("a"), 8192))
	rec := runSTT(t, upstream.URL, body, ct)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("8KB part over a 1KB cap, then a bad header: expected 413, got %d: %s",
			rec.Code, rec.Body.String())
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Errorf("oversized body reached upstream %d time(s)", got)
	}
}

// TestMediaInputCap_CapitalizedContentTypeIsMultipart is BLOCKING-2: RFC 9110
// says a media type is case-insensitive, mime.ParseMediaType lowercases it, and
// every lane parses it that way — but the guard matched "multipart/form-data"
// with strings.Contains. A "Multipart/form-data" request was measured as JSON,
// where a multipart body yields zero media, so no cap fired.
func TestMediaInputCap_CapitalizedContentTypeIsMultipart(t *testing.T) {
	lowerMediaCaps(t, 2, 1<<20)
	var hits int32
	upstream := newCountingUpstream(t, &hits)
	defer upstream.Close()

	body, ct := sttMultipart(t, "whisper-1", [][]byte{[]byte("a"), []byte("b"), []byte("c")})
	capitalized := strings.Replace(ct, "multipart/form-data", "Multipart/Form-Data", 1)
	if capitalized == ct {
		t.Fatalf("fixture did not change the media type: %q", ct)
	}

	rec := runSTT(t, upstream.URL, body, capitalized)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("3 parts with a cap of 2 under a capitalized media type: expected 413, got %d: %s",
			rec.Code, rec.Body.String())
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Errorf("capitalized-media-type request reached upstream %d time(s)", got)
	}
}

// TestMediaInputCap_CountsURLSafeAndWrappedBase64 is BLOCKING-3. Both are legal
// base64 that codexImageDataURL wraps into a data: URL without re-encoding, so
// both reach the provider at full size. The sniff accepted only the standard
// alphabet and rejected any whitespace, so each measured as zero media.
func TestMediaInputCap_CountsURLSafeAndWrappedBase64(t *testing.T) {
	raw := bytes.Repeat([]byte{0xfb, 0xef, 0xbe}, 1400)
	urlSafe := base64.RawURLEncoding.EncodeToString(raw)
	if !strings.ContainsAny(urlSafe, "-_") {
		t.Fatalf("fixture is not URL-safe base64: %q", urlSafe[:32])
	}
	// The same payload wrapped at 76 columns, as `base64` emits it. The
	// newlines are escaped because a literal one is illegal inside a JSON
	// string — the escaped form is the one that actually arrives.
	std := base64.StdEncoding.EncodeToString(raw)
	var wrapped strings.Builder
	for i := 0; i < len(std); i += 76 {
		wrapped.WriteString(std[i:min(i+76, len(std))])
		wrapped.WriteString(`\n`)
	}

	for _, tc := range []struct{ name, payload string }{
		{"url-safe", urlSafe},
		{"line-wrapped", wrapped.String()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			use, err := measureMediaInput("application/json", []byte(`{"image":"`+tc.payload+`"}`))
			if err != nil {
				t.Fatalf("measure: %v", err)
			}
			if use.Items != 1 {
				t.Fatalf("%s base64 in \"image\" measured %d items, want 1", tc.name, use.Items)
			}
			if use.Bytes < int64(len(raw))/2 {
				t.Errorf("%s base64 measured %d bytes; want about the %d decoded",
					tc.name, use.Bytes, len(raw))
			}
		})
	}
}

// TestMediaInputCap_AudioAndVideoAreExemptFromItemCap is MAJOR-4: the per-item
// cap refused exactly the two workloads middleware.MediaMaxBodySize exists for
// — a 30-60 minute recording and a 15-50MB clip (middleware/max_body.go). Both
// are documented, supported input, so both pass the per-item cap; the item
// count and the transport ceiling are what bound them.
func TestMediaInputCap_AudioAndVideoAreExemptFromItemCap(t *testing.T) {
	lowerMediaCaps(t, 8, 1<<20) // 1MB per item, the same shape as the shipped cap
	for _, tc := range []struct{ name, filename string }{
		{"audio", "recording.mp3"},
		{"video", "clip.mp4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits int32
			upstream := newCountingUpstream(t, &hits)
			defer upstream.Close()

			body, ct := mediaMultipart(t, "whisper-1", "file", "media", tc.filename[len(tc.filename)-4:],
				[][]byte{bytes.Repeat([]byte("a"), 4<<20)})
			rec := runSTT(t, upstream.URL, body, ct)

			if rec.Code != http.StatusOK {
				t.Fatalf("a 4MB %s part with a 1MB image cap: expected 200, got %d: %s",
					tc.name, rec.Code, rec.Body.String())
			}
		})
	}
}

// TestMediaInputCap_ImageAndMultipartAgreeOnDecodedBytes is MAJOR-5: the JSON
// branch counted the encoded data URI and the multipart branch counted the
// decoded bytes, so the same image was refused as JSON and accepted as
// multipart. Both now measure what the payload decodes to.
func TestMediaInputCap_ImageAndMultipartAgreeOnDecodedBytes(t *testing.T) {
	const decoded = 900 << 10
	raw := bytes.Repeat([]byte("a"), decoded)
	encoded := base64.StdEncoding.EncodeToString(raw)

	use, err := measureMediaInput("application/json",
		[]byte(`{"image":"data:image/png;base64,`+encoded+`"}`))
	if err != nil {
		t.Fatalf("measure json: %v", err)
	}
	if use.Bytes != int64(decoded) {
		t.Errorf("data URI measured %d bytes, want the %d it decodes to (encoded text is %d)",
			use.Bytes, decoded, len(encoded))
	}

	body, ct := mediaMultipart(t, "dall-e-3", "image", "frame", ".png", [][]byte{raw})
	mpUse, mpErr := measureMediaInput(ct, body.Bytes())
	if mpErr != nil {
		t.Fatalf("measure multipart: %v", mpErr)
	}
	if mpUse.Bytes != use.Bytes {
		t.Errorf("the same %d-byte image measured %d as JSON and %d as multipart",
			decoded, use.Bytes, mpUse.Bytes)
	}
}

// TestMeasureJSONInput_DoesNotDuplicateTheBody is MAJOR-6: unmarshaling the
// body into a generic tree allocated a second copy of it — an 8MB body measured
// +8MB of heap — at the moment the cap was checked. The streaming walk borrows
// the body instead.
func TestMeasureJSONInput_DoesNotDuplicateTheBody(t *testing.T) {
	const size = 8 << 20
	payload := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("a"), size))
	body := []byte(`{"model":"m","prompt":"p","image":"data:image/png;base64,` + payload + `"}`)

	before := liveHeap(t)
	use, err := measureMediaInput("application/json", body)
	after := liveHeap(t)
	if err != nil {
		t.Fatalf("measure: %v", err)
	}
	if use.Items != 1 {
		t.Fatalf("expected 1 media item, got %d", use.Items)
	}
	if growth := int64(after) - int64(before); growth > size/2 {
		t.Errorf("measuring a %d-byte body grew the live heap by %d bytes; the walk must not copy the body",
			len(body), growth)
	}
}

// TestMediaInputCap_IgnoresADataColonPrompt is the other half of the data: URI
// narrowing: a prompt that merely begins with "data:" is not a payload. The
// prefix alone used to count it as one media item.
func TestMediaInputCap_IgnoresADataColonPrompt(t *testing.T) {
	use, err := measureMediaInput("application/json",
		[]byte(`{"prompt":"data: the rows below are stale, please re-check them"}`))
	if err != nil {
		t.Fatalf("measure: %v", err)
	}
	if use.Items != 0 {
		t.Errorf("a prompt beginning with \"data:\" measured %d media items, want 0", use.Items)
	}
}

// TestMediaInputCap_SizeIsReportedBeforeCount pins the order the caps are
// checked in. A request that carries both problems used to be answered with the
// item count, which tells the caller less than the size does: the count says
// "send fewer", the size says which file to shrink.
func TestMediaInputCap_SizeIsReportedBeforeCount(t *testing.T) {
	lowerMediaCaps(t, 2, 1024)
	use := mediaInputUse{Items: 9, Bytes: 9 * 100 << 10, MaxItem: 100 << 10}
	msg := use.capViolation()
	if !strings.Contains(msg, "one item is") {
		t.Fatalf("a request over both caps named %q; the per-item size is the actionable one", msg)
	}
}

// TestMediaCaps_ShippedValues pins the numbers clients are held to. Every other
// test lowers them, so without this one a change to the shipped ceiling would
// pass the whole suite.
func TestMediaCaps_ShippedValues(t *testing.T) {
	if MediaMaxItems != 8 {
		t.Errorf("MediaMaxItems = %d, want 8", MediaMaxItems)
	}
	if MediaMaxItemBytes != 25<<20 {
		t.Errorf("MediaMaxItemBytes = %d, want %d (25MB)", MediaMaxItemBytes, 25<<20)
	}
	// The transport ceiling is what bounds a large audio or video part now that
	// the per-item cap exempts them, so the two must not drift apart.
	if got := int64(middleware.MediaMaxBodySize); got != 128<<20 {
		t.Errorf("middleware.MediaMaxBodySize = %d, want %d (128MB)", got, 128<<20)
	}
}

// TestMediaInputCap_AccessIsCheckedBeforeTheCap pins the ordering finding: a
// key that may not call the model was answered 413 with the limit and the
// measured figure, instead of the 403 it is owed. The limit is policy detail a
// denied key has no business learning.
func TestMediaInputCap_AccessIsCheckedBeforeTheCap(t *testing.T) {
	var hits int32
	upstream := newCountingUpstream(t, &hits)
	defer upstream.Close()

	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	seedOpenAIConn(t, database, upstream.URL)
	// The fixture schema in the media tests is the minimal one; the allowlist
	// table is part of the real core schema, so it is created here.
	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS api_key_model_access (
		api_key_id TEXT NOT NULL, model TEXT NOT NULL, created_at TEXT NOT NULL,
		PRIMARY KEY (api_key_id, model))`); err != nil {
		t.Fatalf("create allowlist table: %v", err)
	}
	repo := db.NewRepo(database)
	if err := repo.SetAllowedModels("key-restricted", []string{"openai/some-other-model"}); err != nil {
		t.Fatalf("set allowlist: %v", err)
	}
	handler := newTestMediaHandler(repo)

	lowerMediaCaps(t, 1, 1<<20)
	body, ct := sttMultipart(t, "whisper-1", [][]byte{[]byte("a"), []byte("b")})
	rec := httptest.NewRecorder()
	req := requestWithAPIKey(httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", body),
		&models.APIKey{ID: "key-restricted", Key: "sk-restricted", IsActive: 1})
	req.Header.Set("Content-Type", ct)
	handler.HandleAudioTranscriptions(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("oversized request from a key that may not call the model: expected 403, got %d: %s",
			rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "too large") {
		t.Errorf("403 leaked the cap: %s", rec.Body.String())
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Errorf("denied request reached upstream %d time(s)", got)
	}
}

// requestWithAPIKey attaches an authenticated key the way RequireApiKey does,
// so a handler's EnforceModelAccess reads a real allowlist.
func requestWithAPIKey(r *http.Request, key *models.APIKey) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), middleware.ApiKeyContextKey, key))
}
