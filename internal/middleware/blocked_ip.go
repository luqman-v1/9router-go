package middleware

import (
	"net/http"
	"strings"

	"9router/proxy/internal/auth"
	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
)

// EnforceBlockedIP checks whether the incoming client IP is on the blacklist.
// Fails open on lookup error; exempts loopback and dashboard routes.
func EnforceBlockedIP(repo *db.Repo) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path
			// Dashboard routes and auth login are exempt to prevent operator lockout
			if strings.HasPrefix(path, "/dashboard") || strings.HasPrefix(path, "/api/auth/login") || strings.HasPrefix(path, "/api/auth/reset-password") {
				next.ServeHTTP(w, r)
				return
			}

			ip := auth.LoginClientIP(r)
			if ip == "" || ip == "unknown" || ip == "127.0.0.1" || ip == "::1" {
				next.ServeHTTP(w, r)
				return
			}

			if repo != nil {
				blocked, err := repo.IsIPBlocked(ip)
				if err == nil && blocked {
					handlerutil.WriteJSONError(w, http.StatusForbidden, "Access denied")
					return
				}
			}

			next.ServeHTTP(w, r)
		})
	}
}
