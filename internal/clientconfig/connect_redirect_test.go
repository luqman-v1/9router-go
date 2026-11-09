package clientconfig

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

// testEndpoint parses a URL the redirect tests already know is valid. They are
// about CheckRedirect, so going through ValidateConnectURL would add steps that
// are covered elsewhere.
func testEndpoint(t *testing.T, raw string) ConnectEndpoint {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return ConnectEndpoint{URL: u}
}

// allowAllPublic is the step-8 stub for tests whose servers are loopback
// httptest instances. The real guard blocks loopback by design, which would
// make every redirect test fail for the wrong reason.
func allowAllPublic(string) error { return nil }

func doWithCredential(t *testing.T, client *http.Client, raw string) error {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Cookie", "auth_token=SECRET")
	req.Header.Set("Authorization", "Bearer TOKEN")
	resp, err := client.Do(req)
	if err == nil {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	return err
}

func doWithoutCredential(t *testing.T, client *http.Client, raw string) error {
	t.Helper()
	resp, err := client.Get(raw) //nolint:noctx // httptest only; the client carries the timeout
	if err == nil {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	return err
}

// R1 — an HTTPS server redirecting to plaintext HTTP. The plaintext server must
// see zero requests: the check runs before the request leaves, not after it
// fails.
func TestConnectRedirect_RejectsHTTPSDowngrade(t *testing.T) {
	var plainHit atomic.Int64
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plainHit.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer plain.Close()

	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/next", http.StatusFound)
	}))
	defer tls.Close()

	client := NewConnectClient(tls.Client().Transport, testEndpoint(t, tls.URL), allowAllPublic)
	err := doWithCredential(t, client, tls.URL)

	if !errors.Is(err, ErrRedirectDowngrade) {
		t.Fatalf("err = %v, want ErrRedirectDowngrade", err)
	}
	if got := plainHit.Load(); got != 0 {
		t.Errorf("plaintext server received %d requests, want 0", got)
	}
}

// R1b — the same rule on hop two: TLS → TLS → plaintext. Both TLS hops live on
// one server so the first redirect stays same-origin and the second one is what
// the case is about.
func TestConnectRedirect_RejectsDowngradeOnSecondHop(t *testing.T) {
	var plainHit atomic.Int64
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plainHit.Add(1)
	}))
	defer plain.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/first", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/second", http.StatusFound)
	})
	mux.HandleFunc("/second", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/down", http.StatusFound)
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()

	client := NewConnectClient(srv.Client().Transport, testEndpoint(t, srv.URL), allowAllPublic)
	if err := doWithCredential(t, client, srv.URL+"/first"); !errors.Is(err, ErrRedirectDowngrade) {
		t.Fatalf("err = %v, want ErrRedirectDowngrade", err)
	}
	if got := plainHit.Load(); got != 0 {
		t.Errorf("plaintext server received %d requests, want 0", got)
	}
}

// R2 — the port-only redirect while carrying a credential. This is the case
// net/http does not catch: it strips headers on a hostname change and on
// nothing else, so this test is the only thing standing between the cookie and
// server B.
//
// Mechanism guard (docs/specs/connect-security.md §7.4): comparing Hostname()
// instead of the full origin would let this pass.
func TestConnectRedirect_RejectsPortChangeWithCredential(t *testing.T) {
	var bHit atomic.Int64
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bHit.Add(1)
	}))
	defer b.Close()

	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, b.URL+"/stolen", http.StatusFound)
	}))
	defer a.Close()

	client := NewConnectClient(http.DefaultTransport, testEndpoint(t, a.URL), allowAllPublic)
	err := doWithCredential(t, client, a.URL)

	if !errors.Is(err, ErrRedirectCrossOrigin) {
		t.Fatalf("err = %v, want ErrRedirectCrossOrigin", err)
	}
	if got := bHit.Load(); got != 0 {
		t.Errorf("second origin received %d requests, want 0", got)
	}
}

// R3 — a hostname change. net/http would strip the headers here anyway, but the
// refusal has to come from the policy, so the error is the same one.
func TestConnectRedirect_RejectsHostnameChangeWithCredential(t *testing.T) {
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer b.Close()
	bURL, _ := url.Parse(b.URL)

	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://localhost:"+bURL.Port()+"/stolen", http.StatusFound)
	}))
	defer a.Close()

	client := NewConnectClient(http.DefaultTransport, testEndpoint(t, a.URL), allowAllPublic)
	if err := doWithCredential(t, client, a.URL); !errors.Is(err, ErrRedirectCrossOrigin) {
		t.Fatalf("err = %v, want ErrRedirectCrossOrigin", err)
	}
}

