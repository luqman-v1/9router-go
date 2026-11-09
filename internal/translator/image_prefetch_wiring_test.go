package translator

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestPrefetchInlineImages_ProductionWiring is the red-first test for G11:
// the contract helpers exist only in a test file today, so nothing in
// production rewrites a remote image URL into inline base64. This test calls
// the entry point that production wiring is supposed to provide.
func TestPrefetchInlineImages_ProductionWiring(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("\x00", 120))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png)
	}))
	defer srv.Close()

	opts := PrefetchOptions{AllowLoopback: true}

	body := []byte(`{"model":"m","messages":[{"role":"user","content":[` +
		`{"type":"image_url","image_url":{"url":"` + srv.URL + `/a.png"}}]}]}`)

	out := PrefetchInlineImages(context.Background(), body, opts)
	if strings.Contains(string(out), srv.URL) {
		t.Fatalf("remote image URL survived prefetch; body unchanged:\n%s", out)
	}
	if !strings.Contains(string(out), "data:image/png;base64,") {
		t.Fatalf("expected inline data URL, got:\n%s", out)
	}
}

var tinyPNG = []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("\x00", 120))

// prefetchTestBody builds a one-image OpenAI-shaped request body.
func prefetchTestBody(imageURL string) []byte {
	return []byte(`{"model":"m","messages":[{"role":"user","content":[` +
		`{"type":"image_url","image_url":{"url":"` + imageURL + `"}}]}]}`)
}

func TestPrefetchInlineImages_Refusals(t *testing.T) {
	allow := PrefetchOptions{AllowLoopback: true}
	strict := PrefetchOptions{}

	t.Run("private_range_refused", func(t *testing.T) {
		// Point at a live loopback server, but do not grant loopback: the
		// literal 127.0.0.1 is refused before any dial.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(tinyPNG)
		}))
		defer srv.Close()

		body := prefetchTestBody(srv.URL + "/a.png")
		out := PrefetchInlineImages(context.Background(), body, strict)
		if string(out) != string(body) {
			t.Fatalf("private-range URL was rewritten:\n%s", out)
		}
	})

	t.Run("redirect_into_private_range_refused", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
		}))
		defer srv.Close()

		body := prefetchTestBody(srv.URL + "/a.png")
		out := PrefetchInlineImages(context.Background(), body, allow)
		if string(out) != string(body) {
			t.Fatalf("redirect into metadata range was followed:\n%s", out)
		}
	})

	t.Run("oversized_body_refused", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(append(append([]byte{}, tinyPNG...), make([]byte, 4096)...))
		}))
		defer srv.Close()

		opts := allow
		opts.MaxBytes = 256
		body := prefetchTestBody(srv.URL + "/a.png")
		out := PrefetchInlineImages(context.Background(), body, opts)
		if string(out) != string(body) {
			t.Fatalf("oversized image was inlined:\n%s", out)
		}
	})

	t.Run("disallowed_mime_refused", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "image/svg+xml")
			_, _ = w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`))
		}))
		defer srv.Close()

		body := prefetchTestBody(srv.URL + "/a.svg")
		out := PrefetchInlineImages(context.Background(), body, allow)
		if string(out) != string(body) {
			t.Fatalf("SVG was inlined:\n%s", out)
		}
	})

	t.Run("mime_spoof_refused", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("<!DOCTYPE html><html><body>not an image</body></html>"))
		}))
		defer srv.Close()

		body := prefetchTestBody(srv.URL + "/a.png")
		out := PrefetchInlineImages(context.Background(), body, allow)
		if string(out) != string(body) {
			t.Fatalf("HTML spoofed as PNG was inlined:\n%s", out)
		}
	})

	t.Run("canceled_context_aborts", func(t *testing.T) {
		hit := make(chan struct{}, 1)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hit <- struct{}{}
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(tinyPNG)
		}))
		defer srv.Close()

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		body := prefetchTestBody(srv.URL + "/a.png")
		out := PrefetchInlineImages(ctx, body, allow)
		if string(out) != string(body) {
			t.Fatalf("canceled context still inlined:\n%s", out)
		}
		select {
		case <-hit:
			t.Fatal("canceled context still reached the origin server")
		case <-time.After(50 * time.Millisecond):
		}
	})

	t.Run("image_budget_caps_prefetch_count", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(tinyPNG)
		}))
		defer srv.Close()

		opts := allow
		opts.MaxImages = 1
		body := []byte(`{"model":"m","messages":[{"role":"user","content":[` +
			`{"type":"image_url","image_url":{"url":"` + srv.URL + `/1.png"}},` +
			`{"type":"image_url","image_url":{"url":"` + srv.URL + `/2.png"}}]}]}`)
		out := string(PrefetchInlineImages(context.Background(), body, opts))
		if got := strings.Count(out, "data:image/png;base64,"); got != 1 {
			t.Fatalf("expected exactly 1 inlined image, got %d:\n%s", got, out)
		}
		if !strings.Contains(out, "/2.png") {
			t.Fatalf("second image should have stayed a URL:\n%s", out)
		}
	})

	t.Run("claude_shape_rewritten", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(tinyPNG)
		}))
		defer srv.Close()

		body := []byte(`{"model":"m","messages":[{"role":"user","content":[` +
			`{"type":"image","source":{"type":"url","url":"` + srv.URL + `/a.png"}}]}]}`)
		out := string(PrefetchInlineImages(context.Background(), body, allow))
		if strings.Contains(out, srv.URL) {
			t.Fatalf("claude url source not rewritten:\n%s", out)
		}
		if !strings.Contains(out, `"type":"base64"`) || !strings.Contains(out, `"media_type":"image/png"`) {
			t.Fatalf("expected claude base64 source, got:\n%s", out)
		}
	})

	t.Run("data_url_passthrough_untouched", func(t *testing.T) {
		inline := "data:image/png;base64,iVBORw0KGgo="
		body := prefetchTestBody(inline)
		out := PrefetchInlineImages(context.Background(), body, allow)
		if string(out) != string(body) {
			t.Fatalf("inline data URL should be left alone:\n%s", out)
		}
	})
}
