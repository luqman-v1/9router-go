//go:build integration

package integration

import (
	"net/http"
	"testing"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
)

// TestPeriodBudgetRefusesExhaustedKey drives the real router: a key with a
// per-period budget whose measured spend already exceeds the ceiling must be
// answered 402 before any upstream call is made.
//
// This is the integration half of the budget engine. The unit tests prove the
// thresholds; this proves the wiring — that the limiter reads the budget off
// the resolved policy, that the reader measures the ledger this same harness
// writes, and that a request refused here never reaches the upstream.
func TestPeriodBudgetRefusesExhaustedKey(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, chatCompletionResponder())
	env.AddConnection(t, "conn-budget", "deepseek", "DeepSeek Budget", upstream, "sk-upstream")

	if err := env.Repo.CreateApiKey("budget-key", "sk-budget-integration-0001", "Budget Key", ""); err != nil {
		t.Fatalf("seed api key: %v", err)
	}
	// A $1 monthly ceiling with a hard cutoff, and one turn already recorded
	// against it that costs $2 — so the key is over before this request.
	if err := env.Repo.SetApiKeyPeriodBudget("budget-key", 1_000_000, 0, "monthly", 0, true); err != nil {
		t.Fatalf("SetApiKeyPeriodBudget: %v", err)
	}
	if err := env.Repo.InsertUsageHistory("deepseek", "deepseek-chat", "conn-budget",
		"sk-upstream", "/v1/chat/completions", 100000, 50000, 2.0, "success", 150000, "{}", "{}",
		handlerutil.MaskAPIKey("sk-budget-integration-0001")); err != nil {
		t.Fatalf("seed usage history: %v", err)
	}

	res := env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-chat", false), WithAPIKey("sk-budget-integration-0001"))
	if res.Status != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402 (body: %s)", res.Status, truncate(res.Body))
	}
	if got := res.Header.Get("X-Budget-Limit"); got != "1.00" {
		t.Errorf("X-Budget-Limit = %q, want 1.00", got)
	}
	if got := res.Header.Get("X-Budget-Spent"); got != "2.00" {
		t.Errorf("X-Budget-Spent = %q, want 2.00", got)
	}
	// The refusal must not have consumed an upstream turn.
	if hits := upstream.Count(); hits != 0 {
		t.Errorf("upstream received %d requests, want 0 for a refused key", hits)
	}
}

// TestPeriodBudgetAllowsUnderCeiling is the other direction: the same key with
// its spend inside the ceiling is served, and the turn it spends is written to
// the ledger the budget is measured from. A gate that refused everything would
// pass the test above and fail every real request.
func TestPeriodBudgetAllowsUnderCeiling(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, chatCompletionResponder())
	env.AddConnection(t, "conn-budget-ok", "deepseek", "DeepSeek Budget OK", upstream, "sk-upstream")

	if err := env.Repo.CreateApiKey("budget-ok", "sk-budget-ok-integration001", "Budget OK", ""); err != nil {
		t.Fatalf("seed api key: %v", err)
	}
	if err := env.Repo.SetApiKeyPeriodBudget("budget-ok", 10_000_000, 0, "monthly", 0, true); err != nil {
		t.Fatalf("SetApiKeyPeriodBudget: %v", err)
	}
	// One cheap turn already on the ledger: $0.50 of a $10 ceiling.
	if err := env.Repo.InsertUsageHistory("deepseek", "deepseek-chat", "conn-budget-ok",
		"sk-upstream", "/v1/chat/completions", 1000, 500, 0.50, "success", 1500, "{}", "{}",
		handlerutil.MaskAPIKey("sk-budget-ok-integration001")); err != nil {
		t.Fatalf("seed usage history: %v", err)
	}

	res := env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-chat", false), WithAPIKey("sk-budget-ok-integration001"))
	if res.Status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	if hits := upstream.Count(); hits != 1 {
		t.Errorf("upstream received %d requests, want 1", hits)
	}
}

