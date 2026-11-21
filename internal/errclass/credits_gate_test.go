package errclass

import "testing"

// A 400 is how an upstream rejects the CALLER's request, and many quote that
// request back in error.message. A credit phrase inside such an echo is request
// text, not a billing verdict: reading it as one parked a healthy shared
// connection for every user until the recovery tick cleared it.
func TestClassifyDetail_400CreditsNeedStructuredCode(t *testing.T) {
	tests := []struct {
		name     string
		code     int
		status   string
		message  string
		wantKind Kind
		wantSc   Scope
	}{
		{
			name:     "400 echoing insufficient_quota is request-scoped",
			code:     400,
			message:  "insufficient_quota: exceeded your current quota",
			wantKind: KindBadRequest,
			wantSc:   ScopeRequest,
		},
		{
			name:     "400 echoing a credit balance is request-scoped",
			code:     400,
			message:  "your credit balance is too low to access the API",
			wantKind: KindBadRequest,
			wantSc:   ScopeRequest,
		},
		{
			name:     "402 keeps the credit reading",
			code:     402,
			message:  "insufficient_quota: exceeded your current quota",
			wantKind: KindQuotaExhausted,
			wantSc:   ScopeAccount,
		},
		{
			name:     "429 keeps the credit reading",
			code:     429,
			message:  "credit balance is too low",
			wantKind: KindQuotaExhausted,
			wantSc:   ScopeAccount,
		},
		{
			// A throttle at any status is still a throttle: only the quota
			// reading is gated, and this wording says "wait", not "top up".
			name:     "400 rate limit is a throttle, not a verdict",
			code:     400,
			message:  "too many requests, rate limit reached",
			wantKind: KindRateLimit,
			wantSc:   ScopeAccount,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := ClassifyDetail("", tt.code, tt.status, tt.message)
			if c.Kind != tt.wantKind {
				t.Errorf("Kind = %q, want %q", c.Kind, tt.wantKind)
			}
			if c.EffectiveScope() != tt.wantSc {
				t.Errorf("Scope = %q, want %q", c.EffectiveScope(), tt.wantSc)
			}
		})
	}
}

// A structured credit code is the one signal that cannot be the caller's own
// words quoted back, so it admits the credit reading on a 400.
func TestLooksLikeCreditErrorCode(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{"openai error.code", `{"error":{"code":"insufficient_quota"}}`, true},
		{"top level code", `{"code":"credit_balance_too_low"}`, true},
		{"new-api code", `{"error":{"code":"insufficient_user_quota"}}`, true},
		{"anthropic code", `{"error":{"code":"credit_balance_exhausted"}}`, true},
		{"case and space", `{"error":{"code":" Insufficient_Quota "}}`, true},
		{"numeric code says nothing about billing", `{"error":{"code":429}}`, false},
		{"unrelated code", `{"error":{"code":"model_not_found"}}`, false},
		{"no code", `{"error":{"message":"insufficient_quota"}}`, false},
		{"not json", `insufficient_quota`, false},
		{"empty", ``, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := LooksLikeCreditErrorCode([]byte(tt.body)); got != tt.want {
				t.Errorf("LooksLikeCreditErrorCode(%q) = %v, want %v", tt.body, got, tt.want)
			}
		})
	}
}
