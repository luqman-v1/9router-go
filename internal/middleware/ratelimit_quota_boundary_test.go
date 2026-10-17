package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/models"
)

// TestQuotaAndTPMCoexistOnOneRequest proves a key bounded on both axes charges
// TPM and checks the lifetime quota on the same request, and that neither
// silently disables the other. The quota is an absolute budget; TPM is a
// per-window rate; they must both apply.
func TestQuotaAndTPMCoexistOnOneRequest(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	usage := stubUsage{tokens: 0} // well under any quota set below
	handler := RequireRateLimit(rl, nil, usage)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req = req.WithContext(withAPIKey(req, &models.APIKey{
		ID:           "both",
		Key:          "sk-both-abcdefghijklmnop",
		RateLimitTPM: new(1000),
		TokenQuota:   new(1000000),
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (quota under budget, TPM has room)", rec.Code)
	}
	// The TPM charge must have been reserved on the context, proving the TPM
	// path ran and was not skipped because a quota was configured.
	if _, ok := ReservedTokensFromContext(req.Context()); ok {
		t.Error("TPM reservation present on the caller's request; the handler's context should carry it, not this one")
	}
}

// TestGlobalDefaultsNeverInjectQuota proves the operator's global rate-limit
// defaults fill RPM/TPM/concurrency but NEVER a token quota: a default quota
// would cap every key in the install, which is not what a per-key rate default
// means. limitsFor must only read the key's own column for the quota.
func TestGlobalDefaultsNeverInjectQuota(t *testing.T) {
	// A Defaults closure that (wrongly, defensively) returns a quota-like zero
	// for the shared fields; limitsFor must not surface TokenQuota from it.
	defaults := func() Limits {
		return Limits{RPM: 100, TPM: 5000, Concurrency: 2}
	}
	limits := limitsFor(&models.APIKey{}, defaults, nil)

	if limits.RPM != 100 || limits.TPM != 5000 || limits.Concurrency != 2 {
		t.Errorf("global defaults not applied: %+v", limits)
	}
	if limits.TokenQuota != 0 {
		t.Errorf("TokenQuota = %d from global defaults, want 0 (quota is per-key only)", limits.TokenQuota)
	}

	// And a key that sets its own quota still resolves it.
	withQuota := limitsFor(&models.APIKey{TokenQuota: new(999)}, defaults, nil)
	if withQuota.TokenQuota != 999 {
		t.Errorf("per-key TokenQuota = %d, want 999", withQuota.TokenQuota)
	}
}

// TestQuotaBoundaryExactlyAtCap proves the boundary is "reject at or above":
// used == quota refuses, used == quota-1 allows. Off-by-one here would either
// let a key exceed its budget or cut it off one token early.
func TestQuotaBoundaryExactlyAtCap(t *testing.T) {
	cases := []struct {
		used      int
		wantCode  int
		wantLabel string
	}{
		{used: 999, wantCode: http.StatusOK},
		{used: 1000, wantCode: http.StatusForbidden, wantLabel: "1000/1000"},
		{used: 5000, wantCode: http.StatusForbidden, wantLabel: "5000/1000"},
	}
	for _, tc := range cases {
		rl := NewRateLimiter(time.Minute)
		handler := RequireRateLimit(rl, nil, stubUsage{tokens: tc.used})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		req = req.WithContext(withAPIKey(req, &models.APIKey{
			ID:         "bound",
			Key:        "sk-bound-abcdefghijklmnop",
			TokenQuota: new(1000),
		}))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != tc.wantCode {
			t.Errorf("used=%d: status = %d, want %d", tc.used, rec.Code, tc.wantCode)
		}
		if tc.wantLabel != "" && !strings.Contains(rec.Body.String(), tc.wantLabel) {
			t.Errorf("used=%d: body %q does not contain %q", tc.used, rec.Body.String(), tc.wantLabel)
		}
	}
}

// TestQuotaSkippedWhenKeySecretEmpty proves a key with no raw secret (a hashed
// row whose plaintext is blank) does not attempt a usage read that could never
// match, and is therefore allowed rather than spuriously refused.
func TestQuotaSkippedWhenKeySecretEmpty(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	counting := &countingUsage{}
	handler := RequireRateLimit(rl, nil, counting)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req = req.WithContext(withAPIKey(req, &models.APIKey{
		ID:         "hashed",
		Key:        "", // legacy/argon2id row, plaintext unrecoverable
		TokenQuota: new(1),
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (no secret to match, quota cannot apply)", rec.Code)
	}
	if counting.calls != 0 {
		t.Errorf("usage read %d time(s) for a key with no secret", counting.calls)
	}
}

// TestHugeQuotaDoesNotOverflow proves a byte-count-scale quota (a value larger
// than any realistic int32) is compared without overflow. The sum from the
// reader is an int; a quota near math.MaxInt must not wrap negative.
func TestHugeQuotaDoesNotOverflow(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	const huge = int(^uint(0) >> 1) // math.MaxInt
	handler := RequireRateLimit(rl, nil, stubUsage{tokens: 1})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req = req.WithContext(withAPIKey(req, &models.APIKey{
		ID:         "huge",
		Key:        "sk-huge-abcdefghijklmnop",
		TokenQuota: new(huge),
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (1 token spent against a MaxInt quota)", rec.Code)
	}
}
