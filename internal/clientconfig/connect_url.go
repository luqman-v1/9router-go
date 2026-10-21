package clientconfig

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"9router/proxy/internal/handlerutil"
)

// ConnectURLOptions are the inputs to URL validation that are not the URL
// itself. Every field has a safe zero value: no allow-insecure, the real SSRF
// guard, and no dry-run.
type ConnectURLOptions struct {
	// AllowInsecure permits plaintext HTTP, but only on a loopback host. It
	// never relaxes the rule for a public host — see step 7 below and case
	// U14b in docs/specs/connect-security.md §7.4.
	AllowInsecure bool

	// DryRun skips step 8, the only step that touches the network. Callers
	// learn that from ConnectEndpoint.Step8Skipped rather than being told the
	// URL is fully safe.
	DryRun bool

	// AssertPublic is the step 8 seam. nil means the production guard.
	AssertPublic AssertPublicFunc
}

// AssertPublicFunc reports whether a URL's host may be contacted. It exists so
// a test can inject a stub instead of resolving DNS.
type AssertPublicFunc func(rawURL string) error

// ConnectEndpoint is the result of validation: one canonical URL, and the one
// thing that was deliberately not checked.
type ConnectEndpoint struct {
	// URL is the only form any later code may use to build a request. The raw
	// string the user typed is never parsed again (§3.1).
	URL *url.URL

	// Step8Skipped is true under DryRun: the DNS/SSRF check did not run, so a
	// real run may still refuse this host. The caller must say so (§5.4).
	Step8Skipped bool
}

// DryRunStep8Notice is the line the CLI prints when Step8Skipped is true. It
// lives here so the wording is tested next to the decision it describes.
const DryRunStep8Notice = "Kiểm tra DNS/SSRF (bước 8): BỎ QUA — cần mạng, không chạy trong --dry-run."

// DefaultAssertPublic wires step 8 to the existing SSRF guard. This package does
// not reimplement the predicate: handlerutil's guard is the layer that resolves
// DNS and checks the resolved addresses, which is what defeats a public name
// pointing at a private address (§3.7).
//
// Read the guard's limits before trusting it alone: it accepts userinfo,
// non-ASCII hostnames and query strings, and it blocks loopback. Steps 2–7 exist
// precisely because it is an SSRF guard, not a URL validator (§3.9).
func DefaultAssertPublic(rawURL string) error {
	if err := handlerutil.AssertPublicURL(rawURL); err != nil {
		return fmt.Errorf("%w: %w", ErrBlockedHost, err)
	}
	return nil
}

