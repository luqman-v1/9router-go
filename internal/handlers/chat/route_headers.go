package chat

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// isRouteHeadersEnabled checks if route response headers are enabled via NINEROUTER_ROUTE_HEADERS.
// Defaults to true. Returns false if set to "0" or "false" (case-insensitive).
func isRouteHeadersEnabled() bool {
	val := os.Getenv("NINEROUTER_ROUTE_HEADERS")
	if val == "" {
		return true
	}
	low := strings.ToLower(val)
	if low == "0" || low == "false" {
		return false
	}
	return true
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
