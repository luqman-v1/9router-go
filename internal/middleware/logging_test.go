package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsQuietPath(t *testing.T) {
	quietPaths := []string{
		"/providers/claude.png",
		"/providers/inference.dahl.global.png",
		"/assets/index.js",
		"/favicon.ico",
		"/favicon.svg",
		"/icons.svg",
		"/api/connections",
		"/api/provider-nodes",
		"/api/usage/stats",
		"/api/usage/stream",
		"/usage/stream",
		"/health",
		"/api/tunnel/status",
	}

	for _, p := range quietPaths {
		if !isQuietPath(p) {
			t.Errorf("expected %s to be quiet path, got false", p)
		}
	}

	nonQuietPaths := []string{
		"/v1/chat/completions",
		"/chat/completions",
		"/v1/messages",
		"/v1/models",
		"/api/keys",
		"/api/combos",
	}

	for _, p := range nonQuietPaths {
		if isQuietPath(p) {
			t.Errorf("expected %s NOT to be quiet path, got true", p)
		}
	}
}

func TestRequestLogger_ServesAndPassesThrough(t *testing.T) {
	called := false
	dummyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	loggedHandler := RequestLogger(dummyHandler)

	req := httptest.NewRequest("GET", "/v1/api/usage/stats", nil)
	rec := httptest.NewRecorder()

	loggedHandler.ServeHTTP(rec, req)

	if !called {
		t.Error("expected wrapped handler to be called")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	if rec.Body.String() != "ok" {
		t.Errorf("expected body 'ok', got %q", rec.Body.String())
	}
}

func TestStatusWriter_FlushAndHeaderCases(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := &statusWriter{ResponseWriter: rec}

	// Flush forwards to recorder flusher
	sw.Flush()
	if !rec.Flushed {
		t.Error("expected rec to be flushed")
	}

	// Write without WriteHeader implicitly sets 200
	sw.Write([]byte("hello"))
	if sw.status != http.StatusOK {
		t.Errorf("expected status 200, got %d", sw.status)
	}

	// Second WriteHeader does not change status
	sw.WriteHeader(http.StatusBadRequest)
	if sw.status != http.StatusOK {
		t.Errorf("status should remain 200, got %d", sw.status)
	}
}
