package handlerutil

import (
	"strings"

	"9router/proxy/internal/constants"
)

// WithDefaultUserAgent returns headers carrying a User-Agent when the caller
// supplied none (in any casing).
//
// A Go http.Client sends "Go-http-client/…" when the header is absent, and
// edge WAFs read that as a bot: Cloudflare fronts Nous Research's inference API
// and answers it with HTTP 403 "Attention Required!" on /v1/models, so a
// catalogue fetch that leaves the header unset fails while the chat path —
// which presents constants.DefaultClientUserAgent — succeeds.
//
// The input map is never mutated when the default has to be added: the
// dashboard's probe configs share one header map across concurrent requests.
func WithDefaultUserAgent(headers map[string]string) map[string]string {
	for k := range headers {
		if strings.EqualFold(k, constants.HeaderUserAgent) {
			return headers
		}
	}
	out := make(map[string]string, len(headers)+1)
	for k, v := range headers {
		out[k] = v
	}
	out[constants.HeaderUserAgent] = constants.DefaultClientUserAgent
	return out
}
