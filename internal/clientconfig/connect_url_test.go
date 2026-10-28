package clientconfig

import (
	"errors"
	"strings"
	"testing"
)

// noNetwork is the AssertPublic stub every validation test uses unless the case
// is about step 8 itself. It fails the test if called, so a case that must stop
// at step 7 cannot quietly reach DNS.
func noNetwork(t *testing.T) AssertPublicFunc {
	t.Helper()
	return func(rawURL string) error {
		t.Errorf("step 8 called with %q — this case must stop before it", rawURL)
		return nil
	}
}

func TestValidateConnectURL_RejectsAndAccepts(t *testing.T) {
	allowPublic := func(string) error { return nil }

	tests := []struct {
		name  string
		raw   string
		opts  ConnectURLOptions
		want  string // canonical String(), when the case is accepted
		errIs error
	}{
		// U1 — userinfo is refused outright: connect logs in with a JSON body,
		// so a "user:pass@" URL is a pasted connection string or a secret about
		// to be printed (§3.4).
		{name: "U1 userinfo rejected", raw: "https://user:pass@example.com/", errIs: ErrUserinfoNotAllowed},
		{name: "U1b percent-encoded userinfo rejected", raw: "https://user%40x:pass@example.com/", errIs: ErrUserinfoNotAllowed},

		// U2–U3b — query and fragment.
		{name: "U2 query rejected", raw: "https://example.com/?a=b", errIs: ErrQueryFragmentNotAllowed},
		{name: "U3 fragment rejected", raw: "https://example.com/#top", errIs: ErrQueryFragmentNotAllowed},
		{name: "U3b query and fragment rejected", raw: "https://example.com/?a=b#c", errIs: ErrQueryFragmentNotAllowed},

		// U4–U4b — path, including the percent-decoded traversal.
		{name: "U4 path rejected", raw: "https://example.com/admin", errIs: ErrPathNotAllowed},
		{name: "U4b percent-decoded traversal rejected", raw: "https://example.com/%2e%2e/", errIs: ErrPathNotAllowed},
		{name: "empty path accepted", raw: "https://example.com", opts: ConnectURLOptions{AssertPublic: allowPublic}, want: "https://example.com"},
		{name: "single slash accepted", raw: "https://example.com/", opts: ConnectURLOptions{AssertPublic: allowPublic}, want: "https://example.com"},

		// U5–U6 — port.
		{name: "U5 leading-zero port rejected", raw: "https://example.com:0443/", errIs: ErrInvalidPort},
		{name: "U6 port zero rejected", raw: "https://example.com:0/", errIs: ErrInvalidPort},
		{name: "port above 65535 rejected", raw: "https://example.com:99999/", errIs: ErrInvalidPort},

		// U7–U8 — the two hostname cases. U8 is a mechanism guard: it uses the
		// real Cyrillic "а" (U+0430), so an implementation that converted to
		// punycode instead of refusing would let it through and this case would
		// go red. See docs/specs/connect-security.md §7.4.
		{name: "U7 unicode hostname rejected", raw: "https://éxample.com/", errIs: ErrNonASCIIHostname},
		{name: "U8 Cyrillic homograph rejected", raw: "https://exаmple.com/", errIs: ErrNonASCIIHostname},

		// U9 — scheme.
		{name: "U9 ftp scheme rejected", raw: "ftp://example.com/", errIs: ErrUnsupportedScheme},
		{name: "javascript scheme rejected", raw: "javascript://x.example.com/", errIs: ErrUnsupportedScheme},

		// U10 — a public host with a passing step 8.
		{name: "U10 public host accepted", raw: "https://gateway.example.com/", opts: ConnectURLOptions{AssertPublic: allowPublic}, want: "https://gateway.example.com"},

		// U11–U12 — canonical form.
		{name: "U11 uppercase scheme and host canonicalised", raw: "HTTPS://EXAMPLE.COM", opts: ConnectURLOptions{AssertPublic: allowPublic}, want: "https://example.com"},
		{name: "U12 trailing dot stripped", raw: "https://example.com./", opts: ConnectURLOptions{AssertPublic: allowPublic}, want: "https://example.com"},
		{name: "default port dropped", raw: "https://example.com:443/", opts: ConnectURLOptions{AssertPublic: allowPublic}, want: "https://example.com"},
		{name: "non-default port kept", raw: "https://example.com:8443/", opts: ConnectURLOptions{AssertPublic: allowPublic}, want: "https://example.com:8443"},

		// U13–U14b — the HTTPS rule. U14b is the mandatory negative: the flag
		// only ever relaxed the loopback half of the condition.
		{name: "U13 loopback http without flag rejected", raw: "http://127.0.0.1:20128", errIs: ErrInsecureConnection},
		{name: "U14 loopback http with flag accepted", raw: "http://127.0.0.1:20128", opts: ConnectURLOptions{AllowInsecure: true}, want: "http://127.0.0.1:20128"},
		{name: "U14b public http with flag still rejected", raw: "http://example.com", opts: ConnectURLOptions{AllowInsecure: true}, errIs: ErrInsecureConnection},
		{name: "public http without flag rejected", raw: "http://example.com", errIs: ErrInsecureConnection},

		// U15–U15c — loopback forms.
		{name: "U15 IPv6 loopback accepted", raw: "http://[::1]:20128", opts: ConnectURLOptions{AllowInsecure: true}, want: "http://[::1]:20128"},
		{name: "U15b IPv4-mapped loopback accepted", raw: "http://[::ffff:127.0.0.1]:20128", opts: ConnectURLOptions{AllowInsecure: true}, want: "http://[::ffff:127.0.0.1]:20128"},
		{name: "U15c unspecified address is not loopback", raw: "http://0.0.0.0:20128", opts: ConnectURLOptions{AllowInsecure: true}, errIs: ErrInsecureConnection},
		{name: "localhost over http needs the flag", raw: "http://localhost:20128", errIs: ErrInsecureConnection},
		{name: "loopback with leading-zero port rejected", raw: "http://127.0.0.1:020128", opts: ConnectURLOptions{AllowInsecure: true}, errIs: ErrInvalidPort},

		// U17 — a bare host defaults to https, never http (§3.2.1).
		{name: "U17 missing scheme defaults to https", raw: "gateway.example.com", opts: ConnectURLOptions{AssertPublic: allowPublic}, want: "https://gateway.example.com"},
		{name: "bare host with port keeps the port", raw: "localhost:20128", want: "https://localhost:20128"},

		// Remaining step-0/step-1 edges.
		{name: "empty input rejected", raw: "   ", errIs: ErrInvalidURL},
		{name: "scheme only rejected", raw: "https://", errIs: ErrInvalidURL},
		{name: "host of dots rejected", raw: "https://./", errIs: ErrInvalidURL},
		{name: "NUL byte stripped before parse", raw: "https://exa\x00mple.com/", opts: ConnectURLOptions{AssertPublic: allowPublic}, want: "https://example.com"},
		{name: "surrounding whitespace trimmed", raw: "  https://example.com/  ", opts: ConnectURLOptions{AssertPublic: allowPublic}, want: "https://example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := tt.opts
			if opts.AssertPublic == nil {
				opts.AssertPublic = noNetwork(t)
			}
			got, err := ValidateConnectURL(tt.raw, opts)

			if tt.errIs != nil {
				if !errors.Is(err, tt.errIs) {
					t.Fatalf("ValidateConnectURL(%q) err = %v, want errors.Is(%v)", tt.raw, err, tt.errIs)
				}
				if got.URL != nil {
					t.Errorf("rejected input returned endpoint %v", got.URL)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateConnectURL(%q) = %v, want nil", tt.raw, err)
			}
			if got.URL.String() != tt.want {
				t.Errorf("canonical = %q, want %q", got.URL.String(), tt.want)
			}
		})
	}
}

