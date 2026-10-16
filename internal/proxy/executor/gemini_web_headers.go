package executor

import (
	"sort"
	"strings"

	fhttp "github.com/bogdanfinn/fhttp"
)

// The header set and order the site's own client sends.
//
// Order is part of the fingerprint, not a cosmetic detail. Chrome writes request
// headers in a fixed, non-alphabetical order, and Go's net/http sorts them
// alphabetically before writing — verified on the wire, not assumed. fhttp
// exposes HeaderOrderKey for exactly this, so the ordered lists below are built
// with Header.Append and replayed in sequence.
//
// The lists are the bridge's (azrinaldi/gemini-stealth-bridge
// internal/transport/client.go), which captured them from the site's own client.
const (
	// geminiWebSecCHUA is the Chrome client hint the UA string names. The two
	// have to agree: a UA claiming Chrome 150 next to a hint naming Chrome 131
	// is a stronger signal than either alone.
	geminiWebSecCHUA = `"Not;A=Brand";v="8", "Chromium";v="150", "Google Chrome";v="150"`
	// geminiWebSecCHUAPlatform tracks the UA's own platform token. It is a
	// constant rather than runtime OS detection because the UA above is.
	geminiWebSecCHUAPlatform = `"Linux"`
)

// geminiWebNavigationOrder is what the bootstrap page is fetched with.
//
// The page request is a navigation, not an XHR: its sec-fetch-* set says so, and
// a document request carrying XHR hints describes a browser that does not exist.
var geminiWebNavigationOrder = []string{
	"sec-ch-ua",
	"sec-ch-ua-mobile",
	"sec-ch-ua-platform",
	"upgrade-insecure-requests",
	"user-agent",
	"accept",
	"sec-fetch-site",
	"sec-fetch-mode",
	"sec-fetch-user",
	"sec-fetch-dest",
	"referer",
	"accept-encoding",
	"accept-language",
	"cookie",
	"priority",
}

// geminiWebCORSOrder is the order an in-page XHR carries. `sec-fetch-site:
// same-origin` is the load-bearing member: the page's own JavaScript is
// same-origin, and a request claiming otherwise is not the page.
var geminiWebCORSOrder = []string{
	"sec-ch-ua",
	"x-same-domain",
	"sec-ch-ua-mobile",
	"user-agent",
	"content-type",
	"x-goog-authuser",
	"x-goog-ext-525001261-jspb",
	"x-goog-ext-525005358-jspb",
	"x-goog-ext-73010989-jspb",
	"x-goog-ext-73010990-jspb",
	"x-tenant-id",
	"push-id",
	"x-client-pctx",
	"x-goog-upload-command",
	"x-goog-upload-protocol",
	"x-goog-upload-header-content-length",
	"x-goog-upload-offset",
	"authorization",
	"sec-ch-ua-platform",
	"accept",
	"origin",
	"sec-fetch-site",
	"sec-fetch-mode",
	"sec-fetch-dest",
	"referer",
	"accept-encoding",
	"accept-language",
	"cookie",
	"priority",
}

// geminiWebNavigationValues are the navigation defaults.
func geminiWebNavigationValues(origin, cookie string) map[string]string {
	values := map[string]string{
		"user-agent":                geminiWebUserAgent,
		"accept":                    "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8",
		"accept-language":           "en-US,en;q=0.9",
		"sec-ch-ua":                 geminiWebSecCHUA,
		"sec-ch-ua-mobile":          "?0",
		"sec-ch-ua-platform":        geminiWebSecCHUAPlatform,
		"upgrade-insecure-requests": "1",
		"sec-fetch-site":            "none",
		"sec-fetch-mode":            "navigate",
		"sec-fetch-user":            "?1",
		"sec-fetch-dest":            "document",
		"priority":                  "u=0, i",
		"referer":                   origin + "/",
		"cookie":                    cookie,
	}
	return values
}

// geminiWebCORSValues are the XHR defaults.
func geminiWebCORSValues(origin, referer string) map[string]string {
	return map[string]string{
		"user-agent":         geminiWebUserAgent,
		"sec-ch-ua":          geminiWebSecCHUA,
		"sec-ch-ua-mobile":   "?0",
		"sec-ch-ua-platform": geminiWebSecCHUAPlatform,
		"x-same-domain":      "1",
		"accept":             "*/*",
		"origin":             origin,
		"sec-fetch-site":     "same-origin",
		"sec-fetch-mode":     "cors",
		"sec-fetch-dest":     "empty",
		"referer":            referer,
		"accept-language":    "en-US,en;q=0.9",
		"priority":           "u=1, i",
	}
}

// geminiWebSetOrdered writes values onto an fhttp header set in the given order.
//
// Each name is appended through Header.Append, which records it in HeaderOrderKey
// as it goes; the order slice decides the sequence, so the request goes out in
// Chrome's order rather than Go's alphabetical one. A name with no value is
// skipped rather than sent empty, which is how a caller says "this request does
// not carry this".
func geminiWebSetOrdered(header fhttp.Header, order []string, values map[string]string) {
	for _, name := range order {
		value, ok := values[name]
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		header.Append(name, value)
	}
	// Anything the caller supplied that the known order does not name still has
	// to be sent, appended after the ordered set: dropping a header because it is
	// unfamiliar would silently discard request-specific fields.
	named := make(map[string]bool, len(order))
	for _, name := range order {
		named[name] = true
	}
	extra := make([]string, 0, len(values))
	for name := range values {
		if !named[name] {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	for _, name := range extra {
		if strings.TrimSpace(values[name]) == "" {
			continue
		}
		header.Append(name, values[name])
	}
}

// geminiWebPseudoHeaderOrder is the HTTP/2 pseudo-header order Chrome uses. It is
// fixed by the protocol and is part of what makes the connection look like a
// browser rather than a Go client.
var geminiWebPseudoHeaderOrder = []string{":method", ":authority", ":scheme", ":path"}
