package clientconfig

import "errors"

// Connect errors are sentinels so the CLI can map each one to a message that
// tells the user what to fix — "URL không hợp lệ" for all of them would fail the
// diagnostic half of G5 in docs/specs/connect-security.md §1.2. Every wrap in
// this package uses %w so errors.Is works at the CLI boundary.
//
// The codes (E1–E12, REDIR-*) are the spec's table §3.3, §4.6 and §6.4.
var (
	// E1 ErrInvalidURL reports a string net/url cannot parse, or one that parses
	// to nothing usable (no host, or a host that is only dots).
	ErrInvalidURL = errors.New("connect: invalid URL")

	// E2 ErrUnsupportedScheme reports a scheme other than http or https.
	ErrUnsupportedScheme = errors.New("connect: unsupported URL scheme")

	// E3 ErrUserinfoNotAllowed reports a "user:pass@" component. Refused with no
	// exception: connect authenticates with a JSON body, never HTTP Basic, so a
	// userinfo URL is either a copied connection string or a credential about to
	// be printed by every log line that shows the URL (§3.4).
	ErrUserinfoNotAllowed = errors.New("connect: URL must not contain user:password@")

	// E4 ErrQueryFragmentNotAllowed reports a query string or fragment. The
	// request endpoints are fixed, so neither can mean anything (§3.5).
	ErrQueryFragmentNotAllowed = errors.New("connect: URL must not contain a query or fragment")

	// E5 ErrPathNotAllowed reports a path other than "" or "/". A percent-decoded
	// "%2e%2e" arrives here as "/../", which is the traversal E5 exists for.
	ErrPathNotAllowed = errors.New("connect: URL must not contain a path")

	// E6 ErrInvalidPort reports a port of "0", a port with a leading zero
	// ("0443"), or one outside 1–65535.
	ErrInvalidPort = errors.New("connect: invalid port")

	// E7 ErrNonASCIIHostname reports a hostname holding any rune above 127.
	// url.Parse accepts it and keeps the Unicode, while net/http converts to
	// punycode only later — the gap a homograph lives in (§3.6, step 6).
	ErrNonASCIIHostname = errors.New("connect: hostname must be ASCII")

	// E8 ErrInsecureConnection reports plaintext HTTP where it would leave the
	// machine: any public host, and any loopback host without --allow-insecure.
	ErrInsecureConnection = errors.New("connect: plaintext HTTP is not allowed here")

	// E9 ErrBlockedHost reports a public hostname that the SSRF guard refused —
	// typically one resolving to a private, loopback or metadata address.
	ErrBlockedHost = errors.New("connect: host is blocked")

	// E10 ErrNoTTY reports that no terminal is available for the password
	// prompt and NINE_ROUTER_PASSWORD is unset (§6.2.3).
	ErrNoTTY = errors.New("connect: a terminal is required to enter the password")

	// E11 ErrPasswordTooLong reports a password above 256 bytes, refused before
	// it reaches bcrypt's 72-byte truncation (§6.4).
	ErrPasswordTooLong = errors.New("connect: password is longer than 256 characters")

	// E11b ErrPasswordTruncatedByBcrypt reports a password above bcrypt's own
	// 72-byte limit. It is separate from ErrPasswordTooLong because it is a
	// different fault: this one would authenticate against the wrong prefix —
	// bcrypt.CompareHashAndPassword ignores the bytes past 72 — while the
	// 256-byte one only catches an accidental paste (§6.4).
	ErrPasswordTruncatedByBcrypt = errors.New("connect: password is longer than bcrypt's 72-byte limit")

	// E12 ErrPasswordFlagRemoved reports use of the removed --password flag. It
	// exists only for diagnosis: upstream users type it and deserve a reason,
	// not "unknown option" (§6.4).
	ErrPasswordFlagRemoved = errors.New("connect: --password has been removed")

	// REDIR-DOWNGRADE: an https hop tried to become http (§4.3a).
	ErrRedirectDowngrade = errors.New("connect: server redirected from HTTPS to HTTP")

	// REDIR-CROSS-ORIGIN: the origin changed while a credential was attached
	// (§4.3b). Port-only changes count — net/http does not strip headers for
	// them, so this check is the only one standing (§4.1).
	ErrRedirectCrossOrigin = errors.New("connect: server redirected to a different origin")

	// REDIR-TOO-MANY: more than five redirect hops (§4.4). No retry, no extra
	// hop "just in case".
	ErrTooManyRedirects = errors.New("connect: too many redirects")
)