// U16 — step 8 runs, and its refusal surfaces as ErrBlockedHost.
func TestValidateConnectURL_Step8BlocksPublicHost(t *testing.T) {
	called := 0
	opts := ConnectURLOptions{AssertPublic: func(rawURL string) error {
		called++
		if rawURL != "https://rebind.example.com" {
			t.Errorf("step 8 got %q, want the canonical form", rawURL)
		}
		return errors.New("resolves to 127.0.0.1")
	}}

	_, err := ValidateConnectURL("https://rebind.example.com/", opts)
	if !errors.Is(err, ErrBlockedHost) {
		t.Fatalf("err = %v, want ErrBlockedHost", err)
	}
	if called != 1 {
		t.Errorf("step 8 called %d times, want 1", called)
	}
	if !strings.Contains(err.Error(), "127.0.0.1") {
		t.Errorf("err = %v, want the guard's reason preserved", err)
	}
}

// A stub that already names ErrBlockedHost must not be wrapped twice, or the
// message repeats itself and errors.Is matching gets harder to read.
func TestValidateConnectURL_Step8WrapIsNotDoubled(t *testing.T) {
	opts := ConnectURLOptions{AssertPublic: func(string) error {
		return ErrBlockedHost
	}}
	_, err := ValidateConnectURL("https://x.example.com/", opts)
	if !errors.Is(err, ErrBlockedHost) {
		t.Fatalf("err = %v, want ErrBlockedHost", err)
	}
	if n := strings.Count(err.Error(), ErrBlockedHost.Error()); n != 1 {
		t.Errorf("sentinel appears %d times in %q, want 1", n, err)
	}
}

