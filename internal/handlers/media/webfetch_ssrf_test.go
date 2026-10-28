package media

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

// A web-fetch URL that names an internal address must be refused before the
// gateway dials it. The dialer has its own socket-level guard, but that
// surfaces as an opaque transport error from whatever provider is in front;
// the caller should be told the address was refused.
func TestHandleWebFetch_RejectsPrivateTargets(t *testing.T) {
	// The package's TestMain turns private networking on so the fixtures can
	// reach httptest servers on loopback; this test is about the refusal.
	t.Setenv("ALLOW_PRIVATE_NETWORKING", "false")

	for _, url := range []string{
		"http://127.0.0.1:8080/admin",
		"http://localhost/",
		"http://169.254.169.254/latest/meta-data/",
		"http://10.0.0.5/",
		"http://192.168.1.1/",
		"http://[::1]/",
	} {
		t.Run(url, func(t *testing.T) {
			d, cleanup := setupMultimodalTestDB(t)
			defer cleanup()
			handler := newTestMediaHandler(db.NewRepo(d))

			rec := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/v1/web/fetch", strings.NewReader(`{"url":"`+url+`"}`))
			req.Header.Set("Content-Type", "application/json")
			handler.HandleWebFetch(rec, req)

			if rec.Code != http.StatusForbidden {
				t.Fatalf("expected 403 for %s, got %d: %s", url, rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "private IP blocked") {
				t.Errorf("expected SSRF message, got %s", rec.Body.String())
			}
		})
	}
}

func TestHandleWebFetch_RejectsNonHTTPSchemes(t *testing.T) {
	for _, url := range []string{"file:///etc/passwd", "gopher://127.0.0.1/", "ftp://example.com/"} {
		d, cleanup := setupMultimodalTestDB(t)
		defer cleanup()
		handler := newTestMediaHandler(db.NewRepo(d))

		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/web/fetch", strings.NewReader(`{"url":"`+url+`"}`))
		req.Header.Set("Content-Type", "application/json")
		handler.HandleWebFetch(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for %s, got %d", url, rec.Code)
		}
	}
}

func TestHandleWebFetch_RejectsMissingURL(t *testing.T) {
	d, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	handler := newTestMediaHandler(db.NewRepo(d))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/web/fetch", strings.NewReader(`{"model":"jina-reader"}`))
	req.Header.Set("Content-Type", "application/json")
	handler.HandleWebFetch(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

// ALLOW_PRIVATE_NETWORKING is the operator's own switch; when it is on, the
// pre-flight check must not override it. The two host shapes — a literal IP and
// a name that resolves to the same address — must answer alike, which is what
// the loopback row checks twice.
func TestTargetPrivate_RespectsAllowPrivateNetworking(t *testing.T) {
	tests := []struct {
		name  string
		allow string
		host  string
		want  bool
	}{
		{"allowed: public name", "true", "example.com", false},
		{"allowed: public literal", "true", "8.8.8.8", false},
		{"allowed: loopback literal", "true", "127.0.0.1", false},
		{"allowed: private literal", "true", "10.0.0.5", false},
		{"allowed: ipv6 loopback literal", "true", "::1", false},
		{"allowed: metadata literal", "true", "169.254.169.254", false},
		{"allowed: loopback name", "true", "localhost", false},

		{"blocked: public name", "false", "example.com", false},
		{"blocked: public literal", "false", "8.8.8.8", false},
		{"blocked: loopback literal", "false", "127.0.0.1", true},
		{"blocked: private literal", "false", "10.0.0.5", true},
		{"blocked: ipv6 loopback literal", "false", "::1", true},
		{"blocked: metadata literal", "false", "169.254.169.254", true},
		{"blocked: loopback name", "false", "localhost", true},
		{"blocked: empty host", "false", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ALLOW_PRIVATE_NETWORKING", tt.allow)
			if got := targetPrivate(context.Background(), tt.host); got != tt.want {
				t.Errorf("targetPrivate(%q) with ALLOW_PRIVATE_NETWORKING=%s = %v, want %v",
					tt.host, tt.allow, got, tt.want)
			}
		})
	}
}
