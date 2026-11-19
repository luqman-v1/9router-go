package providers

import (
	"net/http"
	"testing"
)

// TestClassifyErrorNVIDIADegradedIsModelScoped pins the nvidia rule from
// upstream errorConfig (decolua@2275bc0d): a 400 "degraded" names an unhealthy
// cluster behind one model, so the verdict must fall back, cool that model for
// 15 minutes, and never be reported as an account-scoped lock — the API key
// still serves every sibling model.
func TestClassifyErrorNVIDIADegradedIsModelScoped(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		text     string
		provider string
	}{
		{"nvidia degraded 400", http.StatusBadRequest, "DEGRADED function cannot be invoked", "nvidia"},
		{"nvidia degraded lower case", http.StatusBadRequest, "the model cluster is degraded", "nvidia"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cls := ClassifyErrorForProvider(tc.status, tc.text, 0, tc.provider)
			if !cls.ShouldFallback {
				t.Fatalf("ShouldFallback = false, want true: %+v", cls)
			}
			if !cls.ModelScoped {
				t.Errorf("ModelScoped = false, want true (the key must stay unlocked)")
			}
			if cls.CooldownMs != 15*60*1000 {
				t.Errorf("CooldownMs = %d, want %d", cls.CooldownMs, 15*60*1000)
			}
			if cls.NewBackoffLevel != 0 {
				t.Errorf("NewBackoffLevel = %d, want 0 (a model fault is not a throttle)", cls.NewBackoffLevel)
			}
		})
	}
}

// TestClassifyErrorNVIDIADegradedDoesNotLeakToOtherProviders pins the rule's
// provider scope: "degraded" in another provider's prose must not pick up a
// model-scoped 15-minute cooldown, because that wording is not a verdict there.
func TestClassifyErrorNVIDIADegradedDoesNotLeakToOtherProviders(t *testing.T) {
	cls := ClassifyErrorForProvider(http.StatusBadRequest, "DEGRADED function cannot be invoked", 0, "openrouter")
	if cls.ModelScoped {
		t.Errorf("ModelScoped = true for openrouter, want false: %+v", cls)
	}
	if cls.CooldownMs == 15*60*1000 {
		t.Errorf("CooldownMs = %d for openrouter, want no nvidia cooldown", cls.CooldownMs)
	}
}

// TestClassifyErrorAccountScopedRulesStayAccountScoped guards the ModelScoped
// flag against a blanket default: every other rule must keep reporting an
// account-scoped verdict, or the failover loop would stop parking dead keys.
func TestClassifyErrorAccountScopedRulesStayAccountScoped(t *testing.T) {
	cases := []struct {
		name   string
		status int
		text   string
	}{
		{"quota", http.StatusBadRequest, "insufficient balance"},
		{"auth", http.StatusUnauthorized, "invalid api key"},
		{"throttle", http.StatusTooManyRequests, "rate limit exceeded"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if cls := ClassifyError(tc.status, tc.text, 0); cls.ModelScoped {
				t.Errorf("ModelScoped = true for %q, want false: %+v", tc.text, cls)
			}
		})
	}
}
