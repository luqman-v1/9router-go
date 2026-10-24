package providers

import (
	"net/http"
	"testing"
)

func TestClassifyError_CapacityExhausted(t *testing.T) {
	// Status 503 with MODEL_CAPACITY_EXHAUSTED
	cls := ClassifyError(http.StatusServiceUnavailable, "No capacity available for model gemini-3.8-flash-tiered on the server MODEL_CAPACITY_EXHAUSTED", 0)
	if !cls.ShouldFallback {
		t.Error("expected fallback for capacity exhausted error")
	}
	if cls.NewBackoffLevel != 1 {
		t.Errorf("expected backoff level 1, got %d", cls.NewBackoffLevel)
	}
	if cls.CooldownMs != 2000 {
		t.Errorf("expected 2000ms cooldown, got %d", cls.CooldownMs)
	}
}

func TestClassifyError_ResourceExhausted429(t *testing.T) {
	cls := ClassifyError(http.StatusTooManyRequests, "Resource has been exhausted (e.g. check quota). RESOURCE_EXHAUSTED", 0)
	if !cls.ShouldFallback {
		t.Error("expected fallback for 429 resource exhausted")
	}
	if cls.NewBackoffLevel != 1 {
		t.Errorf("expected backoff level 1, got %d", cls.NewBackoffLevel)
	}
}

func TestClassifyError_CreditExhaustionBacksOff(t *testing.T) {
	// Out-of-credits 400s are account-scoped: fall back and cool the account
	// down with an incremented backoff level instead of leaving it healthy
	// (upstream b82f5c4a).
	for _, text := range []string{"Insufficient credits to complete this request", "Your credit balance is too low to access the API"} {
		cls := ClassifyError(http.StatusBadRequest, text, 0)
		if !cls.ShouldFallback {
			t.Errorf("expected fallback for %q, got %+v", text, cls)
		}
		if cls.NewBackoffLevel != 1 {
			t.Errorf("expected backoff level 1 for %q, got %d", text, cls.NewBackoffLevel)
		}
		if cls.CooldownMs != BackoffConfig.BaseMs {
			t.Errorf("expected %dms cooldown for %q, got %d", BackoffConfig.BaseMs, text, cls.CooldownMs)
		}
	}
	// A 400 that carries no account fault still says nothing about the credential.
	cls := ClassifyError(http.StatusBadRequest, "invalid request body", 0)
	if cls.ShouldFallback {
		t.Errorf("expected no fallback for a plain 400, got %+v", cls)
	}
}

func TestClassifyError_Status503Fallback(t *testing.T) {
	// Plain 503 without specific matching text should trigger backoff via status rule
	cls := ClassifyError(http.StatusServiceUnavailable, "", 0)
	if !cls.ShouldFallback {
		t.Error("expected fallback for 503")
	}
	if cls.NewBackoffLevel != 1 {
		t.Errorf("expected backoff level 1 for 503, got %d", cls.NewBackoffLevel)
	}
}

func TestClassifyError_RequestScoped4xxNoFallback(t *testing.T) {
	// Upstream parity (checkFallbackError): request-scoped 4xx must not lock the account.
	for _, status := range []int{http.StatusBadRequest, http.StatusMethodNotAllowed, http.StatusConflict, http.StatusUnprocessableEntity} {
		cls := ClassifyError(status, "bad request body", 0)
		if cls.ShouldFallback {
			t.Errorf("expected no fallback for %d, got %+v", status, cls)
		}
	}
	// Account-scoped statuses keep fallback.
	for _, status := range []int{http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden, http.StatusTooManyRequests} {
		cls := ClassifyError(status, "", 0)
		if !cls.ShouldFallback {
			t.Errorf("expected fallback for %d, got %+v", status, cls)
		}
	}
	// 404 keeps its rule (upstream NON_RETRIABLE + status rule).
	cls := ClassifyError(http.StatusNotFound, "", 0)
	if !cls.ShouldFallback {
		t.Errorf("expected fallback for 404, got %+v", cls)
	}
}
