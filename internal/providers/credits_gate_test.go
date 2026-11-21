package providers

import (
	"net/http"
	"testing"
)

// An HTTP 400 is how an upstream rejects the caller's request, and many quote
// that request back in error.message. A credit phrase inside such an echo is the
// caller's text, not the account's state: parking on it took a shared connection
// out of rotation for an hour on every user's request. Ported from OmniRoute PR
// #20567.
func TestClassifyError_400CreditsNeedStructuredCode(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		text        string
		body        string
		provider    string
		wantFall    bool
		wantCool    int
		wantNoCools []string
	}{
		{
			name:     "400 echoing a credit phrase does not park",
			status:   http.StatusBadRequest,
			text:     "insufficient_quota: exceeded your current quota",
			body:     `{"error":{"message":"insufficient_quota: exceeded your current quota"}}`,
			wantFall: false,
		},
		{
			name:     "400 echoing a balance warning does not park",
			status:   http.StatusBadRequest,
			text:     "Your credit balance is too low to access the API",
			body:     `{"error":{"message":"Your credit balance is too low to access the API"}}`,
			wantFall: false,
		},
		{
			name:     "400 carrying a structured credit code parks for an hour",
			status:   http.StatusBadRequest,
			text:     "Insufficient credits to complete this request",
			body:     `{"error":{"code":"insufficient_quota","message":"Insufficient credits to complete this request"}}`,
			wantFall: true,
			wantCool: QuotaExhaustedCooldownMs,
		},
		{
			name:     "400 with a top-level credit code parks for an hour",
			status:   http.StatusBadRequest,
			text:     "credit exhausted",
			body:     `{"code":"credit_balance_too_low","message":"credit exhausted"}`,
			wantFall: true,
			wantCool: QuotaExhaustedCooldownMs,
		},
		{
			name:     "402 with prose alone still parks",
			status:   http.StatusPaymentRequired,
			text:     "payment required",
			wantFall: true,
			wantCool: QuotaExhaustedCooldownMs,
		},
		{
			name:     "429 with a credit phrase still parks",
			status:   http.StatusTooManyRequests,
			text:     "insufficient balance",
			body:     `{"error":{"message":"insufficient balance"}}`,
			wantFall: true,
			wantCool: QuotaExhaustedCooldownMs,
		},
		{
			// A non-quota rule on a 400 is unaffected: the gate skips the
			// Quota rules only, so codebuddy's block rule still fires.
			name:     "400 codebuddy block rule still fires",
			status:   http.StatusBadRequest,
			text:     "request illegal",
			body:     `{"error":{"message":"request illegal"}}`,
			provider: "codebuddy-intl",
			wantFall: true,
			wantCool: 0, // Backoff rule: level 1 → 2s, not a fixed cooldown
		},
		{
			// The allow-list: these upstreams answer a spent balance with a 400
			// and no structured code, so their prose still counts.
			name:     "400 commandcode allow-listed by canonical id",
			status:   http.StatusBadRequest,
			text:     "You have no remaining credits",
			body:     `{"error":{"message":"You have no remaining credits"}}`,
			provider: "commandcode",
			wantFall: true,
			wantCool: QuotaExhaustedCooldownMs,
		},
		{
			name:     "400 commandcode allow-listed through its alias",
			status:   http.StatusBadRequest,
			text:     "You have no remaining credits",
			provider: "cmc",
			wantFall: true,
			wantCool: QuotaExhaustedCooldownMs,
		},
		{
			name:     "400 bai allow-listed through its alias",
			status:   http.StatusBadRequest,
			text:     "insufficient credit",
			provider: "b-ai",
			wantFall: true,
			wantCool: QuotaExhaustedCooldownMs,
		},
		{
			name:     "400 openai-compatible node is allow-listed by prefix",
			status:   http.StatusBadRequest,
			text:     "insufficient balance",
			provider: "openai-compatible-mygateway",
			wantFall: true,
			wantCool: QuotaExhaustedCooldownMs,
		},
		{
			// Prefix only, never substring: a provider id that merely contains
			// the node name is not an openai-compatible node.
			name:     "400 lookalike provider id is not allow-listed",
			status:   http.StatusBadRequest,
			text:     "insufficient balance",
			provider: "not-openai-compatible-mygateway",
			wantFall: false,
		},
		{
			name:     "400 unknown provider prose is not a verdict",
			status:   http.StatusBadRequest,
			text:     "quota exceeded",
			provider: "deepseek",
			wantFall: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cls := ClassifyErrorForProvider(tt.status, tt.text, 0, tt.provider, []byte(tt.body))
			if cls.ShouldFallback != tt.wantFall {
				t.Fatalf("ShouldFallback = %v, want %v (cls = %+v)", cls.ShouldFallback, tt.wantFall, cls)
			}
			if tt.wantCool != 0 && cls.CooldownMs != tt.wantCool {
				t.Errorf("CooldownMs = %d, want %d", cls.CooldownMs, tt.wantCool)
			}
			if !tt.wantFall && cls.CooldownMs != 0 {
				t.Errorf("CooldownMs = %d, want 0 for a request-scoped 400", cls.CooldownMs)
			}
		})
	}
}

