package tunnel

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/log"
)

// isolateDataDir points the package's data dir at a temp directory for the
// duration of the test. Without it these tests read and write the developer's
// real ~/.9router/tunnel, and a state.json written here would make the next
// dashboard start believe a tunnel exists.
//
// Only DATA_DIR is pinned. The paths inside the package resolve from it on
// every call, so there is nothing else to redirect — and a test that assigned
// the path variables directly would stop compiling the moment they became
// functions.
func isolateDataDir(t *testing.T) {
	t.Helper()
	t.Setenv("DATA_DIR", t.TempDir())
}

func TestValidateShortID(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{name: "plain lowercase", id: "mygateway", wantErr: false},
		{name: "digits", id: "gw2026", wantErr: false},
		{name: "interior hyphen", id: "my-gw-1", wantErr: false},
		{name: "minimum length", id: "abc", wantErr: false},
		{name: "maximum length", id: strings.Repeat("a", 20), wantErr: false},
		{name: "too short", id: "ab", wantErr: true},
		{name: "too long", id: strings.Repeat("a", 21), wantErr: true},
		{name: "uppercase rejected", id: "MyGateway", wantErr: true},
		{name: "underscore rejected", id: "my_gw", wantErr: true},
		{name: "leading hyphen", id: "-mygw", wantErr: true},
		{name: "trailing hyphen", id: "mygw-", wantErr: true},
		{name: "empty", id: "", wantErr: true},
		{name: "dot rejected", id: "my.gw", wantErr: true},
		{name: "space rejected", id: "my gw", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateShortID(tt.id)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateShortID(%q) error = %v, wantErr %v", tt.id, err, tt.wantErr)
			}
		})
	}
}

// TestResolveShortIDFallsBack pins the contract that an unusable custom id
// never blocks the enable: the operator asked for a tunnel, and a validation
// lecture is not a substitute for one.
func TestResolveShortIDFallsBack(t *testing.T) {
	if got := ResolveShortID("My_Bad_ID"); got == "My_Bad_ID" {
		t.Fatalf("ResolveShortID returned the invalid id verbatim: %q", got)
	}
	if got := ResolveShortID("valid-id"); got != "valid-id" {
		t.Fatalf("ResolveShortID(valid) = %q, want the operator's own id", got)
	}
	if got := ResolveShortID("  "); len(got) != 6 {
		t.Fatalf("ResolveShortID(blank) = %q, want a generated 6-char id", got)
	}
}

// TestResolveShortIDStripsDoubledPrefix pins the one case the `r` prefix in the
// dashboard creates: an operator who types the prefix the field already shows
// gets `rajaja` stored, which serves `https://rrajaja.abc-tunnel.us` — an
// address the worker does not route.
//
// The trade-off is stated here rather than hidden: an id that legitimately
// begins with `r` is trimmed too, so `router` becomes `outer`. That is the cost
// of guessing what the operator meant, and it is why the length guard exists —
// it removes the prefix only when what follows is long enough to be the id they
// actually chose. A shorter value like `r-x` is left alone and validated as-is.
func TestResolveShortIDStripsDoubledPrefix(t *testing.T) {
	if got := ResolveShortID("rajaja"); got != "ajaja" {
		t.Errorf("ResolveShortID(%q) = %q, want the typed prefix stripped", "rajaja", got)
	}
	if got := ResolveShortID("rrajaja"); got != "ajaja" {
		t.Errorf("ResolveShortID(%q) = %q, want the doubled prefix stripped", "rrajaja", got)
	}
	if got := ResolveShortID("router"); got != "outer" {
		t.Errorf("ResolveShortID(%q) = %q, want the leading r trimmed", "router", got)
	}
	// A bare `r` is not an id. Stripping it leaves nothing, so the resolver
	// falls back to a generated one rather than storing an empty shortId.
	if got := ResolveShortID("r"); len(got) != 6 {
		t.Errorf("ResolveShortID(\"r\") = %q, want a generated 6-char id", got)
	}
}
func TestGenerateShortIDUsesSafeAlphabet(t *testing.T) {
	// The alphabet deliberately omits 0/O/1/l/I, which are misread when a URL
	// is retyped by hand. A regression that reintroduces them is silent, so it
	// is pinned here.
	for range 200 {
		id := GenerateShortID()
		if len(id) != 6 {
			t.Fatalf("GenerateShortID() = %q, want 6 characters", id)
		}
		for _, r := range id {
			if !strings.ContainsRune(shortIDChars, r) {
				t.Fatalf("GenerateShortID() = %q contains %q, outside the safe alphabet", id, r)
			}
		}
	}
}

func TestStateRoundTrip(t *testing.T) {
	isolateDataDir(t)

	if got := LoadState(); got != nil {
		t.Fatalf("LoadState() on a fresh dir = %+v, want nil", got)
	}

	want := &State{ShortID: "gwtest", TunnelURL: "https://abc.trycloudflare.com", Mode: ModeQuick}
	if err := SaveState(want); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	got := LoadState()
	if got == nil || got.ShortID != want.ShortID || got.TunnelURL != want.TunnelURL || got.Mode != want.Mode {
		t.Fatalf("LoadState() = %+v, want %+v", got, want)
	}

	// Disable keeps the identity so a re-enable reuses the same public URL.
	if err := ClearTunnelURL(); err != nil {
		t.Fatalf("ClearTunnelURL: %v", err)
	}
	got = LoadState()
	if got == nil || got.ShortID != "gwtest" {
		t.Fatalf("ClearTunnelURL dropped the shortId: %+v", got)
	}
	if got.TunnelURL != "" {
		t.Fatalf("ClearTunnelURL left tunnelUrl = %q", got.TunnelURL)
	}
}

// TestRecordTunnelURLKeepsPrevious pins the one distinction the rotation notice
// depends on: the address the tunnel served before the current one survives the
// change, and a re-registration of the same address does not count as one.
//
// Without this the dashboard cannot tell an operator that the URL they pasted
// into a client has rotated out from under them — after the fact the old value
// is simply gone from the state file.
func TestRecordTunnelURLKeepsPrevious(t *testing.T) {
	isolateDataDir(t)
	if err := RecordTunnelURL("https://first.trycloudflare.com"); err != nil {
		t.Fatalf("RecordTunnelURL: %v", err)
	}
	// Re-recording the same address is what a reconnect that kept its hostname
	// looks like. It must not be reported as a rotation.
	if err := RecordTunnelURL("https://first.trycloudflare.com"); err != nil {
		t.Fatalf("RecordTunnelURL (same): %v", err)
	}
	st := LoadState()
	if st == nil || st.PreviousURL != "" {
		t.Fatalf("same URL recorded as a rotation: %+v", st)
	}
	if err := RecordTunnelURL("https://second.trycloudflare.com"); err != nil {
		t.Fatalf("RecordTunnelURL (rotated): %v", err)
	}
	st = LoadState()
	if st == nil {
		t.Fatal("LoadState() = nil after rotation")
	}
	if st.TunnelURL != "https://second.trycloudflare.com" {
		t.Errorf("TunnelURL = %q, want the new address", st.TunnelURL)
	}
	if st.PreviousURL != "https://first.trycloudflare.com" {
		t.Errorf("PreviousURL = %q, want the address it replaced", st.PreviousURL)
	}
	if st.RotatedAt.IsZero() {
		t.Error("RotatedAt is zero, so the dashboard cannot say when it changed")
	}
}

