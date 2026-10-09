package chat

import (
	"testing"
	"time"
)

func TestExtractResetDuration_GoogleRPCDetails(t *testing.T) {
	body := []byte(`{
  "error": {
    "code": 429,
    "message": "Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 1h12m28s.",
    "status": "RESOURCE_EXHAUSTED",
    "details": [
      {
        "@type": "type.googleapis.com/google.rpc.ErrorInfo",
        "reason": "QUOTA_EXHAUSTED",
        "domain": "cloudcode-pa.googleapis.com",
        "metadata": {
          "uiMessage": "true",
          "model": "gemini-3.8-flash-tiered",
          "quotaResetDelay": "1h12m28.109534319s"
        }
      }
    ]
  }
}`)

	dur, ok := extractResetDuration(body)
	if !ok {
		t.Fatalf("expected extractResetDuration to succeed")
	}

	expectedMin := 1*time.Hour + 12*time.Minute + 28*time.Second
	expectedMax := 1*time.Hour + 12*time.Minute + 29*time.Second
	if dur < expectedMin || dur > expectedMax {
		t.Errorf("expected duration around 1h12m28s, got %v", dur)
	}

	retryAfter := extractRetryAfter(body)
	if retryAfter == "" {
		t.Errorf("expected non-empty retryAfter from Google RPC body")
	}
}

func TestExtractResetDuration_TextMessage(t *testing.T) {
	body := []byte(`{"error":{"message":"Rate limit exceeded. Resets in 45m30s. Try again later."}}`)
	dur, ok := extractResetDuration(body)
	if !ok {
		t.Fatalf("expected extractResetDuration to succeed on message text")
	}
	expected := 45*time.Minute + 30*time.Second
	if dur != expected {
		t.Errorf("expected 45m30s, got %v", dur)
	}
}

func TestExtractResetDuration_ClampingSafety(t *testing.T) {
	// Case 1: Extremely long duration (> 2 hours) is clamped to maxResetCooldown (2 hours) to avoid deadlocks.
	bodyLong := []byte(`{"error":{"message":"Resets in 720h."}}`)
	durLong, ok := extractResetDuration(bodyLong)
	if !ok {
		t.Fatalf("expected ok for long duration")
	}
	if durLong != maxResetCooldown {
		t.Errorf("expected clamped to %v, got %v", maxResetCooldown, durLong)
	}

	// Case 2: Sub-5s duration is clamped to minResetCooldown (5s)
	bodyShort := []byte(`{"error":{"message":"Resets in 1s."}}`)
	durShort, ok := extractResetDuration(bodyShort)
	if !ok {
		t.Fatalf("expected ok for short duration")
	}
	if durShort != minResetCooldown {
		t.Errorf("expected clamped to %v, got %v", minResetCooldown, durShort)
	}
}

func TestBlockAntigravityModelUntil_Safety(t *testing.T) {
	ClearAntigravityQuotaCache()
	connID := "test-conn-123"
	// The 3.7 family shares one backend id across its tiers, so a block on one
	// tier must block the whole family.
	model := "gemini-3.7-flash-low"

	// Initially not blocked
	if IsAntigravityModelBlocked(connID, model) {
		t.Errorf("expected not blocked initially")
	}

	// Block for 1 hour
	resetAt := time.Now().UTC().Add(1 * time.Hour)
	BlockAntigravityModelUntil(connID, model, resetAt)

	// Model should now be blocked
	if !IsAntigravityModelBlocked(connID, model) {
		t.Errorf("expected model %s to be blocked", model)
	}

	// Its canonical backend must also be blocked
	if !IsAntigravityModelBlocked(connID, "gemini-3.7-flash-tiered") {
		t.Errorf("expected canonical model gemini-3.7-flash-tiered to also be blocked")
	}

	// An expired resetAt should not block
	pastResetAt := time.Now().UTC().Add(-1 * time.Minute)
	BlockAntigravityModelUntil(connID, "gemini-3.7-flash-high", pastResetAt)
	if IsAntigravityModelBlocked(connID, "gemini-3.7-flash-high") {
		t.Errorf("expected expired resetAt not to block")
	}
}
