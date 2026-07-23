package middleware

import "net/http"

// MaxBodySize limits the maximum size of request bodies to prevent OOM attacks.
// Default limit: 10MB. Media endpoints may need higher limits.
const DefaultMaxBodySize = 10 * 1024 * 1024 // 10MB

// MaxBody returns a middleware that limits request body size.
func MaxBody(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			next.ServeHTTP(w, r)
		})
	}
}
