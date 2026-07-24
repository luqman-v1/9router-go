package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"

	"9router/proxy/internal/log"
)

// RequestID returns a middleware that injects a unique request ID (Correlation ID)
// into each request context and response header (X-Request-ID).
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := r.Header.Get("X-Request-ID")
		if reqID == "" {
			reqID = generateRequestID()
		}

		w.Header().Set("X-Request-ID", reqID)
		ctx := context.WithValue(r.Context(), log.RequestIDKey, reqID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// GetRequestID retrieves the request ID from an http.Request.
func GetRequestID(r *http.Request) string {
	if r == nil {
		return ""
	}
	return GetRequestIDFromContext(r.Context())
}

// GetRequestIDFromContext retrieves the request ID from a context.Context.
func GetRequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	val := ctx.Value(log.RequestIDKey)
	if val == nil {
		return ""
	}
	reqID, ok := val.(string)
	if !ok {
		return ""
	}
	return reqID
}

func generateRequestID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "req_unknown"
	}
	return "req_" + hex.EncodeToString(b)
}
