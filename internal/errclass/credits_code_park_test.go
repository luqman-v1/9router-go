package errclass

import (
	"encoding/json"
	"testing"
)

// creditErrorCodes is the vocabulary isCreditErrorCode admits; keep this list
// identical to the switch there.
var creditErrorCodes = []string{
	"insufficient_quota",
	"insufficient_user_quota",
	"insufficient_balance",
	"billing_hard_limit_reached",
	"credit_balance_too_low",
	"credit_balance_exhausted",
}

// creditCodeBodies is one envelope per code whose prose carries no credit
// phrase — a localized or terse message the phrase table cannot read. That is
// the case the maintainer's 400 probe failed on: the code was admitted by
// isCreditErrorCode, yet creditsExhaustedForStatus still required a prose hit
// as well, so a 400 with an untranslatable message stayed request-scoped.
var creditCodeBodies = map[string]string{
	"insufficient_quota":         `{"error":{"code":"insufficient_quota","message":"リクエストの上限に達しました"}}`,
	"insufficient_user_quota":    `{"error":{"code":"insufficient_user_quota","message":"ご利用いただけるクレジットがありません"}}`,
	"insufficient_balance":       `{"error":{"code":"insufficient_balance","message":"saldo tidak mencukupi"}}`,
	"billing_hard_limit_reached": `{"error":{"code":"billing_hard_limit_reached","message":"La quota è terminata"}}`,
	"credit_balance_too_low":     `{"error":{"code":"credit_balance_too_low","message":"Kein Guthaben mehr"}}`,
	"credit_balance_exhausted":   `{"error":{"code":"credit_balance_exhausted","message":"credito esaurito"}}`,
}

// A 400 carrying one of the six credit codes is a spent balance on its own
// words-free merit: the code is the upstream's verdict, not the caller's echo.
func TestCreditsExhaustedForStatus_CreditCodeNeedsNoProse(t *testing.T) {
	for _, code := range creditErrorCodes {
		body := creditCodeBodies[code]
		if !LooksLikeCreditErrorCode([]byte(body)) {
			t.Fatalf("LooksLikeCreditErrorCode = false for %s (body %s)", code, body)
		}
		if LooksLikeCreditsExhausted(creditCodeMessage(body)) {
			t.Fatalf("phrase table already matches %s prose; body no longer isolates the code", code)
		}
		if !creditsExhaustedForStatus(400, creditCodeMessage(body), []byte(body)) {
			t.Errorf("%s: a 400 carrying the code did not read as exhausted", code)
		}
	}
	// The gate itself still holds: prose alone at a 400 is request text.
	if creditsExhaustedForStatus(400, "insufficient_quota", nil) {
		t.Error("a 400 with prose and no structured code read as exhausted")
	}
}

// creditCodeMessage extracts error.message from an envelope, the way a caller
// hands prose to the classifier separately from the raw body.
func creditCodeMessage(body string) string {
	var env struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(body), &env) == nil && env.Error.Message != "" {
		return env.Error.Message
	}
	return body
}