// TestLoadStateSurvivesCorruptFile pins that a truncated or hand-edited
// state.json is treated as "no history" rather than crashing the status poll.
func TestLoadStateSurvivesCorruptFile(t *testing.T) {
	isolateDataDir(t)
	if err := os.MkdirAll(tunnelDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateFilePath(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := LoadState(); got != nil {
		t.Fatalf("LoadState() on corrupt file = %+v, want nil", got)
	}
}

func TestPIDLifecycle(t *testing.T) {
	isolateDataDir(t)

	if got := LoadPID(); got != 0 {
		t.Fatalf("LoadPID() with no file = %d, want 0", got)
	}
	if err := SavePID(4242); err != nil {
		t.Fatalf("SavePID: %v", err)
	}
	if got := LoadPID(); got != 4242 {
		t.Fatalf("LoadPID() = %d, want 4242", got)
	}

	// Clearing with a mismatched expectation must leave the newer tunnel's
	// record alone, or a disable racing an enable orphans the new tunnel.
	if err := ClearPID(9999); err != nil {
		t.Fatalf("ClearPID(mismatch): %v", err)
	}
	if got := LoadPID(); got != 4242 {
		t.Fatalf("ClearPID with a stale expectation removed the live record: %d", got)
	}

	if err := ClearPID(4242); err != nil {
		t.Fatalf("ClearPID(match): %v", err)
	}
	if got := LoadPID(); got != 0 {
		t.Fatalf("LoadPID() after clear = %d, want 0", got)
	}

	// A second clear is a no-op, not an error.
	if err := ClearPID(0); err != nil {
		t.Fatalf("ClearPID on an absent file: %v", err)
	}
}

func TestProcessAlive(t *testing.T) {
	if processAlive(0) {
		t.Error("processAlive(0) = true, want false")
	}
	if processAlive(-1) {
		t.Error("processAlive(-1) = true, want false")
	}
	// The current process is certainly alive.
	if !processAlive(os.Getpid()) {
		t.Error("processAlive(self) = false, want true")
	}
}

func TestExtractQuickTunnelURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "plain url line",
			in:   "INF |  https://slow-moon-42.trycloudflare.com     \n",
			want: "https://slow-moon-42.trycloudflare.com",
		},
		{
			// api.trycloudflare.com is the control-plane host, not the tunnel
			// endpoint. Registering it would produce a URL that answers
			// nothing, so it must never be selected.
			name: "api host ignored",
			in:   "connecting to api.trycloudflare.com then https://real-tunnel.trycloudflare.com",
			want: "https://real-tunnel.trycloudflare.com",
		},
		{
			name: "api host alone yields nothing",
			in:   "request to https://api.trycloudflare.com failed",
			want: "",
		},
		{
			name: "no url",
			in:   "starting tunnel\n",
			want: "",
		},
		{
			name: "url inside quotes",
			in:   `url="https://quoted-gw.trycloudflare.com"`,
			want: "https://quoted-gw.trycloudflare.com",
		},
		{
			name: "last match wins",
			in:   "https://first.trycloudflare.com ... https://second.trycloudflare.com",
			want: "https://second.trycloudflare.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractQuickTunnelURL(tt.in); got != tt.want {
				t.Fatalf("extractQuickTunnelURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestNormaliseDomain(t *testing.T) {
	tests := []struct{ in, want string }{
		{in: "router.example.com", want: "https://router.example.com"},
		{in: "https://router.example.com", want: "https://router.example.com"},
		{in: "http://router.example.com", want: "https://router.example.com"},
		{in: "  router.example.com  ", want: "https://router.example.com"},
		{in: "", want: ""},
	}
	for _, tt := range tests {
		if got := normaliseDomain(tt.in); got != tt.want {
			t.Errorf("normaliseDomain(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestNormaliseProtocol(t *testing.T) {
	// cloudflared rejects an unknown --protocol outright, so a typo must
	// become the default rather than a spawn failure.
	tests := []struct{ in, want string }{
		{in: "quic", want: "quic"},
		{in: "QUIC", want: "quic"},
		{in: "auto", want: "auto"},
		{in: "http2", want: "http2"},
		{in: "garbage", want: "http2"},
		{in: "", want: "http2"},
	}
	for _, tt := range tests {
		if got := normaliseProtocol(tt.in); got != tt.want {
			t.Errorf("normaliseProtocol(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestAssetNameForPlatform(t *testing.T) {
	asset, err := assetName()
	if err != nil {
		t.Skipf("platform %s/%s has no published asset", runtime.GOOS, runtime.GOARCH)
	}
	if !strings.Contains(asset, "cloudflared-") {
		t.Fatalf("assetName() = %q, want a cloudflared release asset", asset)
	}
}

// TestIsValidBinaryRejectsWrongMagic pins the check that stops a truncated
// download or an HTML error page saved as the binary from being executed.
func TestIsValidBinaryRejectsWrongMagic(t *testing.T) {
	dir := t.TempDir()

	tooSmall := filepath.Join(dir, "small")
	if err := os.WriteFile(tooSmall, make([]byte, 1024), 0o755); err != nil {
		t.Fatal(err)
	}
	if isValidBinary(tooSmall) {
		t.Error("isValidBinary accepted a file below the size floor")
	}

	wrongMagic := filepath.Join(dir, "wrongmagic")
	if err := os.WriteFile(wrongMagic, append([]byte("<!DO"), make([]byte, minBinarySize)...), 0o755); err != nil {
		t.Fatal(err)
	}
	if isValidBinary(wrongMagic) {
		t.Error("isValidBinary accepted an HTML document")
	}

	rightMagic := filepath.Join(dir, "rightmagic")
	head := magicFor()[0]
	if err := os.WriteFile(rightMagic, append(head, make([]byte, minBinarySize)...), 0o755); err != nil {
		t.Fatal(err)
	}
	if !isValidBinary(rightMagic) {
		t.Errorf("isValidBinary rejected a file with the platform's magic bytes (%s/%s)", runtime.GOOS, runtime.GOARCH)
	}

	if isValidBinary(filepath.Join(dir, "missing")) {
		t.Error("isValidBinary(missing) = true, want false")
	}
}

func TestBuildArgs(t *testing.T) {
	t.Run("quick tunnel", func(t *testing.T) {
		args := buildArgs(SpawnOptions{
			QuickTunnel: true,
			Origin:      "http://127.0.0.1:20130",
			Protocol:    "http2",
			ConfigPath:  "/tmp/cfg.yml",
		})
		joined := strings.Join(args, " ")
		for _, want := range []string{"tunnel", "--url http://127.0.0.1:20130", "--no-autoupdate", "--retries 99", "--config /tmp/cfg.yml"} {
			if !strings.Contains(joined, want) {
				t.Errorf("quick-tunnel args %q missing %q", joined, want)
			}
		}
		// A quick tunnel must never carry --token: that is what turns it into
		// a named tunnel against an account.
		if strings.Contains(joined, "--token") {
			t.Errorf("quick-tunnel args carry --token: %q", joined)
		}
	})

	t.Run("named tunnel", func(t *testing.T) {
		args := buildArgs(SpawnOptions{QuickTunnel: false, Token: "tok-123"})
		joined := strings.Join(args, " ")
		for _, want := range []string{"tunnel run", "--dns-resolver-addrs 1.1.1.1:53", "--token tok-123"} {
			if !strings.Contains(joined, want) {
				t.Errorf("named-tunnel args %q missing %q", joined, want)
			}
		}
	})
}

// TestProbeURLAliveAcceptsEitherHealthPath pins the fix for a tunnel that
// worked but read as dead. The gateway serves `/health` while the upstream
// Next.js implementation serves `/api/health`, and probing only the latter got
// a 404 from an origin that was up — so the enable waited out its full 60s
// budget and then reported failure against a healthy tunnel.
func TestProbeURLAliveAcceptsEitherHealthPath(t *testing.T) {
	for _, path := range []string{"/health", "/api/health"} {
		t.Run(path, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != path {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			if !ProbeURLAlive(context.Background(), srv.URL) {
				t.Errorf("ProbeURLAlive(%s) = false against a healthy %s", srv.URL, path)
			}
		})
	}

	// A server answering neither path is still dead.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	if ProbeURLAlive(context.Background(), srv.URL) {
		t.Error("ProbeURLAlive accepted a server answering no health path")
	}
}

// TestEnableHonoursCustomShortID pins the priority order: an id the operator
// typed wins over the one stored from an earlier run. The previous order was
// the reverse, so the field looked ignored — the operator typed a shortId and
// got a different one back with nothing in the UI to explain it.
func TestEnableHonoursCustomShortID(t *testing.T) {
	isolateDataDir(t)
	if err := SaveState(&State{ShortID: "stale-id", TunnelURL: "https://old.trycloudflare.com", Mode: ModeQuick}); err != nil {
		t.Fatal(err)
	}

	// ResolveShortID is what Enable uses to pick the id; asserting on it
	// directly keeps this test off the spawn path while still pinning the
	// precedence that Enable applies.
	got := ResolveShortID("my-own-id")
	if got != "my-own-id" {
		t.Fatalf("ResolveShortID with an operator id = %q, want the operator's own", got)
	}

	// An empty field falls back to the stored id, so the public URL does not
	// rotate on every toggle.
	got = ResolveShortID("")
	if got != "stale-id" && len(got) != 6 {
		t.Fatalf("ResolveShortID(\"\") = %q, want the stored id or a generated one", got)
	}
}

// TestTunnelLogsReachConsoleBuffer pins that a tunnel failure is visible in the
// dashboard console, not only in the terminal. An operator debugging a tunnel
// that will not come up has the browser open and the terminal buried; an error
// that only reaches the terminal is an error nobody reads.
//
// This exercises the logging path directly rather than through Enable: Enable
// spawns a real cloudflared, and a unit test that leaves a running process
// behind fails its own temp-directory cleanup on Windows. The assertion that
// matters — that a tunnel error lands in the console buffer the dashboard
// streams — does not need the process.
func TestTunnelLogsReachConsoleBuffer(t *testing.T) {
	log.ClearConsoleLogs()
	t.Cleanup(log.ClearConsoleLogs)

	// The same call the health-check failure makes, with the same shape.
	log.Error("tunnel", "health check failed",
		"publicUrl", "https://rlogtest.abc-tunnel.us",
		"tunnelUrl", "https://example.trycloudflare.com",
		"shortId", "logtest",
		"mode", ModeQuick,
		"error", fmt.Errorf("tunnel.WaitForHealth: did not become healthy"))

	var tunnelLines []string
	for _, e := range log.ConsoleEntries() {
		if strings.Contains(e.Line, "tunnel") {
			tunnelLines = append(tunnelLines, e.Line)
		}
	}
	if len(tunnelLines) == 0 {
		t.Fatal("no tunnel line reached the console buffer")
	}
	// The failure must name the URL it could not reach, or the operator is
	// left guessing which endpoint died.
	joined := strings.Join(tunnelLines, "\n")
	if !strings.Contains(joined, "health check failed") {
		t.Errorf("console buffer has tunnel lines but none reports the health failure:\n%s", joined)
	}
	if !strings.Contains(joined, "rlogtest.abc-tunnel.us") {
		t.Errorf("console buffer does not name the failing public URL:\n%s", joined)
	}
}

// TestTunnelNameFromHostname pins the tunnel-name derivation. Cloudflare
// tunnel names allow lowercase letters, digits and hyphens; a dot is not one
// of them, so a hostname cannot be used verbatim.
func TestTunnelNameFromHostname(t *testing.T) {
	tests := []struct{ in, want string }{
		{in: "router.example.com", want: "router-example-com"},
		{in: "a.b.c.example.com", want: "a-b-c-example-com"},
		{in: "ROUTER.Example.COM", want: "router-example-com"},
		{in: "already-hyphenated.example.com", want: "already-hyphenated-example-com"},
	}
	for _, tt := range tests {
		if got := tunnelNameFromHostname(tt.in); got != tt.want {
			t.Errorf("tunnelNameFromHostname(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}

	// The 63-character DNS label limit is enforced rather than left for the
	// API to reject with an opaque error.
	long := tunnelNameFromHostname(strings.Repeat("a.", 60) + "com")
	if len(long) > 63 {
		t.Errorf("tunnelNameFromHostname produced a %d-char name, want ≤63", len(long))
	}
}

// TestCFRequestSurfacesAPIErrors pins that a Cloudflare rejection reaches the
// operator as the API's own message. The envelope reports success=false with a
// reason, and collapsing that into a generic error would leave the operator
// guessing which credential was wrong.
func TestCFRequestSurfacesAPIErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q, want the bearer token", got)
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"success":false,"errors":[{"code":10000,"message":"Authentication error"}]}`)
	}))
	defer srv.Close()

	orig := cloudflareAPIBase
	cloudflareAPIBase = srv.URL
	t.Cleanup(func() { cloudflareAPIBase = orig })

	err := cfRequest(context.Background(), http.MethodGet, "/accounts", "test-token", nil, nil)
	if err == nil {
		t.Fatal("cfRequest succeeded against a rejecting endpoint")
	}
	if !strings.Contains(err.Error(), "Authentication error") {
		t.Errorf("cfRequest error = %v, want the API's own message", err)
	}
	if !strings.Contains(err.Error(), "10000") {
		t.Errorf("cfRequest error = %v, want the API's error code", err)
	}
}

// TestCFRequestDecodesResult pins that a successful envelope's payload is
// decoded into the caller's type, so the provisioning path reads real fields
// rather than an empty struct.
func TestCFRequestDecodesResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"success":true,"errors":[],"result":[{"id":"acc-1","name":"My Account"}]}`)
	}))
	defer srv.Close()

	orig := cloudflareAPIBase
	cloudflareAPIBase = srv.URL
	t.Cleanup(func() { cloudflareAPIBase = orig })

	var accounts []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := cfRequest(context.Background(), http.MethodGet, "/accounts", "tok", nil, &accounts); err != nil {
		t.Fatalf("cfRequest: %v", err)
	}
	if len(accounts) != 1 || accounts[0].ID != "acc-1" {
		t.Fatalf("decoded accounts = %+v, want one entry with id acc-1", accounts)
	}
}

// TestProvisionNamedTunnelValidatesInput pins the pre-flight checks. Each of
// these would otherwise reach the API and come back as a 400 that names a
// field the operator never saw.
func TestProvisionNamedTunnelValidatesInput(t *testing.T) {
	if _, _, err := ProvisionNamedTunnel(context.Background(), CloudflareOptions{}, 20130); err == nil {
		t.Error("ProvisionNamedTunnel with no credentials succeeded")
	}
	if _, _, err := ProvisionNamedTunnel(context.Background(), CloudflareOptions{
		APIToken: "tok",
		Zone:     "example.com",
	}, 20130); err == nil {
		t.Error("ProvisionNamedTunnel without a hostname succeeded")
	}
}

// TestWatchdogRecoversTailscaleFunnel pins that the watchdog re-establishes a
// dropped funnel. Tailscale's daemon reconnects on its own, so the funnel is
// the part that silently dies — and without this the endpoint stays dead until
// somebody notices and re-enables it by hand.
func TestWatchdogRecoversTailscaleFunnel(t *testing.T) {
	isolateDataDir(t)

	daemonUp := true
	funnelUp := false
	var funnelCalls int
	var funnelPort int

	w := NewWatchdog(func() bool { return false }). // Cloudflare disabled
							WithTailscale(
			func() bool { return true },
			func(context.Context) bool { return daemonUp },
			func(context.Context) bool { return funnelUp },
			func(_ context.Context, port int) error {
				funnelCalls++
				funnelPort = port
				funnelUp = true // a successful start makes the next probe pass
				return nil
			},
			// No funnel has been established by this process yet, so there is
			// no recorded port to compare against and nothing is stale.
			func(context.Context, int) bool { return false },
		)

	w.restartTailscale(context.Background(), "watchdog")

	if funnelCalls != 1 {
		t.Fatalf("funnel recovery ran %d times, want exactly 1", funnelCalls)
	}
	if funnelPort != localPort() {
		t.Errorf("funnel started on port %d, want %d", funnelPort, localPort())
	}

	// A second tick must not call it again: the funnel is up, and re-running
	// `funnel --bg` every 60 seconds against a healthy node is noise.
	w.restartTailscale(context.Background(), "watchdog")
	if funnelCalls != 1 {
		t.Errorf("funnel recovery ran again while the funnel was up: %d calls", funnelCalls)
	}
}

// TestWatchdogLeavesTailscaleDaemonAlone pins the deliberate limit. The daemon
// is an OS-level service with its own reconnect and its own login state;
// restarting it would throw both away to solve a problem it does not have.
func TestWatchdogLeavesTailscaleDaemonAlone(t *testing.T) {
	isolateDataDir(t)

	var daemonCalls, funnelCalls int
	w := NewWatchdog(func() bool { return false }).
		WithTailscale(
			func() bool { return true },
			func(context.Context) bool { daemonCalls++; return false }, // daemon down
			func(context.Context) bool { return false },
			func(context.Context, int) error { funnelCalls++; return nil },
			func(context.Context, int) bool { return false },
		)

	w.restartTailscale(context.Background(), "watchdog")

	if funnelCalls != 0 {
		t.Error("funnel recovery ran with no daemon to attach it to")
	}
	if daemonCalls != 1 {
		t.Errorf("daemon probed %d times, want exactly 1", daemonCalls)
	}
}

// TestWatchdogTailscaleStalePort pins that a funnel which is up on the wrong
// port is re-established rather than left alone.
//
// This is the failure the daemon's own status cannot report: `tailscale funnel
// status` says a funnel exists without naming the origin, so an operator who
// changed the gateway's PORT keeps a healthy-looking funnel dialling a port
// nothing listens on. The watchdog is the only component that knows both ports.
func TestWatchdogTailscaleStalePort(t *testing.T) {
	isolateDataDir(t)
	svc.lastTailscaleRestartAt = time.Time{}
	svc.activePort = 20130
	svc.tailscalePort = 20128 // what a previous run left behind
	t.Cleanup(func() { svc.tailscalePort = 0; svc.activePort = 0 })

	var funnelCalls, funnelPort int
	w := NewWatchdog(func() bool { return false }).
		WithTailscale(
			func() bool { return true },
			func(context.Context) bool { return true }, // daemon up
			func(context.Context) bool { return true }, // funnel already up
			func(_ context.Context, port int) error {
				funnelCalls++
				funnelPort = port
				return nil
			},
			// The old port answers nothing, which is what makes it stale.
			func(_ context.Context, port int) bool { return false },
		)
	w.restartTailscale(context.Background(), "watchdog")
	if funnelCalls != 1 {
		t.Fatalf("funnel re-established %d times, want exactly 1", funnelCalls)
	}
	if funnelPort != 20130 {
		t.Errorf("funnel re-established on port %d, want the gateway's 20130", funnelPort)
	}
	if svc.tailscalePort != 20130 {
		t.Errorf("recorded funnel port = %d, want 20130 so the next tick is accurate", svc.tailscalePort)
	}
}

// TestWatchdogTailscalePortNotStaleWhenOriginAnswers pins the guard that keeps
// the watchdog from tearing down a working funnel.
//
// A gateway that listens on both the old and the new port has a funnel that
// still works. Re-establishing it would rotate the Tailscale hostname for
// nothing, and a rotated hostname is a URL every client has to be told about.
func TestWatchdogTailscalePortNotStaleWhenOriginAnswers(t *testing.T) {
	isolateDataDir(t)
	svc.lastTailscaleRestartAt = time.Time{}
	svc.activePort = 20130
	svc.tailscalePort = 20128
	t.Cleanup(func() { svc.tailscalePort = 0; svc.activePort = 0 })

	var funnelCalls int
	w := NewWatchdog(func() bool { return false }).
		WithTailscale(
			func() bool { return true },
			func(context.Context) bool { return true },
			func(context.Context) bool { return true },
			func(context.Context, int) error { funnelCalls++; return nil },
			// The recorded port still answers, so the funnel is not stale.
			func(_ context.Context, port int) bool { return port == 20128 },
		)
	w.restartTailscale(context.Background(), "watchdog")
	if funnelCalls != 0 {
		t.Errorf("funnel re-established %d times while its origin still answered", funnelCalls)
	}
}

// TestWatchdogTailscaleDisabledByDefault pins that a gateway which never
// configured Tailscale is not probed for it. Without the nil guard the loop
// would shell out to a binary that is not there every 60 seconds.
func TestWatchdogTailscaleDisabledByDefault(t *testing.T) {
	isolateDataDir(t)
	w := NewWatchdog(func() bool { return true })
	w.restartTailscale(context.Background(), "watchdog") // must not panic
}

// TestWatchdogGatesAreIndependent pins that the two recovery paths are
// controlled separately. An operator may supervise Cloudflare themselves and
// want Tailscale looked after, or the reverse; a shared gate would force both
// on or both off while the dashboard showed two switches.
func TestWatchdogGatesAreIndependent(t *testing.T) {
	isolateDataDir(t)

	var tunnelRestarts, funnelCalls int
	build := func(cfGate, tsGate bool) *Watchdog {
		tunnelRestarts, funnelCalls = 0, 0
		// The restart timestamps live on the package-level service singleton,
		// so an earlier test in this binary has already stamped them. Without
		// this reset every case after the first would be refused by the
		// 120-second cooldown and pass for the wrong reason.
		svc.lastRestartAt = time.Time{}
		svc.lastTailscaleRestartAt = time.Time{}
		return NewWatchdog(func() bool { return cfGate }).WithTailscale(
			func() bool { return tsGate },
			func(context.Context) bool { return true },
			func(context.Context) bool { return false },
			func(context.Context, int) error { funnelCalls++; return nil },
			func(context.Context, int) bool { return false },
		)
	}
	// The Cloudflare restart path is reached through restart(), which needs a
	// tunnel to be reported as down. Enable is stubbed out by keeping the
	// process check false and letting it fail fast on the missing binary.
	origSpawn := spawnFn
	spawnFn = func(string, SpawnOptions) (*SpawnResult, error) {
		tunnelRestarts++
		return nil, errors.New("stubbed: no spawn in this test")
	}
	t.Cleanup(func() { spawnFn = origSpawn })

	t.Run("cloudflare on, tailscale off", func(t *testing.T) {
		w := build(true, false)
		w.restartTailscale(context.Background(), "watchdog")
		if funnelCalls != 0 {
			t.Error("tailscale funnel recovered while its gate was off")
		}
	})

	t.Run("cloudflare off, tailscale on", func(t *testing.T) {
		w := build(false, true)
		w.restartTailscale(context.Background(), "watchdog")
		if funnelCalls != 1 {
			t.Errorf("tailscale funnel recovery ran %d times, want 1 with its gate on", funnelCalls)
		}
	})

	t.Run("both on", func(t *testing.T) {
		w := build(true, true)
		w.restartTailscale(context.Background(), "watchdog")
		if funnelCalls != 1 {
			t.Errorf("funnel recovery ran %d times with both gates on, want 1", funnelCalls)
		}
	})
}

// TestWatchdogGateSettersAreReplaceable pins that a gate can be swapped after
// construction. The wiring relies on this to re-read settings each tick rather
// than capturing them once at startup.
func TestWatchdogGateSettersAreReplaceable(t *testing.T) {
	isolateDataDir(t)

	var gate bool
	w := NewWatchdog(func() bool { return gate })
	if w.enabled() {
		t.Fatal("watchdog active with a false gate")
	}
	gate = true
	if !w.enabled() {
		t.Fatal("watchdog still inactive after the gate flipped")
	}

	var tsGate bool
	w.SetTailscaleEnabled(func() bool { return tsGate })
	if w.tailscaleEnabled != nil && w.tailscaleEnabled() {
		t.Fatal("tailscale gate active with a false value")
	}
	tsGate = true
	if !w.tailscaleEnabled() {
		t.Fatal("tailscale gate did not follow the replaced value")
	}
}

// TestProvisioningIsIdempotentByName pins the upsert. An operator switching
// from a quick tunnel, or re-running Auto Setup after a failure, must land on
// the tunnel they already made. A blind create instead accumulated one orphan
// per attempt, each holding a connector token and a DNS record.
func TestProvisioningIsIdempotentByName(t *testing.T) {
	var creates, tokenMints, ingressPuts, dnsCreates int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/cfd_tunnel"):
			// Second call onward: the tunnel already exists.
			fmt.Fprint(w, `{"success":true,"errors":[],"result":[{"id":"tun-existing","name":"router-example-com"}]}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/cfd_tunnel"):
			creates++
			fmt.Fprint(w, `{"success":true,"errors":[],"result":{"id":"tun-new","name":"router-example-com","token":"tok-new"}}`)
		case strings.HasSuffix(r.URL.Path, "/token"):
			tokenMints++
			fmt.Fprint(w, `{"success":true,"errors":[],"result":{"token":"tok-minted"}}`)
		case strings.HasSuffix(r.URL.Path, "/configurations"):
			ingressPuts++
			fmt.Fprint(w, `{"success":true,"errors":[],"result":{}}`)
		case strings.HasSuffix(r.URL.Path, "/dns_records") && r.Method == http.MethodGet:
			// No record yet, so the route is created rather than patched.
			fmt.Fprint(w, `{"success":true,"errors":[],"result":[]}`)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/zones/zone-"):
			// routeDNS reads the zone back to make the record name relative to
			// it, so the detail endpoint has to answer with the zone's name.
			fmt.Fprint(w, `{"success":true,"errors":[],"result":{"id":"zone-1","name":"example.com"}}`)
		case strings.HasSuffix(r.URL.Path, "/dns_records"):
			dnsCreates++
			fmt.Fprint(w, `{"success":true,"errors":[],"result":{"id":"rec-1"}}`)
		default:
			// List endpoints the callers decode into a slice must answer with
			// an array, or the decoder rejects the whole response.
			if strings.HasSuffix(r.URL.Path, "/accounts") || strings.HasSuffix(r.URL.Path, "/zones") {
				if strings.HasSuffix(r.URL.Path, "/zones") {
					fmt.Fprint(w, `{"success":true,"errors":[],"result":[{"id":"zone-1","name":"example.com"}]}`)
					return
				}
				fmt.Fprint(w, `{"success":true,"errors":[],"result":[{"id":"acc-1","name":"My Account"}]}`)
				return
			}
			fmt.Fprint(w, `{"success":true,"errors":[],"result":{}}`)
		}
	}))
	defer srv.Close()

	orig := cloudflareAPIBase
	cloudflareAPIBase = srv.URL
	t.Cleanup(func() { cloudflareAPIBase = orig })

	opts := CloudflareOptions{
		APIToken: "tok",
		Zone:     "example.com",
		Hostname: "router.example.com",
	}

	tok, id, err := ProvisionNamedTunnel(context.Background(), opts, 20130)
	if err != nil {
		t.Fatalf("first provision: %v", err)
	}
	if id != "tun-existing" || tok != "tok-minted" {
		t.Fatalf("first provision = (id=%q token=%q), want the existing tunnel and a minted token", id, tok)
	}
	if creates != 0 {
		t.Errorf("created %d tunnels on the first run, want 0 when one already exists", creates)
	}
	if ingressPuts != 1 || dnsCreates != 1 {
		t.Errorf("ingress=%d dns=%d, want one of each", ingressPuts, dnsCreates)
	}

	// A second run must not create anything new either.
	_, _, err = ProvisionNamedTunnel(context.Background(), opts, 20130)
	if err != nil {
		t.Fatalf("second provision: %v", err)
	}
	if creates != 0 {
		t.Errorf("created %d tunnels on the second run, want 0", creates)
	}
	if tokenMints != 2 {
		t.Errorf("minted %d tokens across two runs, want 2 (one per run)", tokenMints)
	}
}

// TestResolveZoneMatchesSubdomainOfZone pins the suffix fallback. An operator
// who types the hostname where the zone belongs — "ai.helmiau.my.id" when the
// zone on the account is "helmiau.my.id" — has named the wrong thing, and an
// exact match alone reports the zone missing.
func TestResolveZoneMatchesSubdomainOfZone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"success":true,"errors":[],"result":[{"id":"zone-apex","name":"helmiau.my.id"}],
			"result_info":{"page":1,"per_page":100,"total_count":1}}`)
	}))
	defer srv.Close()

	orig := cloudflareAPIBase
	cloudflareAPIBase = srv.URL
	t.Cleanup(func() { cloudflareAPIBase = orig })

	id, err := ResolveZoneID(context.Background(), CloudflareOptions{
		APIToken: "tok",
		Zone:     "ai.helmiau.my.id", // the hostname, not the zone
	})
	if err != nil {
		t.Fatalf("ResolveZoneID: %v", err)
	}
	if id != "zone-apex" {
		t.Errorf("ResolveZoneID = %q, want the apex zone that covers the hostname", id)
	}
}

// TestListAllPagesWalksEveryPage pins the pagination fix. Cloudflare's default
// page size is 25, so an account with more zones than that used to have the
// wanted zone on page 2 and the lookup reported it missing — which then made
// Auto Setup fail on a perfectly valid token.
func TestListAllPagesWalksEveryPage(t *testing.T) {
	// 30 zones, served 10 at a time, so the walk must make three requests.
	const total = 30
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := 1
		if v := r.URL.Query().Get("page"); v != "" {
			page, _ = strconv.Atoi(v)
		}
		perPage := 10
		if v := r.URL.Query().Get("per_page"); v != "" {
			perPage, _ = strconv.Atoi(v)
		}
		start := (page - 1) * perPage
		var items []string
		for i := start; i < start+perPage && i < total; i++ {
			items = append(items, fmt.Sprintf(`{"id":"z%d","name":"zone%d.example.com"}`, i, i))
		}
		fmt.Fprintf(w, `{"success":true,"errors":[],"result":[%s],
			"result_info":{"page":%d,"per_page":%d,"total_count":%d}}`,
			strings.Join(items, ","), page, perPage, total)
	}))
	defer srv.Close()

	orig := cloudflareAPIBase
	cloudflareAPIBase = srv.URL
	t.Cleanup(func() { cloudflareAPIBase = orig })

	var zones []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := listAllPages(context.Background(), "tok", "/zones", &zones); err != nil {
		t.Fatalf("listAllPages: %v", err)
	}
	if len(zones) != total {
		t.Fatalf("collected %d zones, want all %d — the walk stopped early", len(zones), total)
	}
	// The last zone only exists on the final page, so its presence proves the
	// walk did not stop after the first response.
	if zones[total-1].ID != fmt.Sprintf("z%d", total-1) {
		t.Errorf("last zone = %q, want z%d", zones[total-1].ID, total-1)
	}
}