// Loopback is the point of the command and the SSRF guard blocks it by design,
// so step 8 must not be consulted there at all.
func TestValidateConnectURL_Step8NotCalledForLoopback(t *testing.T) {
	opts := ConnectURLOptions{AssertPublic: noNetwork(t)}
	if _, err := ValidateConnectURL("http://127.0.0.1:20128", ConnectURLOptions{
		AllowInsecure: true, AssertPublic: opts.AssertPublic,
	}); err != nil {
		t.Fatalf("loopback endpoint refused: %v", err)
	}
}

// D3 — under dry-run the DNS step is skipped, and the caller is told so rather
// than being handed a URL that looks fully checked.
func TestValidateConnectURL_DryRunSkipsStep8(t *testing.T) {
	got, err := ValidateConnectURL("https://gateway.example.com/", ConnectURLOptions{
		DryRun:       true,
		AssertPublic: func(string) error { panic("dry-run reached DNS") },
	})
	if err != nil {
		t.Fatalf("dry-run refused a valid URL: %v", err)
	}
	if !got.Step8Skipped {
		t.Error("Step8Skipped = false, want true — the caller cannot print the §5.4 notice")
	}
	if got.URL.String() != "https://gateway.example.com" {
		t.Errorf("canonical = %q", got.URL.String())
	}
}

// D4 — dry-run relaxes nothing: the pure steps still reject.
func TestValidateConnectURL_DryRunStillRejects(t *testing.T) {
	if _, err := ValidateConnectURL("https://user:pass@x.example.com/", ConnectURLOptions{DryRun: true}); !errors.Is(err, ErrUserinfoNotAllowed) {
		t.Fatalf("err = %v, want ErrUserinfoNotAllowed", err)
	}
}

func TestIsLoopbackHost(t *testing.T) {
	tests := []struct {
		host string
		want bool
	}{
		{"localhost", true},
		{"127.0.0.1", true},
		{"127.1.2.3", true},
		{"::1", true},
		{"::ffff:127.0.0.1", true},
		{"0.0.0.0", false}, // D11: "every interface", not "this machine"
		{"example.com", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			if got := isLoopbackHost(tt.host); got != tt.want {
				t.Errorf("isLoopbackHost(%q) = %v, want %v", tt.host, got, tt.want)
			}
		})
	}
}

