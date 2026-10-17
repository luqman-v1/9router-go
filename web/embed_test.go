package web_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/web"
)

func TestHandler_PWAAssets(t *testing.T) {
	handler := web.Handler()

	tests := []struct {
		name               string
		targetPath         string
		expectedStatus     int
		expectedHeaderKey  string
		expectedHeaderPart string
	}{
		{
			name:               "serves manifest.webmanifest with correct Content-Type",
			targetPath:         "/manifest.webmanifest",
			expectedStatus:     http.StatusOK,
			expectedHeaderKey:  "Content-Type",
			expectedHeaderPart: "application/manifest+json",
		},
		{
			name:               "serves manifest.json with JSON or text Content-Type",
			targetPath:         "/manifest.json",
			expectedStatus:     http.StatusOK,
			expectedHeaderKey:  "Content-Type",
			expectedHeaderPart: "application/json",
		},
		{
			name:               "serves sw.js with no-cache and javascript Content-Type",
			targetPath:         "/sw.js",
			expectedStatus:     http.StatusOK,
			expectedHeaderKey:  "Content-Type",
			expectedHeaderPart: "javascript",
		},
		{
			name:               "serves sw.js with Service-Worker-Allowed header",
			targetPath:         "/sw.js",
			expectedStatus:     http.StatusOK,
			expectedHeaderKey:  "Service-Worker-Allowed",
			expectedHeaderPart: "/",
		},
		{
			name:               "serves icon-192.png",
			targetPath:         "/icons/icon-192.png",
			expectedStatus:     http.StatusOK,
			expectedHeaderKey:  "Content-Type",
			expectedHeaderPart: "image/png",
		},
		{
			name:               "serves icon-512.png",
			targetPath:         "/icons/icon-512.png",
			expectedStatus:     http.StatusOK,
			expectedHeaderKey:  "Content-Type",
			expectedHeaderPart: "image/png",
		},
		{
			name:               "SPA fallback to index.html on unknown route",
			targetPath:         "/combos",
			expectedStatus:     http.StatusOK,
			expectedHeaderKey:  "Content-Type",
			expectedHeaderPart: "text/html",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.targetPath, nil)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.expectedStatus {
				t.Fatalf("expected status %d for %q, got %d", tt.expectedStatus, tt.targetPath, rec.Code)
			}

			if tt.expectedHeaderKey != "" {
				val := rec.Header().Get(tt.expectedHeaderKey)
				if !strings.Contains(val, tt.expectedHeaderPart) {
					t.Errorf("expected header %s to contain %q for %q, got %q",
						tt.expectedHeaderKey, tt.expectedHeaderPart, tt.targetPath, val)
				}
			}
		})
	}
}

// The entry document must not be cached: it names the hashed bundle, and an old
// entry keeps an old dashboard on screen after an upgrade. The hashed assets are
// the opposite — their name changes with their content, so they can be kept.
func TestHandlerCacheHeaders(t *testing.T) {
	h := web.Handler()

	for _, tc := range []struct{ path, want, why string }{
		{"/", "no-cache", "SPA entry"},
		{"/dashboard/usage", "no-cache", "client-side route"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if got := rec.Header().Get("Cache-Control"); got != tc.want {
			t.Errorf("%s (%s): Cache-Control = %q, want %q", tc.path, tc.why, got, tc.want)
		}
	}

	// Any asset the entry points at is content-hashed and may be kept forever.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/whatever.js", nil))
	if rec.Code == http.StatusOK {
		if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
			t.Errorf("/assets/*: Cache-Control = %q, want an immutable policy", got)
		}
	}
}