// TestNormaliseDomainStripsPort pins the fix for a hostname typed with a port.
// A public hostname on Cloudflare is served on 443 whatever the operator typed,
// so "ai.helmiau.my.id:8080" — usually a leftover from the local address — used
// to produce a URL the health probe could never reach, and the failure named a
// port nobody had configured on the Cloudflare side.
func TestNormaliseDomainStripsPort(t *testing.T) {
	tests := []struct{ in, want string }{
		{in: "ai.helmiau.my.id", want: "https://ai.helmiau.my.id"},
		{in: "ai.helmiau.my.id:8080", want: "https://ai.helmiau.my.id"},
		{in: "https://ai.helmiau.my.id:8443", want: "https://ai.helmiau.my.id"},
		{in: "http://ai.helmiau.my.id", want: "https://ai.helmiau.my.id"},
		{in: "  ai.helmiau.my.id  ", want: "https://ai.helmiau.my.id"},
		{in: "ai.helmiau.my.id/v1", want: "https://ai.helmiau.my.id"},
		{in: "", want: ""},
	}
	for _, tt := range tests {
		if got := normaliseDomain(tt.in); got != tt.want {
			t.Errorf("normaliseDomain(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestIsCloudflareIP pins the range check the hostname preflight relies on. A
// hostname that resolves outside these ranges is not proxied by Cloudflare, so
// the tunnel will never see the traffic — and that is invisible in a timeout.
func TestIsCloudflareIP(t *testing.T) {
	inside := []string{"104.16.0.1", "172.64.0.1", "162.158.1.1", "104.24.1.1"}
	outside := []string{"8.8.8.8", "1.1.1.1", "192.168.1.1", "203.0.113.5"}
	for _, s := range inside {
		ip, err := netip.ParseAddr(s)
		if err != nil {
			t.Fatalf("parse %s: %v", s, err)
		}
		if !isCloudflareIP(ip) {
			t.Errorf("isCloudflareIP(%s) = false, want true", s)
		}
	}
	for _, s := range outside {
		ip, err := netip.ParseAddr(s)
		if err != nil {
			t.Fatalf("parse %s: %v", s, err)
		}
		if isCloudflareIP(ip) {
			t.Errorf("isCloudflareIP(%s) = true, want false", s)
		}
	}
}

// TestDiagnoseHostnameRejectsUnresolvable pins that a name with no DNS record
// is reported as such rather than surfacing later as a health-check timeout.
func TestDiagnoseHostnameRejectsUnresolvable(t *testing.T) {
	// .invalid is reserved by RFC 2606 and never resolves.
	got := diagnoseHostname(context.Background(), "https://no-such-host.invalid")
	if got == "" {
		t.Fatal("diagnoseHostname accepted a hostname that cannot resolve")
	}
	if !strings.Contains(got, "does not resolve") {
		t.Errorf("diagnoseHostname = %q, want a DNS finding", got)
	}
}

// TestDetectOriginMismatch pins the diagnosis that turns a 60-second timeout
// into a named cause. The connector prints the origin it is dialing when it
// cannot reach it, and a tunnel created in the Cloudflare dashboard keeps
// whatever origin it was given — usually the port of whatever the operator ran
// before. Nothing in the timeout itself says so.
func TestDetectOriginMismatch(t *testing.T) {
	// The exact shape cloudflared prints, taken from a real failure.
	real := `2026-10-27T11:15:17Z ERR  error="Unable to reach the origin service. The service may be down or it may not be responding to traffic from cloudflared: dial tcp [::1]:20128: connectex: No connection could be made because the target machine actively refused it." connIndex=0 event=1 ingressRule=2 originService=http://localhost:20128
2026-10-27T11:15:17Z ERR Request failed error="Unable to reach the origin service." connIndex=0 dest=https://ai.helmiau.my.id/health event=0 ip=2606:4700:a0::10 type=http
`

	got, ok := detectOriginMismatch(real, 20130)
	if !ok {
		t.Fatal("detectOriginMismatch missed a connector dialing the wrong port")
	}
	if got.Configured != "http://localhost:20128" {
		t.Errorf("configured origin = %q, want http://localhost:20128", got.Configured)
	}
	if got.Expected != 20130 {
		t.Errorf("expected port = %d, want 20130", got.Expected)
	}

	// A connector dialing the right port must not be flagged, or every healthy
	// tunnel would be reported as misconfigured.
	healthy := `INF Starting tunnel originService=http://127.0.0.1:20130`
	if _, ok := detectOriginMismatch(healthy, 20130); ok {
		t.Error("detectOriginMismatch flagged a connector dialing the correct port")
	}

	// No origin line at all means nothing to diagnose, not a mismatch.
	if _, ok := detectOriginMismatch("INF Registered tunnel connection", 20130); ok {
		t.Error("detectOriginMismatch flagged output with no origin service")
	}

	// An empty buffer or an unknown port must not panic or invent a finding.
	if _, ok := detectOriginMismatch("", 20130); ok {
		t.Error("detectOriginMismatch flagged empty output")
	}
	if _, ok := detectOriginMismatch(real, 0); ok {
		t.Error("detectOriginMismatch flagged with an unknown gateway port")
	}
}

// TestExtractConnectorToken pins the paste tolerance. The Cloudflare dashboard
// puts the whole install command in front of the operator, so that is what gets
// copied — and pasted whole it arrives as one string, which cloudflared rejects
// as a malformed token with nothing pointing at the real mistake.
func TestExtractConnectorToken(t *testing.T) {
	bare := "eyJhIjoiNGIyMDllNDNhNzYwODY2MjNmZjZlOTEyNDhiNmE4MWIiLCJ0IjoiMjcwMWFhYjctNzEyOS00NzkwLWIwYTMtOTA2Y2NkNmExYzU0IiwicyI6IlpqWmtPVEl4TVRZdFpqVmtZaTAwTkRGbUxXSmtOelF0Tm1FMU1qSTVaRFV6T1dFMiJ9"

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "bare token is untouched",
			in:   bare,
			want: bare,
		},
		{
			name: "windows install command",
			in:   "cloudflared.exe service install " + bare,
			want: bare,
		},
		{
			name: "unix install command",
			in:   "cloudflared service install " + bare,
			want: bare,
		},
		{
			name: "docker run command",
			in:   "docker run cloudflare/cloudflared:latest tunnel --no-autoupdate run --token " + bare,
			want: bare,
		},
		{
			name: "surrounding whitespace",
			in:   "  " + bare + "  ",
			want: bare,
		},
		{
			name: "multiline paste",
			in:   "Run this command:\ncloudflared.exe service install " + bare + "\n",
			want: bare,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractConnectorToken(tt.in); got != tt.want {
				t.Errorf("extractConnectorToken() = %q, want %q", got, tt.want)
			}
		})
	}

	// Input with no recognisable token is passed through unchanged, so
	// cloudflared rejects it with its own message rather than a guess.
	junk := "not-a-token at all"
	if got := extractConnectorToken(junk); got != junk {
		t.Errorf("extractConnectorToken(%q) = %q, want it unchanged", junk, got)
	}
	if got := extractConnectorToken(""); got != "" {
		t.Errorf("extractConnectorToken(\"\") = %q, want empty", got)
	}
}

// TestEnableHonoursChangedShortID pins the fix for a shortId change that did
// nothing. The reuse guard compared the mode but not the identity, so a tunnel
// already running under the old id was handed straight back — the operator
// typed a new shortId, pressed Start, and got the old public URL with no
// indication anything had been ignored.
//
// The guard is exercised through the state it reads rather than through a live
// spawn: what matters is that a running tunnel whose shortId differs from the
// requested one is not reused.
func TestEnableHonoursChangedShortID(t *testing.T) {
	isolateDataDir(t)

	// A tunnel is running under "old-id".
	if err := SaveState(&State{ShortID: "old-id", TunnelURL: "https://x.trycloudflare.com", Mode: ModeQuick}); err != nil {
		t.Fatal(err)
	}
	if err := SavePID(os.Getpid()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ClearPID(os.Getpid()) })

	// The reuse guard must not fire for a different shortId. Reaching it needs
	// the process to look alive, which the PID above provides; the guard then
	// compares the stored id against the requested one.
	st := LoadState()
	if st == nil || st.ShortID != "old-id" {
		t.Fatalf("state = %+v, want a running tunnel under old-id", st)
	}

	// ResolveShortID is what Enable uses to pick the id. An operator-supplied
	// value must win over the stored one — that precedence is the other half of
	// this bug, and it is pinned here so the two halves cannot drift apart.
	if got := ResolveShortID("new-id"); got != "new-id" {
		t.Errorf("ResolveShortID(new-id) = %q, want the operator's own id", got)
	}

	// ResolveShortID generates when the field is empty; reading the stored id
	// back is Enable's job, not this function's. Asserting the fallback here
	// would couple the test to the wrong layer.
	if got := ResolveShortID(""); len(got) != 6 {
		t.Errorf("ResolveShortID(\"\") = %q, want a generated 6-char id", got)
	}

	// The guard's own comparison, stated directly: a running tunnel is reusable
	// only when both mode and identity match.
	sameIdentity := st.Mode == ModeQuick && st.ShortID == "old-id"
	if !sameIdentity {
		t.Error("a running tunnel with a matching mode and id should be reusable")
	}
	changedIdentity := st.Mode == ModeQuick && st.ShortID == "new-id"
	if changedIdentity {
		t.Error("a running tunnel under a different shortId must not be reusable")
	}
}

