package browsercookies

import (
	"io"
	"strings"
)

// The session header.
//
// The site reads a Cookie header, and the order of the cookies in it is
// fixed by the reference bridge: the same set, in the same order, every
// time. A header that carries the right cookies in a different order is
// still a valid header, but there is no reason to vary it, and a fixed order
// makes two reads comparable.

// sessionCookieNames are the Google session cookies, in the canonical order.
//
// This is the list the reference bridge sends
// (azrinaldi/gemini-stealth-bridge internal/cookies/jar.go, EssentialNames).
// A cookie outside it is not a session cookie, and handing one to a second
// host is handing away something the user did not mean to share.
//
// `AEC` is optional: a modern browser profile does not have it.
var sessionCookieNames = []string{
	"SID", "HSID", "SSID", "APISID", "SAPISID",
	"__Secure-1PSID", "__Secure-1PAPISID", "__Secure-3PSID", "__Secure-3PAPISID",
	"__Secure-1PSIDTS", "__Secure-3PSIDTS",
	"SIDCC", "__Secure-1PSIDCC", "__Secure-3PSIDCC", "NID", "AEC",
}

// sessionCookieSet is the same list as a set, for the lookups.
var sessionCookieSet = func() map[string]bool {
	m := make(map[string]bool, len(sessionCookieNames))
	for _, name := range sessionCookieNames {
		m[name] = true
	}
	return m
}()

// deepSeekSessionCookieNames are the DeepSeek cookies that carry the site's
// own session, in the canonical order.
//
// `aws-waf-token` is the one the WAF reads: it is the answer to the
// challenge the WAF serves to a client it does not recognise, and a request
// without it is answered with a challenge rather than with the site. The
// other two are the site's own session cookies.
var deepSeekSessionCookieNames = []string{
	"aws-waf-token",
	"ds_session_id",
	"smidV2",
}

// deepSeekSessionCookieSet is the same list as a set, for the lookups.
var deepSeekSessionCookieSet = func() map[string]bool {
	m := make(map[string]bool, len(deepSeekSessionCookieNames))
	for _, name := range deepSeekSessionCookieNames {
		m[name] = true
	}
	return m
}()

// buildHeader renders the session cookies as a Cookie header.
//
// A cookie is taken from the most specific host that has it, and among
// equals the one that lasts longest. A cookie held for `.google.com` is
// sent to every Google host; one held for `accounts.google.com` is not, and
// sending it there would be sending a cookie the site never set for it.
//
// The allow-list is the site's own: a profile holds the cookies of every
// site the operator uses, and only the ones the site named are the session.
func buildHeader(cookies []Cookie, allowed map[string]bool, order []string) string {
	hostScore := func(host string) int {
		if strings.HasPrefix(host, ".") {
			return 3
		}
		if strings.Contains(host, ".") {
			return 2
		}
		return 1
	}
	best := make(map[string]string, len(cookies))
	bestScore := make(map[string]int, len(cookies))
	bestExpiry := make(map[string]int64, len(cookies))
	for _, cookie := range cookies {
		if !allowed[cookie.Name] || cookie.Value == "" {
			continue
		}
		score := hostScore(cookie.Host)
		if score > bestScore[cookie.Name] ||
			(score == bestScore[cookie.Name] && cookie.ExpiresAt > bestExpiry[cookie.Name]) {
			best[cookie.Name] = cookie.Value
			bestScore[cookie.Name] = score
			bestExpiry[cookie.Name] = cookie.ExpiresAt
		}
	}

	var parts []string
	for _, name := range order {
		if value, ok := best[name]; ok {
			parts = append(parts, name+"="+value)
		}
	}
	return strings.Join(parts, "; ")
}

// buildGoogleHeader renders the Google session cookies as a Cookie header.
func buildGoogleHeader(cookies []Cookie) string {
	return buildHeader(cookies, sessionCookieSet, sessionCookieNames)
}

// ioCopy copies a stream.
//
// It is the standard library's copy, under a name of its own so the file
// does not have to import io for one call.
func ioCopy(dst io.Writer, src io.Reader) (int64, error) {
	return io.Copy(dst, src)
}
