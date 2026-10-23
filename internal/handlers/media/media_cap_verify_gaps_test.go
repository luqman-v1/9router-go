package media

import (
	"bytes"
	"encoding/base64"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"sync/atomic"

	"9router/proxy/internal/db"
	"testing"
)

// The tests here pin the ways the first pass at G20c could be walked around:
// a media part with a media field name but no filename, bare base64 in the
// image fields, a request over the sum cap but under every other one, and an
// upstream allowed to rewrite the usage numbers the gateway itself measured.

// noFilenamePart builds a body whose media part is named like a media field but
// carries no `filename=` attribute. The part media type is a parameter so the
// filename-less bypass can be exercised on audio while the per-item cap — which
// exempts audio — is exercised on an image.
func noFilenamePart(t *testing.T, model string, payload []byte, partType string) (*bytes.Buffer, string) {
	t.Helper()
	buf := &bytes.Buffer{}
	mw := multipart.NewWriter(buf)
	if err := mw.WriteField("model", model); err != nil {
		t.Fatalf("write model field: %v", err)
	}
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", `form-data; name="file"`)
	h.Set("Content-Type", partType)
	fw, err := mw.CreatePart(h)
	if err != nil {
		t.Fatalf("create part: %v", err)
	}
	if _, err := fw.Write(payload); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	return buf, mw.FormDataContentType()
}

// TestMediaInputCap_CountsMediaPartWithoutFilename is red before the fix: a
// part named "file" with no filename measured as zero media, so any number of
// them — and any size — reached upstream unbilled by the cap.
func TestMediaInputCap_CountsMediaPartWithoutFilename(t *testing.T) {
	lowerMediaCaps(t, 2, 1<<20)
	var hits int32
	upstream := newCountingUpstream(t, &hits)
	defer upstream.Close()

	bodies := make([][]byte, 3)
	for i := range bodies {
		bodies[i] = []byte("tiny")
	}
	// Three separate parts, each named "file" without a filename.
	buf := &bytes.Buffer{}
	mw := multipart.NewWriter(buf)
	_ = mw.WriteField("model", "whisper-1")
	for range bodies {
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", `form-data; name="file"`)
		fw, err := mw.CreatePart(h)
		if err != nil {
			t.Fatalf("create part: %v", err)
		}
		_, _ = fw.Write([]byte("tiny"))
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}

	rec := runSTT(t, upstream.URL, buf, mw.FormDataContentType())

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("3 filename-less file parts with cap 2: expected 413, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Errorf("the part reached upstream %d time(s); the cap must count it before any upstream call", got)
	}
}

// TestMediaInputCap_RefusesOversizedFilenamelessPart is the size half of the
// same bypass: one part over the per-item ceiling, still no filename. The part
// is an image because the per-item cap bounds images; audio is exempt.
func TestMediaInputCap_RefusesOversizedFilenamelessPart(t *testing.T) {
	lowerMediaCaps(t, 16, 4096)
	var hits int32
	upstream := newCountingUpstream(t, &hits)
	defer upstream.Close()

	body, ct := noFilenamePart(t, "dall-e-3", bytes.Repeat([]byte("a"), 8192), "image/png")
	rec := runSTT(t, upstream.URL, body, ct)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("8KB filename-less image part with 4KB cap: expected 413, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Errorf("oversized part reached upstream %d time(s)", got)
	}
}

// TestMediaInputCap_CountsBareBase64ImageFields is red before the fix: the
// image lanes accept bare base64 and wrap it into a data URI themselves, but
// the measurement only counted strings starting with "data:", so a multi-megabyte
// bare-base64 image measured as zero media.
func TestMediaInputCap_CountsBareBase64ImageFields(t *testing.T) {
	payload := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("a"), 8<<10))
	if len(payload) < 1024 {
		t.Fatalf("fixture too small to be treated as base64: %d bytes", len(payload))
	}

	use, err := measureMediaInput("application/json", []byte(`{"model":"x","image":"`+payload+`"}`))
	if err != nil {
		t.Fatalf("measure: %v", err)
	}
	// The measurement is the decoded size, not the encoded text: DecodedLen
	// ignores the '=' padding this payload carries, base64DecodedLen does not.
	wantBytes := int64(8 << 10)
	if use.Items != 1 || use.Bytes != wantBytes {
		t.Errorf("bare base64 in \"image\": measured %d item(s) / %d bytes, want 1 / %d (decoded)",
			use.Items, use.Bytes, wantBytes)
	}

	// The list form the image edit lanes accept.
	use, err = measureMediaInput("application/json", []byte(`{"images":["`+payload+`","`+payload+`"]}`))
	if err != nil {
		t.Fatalf("measure: %v", err)
	}
	if use.Items != 2 {
		t.Errorf("bare base64 in \"images\": measured %d items, want 2", use.Items)
	}
}

