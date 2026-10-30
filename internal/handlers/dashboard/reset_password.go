package dashboard

import (
	"net"
	"net/http"
	"net/url"
	"strings"

	"9router/proxy/internal/handlerutil"
)

// isSameOriginOrLocal reports whether the request originates from a same-origin
// or loopback client, rejecting cross-site form POSTs (CSRF).
func isSameOriginOrLocal(r *http.Request) bool {
	// Sec-Fetch-Site: browser-enforced metadata. Reject cross-site requests immediately.
	if site := strings.ToLower(r.Header.Get("Sec-Fetch-Site")); site == "cross-site" {
		return false
	}

	// Origin header: sent by modern browsers on all cross-origin requests and state-changing POSTs.
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil {
			return false
		}
		host := strings.Trim(strings.ToLower(u.Hostname()), "[]")
		if ip := net.ParseIP(host); ip != nil {
			return ip.IsLoopback()
		}
		return host == "localhost" || host == "127.0.0.1" || host == "::1"
	}

	// Referer fallback: if Origin is omitted, ensure Referer (if present) is loopback.
	if referer := r.Header.Get("Referer"); referer != "" {
		u, err := url.Parse(referer)
		if err != nil {
			return false
		}
		host := strings.Trim(strings.ToLower(u.Hostname()), "[]")
		if ip := net.ParseIP(host); ip != nil {
			return ip.IsLoopback()
		}
		return host == "localhost" || host == "127.0.0.1" || host == "::1"
	}

	return true
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
// Gated strictly on RemoteAddr loopback (ignoring client-controlled Host headers)
// and same-origin / CSRF validation.
//
// Caveat for reverse proxies / tunnels on the same host: if an external tunnel
// terminates on this machine and forwards connections to localhost without
// preserving client remote addresses, all forwarded traffic will appear as
// loopback. Recovery assumes direct local access; operators running public tunnels
// should avoid exposing the dashboard unauthenticated or isolate tunnels.
func (h *DashboardHandler) HandleResetPassword(w http.ResponseWriter, r *http.Request) {
	if !nodeRequestIsLocal(r) {
		handlerutil.WriteJSONError(w, http.StatusForbidden, "Password reset is only available from localhost")
		return
	}
	if !isSameOriginOrLocal(r) {
		handlerutil.WriteJSONError(w, http.StatusForbidden, "Cross-site password reset is prohibited")
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
