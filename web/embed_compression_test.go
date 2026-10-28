package web_test

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"9router/proxy/web"
)

// The dashboard bundle is served from the gateway itself, so the bytes cross
// the operator's own link. These tests pin the pre-compression contract:
// a client that accepts gzip or brotli must get a compressed body with the
// matching Content-Encoding, and a client that accepts neither must get the
// original file intact.
//
// The variants are produced by `web/scripts/precompress.ts` at build time, so
// they exist in web/dist next to the originals.

// assetPath returns the dist-relative path of the built JavaScript bundle.
func assetPath(t *testing.T) string {
	t.Helper()
	entries, err := os.ReadDir("dist/assets")
	if err != nil {
		t.Fatalf("web/dist/assets unavailable: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".js") {
			return "assets/" + e.Name()
		}
	}
	t.Fatal("no .js asset in web/dist/assets")
	return ""
}

func TestHandlerServesCompressedAssets(t *testing.T) {
	h := web.Handler()
	js := assetPath(t)
	if js == "" {
		return
	}

	t.Run("the best encoding the client accepts is served", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/"+js, nil)
		req.Header.Set("Accept-Encoding", "gzip, deflate, br")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if got := rec.Header().Get("Content-Encoding"); got != "br" {
			t.Errorf("Content-Encoding = %q, want %q (brotli is preferred on a tie)", got, "br")
		}
		if got := rec.Header().Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
			t.Errorf("Vary = %q, want it to contain Accept-Encoding", got)
		}
		if got := rec.Header().Get("Content-Length"); got == "" {
			t.Error("Content-Length is missing: the compressed body has a known size")
		}

		// The body must be smaller than the original, or the compression is
		// not doing anything.
		original, err := os.ReadFile(filepath.Join("dist", js))
		if err != nil {
			t.Fatalf("read original: %v", err)
		}
		if rec.Body.Len() >= len(original) {
			t.Errorf("compressed body is %d bytes, original is %d: no compression happened", rec.Body.Len(), len(original))
		}
	})

	t.Run("a gzip body decodes to the original file", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/"+js, nil)
		req.Header.Set("Accept-Encoding", "gzip")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
			t.Fatalf("Content-Encoding = %q, want %q", got, "gzip")
		}

		reader, err := gzip.NewReader(rec.Body)
		if err != nil {
			t.Fatalf("body is not gzip-decodable: %v", err)
		}
		defer reader.Close()
		plain, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("read gzip body: %v", err)
		}

		original, err := os.ReadFile(filepath.Join("dist", js))
		if err != nil {
			t.Fatalf("read original: %v", err)
		}
		if string(plain) != string(original) {
			t.Errorf("decoded body differs from the original: %d vs %d bytes", len(plain), len(original))
		}
	})

	t.Run("identity is served when the client accepts no encoding", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/"+js, nil)
		req.Header.Set("Accept-Encoding", "identity")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if got := rec.Header().Get("Content-Encoding"); got != "" {
			t.Errorf("Content-Encoding = %q, want none", got)
		}
		body := rec.Body.Bytes()
		if len(body) == 0 {
			t.Fatal("body is empty")
		}
		// An uncompressed JavaScript file does not start with the gzip magic
		// number; a compressed one served under the wrong header would.
		if len(body) > 2 && body[0] == 0x1f && body[1] == 0x8b {
			t.Error("body is gzip-compressed but was served without Content-Encoding")
		}

		original, err := os.ReadFile(filepath.Join("dist", js))
		if err != nil {
			t.Fatalf("read original: %v", err)
		}
		if string(body) != string(original) {
			t.Error("the uncompressed body differs from the original file")
		}
	})
}

// The negotiation has to prefer brotli over gzip when the client lists both
// without quality values — which is exactly what Chrome sends. A handler that
// keeps the first match hands that browser the larger of the two encodings.
func TestHandlerPrefersBrotliOnATie(t *testing.T) {
	h := web.Handler()
	js := assetPath(t)
	if js == "" {
		return
	}

	for _, header := range []string{
		"gzip, deflate, br",
		"br, gzip",
		"gzip;q=1.0, br;q=1.0",
	} {
		req := httptest.NewRequest(http.MethodGet, "/"+js, nil)
		req.Header.Set("Accept-Encoding", header)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if got := rec.Header().Get("Content-Encoding"); got != "br" {
			t.Errorf("Accept-Encoding %q: Content-Encoding = %q, want %q", header, got, "br")
		}
	}
}

// A client that names one encoding must get that one. Handing a gzip body to
// a client that only accepts brotli (or the reverse) breaks the page.
func TestHandlerHonoursExplicitQuality(t *testing.T) {
	h := web.Handler()
	js := assetPath(t)
	if js == "" {
		return
	}

	for _, tc := range []struct {
		header string
		want   string
	}{
		{"gzip", "gzip"},
		{"br", "br"},
		{"br;q=1.0, gzip;q=0.5", "br"},
		{"gzip;q=1.0, br;q=0.5", "gzip"},
		{"gzip;q=0, br", "br"},
	} {
		req := httptest.NewRequest(http.MethodGet, "/"+js, nil)
		req.Header.Set("Accept-Encoding", tc.header)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if got := rec.Header().Get("Content-Encoding"); got != tc.want {
			t.Errorf("Accept-Encoding %q: Content-Encoding = %q, want %q", tc.header, got, tc.want)
		}
	}
}