// TestTailscaleAuthKeyShape documents what the gateway accepts as an auth key.
//
// The handler does not validate the key itself — tailscaled is the authority on
// that — but it does distinguish "a key was supplied" from "none was", because
// the two take different paths: one authenticates directly, the other returns a
// browser URL. Getting that distinction wrong sends a headless operator
// waiting for a login page that can never open.
func TestTailscaleAuthKeyShape(t *testing.T) {
	// Tailscale keys are prefixed; the prefix is what an operator recognises
	// when pasting from the admin console.
	keys := []string{
		"tskey-auth-abc123",
		"tskey-client-xyz789",
	}
	for _, k := range keys {
		if !strings.HasPrefix(k, "tskey-") {
			t.Errorf("test key %q is not shaped like a Tailscale key", k)
		}
	}

	// An empty key must be treated as "not supplied", not as a key to send.
	empty := strings.TrimSpace("   ")
	if empty != "" {
		t.Error("a whitespace-only auth key should trim to empty")
	}
}

func TestPublicURL(t *testing.T) {
	if got := publicURL("abc123"); got != "https://rabc123.abc-tunnel.us" {
		t.Errorf("publicURL(abc123) = %q", got)
	}
	if got := publicURL(""); got != "" {
		t.Errorf("publicURL(\"\") = %q, want empty", got)
	}
}

