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

// PasswordHeaderCarriesOwnAuth names the one always-protected path whose handler
// re-verifies the dashboard password header itself.
//
// The header is a step-up credential the middleware cannot check on its own —
// verification needs the stored bcrypt hash, which lives in a repo the gate does
// not hold. So it is honoured only where the handler behind the gate validates
// the value: the backup download. Every other always-protected path keeps
// requiring a session or the CLI token, because none of them re-check it, and
// admitting the header by presence alone let any caller able to set one
// arbitrary header reach shutdown, self-update, health reset and the OAuth
// auto-imports.
const PasswordHeaderCarriesOwnAuth = "/api/settings/database"

// DashboardPasswordHeader is the step-up credential the backup download
// re-verifies itself. It lives here as well because the middleware has to let a
// request carrying it reach that verification.
const DashboardPasswordHeader = "x-9r-password"

// allowsPasswordHeader reports whether the password header should admit a
// request past the gate. This is a path check, not a credential check: only the
// handler that re-verifies the value can turn its presence into access.
func allowsPasswordHeader(path string) bool {
	return path == PasswordHeaderCarriesOwnAuth
}

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

// CallerIsDashboardPrivileged reports whether the request should be served
// secret material, as opposed to an engine client key.
//
// The dashboard gate admits three kinds of caller: a valid session cookie, the
// local CLI token, and — when requireLogin is off — anything at all. The first
// two are operator credentials. The third is not a credential, but it is the
// default local install reaching its own dashboard over loopback and the only
// way that install could manage keys at all; excluding it would show an
// operator with login disabled a table of masks and no way to reveal one.
// An engine client key is the single excluded caller: it is handed to a client
// app, and F-6 existed so it could not dump the rest of the key set.
func CallerIsDashboardPrivileged(repo *db.Repo, r *http.Request) bool {
	if !auth.RequireLogin(repo) {
		return true
	}
	return auth.SessionValid(r) ||
		auth.ValidCLIToken(r.Header.Get(auth.CLITokenHeader))
}

// RequireAdminAuth ensures that only requests with a valid dashboard session
// (auth_token cookie) or local CLI token (x-9r-cli-token) can proceed. Client
// API keys are rejected.
//
// The password header is admitted on exactly one path — the backup download —
// whose handler re-verifies it against the stored hash before exporting.
// Without that exception the re-check was unreachable over HTTP, so the step-up
// path existed only in tests mounting the handler directly, and an operator
// scripting a backup had no credential that worked from outside the process.
// The scoping is load-bearing: this gate fronts /api/version/shutdown,
// /api/version/update and /admin/health/reset, whose handlers verify no
// credential of their own, so accepting the header by presence there turned an
// arbitrary header into remote shutdown, remote self-update, and a health reset.
func RequireAdminAuth() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if auth.SessionValid(r) ||
				auth.ValidCLIToken(r.Header.Get(auth.CLITokenHeader)) ||
				allowsPasswordHeader(r.URL.Path) && r.Header.Get(DashboardPasswordHeader) != "" {
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
			// password header is admitted only on the backup download, which
			// re-verifies it against the stored hash — without that the step-up
			// credential could never reach the handler over HTTP, so a scripted
			// backup had no working credential (issue #47). The other
			// always-protected paths verify no credential themselves, so a header
			// present on them must not count as anything (see allowsPasswordHeader).
			if IsAlwaysProtectedPath(r.URL.Path) {
				if auth.SessionValid(r) ||
					auth.ValidCLIToken(r.Header.Get(auth.CLITokenHeader)) ||
					allowsPasswordHeader(r.URL.Path) && r.Header.Get(DashboardPasswordHeader) != "" {
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