// The legacy numeric IPv4 spellings — "2130706433", "127.1", "0177.0.0.1",
// "0x7f000001" — are 127.0.0.1 in inet_aton's grammar, and glibc's getaddrinfo
// resolves all of them to loopback.
//
// They are not loopback here, and that is deliberate: net.ParseIP rejects
// them, so treating them as loopback would need a second, hand-written
// inet_aton parser whose only job would be to widen the set of hosts the
// plaintext rule accepts.
//
// The case that matters is that they fail closed. Measured on Linux (Ubuntu
// 24.04, glibc 2.39) and Windows 11:
//
//   - Plaintext is refused, with or without --allow-insecure. Step 7 requires
//     a loopback host for plaintext, and these are not one.
//   - Over HTTPS the host reaches step 8, where AssertPublicURL resolves it.
//     When the resolver answers, the answer is 127.0.0.1 and the guard blocks
//     it. When the resolver does not answer — Go's own resolver refuses these
//     spellings outright — the guard has nothing to inspect, and the dial
//     that follows fails the same way, because Go will not connect to a host
//     it cannot resolve either.
//
// So no connection to loopback is reachable through these spellings, by
// either route. This test pins that, so a future change that makes them
// loopback — or makes plaintext acceptable for a non-loopback host — has to
// come here and say why.
func TestLegacyIPv4Spellings_FailClosed(t *testing.T) {
	legacy := []string{"2130706433", "127.1", "0177.0.0.1", "0x7f000001"}

	for _, host := range legacy {
		t.Run(host, func(t *testing.T) {
			// Not loopback, by definition.
			if isLoopbackHost(host) {
				t.Errorf("isLoopbackHost(%q) = true; the legacy spellings are not loopback here", host)
			}

			// Plaintext is refused even with --allow-insecure: step 7 requires
			// a loopback host.
			_, err := ValidateConnectURL("http://"+host+"/", ConnectURLOptions{AllowInsecure: true})
			if !errors.Is(err, ErrInsecureConnection) {
				t.Errorf("plaintext err = %v, want ErrInsecureConnection", err)
			}

			// The plain form is refused for the same reason.
			_, err = ValidateConnectURL("http://"+host+"/", ConnectURLOptions{})
			if !errors.Is(err, ErrInsecureConnection) {
				t.Errorf("plaintext without the flag err = %v, want ErrInsecureConnection", err)
			}
		})
	}
}

// The same spellings over HTTPS reach step 8, and the guard is what decides.
// The stub stands in for the real resolver so the case runs offline and
// deterministically: a resolver that answers 127.0.0.1 is refused.
func TestLegacyIPv4Spellings_BlockedWhenTheyResolveToLoopback(t *testing.T) {
	legacy := []string{"2130706433", "127.1", "0177.0.0.1", "0x7f000001"}

	for _, host := range legacy {
		t.Run(host, func(t *testing.T) {
			_, err := ValidateConnectURL("https://"+host+"/", ConnectURLOptions{
				AssertPublic: func(string) error {
					return errors.New("blocked URL: hostname resolves to an internal host")
				},
			})
			if !errors.Is(err, ErrBlockedHost) {
				t.Errorf("err = %v, want ErrBlockedHost", err)
			}
		})
	}
}

// The canonical form must equal what normalizeHost in handlerutil would produce
// for the same name, or the two layers disagree about identity (§3.8).
func TestNormalizeConnectHost_MatchesHandlerutilShape(t *testing.T) {
	tests := map[string]string{
		"LocalHost.":     "localhost",
		"EXAMPLE.COM...": "example.com",
		"example.com":    "example.com",
		".":              "",
	}
	for in, want := range tests {
		if got := normalizeConnectHost(in); got != want {
			t.Errorf("normalizeConnectHost(%q) = %q, want %q", in, got, want)
		}
	}
}