// TestMediaInputCap_IgnoresProseInImageField guards the other direction: the
// base64 sniff must not treat an ordinary sentence in a media-named field as
// media, or every request mentioning an image would count as one.
func TestMediaInputCap_IgnoresProseInImageField(t *testing.T) {
	prose := strings.Repeat("please make the sky a softer blue, ", 60) // >1024 bytes
	use, err := measureMediaInput("application/json", []byte(`{"image":"`+prose+`"}`))
	if err != nil {
		t.Fatalf("measure: %v", err)
	}
	if use.Items != 0 {
		t.Errorf("prose in \"image\" measured as %d media item(s), want 0", use.Items)
	}
}

// TestMediaInputCap_CountsEscapedBase64ImageFields is red before the fix: the
// JSON walk sliced the RAW token text out of the body and rejected the value at
// the first backslash that was not \n \r \t. But the lanes forward the DECODED
// string — encoding/json/v2 removes the escapes before they see it — so a legal
// escape at the head of a payload made the guard measure zero while the whole
// payload went upstream. The escape \u0041 is 'A' and \/ is '/', both in the base64
// alphabet, so the decoded value is byte-identical to the original payload.
func TestMediaInputCap_CountsEscapedBase64ImageFields(t *testing.T) {
	payload := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("a"), 8<<10))
	if len(payload) < 1024 {
		t.Fatalf("fixture too small to be treated as base64: %d bytes", len(payload))
	}
	// The escape only has to land in the first mediaProbeLen bytes, which is the
	// window the sniff samples.
	const unicodeEscapeA = `\u0041`
	const escapedSolidus = `\/`
	cases := []struct{ name, escaped string }{
		{"unicode escape", unicodeEscapeA + payload[1:]},
		{"escaped solidus", escapedSolidus + payload[1:]},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			use, err := measureMediaInput("application/json", []byte(`{"model":"x","image":"`+tc.escaped+`"}`))
			if err != nil {
				t.Fatalf("measure: %v", err)
			}
			wantBytes := int64(8 << 10)
			if use.Items != 1 || use.Bytes != wantBytes {
				t.Errorf("escaped bare base64 in \"image\": measured %d item(s) / %d bytes, want 1 / %d (decoded)",
					use.Items, use.Bytes, wantBytes)
			}
		})
	}

	// The count cap falls with the size half: nine escaped images measured as
	// zero, so the 8-item cap could not fire either.
	t.Run("count cap", func(t *testing.T) {
		var b strings.Builder
		b.WriteString(`{"images":[`)
		for i := 0; i < MediaMaxItems+1; i++ {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(`"` + `A` + payload[1:] + `"`)
		}
		b.WriteString(`]}`)
		use, err := measureMediaInput("application/json", []byte(b.String()))
		if err != nil {
			t.Fatalf("measure: %v", err)
		}
		if use.Items != MediaMaxItems+1 {
			t.Errorf("measured %d items, want %d — an escaped payload must still count", use.Items, MediaMaxItems+1)
		}
	})
}

// TestMediaInputCap_NoSumCap pins the decision that replaced MediaMaxTotalBytes.
// The old sum cap was 64MB and refused the 30-60 minute recording that
// middleware.MediaMaxBodySize was raised to 128MB to accept; raising it to the
// ceiling would have made it a number that can never fire. The item count and
// the transport ceiling bound a request's total on their own, so the cap is
// gone and this test is what keeps it gone: a request whose media sums far past
// the old 64MB and whose items each fit is accepted.
func TestMediaInputCap_NoSumCap(t *testing.T) {
	if _, ok := any(MediaMaxItems).(int); !ok {
		t.Fatal("unreachable")
	}
	// Two 40MB audio parts: over the old 64MB sum cap, under the 128MB
	// transport ceiling, each under the per-item cap.
	const part = 40 << 20
	use := mediaInputUse{Items: 2, Bytes: 2 * part}
	if msg := use.capViolation(); msg != "" {
		t.Fatalf("a 80MB request of two audio items must not be refused by a sum cap: %s", msg)
	}
}

