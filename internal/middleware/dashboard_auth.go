package middleware

import (
	"9router/proxy/internal/auth"
	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
	"net/http"
)

// RequireDashboardAuth gates the dashboard REST API behind the login session
// when requireLogin is on. It mirrors upstream dashboardGuard: a request is let
// through when requireLogin is disabled, when it carries a valid auth_token
// cookie, when it presents the local CLI token header, or when it authenticates
// with a valid API key (so CLI tools and the built-in dashboard key keep
// working). Anything else gets a flat { error: "Unauthorized" } 401.
// IsAlwaysProtectedPath reports whether the path requires full admin session or CLI token,
// mirroring upstream ALWAYS_PROTECTED in src/dashboardGuard.js.
// Standard client API keys and requireLogin=false are forbidden here.

// DashboardPasswordHeader is the step-up credential the always-protected
// handlers re-verify themselves. It lives here as well because the middleware
// has to let a request carrying it reach that verification.
const DashboardPasswordHeader = "x-9r-password"

func IsAlwaysProtectedPath(path string) bool {
	switch path {
	case "/api/shutdown",
		"/api/settings/database",
		"/api/version/shutdown",
		"/api/version/update",
		"/admin/health/reset",
		"/api/oauth/cursor/auto-import",
		"/api/oauth/kiro/auto-import":
		return true
	default:
		return false
	}
}

// RequireConsoleLogAuth gates console-log APIs to dashboard sessions or the
// local CLI token. Engine client API keys never grant access to operational
// logs, and requireLogin=false still allows an unauthenticated local dashboard.
func RequireConsoleLogAuth(repo *db.Repo) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !auth.RequireLogin(repo) ||
				auth.SessionValid(r) ||
				auth.ValidCLIToken(r.Header.Get(auth.CLITokenHeader)) {
				next.ServeHTTP(w, r)
				return
			}
			handlerutil.WriteJSONError(w, http.StatusUnauthorized, "Unauthorized: dashboard session required")
		})
	}
}

// RequireAdminAuth ensures that only requests with a valid dashboard session (auth_token cookie),
// local CLI token (x-9r-cli-token), or the dashboard password header can proceed. Client API keys
// are rejected.
//
// The password header counts because the handlers behind this gate treat it as
// its own step-up credential: HandleExportDatabase re-checks it with
// verifyDashboardPassword before exporting. Without it that re-check was
// unreachable over HTTP, so the step-up path existed only in tests that mount
// the handler directly, and an operator scripting a backup had no credential
// that worked from outside the process. Client API keys still never grant this.
func RequireAdminAuth() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if auth.SessionValid(r) ||
				auth.ValidCLIToken(r.Header.Get(auth.CLITokenHeader)) ||
				r.Header.Get(DashboardPasswordHeader) != "" {
				next.ServeHTTP(w, r)
				return
			}
			handlerutil.WriteJSONError(w, http.StatusUnauthorized, "Unauthorized: admin session or CLI token required")
		})
	}
}

func RequireDashboardAuth(repo *db.Repo) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Always-protected routes strictly require valid session cookie or CLI token (upstream parity).
			// API keys and requireLogin=false are forbidden here. The dashboard
			// password header is the one exception, because the handler behind
			// /api/settings/database re-verifies it against the stored hash: without
			// it that step-up credential could never reach the handler over HTTP,
			// so a scripted backup had no working credential (issue #47).
			if IsAlwaysProtectedPath(r.URL.Path) {
				if auth.SessionValid(r) ||
					auth.ValidCLIToken(r.Header.Get(auth.CLITokenHeader)) ||
					r.Header.Get(DashboardPasswordHeader) != "" {
					next.ServeHTTP(w, r)
					return
				}
				handlerutil.WriteJSONError(w, http.StatusUnauthorized, "Unauthorized: admin session or CLI token required")
				return
			}

			if !auth.RequireLogin(repo) || auth.SessionValid(r) {
				next.ServeHTTP(w, r)
				return
			}
			if auth.ValidCLIToken(r.Header.Get(auth.CLITokenHeader)) {
				next.ServeHTTP(w, r)
				return
			}
			if key := ExtractApiKey(r); key != "" {
				if obj, err := repo.GetApiKeyByKey(key); err == nil && obj != nil && obj.IsActive == 1 {
					next.ServeHTTP(w, r)
					return
				}
			}
			handlerutil.WriteJSONError(w, http.StatusUnauthorized, "Unauthorized: dashboard session required")
		})
	}
}

// RequireDashboardPage redirects browser navigations to /login when login is
// required and the session cookie is missing, mirroring upstream's
// dashboardGuard redirect for /dashboard routes. A tunnel/tailscale host with
// dashboard access disabled is also bounced to /login (upstream parity).
func RequireDashboardPage(repo *db.Repo) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if auth.RequireLogin(repo) && tunnelPageBlocked(repo, r) {
				http.Redirect(w, r, "/login", http.StatusFound)
				return
			}
			if !auth.RequireLogin(repo) || auth.SessionValid(r) {
				next.ServeHTTP(w, r)
				return
			}
			http.Redirect(w, r, "/login", http.StatusFound)
		})
	}
}

// tunnelPageBlocked reports whether the navigation arrives via a
// tunnel/tailscale hostname whose dashboard access is disabled.
func tunnelPageBlocked(repo *db.Repo, r *http.Request) bool {
	raw, err := repo.GetSettingsRaw()
	if err != nil || raw == nil {
		return false
	}
	return auth.TunnelLoginBlocked(r, raw)
}
