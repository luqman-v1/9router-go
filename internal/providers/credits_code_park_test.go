package providers

import (
	"net/http"
	"testing"
)

// Six structured credit codes, one localized message each, every one of them
// carrying no phrase the prose table can read. The maintainer's probe on the PR
// head found all six but "insufficient_quota" request-scoped at a 400: the code
// admitted the reading (errclass.LooksLikeCreditErrorCode) but the rule loop
// still needed a prose hit, so a 400 from openai/azure/new-api with a terse or
// localized message kept the connection in rotation for a guaranteed-failing
// account (issue #20567). A credit code is the upstream's own verdict, so it
// earns the quota cooldown on its own.
func TestClassifyError_400CreditCodesParkOnAnyMessage(t *testing.T) {
	tests := []struct {
		code string
		body string
	}{
		{"insufficient_quota", `{"error":{"code":"insufficient_quota","message":"リクエストの上限に達しました"}}`},
		{"insufficient_user_quota", `{"error":{"code":"insufficient_user_quota","message":"ご利用いただけるクレジットがありません"}}`},
		{"insufficient_balance", `{"error":{"code":"insufficient_balance","message":"saldo tidak mencukupi"}}`},
		{"billing_hard_limit_reached", `{"error":{"code":"billing_hard_limit_reached","message":"La quota è terminata"}}`},
		{"credit_balance_too_low", `{"error":{"code":"credit_balance_too_low","message":"Kein Guthaben mehr"}}`},
		{"credit_balance_exhausted", `{"error":{"code":"credit_balance_exhausted","message":"credito esaurito"}}`},
	}
	// errorText is what the request path extracts from that body
	// (chat.extractErrorText): the provider's message with the structured code
	// appended. Passed with no body too, which is what a caller holding only the
	// extracted text can do.
	for _, tt := range tests {
		for _, tc := range []struct {
			name      string
			errorText string
			body      []byte
		}{
			{"extracted text and body", tt.code, []byte(tt.body)},
			{"body only", "", []byte(tt.body)},
		} {
			t.Run(tt.code+"/"+tc.name, func(t *testing.T) {
				cls := ClassifyErrorForProvider(http.StatusBadRequest, tc.errorText, 0, "openai", tc.body)
				if !cls.ShouldFallback {
					t.Fatalf("ShouldFallback = false, want true (cls = %+v)", cls)
				}
				if cls.CooldownMs != QuotaExhaustedCooldownMs {
					t.Errorf("CooldownMs = %d, want %d", cls.CooldownMs, QuotaExhaustedCooldownMs)
				}
				if cls.NewBackoffLevel != 0 {
					t.Errorf("NewBackoffLevel = %d, want the level untouched", cls.NewBackoffLevel)
				}
			})
		}
	}
}

// A code at a 400 is the verdict; the same phrase in prose is the caller's echo.
// Both must keep working after the code path above.
func TestClassifyError_400ProseAloneIsNotACreditVerdict(t *testing.T) {
	cls := ClassifyErrorForProvider(http.StatusBadRequest,
		"Unknown parameter: 'insufficient_quota'", 0, "openai",
		[]byte(`{"error":{"message":"Unknown parameter: 'insufficient_quota'","type":"invalid_request_error"}}`))
	if cls.ShouldFallback {
		t.Fatalf("a 400 echoing a credit phrase parked the connection: %+v", cls)
	}
	if cls.CooldownMs != 0 {
		t.Errorf("CooldownMs = %d, want 0", cls.CooldownMs)
	}
}
