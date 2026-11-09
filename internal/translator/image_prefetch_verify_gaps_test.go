package translator

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// The tests in this file each pin a defect the verify pass measured on the
// first draft of the prefetcher. They are grouped here rather than in
// image_prefetch_wiring_test.go so the defect and its guard stay together.

// TestPrefetch_PreservesUntouchedNumbers stops the rewrite from quietly
// changing a request it was only supposed to add images to. A plain
// map[string]any round-trip reads every number as float64, which turned
// 9007199254740993 into …992 and would do the same to any large integer id.
func TestPrefetch_PreservesUntouchedNumbers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(tinyPNG)
	}))
	defer srv.Close()

	body := []byte(`{"model":"m","seed":9007199254740993,"max_tokens":1000000,` +
		`"temperature":0.7,"messages":[{"role":"user","content":[` +
		`{"type":"image_url","image_url":{"url":"` + srv.URL + `/a.png"}}]}]}`)

	out := string(PrefetchInlineImages(context.Background(), body, PrefetchOptions{AllowLoopback: true}))
	if !strings.Contains(out, "data:image/png;base64,") {
		t.Fatalf("the image was not inlined, so this proves nothing:\n%s", out)
	}
	for _, want := range []string{`"seed":9007199254740993`, `"max_tokens":1000000`, `"temperature":0.7`} {
		if !strings.Contains(out, want) {
			t.Errorf("numeric field rewritten: expected %s in\n%s", want, out)
		}
	}
}

// TestPrefetch_LabelsByMagicBytes pins that the inlined label follows the bytes
// rather than the server's claim. A GIF served as image/png used to be labelled
// image/png, which hands the provider a lie it will decode the wrong way.
func TestPrefetch_LabelsByMagicBytes(t *testing.T) {
	gif := []byte("GIF89a" + strings.Repeat("\x00", 120))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(gif)
	}))
	defer srv.Close()

	body := prefetchTestBody(srv.URL + "/a.png")
	out := string(PrefetchInlineImages(context.Background(), body, PrefetchOptions{AllowLoopback: true}))
	if !strings.Contains(out, "data:image/gif;base64,") {
		t.Fatalf("expected the label to follow the magic bytes, got:\n%s", out)
	}
	if strings.Contains(out, "data:image/png;base64,") {
		t.Errorf("the declared Content-Type won over the actual bytes:\n%s", out)
	}
}

// TestPrefetch_RefusalsDoNotSpendTheBudget keeps one bad URL from starving the
// good ones behind it in the same request. The budget used to be charged before
// any validation, so four refused URLs exhausted it.
func TestPrefetch_RefusalsDoNotSpendTheBudget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(tinyPNG)
	}))
	defer srv.Close()

	body := []byte(`{"model":"m","messages":[{"role":"user","content":[` +
		`{"type":"image_url","image_url":{"url":"ftp://example.com/a.png"}},` +
		`{"type":"image_url","image_url":{"url":"http://10.0.0.1/a.png"}},` +
		`{"type":"image_url","image_url":{"url":"http://127.0.0.1/a.png"}},` +
		`{"type":"image_url","image_url":{"url":"ftp://example.com/b.png"}},` +
		`{"type":"image_url","image_url":{"url":"` + srv.URL + `/good.png"}}]}]}`)

	out := string(PrefetchInlineImages(context.Background(), body, PrefetchOptions{AllowLoopback: true, MaxImages: 1}))
	if !strings.Contains(out, "data:image/png;base64,") {
		t.Fatalf("four refused URLs starved the budget and the good one was skipped:\n%s", out)
	}
}

// TestPrefetch_BlocksCloudMetadata ranges pins the predicate both the literal-IP
// check and the dialer hook delegate to. 100.100.100.200 is Alibaba Cloud's
// metadata endpoint and sits inside RFC6598 carrier-grade NAT, which neither
// copy of the predicate used to consider private — an image URL pointing there
// was fetched and its body inlined into the outbound provider request.
func TestPrefetch_BlocksCloudMetadataRanges(t *testing.T) {
	for _, raw := range []string{
		"http://100.100.100.200/latest/meta-data/",
		"http://100.64.0.1/a.png",
		"http://192.0.0.192/a.png",
		"http://198.18.0.1/a.png",
	} {
		if status, err := ValidatePrefetchURL(raw); err == nil {
			t.Errorf("%s was allowed (status %d); that is a metadata service, not an image host", raw, status)
		}
	}
}

// TestPrefetch_KeepsPublicAddressesReachable pins the other side of the same
// predicate: a guard that blocks everything is a guard that breaks the feature.
// The extra CIDR blocks exist to catch the cloud-metadata addresses the
// stdlib predicates miss, and each one has to stop at its own boundary —
// 100.63.255.255 and 100.128.0.1 sit immediately either side of RFC6598
// carrier-grade NAT and are ordinary public addresses.
func TestPrefetch_KeepsPublicAddressesReachable(t *testing.T) {
	for _, raw := range []string{
		"https://1.1.1.1/a.png",
		"https://8.8.8.8/a.png",
		"https://93.184.216.34/a.png",  // example.com
		"https://100.63.255.255/a.png", // just below the CGNAT block
		"https://100.128.0.1/a.png",    // just above it
	} {
		if status, err := ValidatePrefetchURL(raw); err != nil {
			t.Errorf("%s was refused (status %d): it is a public address", raw, status)
		}
	}
}

