package executor

import (
	"net/http"
	"strings"
)

// Port of open-sse/utils/upstreamHeaders.js.
//
// The upstream's retry and rate-limit headers tell the client when a throttle is
// temporary and when it is worth trying again. Without them a 429 or a
// rate-limit refusal reaches the caller as an opaque failure with no way to
// back off intelligently, and a gateway in front of us cannot make a decision
// on our behalf either.

const anthropicRateLimitPrefix = "anthropic-ratelimit-"

// exactForwardedHeaders are copied verbatim.
var exactForwardedHeaders = map[string]bool{
	"retry-after":    true,
	"x-should-retry": true,
	// Claude subscription slow-mode response state headers (OMP anthropic-slow-mode.ts).
	"anthropic-usage-limit": true,
}

// forwardUpstreamResponseHeaders copies the retry and rate-limit headers from
// the upstream response onto ours. It must run before anything writes a status.
func forwardUpstreamResponseHeaders(w http.ResponseWriter, upstream http.Header) {
	if w == nil || upstream == nil {
		return
	}
	for name, values := range upstream {
		lower := strings.ToLower(name)
		if !exactForwardedHeaders[lower] && !strings.HasPrefix(lower, anthropicRateLimitPrefix) {
			continue
		}
		for _, value := range values {
			w.Header().Add(lower, value)
		}
	}
}