// TestPeriodBudgetAlertHeaderOnSoftCeiling proves a key over its ceiling with
// the hard cutoff off is served with the warning header rather than refused —
// the operator asked to be told, not cut off mid-session.
func TestPeriodBudgetAlertHeaderOnSoftCeiling(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, chatCompletionResponder())
	env.AddConnection(t, "conn-budget-soft", "deepseek", "DeepSeek Budget Soft", upstream, "sk-upstream")

	if err := env.Repo.CreateApiKey("budget-soft", "sk-budget-soft-integration", "Budget Soft", ""); err != nil {
		t.Fatalf("seed api key: %v", err)
	}
	// $1 ceiling, hard cutoff OFF, alerting at 80%.
	if err := env.Repo.SetApiKeyPeriodBudget("budget-soft", 1_000_000, 0, "monthly", 80, false); err != nil {
		t.Fatalf("SetApiKeyPeriodBudget: %v", err)
	}
	// $0.90 already spent: over the alert threshold, under nothing enforced.
	if err := env.Repo.InsertUsageHistory("deepseek", "deepseek-chat", "conn-budget-soft",
		"sk-upstream", "/v1/chat/completions", 1000, 500, 0.90, "success", 1500, "{}", "{}",
		handlerutil.MaskAPIKey("sk-budget-soft-integration")); err != nil {
		t.Fatalf("seed usage history: %v", err)
	}

	res := env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-chat", false), WithAPIKey("sk-budget-soft-integration"))
	if res.Status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (soft ceiling alerts, it does not refuse)", res.Status)
	}
	if got := res.Header.Get("X-Budget-Status"); got != "approaching" {
		t.Errorf("X-Budget-Status = %q, want approaching", got)
	}
}

// TestPeriodBudgetWindowFloor proves the period is honoured end to end: a
// monthly budget must not count spend from a previous month, so a key whose
// only spend is three months old is served rather than refused.
func TestPeriodBudgetWindowFloor(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, chatCompletionResponder())
	env.AddConnection(t, "conn-budget-win", "deepseek", "DeepSeek Budget Window", upstream, "sk-upstream")

	if err := env.Repo.CreateApiKey("budget-win", "sk-budget-window-integration", "Budget Window", ""); err != nil {
		t.Fatalf("seed api key: %v", err)
	}
	if err := env.Repo.SetApiKeyPeriodBudget("budget-win", 1_000_000, 0, "monthly", 0, true); err != nil {
		t.Fatalf("SetApiKeyPeriodBudget: %v", err)
	}
	// A turn from three months ago that would exhaust the ceiling if the
	// window were ignored. It is written directly because InsertUsageHistory
	// stamps "now".
	old := time.Now().UTC().AddDate(0, -3, 0).Format("2006-01-02T15:04:05.000Z")
	masked := handlerutil.MaskAPIKey("sk-budget-window-integration")
	if _, err := env.Repo.RawDB().Exec(
		`INSERT INTO usageHistory (timestamp, provider, model, connectionId, apiKey, clientKey, endpoint, promptTokens, completionTokens, cost, status, tokens, meta)
		 VALUES (?, 'deepseek', 'deepseek-chat', 'conn-budget-win', ?, ?, '/v1/chat/completions', 100000, 50000, 9.0, 'success', '{}', '{}')`,
		old, masked, masked,
	); err != nil {
		t.Fatalf("seed old usage history: %v", err)
	}

	res := env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-chat", false), WithAPIKey("sk-budget-window-integration"))
	if res.Status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (spend outside the monthly window must not count)", res.Status)
	}
}

// TestPeriodBudgetPlanAppliesToKey proves a plan's budget governs a key that
// sets no columns of its own, which is the whole point of a reusable template.
func TestPeriodBudgetPlanAppliesToKey(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, chatCompletionResponder())
	env.AddConnection(t, "conn-budget-plan", "deepseek", "DeepSeek Budget Plan", upstream, "sk-upstream")

	if err := env.Repo.CreatePlan(db.Plan{
		ID:          "plan-budget",
		Name:        "Budget plan",
		LimitMicros: 1_000_000,
		Period:      "monthly",
		HardCutoff:  true,
	}); err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	if err := env.Repo.CreateApiKey("budget-plan", "sk-budget-plan-integration01", "Budget Plan", ""); err != nil {
		t.Fatalf("seed api key: %v", err)
	}
	if err := env.Repo.SetApiKeyPlan("budget-plan", "plan-budget"); err != nil {
		t.Fatalf("SetApiKeyPlan: %v", err)
	}
	// $2 already spent against the plan's $1 ceiling.
	if err := env.Repo.InsertUsageHistory("deepseek", "deepseek-chat", "conn-budget-plan",
		"sk-upstream", "/v1/chat/completions", 100000, 50000, 2.0, "success", 150000, "{}", "{}",
		handlerutil.MaskAPIKey("sk-budget-plan-integration01")); err != nil {
		t.Fatalf("seed usage history: %v", err)
	}

	res := env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-chat", false), WithAPIKey("sk-budget-plan-integration01"))
	if res.Status != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402 (the plan's ceiling governs)", res.Status)
	}
}