// ValidateConnectURL runs the nine validation steps of
// docs/specs/connect-security.md §3.3 and returns the canonical endpoint.
//
// Steps 0–7 are pure: no I/O, no DNS, nothing a caller has to mock. Step 8 is
// the only one that reaches the network, it runs only for a public host, and it
// is skipped entirely under DryRun.
func ValidateConnectURL(raw string, opts ConnectURLOptions) (ConnectEndpoint, error) {
	// Step 0 — normalise, then parse once. Everything below works on the
	// *url.URL; the string is never parsed again (§3.1).
	raw = strings.TrimSpace(strings.ReplaceAll(raw, "\x00", ""))
	if i := strings.Index(raw, "://"); i >= 0 {
		// A scheme was typed. Honour it so step 1 can refuse it by name; adding
		// "https://" in front of "ftp://x" would turn one URL into a different,
		// parseable one.
		switch strings.ToLower(schemeHint(raw[:i])) {
		case "http", "https":
		default:
			return ConnectEndpoint{}, fmt.Errorf("%w: %q", ErrUnsupportedScheme, schemeHint(raw[:i]))
		}
	} else {
		// §3.2.1: default to https, never http. "host:port" has no "://", so it
		// lands here and keeps its port instead of being read as a scheme.
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		// net/url's own error embeds the raw input, and the raw input may hold a
		// password ("https://user:secret@host:notaport/"). Parse the host part
		// separately so the message can name what went wrong without naming the
		// secret (§3.1, §3.4).
		return ConnectEndpoint{}, fmt.Errorf("%w: %s", ErrInvalidURL, sanitizeConnectParseError(err))
	}

	// Step 1 — scheme allowlist.
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return ConnectEndpoint{}, fmt.Errorf("%w: %q", ErrUnsupportedScheme, u.Scheme)
	}

	// Step 2 — no userinfo, ever (§3.4).
	if u.User != nil {
		return ConnectEndpoint{}, fmt.Errorf("%w", ErrUserinfoNotAllowed)
	}

	// Step 3 — no query, no fragment (§3.5).
	if u.RawQuery != "" || u.Fragment != "" {
		return ConnectEndpoint{}, fmt.Errorf("%w", ErrQueryFragmentNotAllowed)
	}

	// Step 4 — no path. "%2e%2e" has already been decoded to ".." by Parse,
	// which is why this compares the decoded Path rather than RawPath (§3.5).
	if u.Opaque != "" || (u.Path != "" && u.Path != "/") {
		return ConnectEndpoint{}, fmt.Errorf("%w", ErrPathNotAllowed)
	}

	host := u.Hostname()
	if host == "" {
		return ConnectEndpoint{}, fmt.Errorf("%w: empty host", ErrInvalidURL)
	}

	// Step 5 — port: no leading zero, not zero, inside 1–65535 (§3.6).
	port := u.Port()
	if port != "" {
		if len(port) > 1 && port[0] == '0' {
			return ConnectEndpoint{}, fmt.Errorf("%w: %q", ErrInvalidPort, port)
		}
		n, convErr := strconv.Atoi(port)
		if convErr != nil || n < 1 || n > 65535 {
			return ConnectEndpoint{}, fmt.Errorf("%w: %q", ErrInvalidPort, port)
		}
	}

	// Step 6 — ASCII only. net/http would later convert this to punycode, after
	// every decision here has been made: that gap is where a homograph lives
	// (§3.6, case U8).
	for i := 0; i < len(host); i++ {
		if host[i] > 127 {
			return ConnectEndpoint{}, fmt.Errorf("%w: %q", ErrNonASCIIHostname, host)
		}
	}

	host = normalizeConnectHost(host)
	if host == "" {
		return ConnectEndpoint{}, fmt.Errorf("%w: empty host", ErrInvalidURL)
	}

	// Step 7 — the HTTPS rule. The two conditions stay separate on purpose:
	// --allow-insecure relaxes plaintext only for a loopback destination, and
	// U14b exists to catch anyone who merges them into one `||` (§3.6).
	loopback := isLoopbackHost(host)
	if scheme == "http" {
		if !loopback {
			return ConnectEndpoint{}, fmt.Errorf("%w: use https for a public host", ErrInsecureConnection)
		}
		if !opts.AllowInsecure {
			return ConnectEndpoint{}, fmt.Errorf("%w: retry with --allow-insecure for a loopback host", ErrInsecureConnection)
		}
	}

	canonical := &url.URL{Scheme: scheme, Host: canonicalHostPort(host, port, scheme)}

	// Step 8 — SSRF guard, public hosts only. Loopback is the point of the
	// command and the guard blocks it by design, so it is not called there.
	if loopback {
		return ConnectEndpoint{URL: canonical}, nil
	}
	if opts.DryRun {
		return ConnectEndpoint{URL: canonical, Step8Skipped: true}, nil
	}
	assertPublic := opts.AssertPublic
	if assertPublic == nil {
		assertPublic = DefaultAssertPublic
	}
	if err := assertPublic(canonical.String()); err != nil {
		if errors.Is(err, ErrBlockedHost) {
			return ConnectEndpoint{}, err // the stub already named it
		}
		return ConnectEndpoint{}, fmt.Errorf("%w: %w", ErrBlockedHost, err)
	}
	return ConnectEndpoint{URL: canonical}, nil
}

// schemeHint extracts the scheme from the text before "://".
//
// The text there is not necessarily a scheme: "admin:secret@host://api" puts a
// userinfo prefix in front of it. Only the part before the first "/", "?", "#"
// or "@" can be a scheme, so that is all this keeps — the rest is a password
// and never reaches an error message (§3.4, §6.1).
func schemeHint(prefix string) string {
	if j := strings.IndexAny(prefix, "/?#@"); j >= 0 {
		return prefix[:j]
	}
	return prefix
}

// sanitizeConnectParseError rewrites net/url's parse error so it names the
// problem without echoing the input.
//
// net/url reports `parse "https://user:secret@host:bad/": invalid port`. The
// quoted string is the raw input, so wrapping that error verbatim puts the
// password in the CLI output, in stderr capture and in any crash report. The
// reason is kept — it is what tells the user what to fix — and only the quoted
// input is dropped.
func sanitizeConnectParseError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	// net/url formats the error as: parse "<url>": <reason>
	const prefix = "parse \""
	if i := strings.Index(msg, prefix); i >= 0 {
		rest := msg[i+len(prefix):]
		if j := strings.Index(rest, "\": "); j >= 0 {
			return rest[j+3:]
		}
	}
	return msg
}

// normalizeConnectHost mirrors handlerutil's unexported normalizeHost: lower
// case, trailing dots removed. Kept identical rather than similar — two layers
// that disagree about what a host is are exactly the parser differential this
// package exists to prevent (§3.8).
func normalizeConnectHost(host string) string {
	return strings.ToLower(strings.TrimRight(host, "."))
}

// isLoopbackHost is the loopback definition of §3.6: the name "localhost", or a
// literal address net.IP calls loopback. 0.0.0.0 is deliberately not loopback
// (decision D11): it means "every interface", and whether it reaches only the
// local machine is a matter of OS configuration.
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// canonicalHostPort builds the Host field, dropping the port when it is the
// scheme's default so one endpoint has one spelling (§3.8).
func canonicalHostPort(host, port, scheme string) string {
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		return net.JoinHostPort(host, port)
	}
	if strings.Contains(host, ":") {
		return "[" + host + "]"
	}
	return host
}
