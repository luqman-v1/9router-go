package media

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"9router/proxy/internal/proxy"
)

// TestResolveImageInput_RefusesPrivateAndLoopback pins that the vision bridge
// never downloads a caller-supplied image whose connected address is loopback,
// RFC1918, carrier-grade-NAT or cloud metadata (169.254.169.254). The guarded
// client ignores ALLOW_PRIVATE_NETWORKING, so these are refused even under the
// default (on) setting.
func TestResolveImageInput_RefusesPrivateAndLoopback(t *testing.T) {
	privateURLs := []string{
		"http://127.0.0.1/img.png",
		"http://127.0.0.254/img.png",
		"http://10.0.0.1/internal.png",
		"http://192.168.1.1/admin.png",
		"http://172.16.0.5/infra.png",
		"http://169.254.169.254/latest/meta-data/",
		"http://100.64.0.1/metadata.png",
		"http://localhost/private.png",
		"http://[::1]/image.png",
		"http://[fe80::1]/link-local.png",
	}

	h := newTestMediaHandler(nil)
	ctx := context.Background()
	for _, url := range privateURLs {
		if _, err := h.resolveImageInput(ctx, url); err == nil {
			t.Errorf("resolveImageInput(%q) must be refused, got nil error", url)
		}
	}
}

// TestResolveImageInput_ReachableLoopbackNotDownloaded is the positive
// control: a loopback server that actually serves a valid PNG. The old code
// (h.Client under the default ALLOW_PRIVATE_NETWORKING=true) downloads from it
// and returns base64; the guarded client must refuse at connect time and
// return an error, with the server recording zero hits. Without this case the
// RefusesPrivateAndLoopback list only proves a dial to a dead port fails.
func TestResolveImageInput_ReachableLoopbackNotDownloaded(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("\x89PNG\r\n\x1a\n" + "\x00\x00\x00\rIHDR"))
	}))
	defer srv.Close()

	h := newTestMediaHandler(nil)
	ctx := context.Background()
	if _, err := h.resolveImageInput(ctx, srv.URL+"/img.png"); err == nil {
		t.Fatal("a loopback image server must be refused, got nil error")
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("loopback image server was fetched %d time(s); the connect-time guard was bypassed", got)
	}
}

// TestResolveImageInput_RedirectToPrivateRefused pins that a redirect hop
// landing on a private address is refused by checkRedirectPublic — the policy,
// not the dialer. The original test pointed hop 1 at a loopback httptest server,
// so the dialer blocked the first connect and this check was never reached
// (vacuous). Here the client is driven through a fake RoundTripper that plays
// hop 1: it answers 302 with a Location on loopback, so the socket guard is out
// of the picture and checkRedirectPublic is the only thing that can refuse. A
// private image server records hits; a refusal leaves it at zero.
type redirectRoundTripper struct {
	location string
}

func (rt *redirectRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	h := http.Header{}
	h.Set("Location", rt.location)
	return &http.Response{
		StatusCode: http.StatusFound,
		Header:     h,
		Body:       http.NoBody,
		Request:    req,
	}, nil
}

func TestResolveImageInput_RedirectToPrivateRefused(t *testing.T) {
	var privateHits atomic.Int32
	private := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		privateHits.Add(1)
		_, _ = w.Write([]byte(`{"title":"internal admin"}`))
	}))
	defer private.Close()

	rt := &redirectRoundTripper{location: private.URL + "/admin"}
	fakeClient := &http.Client{
		Transport:     rt,
		CheckRedirect: checkRedirectPublic,
	}

	orig := visionImageDownloadClient
	visionImageDownloadClient = func() *http.Client { return fakeClient }
	t.Cleanup(func() { visionImageDownloadClient = orig })

	h := newTestMediaHandler(nil)
	ctx := context.Background()
	_, err := h.resolveImageInput(ctx, "http://public.example/image.png")
	if err == nil {
		t.Fatal("redirect to private address must be refused, got nil error")
	}
	if !contains(err.Error(), "redirect refused") {
		t.Fatalf("refusal must come from checkRedirectPublic, got: %v", err)
	}
	if hits := privateHits.Load(); hits != 0 {
		t.Fatalf("redirect to private address was followed %d time(s)", hits)
	}
}

// TestResolveImageInput_DownloadRefusedNamesSSRF pins that a refused dial names
// the SSRF guard so callers and operators can tell the failure apart from a
// generic network error.
func TestResolveImageInput_DownloadRefusedNamesSSRF(t *testing.T) {
	h := newTestMediaHandler(nil)
	ctx := context.Background()
	if _, err := h.resolveImageInput(ctx, "http://127.0.0.1/img.png"); err == nil {
		t.Fatal("expected an error")
	} else if !contains(err.Error(), proxy.ErrSSRFBlocked.Error()) && !contains(err.Error(), "SSRF") {
		t.Fatalf("refusal must name the SSRF guard, got: %v", err)
	}
}