// TestMediaUsage_UpstreamCannotOverwriteUsageHeaders is red before the fix:
// every upstream header was copied into the response map unconditionally, so a
// provider emitting X-Media-Items replaced the number the gateway measured.
// TestMediaInputCap_BoundaryIsExact pins that each cap is a cap and not a
// ceiling-with-slack: a request exactly at the limit passes, and one byte over
// it is refused. A comparison that refused at the limit would drop a legal
// request, and one that used `>=` on the wrong side would let an oversized
// request through; both read as "the cap works" from the outside.
func TestMediaInputCap_BoundaryIsExact(t *testing.T) {
	for _, tc := range []struct {
		name  string
		items int
		itemB int64
		files []int
		want  int
		why   string
	}{
		{"items exactly at cap", 4, 1 << 20, []int{1, 1, 1, 1}, http.StatusOK,
			"four items with a cap of four is legal"},
		{"items one over cap", 4, 1 << 20, []int{1, 1, 1, 1, 1}, http.StatusRequestEntityTooLarge,
			"five items with a cap of four is one over"},
		{"item exactly at cap", 16, 1024, []int{1024}, http.StatusOK,
			"an item exactly the cap size is legal"},
		{"item one byte over cap", 16, 1024, []int{1025}, http.StatusRequestEntityTooLarge,
			"an item one byte over the cap is refused"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prevItems, prevItem := MediaMaxItems, MediaMaxItemBytes
			MediaMaxItems, MediaMaxItemBytes = tc.items, tc.itemB
			t.Cleanup(func() {
				MediaMaxItems, MediaMaxItemBytes = prevItems, prevItem
			})

			var hits int32
			upstream := newCountingUpstream(t, &hits)
			defer upstream.Close()

			files := make([][]byte, len(tc.files))
			for i, n := range tc.files {
				files[i] = bytes.Repeat([]byte("a"), n)
			}
			// An image when the per-item cap is what is under test: audio and
			// video are exempt from it, so an .mp3 fixture would pass at any
			// size and the case would assert nothing.
			build := sttMultipart
			model := "whisper-1"
			if tc.itemB < 1<<20 {
				build, model = imageMultipart, "dall-e-3"
			}
			body, ct := build(t, model, files)
			rec := runSTT(t, upstream.URL, body, ct)

			if rec.Code != tc.want {
				t.Fatalf("%s: got %d, want %d: %s", tc.why, rec.Code, tc.want, rec.Body.String())
			}
			if tc.want != http.StatusOK {
				if got := atomic.LoadInt32(&hits); got != 0 {
					t.Errorf("%s: refused request still reached upstream %d time(s)", tc.why, got)
				}
			}
		})
	}
}

func TestMediaUsage_UpstreamCannotOverwriteUsageHeaders(t *testing.T) {
	var hits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Media-Items", "999")
		w.Header().Set("X-Media-Bytes", "999999")
		w.Header().Set("X-Upstream-Trace", "trace-1")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"text":"ok"}`))
	}))
	defer upstream.Close()

	body, ct := sttMultipart(t, "whisper-1", [][]byte{[]byte("abc")})
	rec := runSTT(t, upstream.URL, body, ct)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Media-Items"); got != "1" {
		t.Errorf("X-Media-Items = %q, want the measured 1 — the upstream overwrote it", got)
	}
	if got := rec.Header().Get("X-Media-Bytes"); got != "3" {
		t.Errorf("X-Media-Bytes = %q, want the measured 3 — the upstream overwrote it", got)
	}
	// The fix must not be "drop every upstream header".
	if got := rec.Header().Get("X-Upstream-Trace"); got != "trace-1" {
		t.Errorf("X-Upstream-Trace = %q, want trace-1: an unrelated upstream header was dropped", got)
	}
}

// TestMediaUsage_SystemoneAndMiMoCarryUsage pins that the two lanes that leave
// forwardMediaRequest still measure their request. They were the lanes the "one
// funnel" claim silently skipped.
func TestMediaUsage_SystemoneAndMiMoCarryUsage(t *testing.T) {
	var hits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"audio":{"data":"AAAA","format":"wav"}}}]}`))
	}))
	defer upstream.Close()

	t.Run("Systemone", func(t *testing.T) {
		database, cleanup := setupMultimodalTestDB(t)
		defer cleanup()
		seedOpenAIConn(t, database, upstream.URL)
		handler := newTestMediaHandler(db.NewRepo(database))

		body := `{"model":"openai/gpt-4o","state":"s","questions":{"q":"a"}}`
		req := httptest.NewRequest(http.MethodPost, "/v1/systemone", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.HandleSystemone(rec, req)

		if rec.Code >= 500 {
			t.Fatalf("systemone lane failed: %d %s", rec.Code, rec.Body.String())
		}
		if rec.Header().Get("X-Media-Items") == "" {
			t.Error("the systemone lane reports no request-level usage; it bypasses the funnel")
		}
	})

	t.Run("MiMoSpeech", func(t *testing.T) {
		database, cleanup := setupMultimodalTestDB(t)
		defer cleanup()
		seedMiMoConn(t, database, upstream.URL)
		handler := newTestMediaHandler(db.NewRepo(database))

		body := `{"model":"xiaomi-mimo/tts","input":"hello"}`
		req := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.HandleAudioSpeech(rec, req)

		if rec.Code >= 500 {
			t.Fatalf("MiMo speech lane failed: %d %s", rec.Code, rec.Body.String())
		}
		if rec.Header().Get("X-Media-Items") == "" {
			t.Error("the MiMo speech lane reports no request-level usage; it bypasses the funnel")
		}
	})
}