// The entry document is what a navigation asks for, and it names the hashed
// bundle. It must be served compressed too — it is the request that has to
// complete before the browser discovers anything else.
func TestHandlerServesEntryDocumentCompressed(t *testing.T) {
	h := web.Handler()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "br" {
		t.Errorf("Content-Encoding = %q, want %q", got, "br")
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", got)
	}
}

// A client-side route falls back to the entry document. The fallback must
// carry the same compression treatment as the document itself, or the second
// navigation of a session pays the full uncompressed cost.
func TestHandlerCompressesSPAFallback(t *testing.T) {
	h := web.Handler()

	req := httptest.NewRequest(http.MethodGet, "/dashboard/usage", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "br" {
		t.Errorf("Content-Encoding = %q, want %q", got, "br")
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", got)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("Content-Type = %q, want text/html (the fallback serves the entry document)", got)
	}
}

// A missing asset must still be a 404. Answering it with the entry document
// would hand the browser HTML for a script it asked to execute.
func TestHandlerMissingAssetIsNotFound(t *testing.T) {
	h := web.Handler()

	req := httptest.NewRequest(http.MethodGet, "/assets/does-not-exist.js", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// The compressed variants are build output, produced by
// `web/scripts/precompress.ts`. If a build skips that step the gateway
// still works — it falls back to the original file — but the dashboard is
// served uncompressed, which is the regression the pre-compression exists
// to prevent. Nothing else in the build notices: the handler is correct
// either way, and the transfer is 4x the size.
//
// So the variants are pinned here. This fails when the SPA was built
// without them, which is the only place the omission is observable.
func TestCompressedVariantsExistForTheBundle(t *testing.T) {
	js := assetPath(t)

	for _, ext := range []string{".br", ".gz"} {
		variant := filepath.Join("dist", js+ext)
		info, err := os.Stat(variant)
		if err != nil {
			t.Errorf("%s is missing: the bundle would be served uncompressed (build with `bun run build` or `make web-build`)", variant)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("%s is empty", variant)
		}
	}
}

// The response body depends on the request's Accept-Encoding even when no
// compressed variant is served: the identity body and the compressed body
// are different bytes for the same URL. A shared cache that stored one and
// handed it to a client that asked for the other would break the page, so
// Vary has to be on every path — not only on the compressed one.
func TestVaryIsSetOnEveryNegotiatedResponse(t *testing.T) {
	h := web.Handler()
	js := assetPath(t)

	for _, tc := range []struct {
		name   string
		accept string
	}{
		{"no Accept-Encoding at all", ""},
		{"identity only", "identity"},
		{"gzip", "gzip"},
		{"brotli", "br"},
		{"both", "gzip, deflate, br"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/"+js, nil)
			if tc.accept != "" {
				req.Header.Set("Accept-Encoding", tc.accept)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got := rec.Header().Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
				t.Errorf("Vary = %q, want it to contain Accept-Encoding", got)
			}
		})
	}
}

// A range request has to be answered against the bytes that are actually
// sent. The handler used to answer ranges through http.FileServer, and
// replacing it with a plain write dropped that silently: a client asking for
// part of a file got the whole file with a 200, and a client asking past the
// end got a 200 instead of a 416.
func TestHandlerAnswersRangeRequests(t *testing.T) {
	h := web.Handler()
	js := assetPath(t)

	t.Run("a byte range is answered with 206 and the requested bytes", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/"+js, nil)
		req.Header.Set("Range", "bytes=0-99")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusPartialContent {
			t.Fatalf("status = %d, want 206", rec.Code)
		}
		if got := rec.Header().Get("Content-Range"); got == "" {
			t.Error("Content-Range is missing")
		}
		if rec.Body.Len() != 100 {
			t.Errorf("body is %d bytes, want 100", rec.Body.Len())
		}
		if got := rec.Header().Get("Content-Length"); got != "100" {
			t.Errorf("Content-Length = %q, want %q", got, "100")
		}
	})

	t.Run("a range past the end is answered with 416", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/"+js, nil)
		req.Header.Set("Range", "bytes=999999999-")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusRequestedRangeNotSatisfiable {
			t.Errorf("status = %d, want 416", rec.Code)
		}
	})

	t.Run("a range against a compressed variant is resolved against the variant", func(t *testing.T) {
		// The client sees the compressed bytes, so the range is a range of
		// those. The total in Content-Range has to be the variant's length.
		req := httptest.NewRequest(http.MethodGet, "/"+js, nil)
		req.Header.Set("Accept-Encoding", "br")
		req.Header.Set("Range", "bytes=0-99")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusPartialContent {
			t.Fatalf("status = %d, want 206", rec.Code)
		}
		if got := rec.Header().Get("Content-Encoding"); got != "br" {
			t.Errorf("Content-Encoding = %q, want %q", got, "br")
		}
		if rec.Body.Len() != 100 {
			t.Errorf("body is %d bytes, want 100", rec.Body.Len())
		}
	})
}
