package providers

import (
	"strings"
	"testing"
)

// free-pi distinguishes two 429s, and conflating them wastes the whole retry
// budget on a limit that cannot clear. The hourly cap resets within the hour;
// the daily allowance does not reset until 00:00, so retrying the same account
// only delays the error the client should already have.
//
// Provenance: hungkevin/free-pi-cli-proxy server.mjs:1055-1120 (isRetryableLimit
// / freepiFetchWithRetry), reproduced in testdata/freepi/error-429-taxonomy.json.
func TestClassifyError_FreePiQuotaTaxonomy(t *testing.T) {
	const hourly = "hourly request cap"
	const daily = "daily allowance spent"

	tests := []struct {
		name     string
		status   int
		body     string
		wantCool int
		// wantBackoff is the distinguishing property: a backoff rule re-arms a
		// growing cooldown and keeps the account in rotation, which is right for
		// an hourly throttle and wrong for an exhausted daily allowance.
		wantBackoff bool
		why         string
	}{
		{
			name: "hourly rate limit", status: 429,
			body:     `{"code":"rate_limit_exceeded","message":"Hourly request limit reached."}`,
			wantCool: cooldownShort, wantBackoff: false,
			why: hourly + ": clears at the hour boundary, so a short lock plus rotation is right",
		},
		{
			name: "daily credits exhausted", status: 429,
			body:     `{"code":"creditlimitexceeded","message":"Out of credits for today."}`,
			wantCool: cooldownLong, wantBackoff: false,
			why: daily + ": a fixed long cooldown, never a growing backoff",
		},
		{
			name: "insufficient quota", status: 429,
			body:     `{"error":{"code":"insufficient_quota","message":"You exceeded your current quota."}}`,
			wantCool: cooldownLong, wantBackoff: false,
			why: daily,
		},
		{
			name: "billing error", status: 429,
			body: `{"code":"billing_error"}`, wantCool: cooldownLong, wantBackoff: false,
			why: daily,
		},
		{
			name: "budget limit", status: 429,
			body: `{"code":"budgetlimiterror"}`, wantCool: cooldownLong, wantBackoff: false,
			why: daily,
		},
		{
			name: "usage limit", status: 429,
			body: `{"code":"gousagelimiterror"}`, wantCool: cooldownLong, wantBackoff: false,
			why: daily,
		},
		{
			// A backoff rule reports its cooldown through GetQuotaCooldown, so
			// the expected value is the first backoff step, not a fixed constant.
			name: "unclassified 429 keeps the generic backoff", status: 429,
			body:     `{"message":"slow down"}`,
			wantCool: GetQuotaCooldown(1), wantBackoff: true,
			why: "an unrecognised 429 is still a rate limit: bounded backoff is the safe default",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyError(tt.status, tt.body, 0)

			if !got.ShouldFallback {
				t.Fatalf("ShouldFallback = false; %s should move the request to another account", tt.name)
			}
			if got.CooldownMs != tt.wantCool {
				t.Errorf("CooldownMs = %d, want %d (%s)", got.CooldownMs, tt.wantCool, tt.why)
			}
			// A Backoff rule reports its cooldown through NewBackoffLevel rather
			// than CooldownMs, so the two are mutually exclusive by design.
			gotBackoff := got.NewBackoffLevel > 0
			if gotBackoff != tt.wantBackoff {
				t.Errorf("backoff = %v (level %d), want %v (%s)",
					gotBackoff, got.NewBackoffLevel, tt.wantBackoff, tt.why)
			}
		})
	}
}

// TestClassifyError_FreePiCodesDoNotLeakIntoOtherProviders guards the blast
// radius of the rules above. They are text matches in a shared table, so the
// thing worth proving is that a free-pi code cannot be produced by a provider
// that never emits one, and that an ordinary 429 elsewhere is unchanged.
func TestClassifyError_FreePiCodesDoNotLeakIntoOtherProviders(t *testing.T) {
	// A generic OpenAI-style 429 keeps exponential backoff, not a fixed cooldown.
	generic := ClassifyError(429, `{"error":{"message":"Rate limit reached for requests"}}`, 0)
	if generic.NewBackoffLevel == 0 {
		t.Error("a generic 429 lost its backoff classification")
	}
	if generic.CooldownMs == cooldownLong {
		t.Error("a generic 429 picked up free-pi's daily-quota cooldown")
	}

	// The free-pi codes are distinctive enough not to appear in unrelated prose.
	for _, code := range FreePiDailyQuotaCodes {
		if strings.Contains(strings.ToLower("rate limit reached"), code) {
			t.Errorf("code %q would match unrelated text", code)
		}
	}
}
