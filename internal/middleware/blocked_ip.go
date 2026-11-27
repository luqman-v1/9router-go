package middleware

import (
	"net/http"
	"net/netip"
	"strings"

	"9router/proxy/internal/auth"
	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
)

// EnforceBlockedIP checks whether the incoming client IP is on the blacklist.
// Fails open on lookup error; exempts loopback and dashboard routes.
func EnforceBlockedIP(repo *db.Repo) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path
			// Dashboard routes and auth login are exempt to prevent operator lockout.
			// Note: /api/blocked-ips itself is not exempt, so self-banning from a remote
			// IP requires out-of-band CLI/DB recovery.
			if strings.HasPrefix(path, "/dashboard") || strings.HasPrefix(path, "/api/auth/login") || strings.HasPrefix(path, "/api/auth/reset-password") {
				next.ServeHTTP(w, r)
				return
			}

			ip := auth.LoginClientIP(r)
			if ip == "" || ip == "unknown" {
				next.ServeHTTP(w, r)
				return
			}
			if addr, err := netip.ParseAddr(ip); err == nil && addr.Unmap().IsLoopback() {
				next.ServeHTTP(w, r)
				return
			}

			if repo != nil {
				blocked, err := repo.IsIPBlocked(ip)
				if err != nil {
					log.Warn("blocked_ip", "blacklist lookup failed; failing open", "ip", ip, "error", err)
				} else if blocked {
					handlerutil.WriteJSONError(w, http.StatusForbidden, "Access denied")
					return
				}
			}

			next.ServeHTTP(w, r)
		})
	}
}
