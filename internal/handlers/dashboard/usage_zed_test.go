package dashboard

import (
	"strings"
	"testing"
)

func TestParseZedUsage(t *testing.T) {
	raw := []byte(`{"plan":{"plan_v3":"zed_pro","usage":{
		"edit_predictions":{"used":150,"limit":500},
		"model_requests":{"used":7,"limit":0}
	},"subscription_period":{"ended_at":"2026-12-01T00:00:00Z"},"trial_started_at":"2026-11-01T00:00:00Z"}}`)
	res := parseZedUsage(raw)
	if res.plan != "Zed Pro (Trial active)" {
		t.Errorf("plan = %q", res.plan)
	}
	edit, ok := res.quotas["Edit Predictions"].(map[string]any)
	if !ok || edit["remainingPercentage"] != 70.0 {
		t.Errorf("Edit Predictions quota = %v", res.quotas)
	}
	if _, ok := res.quotas["Hosted Model Requests"]; ok {
		t.Error("model_requests with limit 0 must not create a request quota")
	}
	if !strings.Contains(res.message, "billed per token") {
		t.Errorf("message = %q", res.message)
	}
}

func TestParseZedUsageUnlimitedAndOverdue(t *testing.T) {
	raw := []byte(`{"plan":{"plan_v3":"zed_business","has_overdue_invoices":true,"usage":{
		"edit_predictions":{"used":10,"limit":"unlimited"},
		"model_requests":{"used":4,"limit":{"limited":100}}
	}}}`)
	res := parseZedUsage(raw)
	edit, _ := res.quotas["Edit Predictions"].(map[string]any)
	if edit["unlimited"] != true || edit["remainingPercentage"] != 100.0 {
		t.Errorf("unlimited edit quota = %v", edit)
	}
	hosted, _ := res.quotas["Hosted Model Requests"].(map[string]any)
	if hosted["remainingPercentage"] != 96.0 {
		t.Errorf("hosted quota = %v", hosted)
	}
	if !strings.Contains(res.message, "overdue invoices") {
		t.Errorf("message = %q", res.message)
	}
}
