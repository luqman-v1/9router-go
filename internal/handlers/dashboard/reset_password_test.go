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
	ResetResetPasswordLimiter()
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
	req := httptest.NewRequest(http.MethodPost, "/api/auth/reset-password", strings.NewReader(`{"confirm":true}`))
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
	ResetResetPasswordLimiter()
	repo, cleanup := setupSettingsTestDB(t)
	defer cleanup()

	h := NewDashboardHandler(repo, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/reset-password", strings.NewReader(`{"confirm":true}`))
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
	reqSpoofed := httptest.NewRequest(http.MethodPost, "/api/auth/reset-password", strings.NewReader(`{"confirm":true}`))
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
	ResetResetPasswordLimiter()
	repo, cleanup := setupSettingsTestDB(t)
	defer cleanup()

	h := NewDashboardHandler(repo, nil)

	// Subtests share one peer address, so the bucket is cleared per case.
	t.Run("rejects cross-site Sec-Fetch-Site", func(t *testing.T) {
		ResetResetPasswordLimiter()
		req := httptest.NewRequest(http.MethodPost, "/api/auth/reset-password", strings.NewReader(`{"confirm":true}`))
		req.RemoteAddr = "127.0.0.1:41234"
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		rec := httptest.NewRecorder()
		h.HandleResetPassword(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("cross-site Sec-Fetch-Site status = %d, want 403", rec.Code)
		}
	})

	t.Run("rejects foreign Origin", func(t *testing.T) {
		ResetResetPasswordLimiter()
		req := httptest.NewRequest(http.MethodPost, "/api/auth/reset-password", strings.NewReader(`{"confirm":true}`))
		req.RemoteAddr = "127.0.0.1:41234"
		req.Header.Set("Origin", "https://evil.com")
		rec := httptest.NewRecorder()
		h.HandleResetPassword(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("foreign Origin status = %d, want 403", rec.Code)
		}
	})

	t.Run("rejects foreign Referer", func(t *testing.T) {
		ResetResetPasswordLimiter()
		req := httptest.NewRequest(http.MethodPost, "/api/auth/reset-password", strings.NewReader(`{"confirm":true}`))
		req.RemoteAddr = "127.0.0.1:41234"
		req.Header.Set("Referer", "https://evil.com/attack.html")
		rec := httptest.NewRecorder()
		h.HandleResetPassword(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("foreign Referer status = %d, want 403", rec.Code)
		}
	})

	t.Run("accepts same-origin localhost Origin", func(t *testing.T) {
		ResetResetPasswordLimiter()
		req := httptest.NewRequest(http.MethodPost, "/api/auth/reset-password", strings.NewReader(`{"confirm":true}`))
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
	ResetResetPasswordLimiter()
	repo, cleanup := setupSettingsTestDB(t)
	defer cleanup()

	h := NewDashboardHandler(repo, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/reset-password", strings.NewReader(`{"confirm":true}`))
	req.RemoteAddr = "127.0.0.1:41234"
	req.Host = "127.0.0.1:20128"
	rec := httptest.NewRecorder()

	h.HandleResetPassword(rec, req)

	if strings.Contains(rec.Body.String(), defaultInitialPassword) {
		t.Errorf("response leaked the default password: %s", rec.Body.String())
	}
}

// Clearing the hash is destructive enough that the URL alone must not do it: a
// bare POST — a cross-site form, a prefetch, a stray curl — has to be refused.
func TestHandleResetPasswordRequiresConfirmation(t *testing.T) {
	repo, cleanup := setupSettingsTestDB(t)
	defer cleanup()
	ResetResetPasswordLimiter()

	hash, err := bcrypt.GenerateFromPassword([]byte("forgotten"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateSettingsRaw(map[string]any{"password": string(hash)}); err != nil {
		t.Fatalf("seed password: %v", err)
	}

	h := NewDashboardHandler(repo, nil)
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "no body", body: ""},
		{name: "empty object", body: `{}`},
		{name: "confirm false", body: `{"confirm":false}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ResetResetPasswordLimiter()
			req := httptest.NewRequest(http.MethodPost, "/api/auth/reset-password", strings.NewReader(tc.body))
			req.RemoteAddr = "127.0.0.1:41234"
			rec := httptest.NewRecorder()
			h.HandleResetPassword(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
		})
	}

	// The hash survived every refused attempt, which is the point of the gate.
	raw, err := repo.GetSettingsRaw()
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if got, _ := raw["password"].(string); got == "" {
		t.Error("a refused reset still cleared the stored password")
	}
}

// "Loopback" is not "trusted": a same-host tunnel forwards strangers from
// 127.0.0.1. The rate limit is what stops one caller from clearing the hash in a
// loop, so it has to bite before the reset runs.
func TestHandleResetPasswordRateLimits(t *testing.T) {
	repo, cleanup := setupSettingsTestDB(t)
	defer cleanup()
	ResetResetPasswordLimiter()

	h := NewDashboardHandler(repo, nil)

	codes := make([]int, 0, resetPasswordMaxPerWindow+1)
	for i := 0; i < resetPasswordMaxPerWindow+1; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/reset-password", strings.NewReader(`{"confirm":true}`))
		req.RemoteAddr = "127.0.0.1:41234"
		rec := httptest.NewRecorder()
		h.HandleResetPassword(rec, req)
		codes = append(codes, rec.Code)
	}

	for i, code := range codes[:resetPasswordMaxPerWindow] {
		if code != http.StatusOK {
			t.Fatalf("attempt %d status = %d, want 200", i, code)
		}
	}
	if last := codes[resetPasswordMaxPerWindow]; last != http.StatusTooManyRequests {
		t.Fatalf("attempt past the budget status = %d, want 429", last)
	}

	// A different peer has its own budget.
	req := httptest.NewRequest(http.MethodPost, "/api/auth/reset-password", strings.NewReader(`{"confirm":true}`))
	req.RemoteAddr = "127.0.0.2:41234"
	rec := httptest.NewRecorder()
	h.HandleResetPassword(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("a second peer status = %d, want 200 (budgets are per address)", rec.Code)
	}
}