// TestPrefetch_RewritesResponsesInput pins that the Responses wire is covered.
// The gateway serves /v1/responses, whose turn lives under `input[]` with a
// bare `image_url` string — a shape neither `messages` nor `image_url.url`
// describes, so it was returned byte-identical and kept shipping remote URLs.
func TestPrefetch_RewritesResponsesInput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(tinyPNG)
	}))
	defer srv.Close()

	body := []byte(`{"model":"m","input":[{"role":"user","content":[` +
		`{"type":"input_image","image_url":"` + srv.URL + `/a.png"}]}]}`)

	out := string(PrefetchInlineImages(context.Background(), body, PrefetchOptions{AllowLoopback: true}))
	if strings.Contains(out, srv.URL) {
		t.Fatalf("Responses input_image kept its remote URL:\n%s", out)
	}
	if !strings.Contains(out, "data:image/png;base64,") {
		t.Fatalf("expected an inline data URL in the Responses body, got:\n%s", out)
	}
}

// TestPrefetch_FollowsThreeRedirects pins the documented cap. `via` holds the
// requests already made, so refusing at len(via) >= 3 stopped the third hop —
// two redirects followed, not three.
func TestPrefetch_FollowsThreeRedirects(t *testing.T) {
	var srv *httptest.Server
	hops := []string{"/1", "/2", "/3", "/4"}
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := strings.TrimPrefix(r.URL.Path, "/")
		for i, p := range hops {
			if strings.HasSuffix(p, "/"+cur) && i+1 < len(hops) {
				http.Redirect(w, r, hops[i+1], http.StatusFound)
				return
			}
		}
		if cur == "4" {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(tinyPNG)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	body := prefetchTestBody(srv.URL + "/1")
	out := string(PrefetchInlineImages(context.Background(), body, PrefetchOptions{AllowLoopback: true}))
	if !strings.Contains(out, "data:image/png;base64,") {
		t.Fatalf("three redirects must be followed, the image was not inlined:\n%s", out)
	}
}

// TestPrefetch_BlocksZonedIPv6Literal pins the zone-suffix hole. net.ParseIP
// answers nil for "::1%lo" and "fd00:ec2::254%eth0", so the literal-IP check
// read a zoned address as "not an address" and skipped the guard entirely:
// https://[::1%25lo]:PORT/a.png was fetched and inlined. A zone names the
// interface a packet leaves by, not a different destination.
func TestPrefetch_BlocksZonedIPv6Literal(t *testing.T) {
	for _, raw := range []string{
		"https://[::1%25lo]:41249/a.png",
		"https://[fd00:ec2::254%25eth0]/a.png",
		"https://[fe80::1%25eth0]/a.png",
	} {
		if status, err := ValidatePrefetchURL(raw); err == nil {
			t.Errorf("%s was allowed (status %d); a zone must not exempt a private address", raw, status)
		}
	}
}

// TestPrefetch_DialGuardBlocksZonedLoopback reaches the second half of the same
// guard. The dial-time hook is the boundary that survives DNS rebinding, so it
// has to refuse a zoned literal on its own, whatever the pre-flight check did.
func TestPrefetch_DialGuardBlocksZonedLoopback(t *testing.T) {
	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skipf("no IPv6 loopback on this host: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port

	client := newPrefetchClient(false)
	transport := client.Transport.(*http.Transport)
	conn, err := transport.DialContext(context.Background(), "tcp",
		net.JoinHostPort("::1%lo", strconv.Itoa(port)))
	if conn != nil {
		_ = conn.Close()
	}
	if !errors.Is(err, ErrPrefetchSSRFBlocked) {
		t.Fatalf("zoned loopback literal dialled: err = %v, want ErrPrefetchSSRFBlocked", err)
	}
}

// TestPrefetch_RewritesStringImageURL pins the second OpenAI wire shape. The
// `image_url` key is either an object carrying a `url` field or a bare string,
// and the bare string — the shorter and more common form — was skipped, so the
// remote URL still reached a provider that cannot fetch it. Upstream handles
// both (open-sse/translator/concerns/prefetch.js).
func TestPrefetch_RewritesStringImageURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(tinyPNG)
	}))
	defer srv.Close()

	body := []byte(`{"model":"m","messages":[{"role":"user","content":[` +
		`{"type":"image_url","image_url":"` + srv.URL + `/a.png"}]}]}`)

	out := string(PrefetchInlineImages(context.Background(), body, PrefetchOptions{AllowLoopback: true}))
	if strings.Contains(out, srv.URL) {
		t.Fatalf("string-form image_url kept its remote URL: %s", out)
	}
	if !strings.Contains(out, "data:image/png;base64,") {
		t.Fatalf("expected an inline data URL in the string-form block, got: %s", out)
	}
}
