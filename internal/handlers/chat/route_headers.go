package chat

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
)

var (
	routeHeadersEnabledOnce sync.Once
	routeHeadersEnabledVal  bool
)

// isRouteHeadersEnabled checks if route response headers are enabled via NINEROUTER_ROUTE_HEADERS.
// Defaults to true. Returns false if set to "0" or "false" (case-insensitive).
// Cached after the first lookup to avoid per-request os.Getenv overhead.
func isRouteHeadersEnabled() bool {
	routeHeadersEnabledOnce.Do(func() {
		val := os.Getenv("NINEROUTER_ROUTE_HEADERS")
		if val == "" {
			routeHeadersEnabledVal = true
			return
		}
		low := strings.ToLower(val)
		routeHeadersEnabledVal = low != "0" && low != "false"
	})
	return routeHeadersEnabledVal
}

// resetRouteHeadersConfigForTesting allows unit tests to re-evaluate NINEROUTER_ROUTE_HEADERS.
func resetRouteHeadersConfigForTesting() {
	routeHeadersEnabledOnce = sync.Once{}
}

// injectRouteHeaders sets informational response headers indicating which provider,
// model, combo, and attempt count successfully served the request.
func injectRouteHeaders(w http.ResponseWriter, provider, model, combo string, attempts int) {
	if !isRouteHeadersEnabled() {
		return
	}
	if provider != "" {
		w.Header().Set("x-9router-provider", provider)
	}
	if model != "" {
		w.Header().Set("x-9router-model", model)
	}
	if combo != "" {
		w.Header().Set("x-9router-combo", combo)
	}
	if attempts <= 0 {
		attempts = 1
	}
	w.Header().Set("x-9router-attempts", strconv.Itoa(attempts))
}

// clearRouteHeaders removes route metadata headers from w. Used when a dispatch
// attempt fails so that headers from an unserved attempt do not leak into error responses.
func clearRouteHeaders(w http.ResponseWriter) {
	if w == nil {
		return
	}
	h := w.Header()
	h.Del("x-9router-provider")
	h.Del("x-9router-model")
	h.Del("x-9router-combo")
	h.Del("x-9router-attempts")
}

// attemptCount returns the number of fallback attempts recorded so far on ctx,
// plus one for the current (about-to-be-dispatched) attempt.
func attemptCount(ctx context.Context) int {
	rec := FallbackAttemptRecorderFrom(ctx)
	if rec == nil {
		return 1
	}
	snap := rec.Snapshot()
	return len(snap) + 1
}