// The body is optional at every existing call site that only has errorText.
// Passing none keeps the request-scoped 4xx contract: prose alone never earns a
// quota verdict. A bare credit code in errorText is not prose — it is the
// upstream's own verdict, which chat.extractErrorText folds in when the body is
// not threaded through — so it does park.
func TestClassifyError_400CreditsNoBodyIsRequestScoped(t *testing.T) {
	cls := ClassifyError(http.StatusBadRequest, "exceeded your current quota", 0)
	if cls.ShouldFallback {
		t.Fatalf("ShouldFallback = true, want false without a body to read a code from")
	}
	if cls.CooldownMs != 0 {
		t.Errorf("CooldownMs = %d, want 0", cls.CooldownMs)
	}
	byCode := ClassifyError(http.StatusBadRequest, "credit_balance_exhausted", 0)
	if !byCode.ShouldFallback || byCode.CooldownMs != QuotaExhaustedCooldownMs {
		t.Fatalf("bare credit code in errorText = %+v, want the quota park", byCode)
	}
}

func TestLooksLikeCreditCode(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{"openai error.code", `{"error":{"code":"insufficient_quota"}}`, true},
		{"new-api code", `{"error":{"code":"insufficient_user_quota"}}`, true},
		{"top-level code", `{"code":"billing_hard_limit_reached"}`, true},
		{"numeric code", `{"error":{"code":429}}`, false},
		{"unrelated code", `{"error":{"code":"invalid_request_error"}}`, false},
		{"no code at all", `{"error":{"message":"insufficient_quota"}}`, false},
		{"not json", `insufficient_quota`, false},
		{"empty", ``, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := LooksLikeCreditCode([]byte(tt.body)); got != tt.want {
				t.Errorf("LooksLikeCreditCode(%q) = %v, want %v", tt.body, got, tt.want)
			}
		})
	}
}

func TestCreditMessageOnlyProvider(t *testing.T) {
	tests := []struct {
		provider string
		want     bool
	}{
		{"commandcode", true},
		{"cmc", true},
		{"bai", true},
		{"b-ai", true},
		{"openai-compatible-node", true},
		{"anthropic-compatible-node", true},
		{"custom-embedding-node", true},
		{"deepseek", false},
		{"codebuddy-intl", false},
		{"openai-compatible", false}, // the bare node type is not a node id
		{"myopenai-compatible-node", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			if got := creditMessageOnlyProvider(tt.provider); got != tt.want {
				t.Errorf("creditMessageOnlyProvider(%q) = %v, want %v", tt.provider, got, tt.want)
			}
		})
	}
}
