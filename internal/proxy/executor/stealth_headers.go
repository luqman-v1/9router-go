package executor

import (
	"net/http"
	"net/url"
	"sort"

	fhttp "github.com/bogdanfinn/fhttp"
)

// The shared half of every browser-fidelity transport in this package.
//
// Three things are common to all of them, and each was measured on the wire
// rather than assumed:
//
//  1. Header order is part of the fingerprint. Chrome writes request headers
//     in a fixed, non-alphabetical order, and Go's net/http sorts them
//     alphabetically before writing. fhttp exposes HeaderOrderKey for exactly
//     this, so the ordered lists are built with Header.Append and replayed in
//     sequence.
//
//  2. The HTTP/2 pseudo-header order is fixed by the protocol and is part of
//     what makes the connection look like a browser rather than a Go client.
//
//  3. The proxy is read back off the transport the chat path already
//     resolved, so the operator's egress assignment applies without a second
//     source of truth to disagree with.

// stealthPseudoHeaderOrder is the HTTP/2 pseudo-header order Chrome uses.
var stealthPseudoHeaderOrder = []string{":method", ":authority", ":scheme", ":path"}

// stealthSetOrdered writes values onto an fhttp header set in the given order.
//
// Each name is appended through Header.Append, which records it in
// HeaderOrderKey as it goes; the order slice decides the sequence, so the
// request goes out in Chrome's order rather than Go's alphabetical one. A
// name with no value is skipped rather than sent empty, which is how a caller
// says "this request does not carry this" — the site's own client sends
// `x-device-model` with an empty value, and that empty header is part of the
// fingerprint, so an empty value that the caller means to send has to be
// written explicitly rather than dropped.
func stealthSetOrdered(header fhttp.Header, order []string, values map[string]string) {
	for _, name := range order {
		value, ok := values[name]
		if !ok {
			continue
		}
		header.Append(name, value)
	}
	// Anything the caller supplied that the known order does not name still has
	// to be sent, appended after the ordered set: dropping a header because it
	// is unfamiliar would silently discard request-specific fields.
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
		header.Append(name, values[name])
	}
}

// stealthProxyFrom recovers the proxy a connection was assigned.
//
// The chat path resolves the pool into the transport before the executor runs,
// so this reads it back rather than looking anything up. An unproxied
// connection yields "" and the client dials direct.
//
// The target is a placeholder: the proxy function is asked for a URL to a
// fixed host, and a pool's answer does not vary by the upstream it is
// relaying to.
func stealthProxyFrom(reference *http.Client, target string) string {
	if reference == nil {
		return ""
	}
	transport, ok := reference.Transport.(*http.Transport)
	if !ok || transport.Proxy == nil {
		return ""
	}
	parsed, err := url.Parse(target)
	if err != nil {
		return ""
	}
	proxyURL, err := transport.Proxy(&http.Request{URL: parsed})
	if err != nil || proxyURL == nil {
		return ""
	}
	return proxyURL.String()
}
