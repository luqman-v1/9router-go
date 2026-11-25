package dashboard

import (
	json "encoding/json/v2"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"9router/proxy/internal/handlerutil"
)

// isSameOriginOrLocal reports whether the request originates from a same-origin
// or loopback client, rejecting cross-site form POSTs (CSRF).
//
// A request that carries none of Sec-Fetch-Site, Origin or Referer cannot be
// attributed to a browser at all — it is a local tool such as curl. It is
// allowed here because the loopback gate has already passed and the endpoint
// additionally demands explicit confirmation; see resetPasswordRateLimit.
func isSameOriginOrLocal(r *http.Request) bool {
	// Sec-Fetch-Site: browser-enforced metadata. Reject cross-site requests immediately.
	if site := strings.ToLower(r.Header.Get("Sec-Fetch-Site")); site == "cross-site" {
		return false
	}

	// Origin header: sent by modern browsers on all cross-origin requests and state-changing POSTs.
	if origin := r.Header.Get("Origin"); origin != "" {
		return isLoopbackURLHost(origin)
	}

	// Referer fallback: if Origin is omitted, ensure Referer (if present) is loopback.
	if referer := r.Header.Get("Referer"); referer != "" {
		return isLoopbackURLHost(referer)
	}

	return true
}

// isLoopbackURLHost reports whether the URL's host is a loopback name or address.
func isLoopbackURLHost(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.Trim(strings.ToLower(u.Hostname()), "[]")
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return host == "localhost"
}

// resetPasswordRateLimit bounds how often one peer can clear the password hash.
//
// The endpoint is loopback-only, but "loopback" is not the same as "trusted":
// a same-host tunnel or reverse proxy forwards every external connection from
// 127.0.0.1, so the address gate does not distinguish the operator from a
// stranger. The limit does not make that case safe — nothing short of not
// exposing the dashboard does — but it stops a single caller from clearing the
// hash in a loop and turns an automated attempt into something the log shows.
const (
	resetPasswordMaxPerWindow = 3
	resetPasswordWindow       = 10 * time.Minute
	resetPasswordMaxBuckets   = 1000
)

var (
	resetPasswordMu    sync.Mutex
	resetPasswordHits  = map[string][]time.Time{}
	resetPasswordSweep time.Time
)

// allowResetPassword records an attempt for peer and reports whether it is
// within budget, plus how many whole seconds remain when it is not.
func allowResetPassword(peer string, now time.Time) (bool, int) {
	resetPasswordMu.Lock()
	defer resetPasswordMu.Unlock()

	// Opportunistic sweep: buckets are per peer address, so a scan from many
	// addresses would otherwise grow the map without bound.
	if now.Sub(resetPasswordSweep) > resetPasswordWindow || len(resetPasswordHits) > resetPasswordMaxBuckets {
		resetPasswordSweep = now
		for k, hits := range resetPasswordHits {
			kept := hits[:0]
			for _, t := range hits {
				if now.Sub(t) < resetPasswordWindow {
					kept = append(kept, t)
				}
			}
			if len(kept) == 0 {
				delete(resetPasswordHits, k)
				continue
			}
			resetPasswordHits[k] = kept
		}
	}

	hits := resetPasswordHits[peer]
	kept := hits[:0]
	for _, t := range hits {
		if now.Sub(t) < resetPasswordWindow {
			kept = append(kept, t)
		}
	}
	if len(kept) >= resetPasswordMaxPerWindow {
		resetPasswordHits[peer] = kept
		remaining := resetPasswordWindow - now.Sub(kept[0])
		if remaining < time.Second {
			remaining = time.Second
		}
		return false, int((remaining + time.Second - 1) / time.Second)
	}
	resetPasswordHits[peer] = append(kept, now)
	return true, 0
}

// resetPeerAddress is the bucket key: the TCP peer, never a header.
func resetPeerAddress(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return strings.Trim(strings.ToLower(host), "[]")
}

// ResetResetPasswordLimiter clears the per-peer buckets. Tests use it instead of
// sleeping; production never needs it.
func ResetResetPasswordLimiter() {
	resetPasswordMu.Lock()
	defer resetPasswordMu.Unlock()
	clear(resetPasswordHits)
	resetPasswordSweep = time.Time{}
}

// HandleResetPassword serves POST /api/auth/reset-password: clear the stored
// dashboard password hash so the gateway falls back to the configured initial
// password.
//
// Upstream ships this as the recovery path for an operator who forgot their
// password: it is local-only (the Next dashboard guard enforces that), and it
// never returns the literal. Without it there is no recovery short of deleting
// the database, because `password` is a protected setting and a PUT through
// /api/settings is refused.
//
// Local-only is enforced here too: the route is reachable without a session
// precisely because it exists for a locked-out operator, so the network origin
// is the only thing standing between an exposed tunnel and a password reset.
// Gated strictly on RemoteAddr loopback (ignoring client-controlled Host
// headers), same-origin / CSRF validation, a rate limit, and an explicit
// `{"confirm":true}` body — a request that merely knows the URL cannot clear the
// credential.
//
// Caveat for reverse proxies / tunnels on the same host: if an external tunnel
// terminates on this machine and forwards connections to localhost without
// preserving client remote addresses, all forwarded traffic will appear as
// loopback. Recovery assumes direct local access; operators running public
// tunnels should not expose the dashboard, and nothing in this handler can tell
// the two apart.
func (h *DashboardHandler) HandleResetPassword(w http.ResponseWriter, r *http.Request) {
	peer := resetPeerAddress(r)
	// The attempt is counted before any other check: a caller that keeps
	// hammering the route is rate-limited whether or not it gets the body right.
	if ok, retryAfter := allowResetPassword(peer, time.Now()); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		handlerutil.WriteJSONError(w, http.StatusTooManyRequests, "Too many password reset attempts. Try again later.")
		return
	}

	if !nodeRequestIsLocal(r) {
		handlerutil.WriteJSONError(w, http.StatusForbidden, "Password reset is only available from localhost")
		return
	}
	if !isSameOriginOrLocal(r) {
		handlerutil.WriteJSONError(w, http.StatusForbidden, "Cross-site password reset is prohibited")
		return
	}

	// Explicit confirmation. A bare POST — what a cross-site form or a stray
	// link prefetch sends — is not enough to clear a credential.
	var body struct {
		Confirm bool `json:"confirm"`
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil || json.Unmarshal(raw, &body) != nil || !body.Confirm {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, `Password reset requires explicit confirmation: POST {"confirm":true}`)
		return
	}

	// An empty string, not a deleted key: UpdateSettingsRaw merges, and the
	// readers treat "" as "no stored password" (`hasStoredPassword`). Writing
	// nil would leave the old hash in place.
	if err := h.Repo.UpdateSettingsRaw(map[string]any{"password": ""}); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}