// R4 — six hops. The ceiling is five, lower than net/http's ten.
func TestConnectRedirect_StopsAfterFiveHops(t *testing.T) {
	var srv *httptest.Server
	hops := 10
	var hits atomic.Int64
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if hits.Load() > int64(hops) {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, r, srv.URL+r.URL.Path, http.StatusFound)
	}))
	defer srv.Close()

	client := NewConnectClient(http.DefaultTransport, testEndpoint(t, srv.URL), allowAllPublic)
	if err := doWithoutCredential(t, client, srv.URL); !errors.Is(err, ErrTooManyRedirects) {
		t.Fatalf("err = %v, want ErrTooManyRedirects", err)
	}
	if got := hits.Load(); got > 5 {
		t.Errorf("server saw %d requests before the stop, want at most 5", got)
	}
}

// R5 — a same-origin redirect without credentials must still work. The policy
// blocks the leak, not redirects themselves.
func TestConnectRedirect_AllowsSameOriginWithoutCredential(t *testing.T) {
	var final int
	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/final", http.StatusFound)
	})
	mux.HandleFunc("/final", func(w http.ResponseWriter, r *http.Request) {
		final++
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := NewConnectClient(http.DefaultTransport, testEndpoint(t, srv.URL), allowAllPublic)
	if err := doWithoutCredential(t, client, srv.URL+"/start"); err != nil {
		t.Fatalf("same-origin redirect refused: %v", err)
	}
	if final != 1 {
		t.Errorf("final handler ran %d times, want 1", final)
	}
}

// R6 — pins Jar: nil. A cookie jar would replay the session cookie on its own;
// with none, the target sees an empty header even on a same-origin redirect,
// which is what the flow relies on when it re-attaches credentials explicitly.
func TestConnectRedirect_JarIsNilSoCookiesDoNotSurvive(t *testing.T) {
	var gotCookie, gotAuth string
	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/target", http.StatusFound)
	})
	mux.HandleFunc("/target", func(w http.ResponseWriter, r *http.Request) {
		gotCookie = r.Header.Get("Cookie")
		gotAuth = r.Header.Get("Authorization")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := NewConnectClient(http.DefaultTransport, testEndpoint(t, srv.URL), allowAllPublic)
	if client.Jar != nil {
		t.Fatal("client has a cookie jar; §4.5 requires nil")
	}

	// No credential, so the policy allows the hop; what the target receives is
	// then entirely up to the transport, which is the property being pinned.
	if err := doWithoutCredential(t, client, srv.URL+"/start"); err != nil {
		t.Fatalf("redirect failed: %v", err)
	}
	if gotCookie != "" || gotAuth != "" {
		t.Errorf("target saw Cookie=%q Authorization=%q, want both empty", gotCookie, gotAuth)
	}
}

func TestConnectClient_Timeout(t *testing.T) {
	client := NewConnectClient(nil, ConnectEndpoint{}, allowAllPublic)
	if client.Timeout != connectTimeout {
		t.Errorf("Timeout = %v, want %v", client.Timeout, connectTimeout)
	}
	if client.CheckRedirect == nil {
		t.Error("CheckRedirect is nil; §4.5 makes it mandatory")
	}
	if client.Jar != nil {
		t.Error("Jar is non-nil; §4.5/D6 requires nil")
	}
}

func TestOriginOf(t *testing.T) {
	tests := []struct {
		raw  string
		want Origin
	}{
		{"https://example.com/a", Origin{"https", "example.com", "443"}},
		{"https://example.com:443/a", Origin{"https", "example.com", "443"}},
		{"HTTPS://Example.COM./", Origin{"https", "example.com", "443"}},
		{"http://127.0.0.1:20128", Origin{"http", "127.0.0.1", "20128"}},
		{"http://[::1]:20128", Origin{"http", "::1", "20128"}},
		{"http://example.com", Origin{"http", "example.com", "80"}},
	}
	for _, tt := range tests {
		u, err := url.Parse(tt.raw)
		if err != nil {
			t.Fatalf("parse %q: %v", tt.raw, err)
		}
		if got := OriginOf(u); got != tt.want {
			t.Errorf("OriginOf(%q) = %+v, want %+v", tt.raw, got, tt.want)
		}
	}
}

func TestReqHasCredential(t *testing.T) {
	tests := []struct {
		name   string
		cookie string
		bearer string
		want   bool
	}{
		{"none", "", "", false},
		{"cookie only", "auth_token=x", "", true},
		{"bearer only", "", "Bearer x", true},
		{"both", "auth_token=x", "Bearer x", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, "https://example.com/", nil)
			if tt.cookie != "" {
				req.Header.Set("Cookie", tt.cookie)
			}
			if tt.bearer != "" {
				req.Header.Set("Authorization", tt.bearer)
			}
			if got := reqHasCredential(req); got != tt.want {
				t.Errorf("reqHasCredential() = %v, want %v", got, tt.want)
			}
		})
	}
	if reqHasCredential(nil) {
		t.Error("reqHasCredential(nil) = true")
	}
}

