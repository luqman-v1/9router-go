package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// The recovery path for a locked-out operator: clear the stored hash so the
// gateway falls back to the configured initial password. Without it there is no
// recovery short of deleting the database, because `password` is a protected
// setting and a PUT through /api/settings is refused.
func TestHandleResetPasswordClearsTheStoredHash(t *testing.T) {
	repo, cleanup := setupSettingsTestDB(t)
	defer cleanup()

	hash, err := bcrypt.GenerateFromPassword([]byte("forgotten"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateSettingsRaw(map[string]any{"password": string(hash)}); err != nil {
		t.Fatalf("seed password: %v", err)
	}
	if h, _ := repo.GetSettingsRaw(); h["password"] == "" {
		t.Fatal("fixture did not store a hash")
	}

	h := NewDashboardHandler(repo, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/reset-password", nil)
	req.RemoteAddr = "127.0.0.1:41234"
	req.Host = "localhost:20128"
	rec := httptest.NewRecorder()

	h.HandleResetPassword(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	raw, err := repo.GetSettingsRaw()
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if got, _ := raw["password"].(string); got != "" {
		t.Errorf("stored password = %q, want it cleared", got)
	}
	// The initial password works again, which is what "reset" has to mean.
	if !h.verifyDashboardPassword(defaultInitialPassword) {
		t.Error("the default password should verify after a reset")
	}
	// And the forgotten one does not.
	if h.verifyDashboardPassword("forgotten") {
		t.Error("the old password still verifies after a reset")
	}
}

// The route is reachable without a session by design, so the network origin is
// the only thing between an exposed tunnel and a password reset.
func TestHandleResetPasswordRefusesNonLocalCallers(t *testing.T) {
	repo, cleanup := setupSettingsTestDB(t)
	defer cleanup()

	h := NewDashboardHandler(repo, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/reset-password", nil)
	req.RemoteAddr = "203.0.113.7:5555"
	req.Host = "gateway.example.com"
	rec := httptest.NewRecorder()

	h.HandleResetPassword(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "localhost") {
		t.Errorf("error does not say why: %s", rec.Body.String())
	}

	// Security regression: spoofing Host: localhost must NOT bypass the RemoteAddr gate.
	reqSpoofed := httptest.NewRequest(http.MethodPost, "/api/auth/reset-password", nil)
	reqSpoofed.RemoteAddr = "203.0.113.7:5555"
	reqSpoofed.Host = "localhost"
	recSpoofed := httptest.NewRecorder()
	h.HandleResetPassword(recSpoofed, reqSpoofed)
	if recSpoofed.Code != http.StatusForbidden {
		t.Fatalf("spoofed Host: localhost status = %d, want 403 Forbidden", recSpoofed.Code)
	}
}

// CSRF / Cross-site form POST attacks must be rejected even from loopback.
func TestHandleResetPasswordRefusesCrossSiteCallers(t *testing.T) {
	repo, cleanup := setupSettingsTestDB(t)
	defer cleanup()

	h := NewDashboardHandler(repo, nil)

	t.Run("rejects cross-site Sec-Fetch-Site", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/reset-password", nil)
		req.RemoteAddr = "127.0.0.1:41234"
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		rec := httptest.NewRecorder()
		h.HandleResetPassword(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("cross-site Sec-Fetch-Site status = %d, want 403", rec.Code)
		}
	})

	t.Run("rejects foreign Origin", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/reset-password", nil)
		req.RemoteAddr = "127.0.0.1:41234"
		req.Header.Set("Origin", "https://evil.com")
		rec := httptest.NewRecorder()
		h.HandleResetPassword(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("foreign Origin status = %d, want 403", rec.Code)
		}
	})

	t.Run("rejects foreign Referer", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/reset-password", nil)
		req.RemoteAddr = "127.0.0.1:41234"
		req.Header.Set("Referer", "https://evil.com/attack.html")
		rec := httptest.NewRecorder()
		h.HandleResetPassword(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("foreign Referer status = %d, want 403", rec.Code)
		}
	})

	t.Run("accepts same-origin localhost Origin", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/reset-password", nil)
		req.RemoteAddr = "127.0.0.1:41234"
		req.Header.Set("Origin", "http://localhost:20130")
		rec := httptest.NewRecorder()
		h.HandleResetPassword(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("same-origin localhost Origin status = %d, want 200", rec.Code)
		}
	})
}

// The response must never carry the password literal — a reset that printed it
// would hand the credential to anyone who can reach the endpoint.
func TestHandleResetPasswordDoesNotEchoTheDefault(t *testing.T) {
	repo, cleanup := setupSettingsTestDB(t)
	defer cleanup()

	h := NewDashboardHandler(repo, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/reset-password", nil)
	req.RemoteAddr = "127.0.0.1:41234"
	req.Host = "127.0.0.1:20128"
	rec := httptest.NewRecorder()

	h.HandleResetPassword(rec, req)

	if strings.Contains(rec.Body.String(), defaultInitialPassword) {
		t.Errorf("response leaked the default password: %s", rec.Body.String())
	}
}
