package usagetracker

import (
	"net/http"
	"testing"
)

func TestParseCodexUsageQuotas_Spark(t *testing.T) {
	payload := []byte(`{
		"plan_type": "Pro",
		"rate_limit": {
			"primary_window": {"remaining_fraction": 0.9, "reset_time": "2026-08-31T20:00:00Z"},
			"secondary_window": {"remaining_fraction": 0.95, "reset_time": "2026-09-07T00:00:00Z"}
		},
		"rate_limits_by_limit_id": {
			"gpt-5.3-codex-spark": {
				"primary_window": {"remaining_fraction": 0.4, "reset_time": "2026-08-31T20:00:00Z"},
				"secondary_window": {"remaining_fraction": 0.6, "reset_time": "2026-09-07T00:00:00Z"}
			}
		}
	}`)

	info, err := ParseCodexUsageQuotas(payload)
	if err != nil {
		t.Fatalf("ParseCodexUsageQuotas failed: %v", err)
	}

	if info.Plan != "Pro" {
		t.Errorf("expected plan Pro, got %s", info.Plan)
	}

	if sess, ok := info.Quotas["session"]; !ok || sess.RemainingPercentage != 90 {
		t.Errorf("expected session quota 90%%, got %v", sess)
	}

	if sparkSess, ok := info.Quotas["spark_session"]; !ok || sparkSess.RemainingPercentage != 40 {
		t.Errorf("expected spark_session quota 40%%, got %v", sparkSess)
	}

	if sparkWeekly, ok := info.Quotas["spark_weekly"]; !ok || sparkWeekly.RemainingPercentage != 60 {
		t.Errorf("expected spark_weekly quota 60%%, got %v", sparkWeekly)
	}
}

func TestParseGLMUsageQuotas_Credits(t *testing.T) {
	payload := []byte(`{
		"plan": "Developer",
		"limits": [
			{
				"type": "TOKENS_LIMIT",
				"unit": "5h",
				"remaining_fraction": 0.8
			},
			{
				"type": "CREDIT_LIMIT",
				"unit": "month",
				"total_credits": 1000,
				"used_credits": 250,
				"remaining_fraction": 0.75
			}
		]
	}`)

	info, err := ParseGLMUsageQuotas(payload)
	if err != nil {
		t.Fatalf("ParseGLMUsageQuotas failed: %v", err)
	}

	if sess, ok := info.Quotas["session"]; !ok || sess.RemainingPercentage != 80 {
		t.Errorf("expected session quota 80%%, got %v", sess)
	}

	if creds, ok := info.Quotas["credits"]; !ok || creds.LimitCount != 1000 || creds.UsedCount != 250 {
		t.Errorf("expected credits limit 1000, used 250, got %v", creds)
	}
}

func TestParseZedUsageQuotas(t *testing.T) {
	payload := []byte(`{
		"plan": "Zed Pro",
		"edit_predictions": {
			"used": 150,
			"limit": 500,
			"reset_at": "2026-09-01T00:00:00Z"
		},
		"hosted_model_requests": {
			"used": 42,
			"limit": null,
			"reset_at": "2026-09-01T00:00:00Z"
		}
	}`)

	info, err := ParseZedUsageQuotas(payload)
	if err != nil {
		t.Fatalf("ParseZedUsageQuotas failed: %v", err)
	}

	if info.Plan != "Zed Pro" {
		t.Errorf("expected plan 'Zed Pro', got %s", info.Plan)
	}

	if edit, ok := info.Quotas["edit_predictions"]; !ok || edit.UsedCount != 150 || edit.LimitCount != 500 {
		t.Errorf("expected edit predictions used=150, limit=500, got %v", edit)
	}

	if hosted, ok := info.Quotas["hosted_models"]; !ok || !hosted.IsUnlimited || hosted.UsedCount != 42 {
		t.Errorf("expected hosted models unlimited, used=42, got %v", hosted)
	}
}

func TestParseGroqQuotasFromHeaders(t *testing.T) {
	header := http.Header{}
	header.Set("x-ratelimit-limit-requests", "100")
	header.Set("x-ratelimit-remaining-requests", "90")
	header.Set("x-ratelimit-reset-requests", "2m59.56s")
	header.Set("x-ratelimit-limit-tokens", "10000")
	header.Set("x-ratelimit-remaining-tokens", "8000")
	header.Set("x-ratelimit-reset-tokens", "7.66s")

	info, err := ParseGroqQuotasFromHeaders(header)
	if err != nil {
		t.Fatalf("ParseGroqQuotasFromHeaders failed: %v", err)
	}
	if len(info.Quotas) != 2 {
		t.Fatalf("expected 2 quotas (requests+tokens), got %d", len(info.Quotas))
	}
	if req, ok := info.Quotas["requests"]; !ok || req.UsedCount != 10 || req.LimitCount != 100 {
		t.Errorf("expected requests used 10/100, got %v", req)
	}
	if req, ok := info.Quotas["requests"]; ok && req.RemainingPercentage != 90 {
		t.Errorf("expected 90%% remaining for requests, got %v", req.RemainingPercentage)
	}
	if tok, ok := info.Quotas["tokens"]; !ok || tok.UsedCount != 2000 || tok.LimitCount != 10000 {
		t.Errorf("expected tokens used 2000/10000, got %v", tok)
	}
	if tok, ok := info.Quotas["tokens"]; ok && tok.ResetAt.IsZero() {
		t.Error("expected tokens ResetAt to be set from duration")
	}
	// Missing headers should give empty quotas with message
	emptyHeader := http.Header{}
	info2, err := ParseGroqQuotasFromHeaders(emptyHeader)
	if err != nil {
		t.Fatalf("empty header should not error, got %v", err)
	}
	if len(info2.Quotas) != 0 {
		t.Errorf("expected 0 quotas for empty header, got %d", len(info2.Quotas))
	}
}
