package middleware

import (
	"net/http"
	"time"

	"9router/proxy/internal/log"
)

// statusWriter wraps http.ResponseWriter to capture the status code
// and guard against duplicate WriteHeader calls.
type statusWriter struct {
	http.ResponseWriter
	status  int
	written bool
}

func (w *statusWriter) WriteHeader(code int) {
	if w.written {
		return
	}
	w.written = true
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.written {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

// RequestLogger returns a middleware that logs each HTTP request with
// method, path, status code, duration, and request ID using the
// structured logger. It also strips repeated /v1/ prefixes from paths.
func RequestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		start := time.Now()
		path := req.URL.Path
		for len(path) > 3 && path[:4] == "/v1/" {
			path = path[3:]
		}
		req.URL.Path = path
		reqID := GetRequestIDFromContext(req.Context())
		if reqID != "" {
			w.Header().Set("X-Request-ID", reqID)
		}
		ww := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(ww, req)

		msg := req.Method + " " + path
		durStr := time.Since(start).String()

		switch {
		case ww.status >= 500:
			log.Error("request", msg, "status", ww.status, "duration", durStr, "id", reqID)
		case ww.status >= 400:
			log.Warn("request", msg, "status", ww.status, "duration", durStr, "id", reqID)
		default:
			log.Info("request", msg, "status", ww.status, "duration", durStr, "id", reqID)
		}
	})
}