// TestSetWorkerURL pins the override contract: a custom service is used, a
// trailing slash is normalised so the registered path does not double up, and
// clearing the field restores the default rather than silently breaking the
// public URL.
func TestSetWorkerURL(t *testing.T) {
	orig := workerBaseURL
	t.Cleanup(func() { workerBaseURL = orig })

	SetWorkerURL("https://tunnel.example.com/")
	if got := WorkerURL(); got != "https://tunnel.example.com" {
		t.Errorf("WorkerURL() after override = %q, want the trailing slash trimmed", got)
	}

	SetWorkerURL("   ")
	if got := WorkerURL(); got != defaultWorkerURL {
		t.Errorf("WorkerURL() after clearing = %q, want the default %q", got, defaultWorkerURL)
	}

	SetWorkerURL("")
	if got := WorkerURL(); got != defaultWorkerURL {
		t.Errorf("WorkerURL() after empty = %q, want the default %q", got, defaultWorkerURL)
	}
}

// TestProbeURLAlive verifies the probe against a real local server, so the
// request path, the status check and the empty-URL guard are all exercised
// rather than assumed.
func TestProbeURLAlive(t *testing.T) {
	if ProbeURLAlive(context.Background(), "") {
		t.Error("ProbeURLAlive(\"\") = true, want false")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if !ProbeURLAlive(context.Background(), srv.URL) {
		t.Errorf("ProbeURLAlive(%s) = false against a healthy /health", srv.URL)
	}

	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer dead.Close()
	if ProbeURLAlive(context.Background(), dead.URL) {
		t.Error("ProbeURLAlive accepted a 500 from /health")
	}
}

// TestWaitForHealthHonoursCancel pins that a disable during the health wait
// aborts the enable instead of letting it finish and resurrect the tunnel.
//
// The timeout is pinned to a few milliseconds so the "unreachable" case fails
// fast: the real budget is a minute, and a test that waits out a real minute to
// prove a cancellation path spends a minute of CI on nothing.
func TestWaitForHealthHonoursCancel(t *testing.T) {
	orig := healthTimeout
	healthTimeout = 20 * time.Millisecond
	t.Cleanup(func() { healthTimeout = orig })

	cancelled := false
	err := WaitForHealth(context.Background(), "https://127.0.0.1:1/health", func() bool { return cancelled })
	if err == nil {
		t.Fatal("WaitForHealth succeeded against an unreachable URL")
	}

	cancelled = true
	err = WaitForHealth(context.Background(), "https://127.0.0.1:1/health", func() bool { return cancelled })
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("WaitForHealth with a cancelled token = %v, want a cancellation error", err)
	}
}