// The dashboard password travels in the request BODY, never in a header, and
// net/http replays a 307/308 body verbatim to the redirect target
// (client.go: includeBody). A header-only credential check therefore let the
// password reach a host the user never named.
//
// This is the regression that closes it: the target must receive nothing.
func TestConnectRedirect_RejectsBodyCredentialReplay(t *testing.T) {
	var evilHit atomic.Int64
	var evilBody string
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		evilHit.Add(1)
		b, _ := io.ReadAll(r.Body)
		evilBody = string(b)
	}))
	defer evil.Close()

	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 307 preserves method and body.
		w.Header().Set("Location", evil.URL+"/collect")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer gw.Close()

	client := NewConnectClient(http.DefaultTransport, testEndpoint(t, gw.URL), allowAllPublic)
	body := strings.NewReader(`{"password":"S3CRET-DASHBOARD-PW"}`)
	req, err := http.NewRequest(http.MethodPost, gw.URL+"/api/auth/login", body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if _, err := client.Do(req); err == nil {
		t.Fatal("cross-origin redirect with a credential body was allowed")
	} else if !errors.Is(err, ErrRedirectCrossOrigin) {
		t.Fatalf("err = %v, want ErrRedirectCrossOrigin", err)
	}
	if got := evilHit.Load(); got != 0 {
		t.Errorf("redirect target received %d requests, want 0", got)
	}
	if strings.Contains(evilBody, "S3CRET-DASHBOARD-PW") {
		t.Errorf("password leaked to the redirect target: %q", evilBody)
	}
}

// A redirect target must be validated the way the first URL was. Step 8 is the
// only layer that resolves DNS and checks the resolved address (§3.7); without
// it on every hop, a public host can send the client to a metadata address.
func TestConnectRedirect_RunsTargetThroughSSRFGuard(t *testing.T) {
	var internalHit atomic.Int64
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		internalHit.Add(1)
	}))
	defer internal.Close()

	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, internal.URL+"/latest/meta-data/", http.StatusFound)
	}))
	defer gw.Close()

	var guarded []string
	guard := func(rawURL string) error {
		guarded = append(guarded, rawURL)
		if strings.Contains(rawURL, internalHostOf(t, internal.URL)) {
			return errors.New("blocked URL: internal host")
		}
		return nil
	}

	client := NewConnectClient(http.DefaultTransport, testEndpoint(t, gw.URL), guard)
	resp, err := client.Get(gw.URL) //nolint:noctx // httptest only; the client carries the timeout
	if err == nil {
		resp.Body.Close()
		t.Fatal("redirect to a blocked host was allowed")
	}
	if !errors.Is(err, ErrBlockedHost) {
		t.Fatalf("err = %v, want ErrBlockedHost", err)
	}
	if got := internalHit.Load(); got != 0 {
		t.Errorf("blocked host received %d requests, want 0", got)
	}
	if len(guarded) == 0 {
		t.Error("the SSRF guard was never called for the redirect target")
	}
}

// internalHostOf returns the host:port of a test server URL.
func internalHostOf(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u.Host
}

// The error value must not carry the password: it lands in the CLI's stderr and
// in every log that captured it (§3.4, §6.1).
func TestConnectRedirect_ErrorsDoNotEchoUserinfo(t *testing.T) {
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer evil.Close()

	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "http://admin:S3CRET-TOKEN@"+evilHostOf(t, evil.URL)+"/collect")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer gw.Close()

	client := NewConnectClient(http.DefaultTransport, testEndpoint(t, gw.URL), allowAllPublic)
	req, reqErr := http.NewRequest(http.MethodGet, gw.URL, nil)
	if reqErr != nil {
		t.Fatalf("new request: %v", reqErr)
	}
	resp, err := DoConnect(client, req)
	if err == nil {
		resp.Body.Close()
		t.Fatal("redirect with userinfo was allowed")
	}
	if strings.Contains(err.Error(), "S3CRET-TOKEN") {
		t.Errorf("error echoed the password: %v", err)
	}
	if !errors.Is(err, ErrUserinfoNotAllowed) {
		t.Errorf("err = %v, want ErrUserinfoNotAllowed", err)
	}
}

// evilHostOf returns the host:port of a test server URL, without a scheme.
func evilHostOf(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u.Host
}
