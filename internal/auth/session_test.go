package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSignVerifyRoundTrip(t *testing.T) {
	token, err := Sign("test-secret", time.Now())
	if err != nil {
		t.Fatalf("Sign failed: %v", err)
	}
	if !Verify(token, "test-secret") {
		t.Error("valid token should verify")
	}
	if Verify(token, "wrong-secret") {
		t.Error("token must not verify with a different secret")
	}
}

func TestVerifyRejectsBadInput(t *testing.T) {
	secret := "test-secret"
	token, _ := Sign(secret, time.Now())

	cases := map[string]struct {
		token  string
		secret string
	}{
		"empty token":     {"", secret},
		"empty secret":    {token, ""},
		"not a jwt":       {"not-a-jwt", secret},
		"tampered":        {token + "x", secret},
		"truncated":       {token[:len(token)-4], secret},
		"wrong signature": {"aaa.bbb.ccc", secret},
	}
	for name, c := range cases {
		if Verify(c.token, c.secret) {
			t.Errorf("%s: expected verification to fail", name)
		}
	}
}

func TestVerifyRejectsExpired(t *testing.T) {
	secret := "test-secret"
	expired, err := Sign(secret, time.Now().Add(-25*time.Hour))
	if err != nil {
		t.Fatalf("Sign failed: %v", err)
	}
	if Verify(expired, secret) {
		t.Error("a token older than the 24h TTL must fail")
	}
}

func TestSessionValidReadsCookie(t *testing.T) {
	t.Setenv("JWT_SECRET", "session-test-secret")
	t.Setenv("DATA_DIR", t.TempDir())

	token, err := Sign(Secret(), time.Now())
	if err != nil {
		t.Fatalf("Sign failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	if SessionValid(req) {
		t.Error("request without a cookie must not be a valid session")
	}

	req.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	if !SessionValid(req) {
		t.Error("request with a fresh signed cookie should be a valid session")
	}

	bad := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	bad.AddCookie(&http.Cookie{Name: CookieName, Value: token + "tamper"})
	if SessionValid(bad) {
		t.Error("tampered cookie must not validate")
	}
}

func TestLoginLimiterLocksAfterFiveFails(t *testing.T) {
	ResetLoginLimiter()
	const ip = "limiter-test"
	for range 4 {
		if remaining := RecordLoginFail(ip); remaining != 1 && remaining != 2 && remaining != 3 && remaining != 4 {
			t.Fatalf("unexpected remaining count %d", remaining)
		}
		if locked, _ := LoginLocked(ip); locked {
			t.Fatal("bucket must stay unlocked before the 5th failure")
		}
	}
	RecordLoginFail(ip)
	locked, retryAfter := LoginLocked(ip)
	if !locked {
		t.Fatal("bucket must lock on the 5th failure")
	}
	if retryAfter <= 0 {
		t.Error("lock must report a positive retryAfter")
	}
	RecordLoginSuccess(ip)
	if locked, _ := LoginLocked(ip); locked {
		t.Error("success must clear the bucket")
	}
}

func TestLoginClientIPIgnoresSpoofedHeaders(t *testing.T) {
	ResetLoginLimiter()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	req.RemoteAddr = "203.0.113.50:12345"
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	req.Header.Set("x-9r-real-ip", "203.0.113.9")
	if got := LoginClientIP(req); got != "203.0.113.50" {
		t.Errorf("untrusted peer must use socket IP, got %q", got)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	req.RemoteAddr = "10.0.0.1:45678"
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	t.Setenv("TRUST_PROXY", "true")
	if got := LoginClientIP(req); got != "203.0.113.9" {
		t.Errorf("trusted proxy must read XFF, got %q", got)
	}

	// CF-Connecting-IP takes precedence when present from Cloudflare CIDR under TRUST_PROXY or TRUST_CLOUDFLARE
	req = httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	req.RemoteAddr = "173.245.48.1:45678"
	req.Header.Set("CF-Connecting-IP", "198.51.100.42")
	if got := LoginClientIP(req); got != "198.51.100.42" {
		t.Errorf("expected CF-Connecting-IP 198.51.100.42, got %q", got)
	}

	t.Setenv("TRUST_PROXY", "")
	t.Setenv("TRUST_CLOUDFLARE", "true")
	if got := LoginClientIP(req); got != "198.51.100.42" {
		t.Errorf("expected CF-Connecting-IP under TRUST_CLOUDFLARE, got %q", got)
	}

	// Non-Cloudflare peer must not be trusted for CF-Connecting-IP
	req.RemoteAddr = "10.0.0.1:45678"
	if got := LoginClientIP(req); got != "10.0.0.1" {
		t.Errorf("expected non-CF peer to reject CF-Connecting-IP, got %q", got)
	}
}

func TestLoginClientIP_DirectRemoteAddr(t *testing.T) {
	ResetLoginLimiter()
	t.Setenv("TRUST_PROXY", "")
	t.Setenv("TRUST_CLOUDFLARE", "")
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	req.RemoteAddr = "172.17.0.1:45678"
	if got := LoginClientIP(req); got != "172.17.0.1" {
		t.Errorf("clean direct request must use remote address host, got %q, want 172.17.0.1", got)
	}
}

func TestTunnelLoginBlocked(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	req.Host = "tunnel.example.com"
	raw := map[string]any{"tunnelUrl": "https://tunnel.example.com"}
	if !TunnelLoginBlocked(req, raw) {
		t.Error("tunnel host must be blocked without explicit access")
	}
	raw["tunnelDashboardAccess"] = true
	if TunnelLoginBlocked(req, raw) {
		t.Error("explicit tunnel access must allow login")
	}
	req.Host = "localhost:20130"
	if TunnelLoginBlocked(req, raw) {
		t.Error("local host must never be tunnel-blocked")
	}
}

func TestCLITokenRoundTrip(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	token := CLIToken()
	if token == "" {
		t.Fatal("CLI token must derive")
	}
	if !ValidCLIToken(token) {
		t.Error("derived CLI token must validate")
	}
	if ValidCLIToken("bogus-token-value!!") || ValidCLIToken("") {
		t.Error("arbitrary CLI tokens must not validate")
	}
	if again := CLIToken(); again != token {
		t.Error("CLI token must be stable across calls")
	}
}

func TestLoginLimiter_BoundedUnderScan(t *testing.T) {
	ResetLoginLimiter()
	// Simulate a distributed scan: 3x the cap in unique IPs, few fails each.
	for i := 0; i < 3*maxLoginBuckets; i++ {
		ip := "10.9.0." + string(rune('0'+i%10)) + "." + string(rune('0'+(i/10)%10)) + "." + string(rune('0'+(i/100)%10)) + "." + itoa(i)
		RecordLoginFail(ip)
	}
	loginMu.Lock()
	n := len(loginAttempts)
	loginMu.Unlock()
	if n > maxLoginBuckets {
		t.Fatalf("limiter map grew unbounded: %d > cap %d", n, maxLoginBuckets)
	}
	ResetLoginLimiter()
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	return string(b[p:])
}