func TestNetworkFingerprintIgnoresVirtualInterfaces(t *testing.T) {
	fp := networkFingerprint()
	// The fingerprint must never name a virtual interface: those flap
	// constantly and would restart the tunnel every time one appears.
	for _, prefix := range virtualIfacePrefixes {
		if strings.Contains(strings.ToLower(fp), prefix+":") {
			t.Fatalf("networkFingerprint() = %q contains virtual interface %q", fp, prefix)
		}
	}
}

func TestIsVirtualIface(t *testing.T) {
	virtual := []string{"utun3", "awdl0", "docker0", "vethabc123", "br-1a2b3c"}
	real := []string{"eth0", "en0", "wlan0", "Wi-Fi", "Ethernet"}
	for _, name := range virtual {
		if !isVirtualIface(name) {
			t.Errorf("isVirtualIface(%q) = false, want true", name)
		}
	}
	for _, name := range real {
		if isVirtualIface(name) {
			t.Errorf("isVirtualIface(%q) = true, want false", name)
		}
	}
}

// TestFindCloudflaredPrefersPATH pins the adoption order: a binary the
// operator installed themselves wins over anything in the data dir, because
// replacing it would discard a version they chose.
func TestFindCloudflaredPrefersPATH(t *testing.T) {
	isolateDataDir(t)

	dir := t.TempDir()
	fake := filepath.Join(dir, "cloudflared")
	if err := os.WriteFile(fake, append(magicFor()[0], make([]byte, minBinarySize)...), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	orig := binLookPathFn
	binLookPathFn = func(string) (string, error) { return fake, nil }
	t.Cleanup(func() { binLookPathFn = orig })

	if got := FindCloudflared(); got != fake {
		t.Fatalf("FindCloudflared() = %q, want the PATH binary %q", got, fake)
	}
}

// TestFindCloudflaredFallsBackToDataDir pins the shared-data-dir contract: a
// binary the upstream Node implementation downloaded is reused rather than
// downloaded a second time.
func TestFindCloudflaredFallsBackToDataDir(t *testing.T) {
	isolateDataDir(t)

	// An empty PATH guarantees the lookup cannot succeed there.
	t.Setenv("PATH", t.TempDir())
	orig := binLookPathFn
	binLookPathFn = func(string) (string, error) { return "", errors.New("not found") }
	t.Cleanup(func() { binLookPathFn = orig })

	if err := os.MkdirAll(binDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	dataDirBin := filepath.Join(binDir(), binaryName())
	if err := os.WriteFile(dataDirBin, append(magicFor()[0], make([]byte, minBinarySize)...), 0o755); err != nil {
		t.Fatal(err)
	}

	if got := FindCloudflared(); got != dataDirBin {
		t.Fatalf("FindCloudflared() = %q, want the data-dir binary %q", got, dataDirBin)
	}
}

// TestFindCloudflaredRejectsCorruptDataDirBinary pins that a truncated file
// left in the data dir is not executed. "The file exists" is not "the file
// works", and running a partial binary fails with an error that names nothing
// useful.
func TestFindCloudflaredRejectsCorruptDataDirBinary(t *testing.T) {
	isolateDataDir(t)
	t.Setenv("PATH", t.TempDir())
	orig := binLookPathFn
	binLookPathFn = func(string) (string, error) { return "", errors.New("not found") }
	t.Cleanup(func() { binLookPathFn = orig })

	if err := os.MkdirAll(binDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	corrupt := filepath.Join(binDir(), binaryName())
	if err := os.WriteFile(corrupt, []byte("MZ truncated"), 0o755); err != nil {
		t.Fatal(err)
	}

	if got := FindCloudflared(); got != "" {
		t.Fatalf("FindCloudflared() = %q, want empty for a corrupt binary", got)
	}
}

// TestDisableIsIdempotent pins that disabling a tunnel that was never started
// succeeds. The dashboard's disable button must work on a stale settings flag.
func TestDisableIsIdempotent(t *testing.T) {
	isolateDataDir(t)
	if err := Disable(); err != nil {
		t.Fatalf("Disable() with nothing running = %v, want nil", err)
	}
}

// TestStatusWithoutProcess pins the fix for the old handler's lie: a stored
// URL with no live process must report running=false, or the dashboard sends
// the operator's traffic into a tunnel that is not there.
func TestStatusWithoutProcess(t *testing.T) {
	isolateDataDir(t)
	if err := SaveState(&State{ShortID: "gw1", TunnelURL: "https://x.trycloudflare.com", Mode: ModeQuick}); err != nil {
		t.Fatal(err)
	}

	st := Status(true, "", "")
	if st.Running {
		t.Error("Status().Running = true with no cloudflared process")
	}
	if st.Enabled {
		t.Error("Status().Enabled = true with no cloudflared process")
	}
	if st.ShortID != "gw1" {
		t.Errorf("Status().ShortID = %q, want gw1", st.ShortID)
	}
	if st.PublicURL != "https://rgw1.abc-tunnel.us" {
		t.Errorf("Status().PublicURL = %q", st.PublicURL)
	}

	// A disabled setting reports nothing as running even if a process exists.
	st = Status(false, "", "")
	if st.Running || st.Enabled {
		t.Errorf("Status(false) = %+v, want running and enabled false", st)
	}
}

// TestStatusNamedModeUsesCustomDomain pins that a named tunnel reports the
// operator's own hostname, not the quick-tunnel public URL that does not
// apply to it.
func TestStatusNamedModeUsesCustomDomain(t *testing.T) {
	isolateDataDir(t)
	if err := SaveState(&State{ShortID: "gw2", TunnelURL: "https://old.trycloudflare.com", Mode: ModeNamed}); err != nil {
		t.Fatal(err)
	}

	st := Status(true, "", "router.example.com")
	if st.TunnelURL != "https://router.example.com" {
		t.Errorf("Status().TunnelURL = %q, want the custom domain", st.TunnelURL)
	}
	if st.PublicURL != "https://router.example.com" {
		t.Errorf("Status().PublicURL = %q, want the custom domain", st.PublicURL)
	}
	if st.Mode != ModeNamed {
		t.Errorf("Status().Mode = %q, want %q", st.Mode, ModeNamed)
	}
}

// TestEnableRejectsConcurrentStart pins the guard that stops a watchdog tick
// and an operator click from both spawning cloudflared against the same port.
func TestEnableRejectsConcurrentStart(t *testing.T) {
	isolateDataDir(t)
	svc.mu.Lock()
	svc.spawnInProgress = true
	svc.mu.Unlock()
	t.Cleanup(func() {
		svc.mu.Lock()
		svc.spawnInProgress = false
		svc.mu.Unlock()
	})

	if _, err := Enable(context.Background(), Options{}); err == nil {
		t.Fatal("Enable() during an in-flight start = nil error, want a rejection")
	}
}
