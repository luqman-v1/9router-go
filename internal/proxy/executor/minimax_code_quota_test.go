package executor

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	json "encoding/json/v2"

	"9router/proxy/internal/proxy"
)

// TestNormalizeQuotaResponse is the status/body matrix the account loop turns
// on. A credits refusal has to become the 429 that moves combo fallback to the
// next account; a real auth refusal has to keep its own status so the on-401
// refresh path still sees it.
func TestNormalizeQuotaResponse(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantStatus int
		// wantKept reports that the upstream body must survive verbatim: a
		// refusal that is not about the balance is the client's answer, not a
		// rate limit the router invented.
		wantKept bool
	}{
		{
			name:       "402 naming the balance becomes 429",
			status:     http.StatusPaymentRequired,
			body:       `{"error":{"message":"积分不足，请充值或签到"}}`,
			wantStatus: http.StatusTooManyRequests,
		},
		{
			name:       "403 naming the balance becomes 429",
			status:     http.StatusForbidden,
			body:       `{"message":"insufficient balance"}`,
			wantStatus: http.StatusTooManyRequests,
		},
		{
			name:       "raw Chinese body is matched",
			status:     http.StatusPaymentRequired,
			body:       `额度已用完`,
			wantStatus: http.StatusTooManyRequests,
		},
		{
			name:       "base_resp envelope is unwrapped",
			status:     http.StatusForbidden,
			body:       `{"base_resp":{"status_msg":"credit exhausted"}}`,
			wantStatus: http.StatusTooManyRequests,
		},
		{
			name:       "429 is already the right signal",
			status:     http.StatusTooManyRequests,
			body:       `{"error":{"message":"slow down"}}`,
			wantStatus: http.StatusTooManyRequests,
			wantKept:   true,
		},
		{
			name:       "403 auth refusal keeps its status",
			status:     http.StatusForbidden,
			body:       `{"error":{"message":"invalid token"}}`,
			wantStatus: http.StatusForbidden,
			wantKept:   true,
		},
		{
			name:       "402 with no balance wording keeps its status",
			status:     http.StatusPaymentRequired,
			body:       `{"error":{"message":"model not found"}}`,
			wantStatus: http.StatusPaymentRequired,
			wantKept:   true,
		},
		{
			name:       "status outside the refusal set is untouched",
			status:     http.StatusInternalServerError,
			body:       `{"error":{"message":"insufficient balance"}}`,
			wantStatus: http.StatusInternalServerError,
			wantKept:   true,
		},
		{
			name:       "a success is never rewritten",
			status:     http.StatusOK,
			body:       `{"content":[{"type":"text","text":"your balance looks fine"}]}`,
			wantStatus: http.StatusOK,
			wantKept:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotStatus, gotBody := NormalizeQuotaResponse(tt.status, []byte(tt.body))
			if gotStatus != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", gotStatus, tt.wantStatus, gotBody)
			}
			if tt.wantKept {
				if string(gotBody) != tt.body {
					t.Errorf("body = %s, want the upstream body unchanged", gotBody)
				}
				return
			}
			assertRateLimitEnvelope(t, gotBody, tt.body)
		})
	}
}

// assertRateLimitEnvelope checks the normalized body is the rate_limit_error
// shape the account loop and the client both read, with the upstream message
// still inside it.
func assertRateLimitEnvelope(t *testing.T, body []byte, upstream string) {
	t.Helper()
	var env struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("normalized body is not JSON: %v (%s)", err, body)
	}
	if env.Error.Type != "rate_limit_error" {
		t.Errorf("error.type = %q, want rate_limit_error", env.Error.Type)
	}
	if !strings.Contains(env.Error.Message, "usage limit reached") {
		t.Errorf("error.message = %q, want the usage-limit prefix", env.Error.Message)
	}
	// The upstream message has to survive: it is the only thing telling the
	// operator the account is out of credits rather than merely throttled.
	if msg := quotaMessage([]byte(upstream)); msg != "" && !strings.Contains(env.Error.Message, msg) {
		t.Errorf("error.message = %q, want it to carry %q", env.Error.Message, msg)
	}
}

// The normalized refusal must leave as a typed upstream error carrying the 429,
// or the fallback loop would report a served turn and stop failing over.
func TestNormalizeQuotaResponse_ProducesFallbackError(t *testing.T) {
	status, normalized := NormalizeQuotaResponse(http.StatusForbidden, []byte(`{"error":{"message":"积分不足"}}`))
	if status != http.StatusTooManyRequests {
		t.Fatalf("normalized status = %d, want 429", status)
	}
	err := &proxy.UpstreamError{StatusCode: status, Body: normalized}
	var ue *proxy.UpstreamError
	if !errors.As(error(err), &ue) {
		t.Fatalf("error is not a *proxy.UpstreamError: %v", err)
	}
	if ue.StatusCode != http.StatusTooManyRequests {
		t.Errorf("StatusCode = %d, want 429", ue.StatusCode)
	}
}
