package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"9router/proxy/internal/budget"
	"9router/proxy/internal/db"
	"9router/proxy/internal/models"
)

// stubBudget is a budget.Reader with a fixed reading.
type stubBudget struct {
	spend budget.Spend
	err   error
	calls int
}

func (s *stubBudget) SpendForKeySince(_ context.Context, _, _ string) (budget.Spend, error) {
	s.calls++
	return s.spend, s.err
}

// budgetKey builds a key carrying the budget columns directly, which is the
// shape limitsFor reads when no plan is assigned.
func budgetKey(micros int64, tokens int64, period string, alertPct int, hardCutoff bool) *models.APIKey {
	return &models.APIKey{
		ID:           "budget",
		Key:          "sk-budget-abcdefghijklmnop",
		LimitMicros:  micros,
		LimitTokens:  tokens,
		BudgetPeriod: period,
		AlertPct:     alertPct,
		HardCutoff:   hardCutoff,
	}
}

func budgetRequest(key *models.APIKey) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	return req.WithContext(withAPIKey(req, key))
}

// stubPlan is a policy.PlanSource answering one fixed plan.
type stubPlan struct {
	plan db.Plan
}

func (s stubPlan) GetPlan(id string) (db.Plan, error) {
	if s.plan.ID == id {
		return s.plan, nil
	}
	return db.Plan{}, nil
}

// planWithBudget builds a plan carrying only a budget, which is all the
// precedence tests need.
func planWithBudget(micros int64, period string) db.Plan {
	return db.Plan{
		ID:          "p1",
		Name:        "Budget plan",
		LimitMicros: micros,
		Period:      period,
		HardCutoff:  true,
	}
}

// TestBudgetRefusesExhaustedKey proves the per-period ceiling answers 402 once
// the measured spend reaches it, and that the response names the axis and the
// period reset so a client can back off for the right length of time.
func TestBudgetRefusesExhaustedKey(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	rl.SetBudgetReader(&stubBudget{spend: budget.Spend{Micros: 2_000_000, Tokens: 10}})
	handler := RequireRateLimit(rl, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	key := budgetKey(1_000_000, 0, budget.PeriodMonthly, 0, true)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, budgetRequest(key))
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402", rec.Code)
	}
	if got := rec.Header().Get("X-Budget-Limit"); got != "1.00" {
		t.Errorf("X-Budget-Limit = %q, want 1.00", got)
	}
	if got := rec.Header().Get("X-Budget-Spent"); got != "2.00" {
		t.Errorf("X-Budget-Spent = %q, want 2.00", got)
	}
	// A monthly window has a reset, so Retry-After must name it.
	if rec.Header().Get("Retry-After") == "" {
		t.Error("Retry-After missing; a monthly budget resets")
	}
}

// TestBudgetTokenAxisRefusal proves the token ceiling refuses on its own axis
// when no cost ceiling is configured, with the token counts in the headers.
func TestBudgetTokenAxisRefusal(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	rl.SetBudgetReader(&stubBudget{spend: budget.Spend{Micros: 0, Tokens: 5000}})
	handler := RequireRateLimit(rl, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	key := budgetKey(0, 1000, budget.PeriodDaily, 0, true)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, budgetRequest(key))
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402", rec.Code)
	}
	if got := rec.Header().Get("X-Budget-Limit"); got != "1000" {
		t.Errorf("X-Budget-Limit = %q, want 1000", got)
	}
	if got := rec.Header().Get("X-Budget-Spent"); got != "5000" {
		t.Errorf("X-Budget-Spent = %q, want 5000", got)
	}
}

// TestBudgetSoftCeilingAlertsButServes proves a key over its ceiling with
// hardCutoff off is served with a warning header rather than refused: the
// operator asked to be told, not to be cut off.
func TestBudgetSoftCeilingAlertsButServes(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	reader := &stubBudget{spend: budget.Spend{Micros: 1_500_000, Tokens: 10}}
	rl.SetBudgetReader(reader)
	handler := RequireRateLimit(rl, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	key := budgetKey(1_000_000, 0, budget.PeriodMonthly, 0, false)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, budgetRequest(key))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (soft ceiling alerts, it does not refuse)", rec.Code)
	}
	if got := rec.Header().Get("X-Budget-Status"); got != "approaching" {
		t.Errorf("X-Budget-Status = %q, want approaching", got)
	}
	if reader.calls != 1 {
		t.Errorf("reader called %d times, want 1", reader.calls)
	}
}

// TestBudgetAlertThresholdHeader proves the alert percentage raises the same
// warning header below the ceiling, which is the whole point of the threshold.
func TestBudgetAlertThresholdHeader(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	rl.SetBudgetReader(&stubBudget{spend: budget.Spend{Micros: 850_000, Tokens: 0}})
	handler := RequireRateLimit(rl, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	// 85% of a $1 ceiling, alerting at 80%.
	key := budgetKey(1_000_000, 0, budget.PeriodMonthly, 80, true)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, budgetRequest(key))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (under the ceiling)", rec.Code)
	}
	if got := rec.Header().Get("X-Budget-Status"); got != "approaching" {
		t.Errorf("X-Budget-Status = %q, want approaching", got)
	}
}

// TestBudgetUnarmedKeyCostsNoRead proves an install that has configured no
// budget pays nothing per request: the reader is never called, so the ledger
// is not touched on the hot path for the overwhelmingly common case.
func TestBudgetUnarmedKeyCostsNoRead(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	reader := &stubBudget{spend: budget.Spend{Micros: 9_999_999}}
	rl.SetBudgetReader(reader)
	handler := RequireRateLimit(rl, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	// No budget columns set at all.
	key := &models.APIKey{ID: "plain", Key: "sk-plain-abcdefghijklmnop"}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, budgetRequest(key))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if reader.calls != 0 {
		t.Errorf("reader called %d times, want 0 for an unarmed key", reader.calls)
	}
	if rec.Header().Get("X-Budget-Status") != "" {
		t.Error("X-Budget-Status set for a key with no budget")
	}
}

// TestBudgetReadFailureFailsOpen proves a storage blip does not lock a key out
// of a service it has always been able to call. The ceiling is a spend cap,
// not an access grant.
func TestBudgetReadFailureFailsOpen(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	rl.SetBudgetReader(&stubBudget{err: errors.New("db down")})
	handler := RequireRateLimit(rl, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	key := budgetKey(1_000_000, 0, budget.PeriodMonthly, 0, true)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, budgetRequest(key))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (fail open)", rec.Code)
	}
}

// TestBudgetNoReaderWiredFailsOpen proves a limiter with no budget reader
// wired — every setup that never called SetBudgetReader — serves traffic
// normally rather than dereferencing nil.
func TestBudgetNoReaderWiredFailsOpen(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	handler := RequireRateLimit(rl, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	key := budgetKey(1_000_000, 0, budget.PeriodMonthly, 0, true)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, budgetRequest(key))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (no reader wired)", rec.Code)
	}
}

// TestBudgetTotalPeriodHasNoRetryAfter proves a "total" budget refuses without
// a Retry-After: waiting never helps a lifetime-style ceiling, and promising
// otherwise sends the client into a retry loop that can only fail.
func TestBudgetTotalPeriodHasNoRetryAfter(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	rl.SetBudgetReader(&stubBudget{spend: budget.Spend{Micros: 5_000_000, Tokens: 0}})
	handler := RequireRateLimit(rl, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	key := budgetKey(1_000_000, 0, budget.PeriodTotal, 0, true)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, budgetRequest(key))
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "" {
		t.Errorf("Retry-After = %q, want empty for a total budget", got)
	}
}

// TestBudgetRunsBeforeTPM proves the ordering: a key that is both over budget
// and over its TPM bucket is answered with the budget's 402, not the token
// bucket's 429. A client reading 429 would go looking at the wrong limit.
func TestBudgetRunsBeforeTPM(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	rl.SetBudgetReader(&stubBudget{spend: budget.Spend{Micros: 2_000_000, Tokens: 0}})
	handler := RequireRateLimit(rl, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	key := budgetKey(1_000_000, 0, budget.PeriodMonthly, 0, true)
	// A TPM limit of 1 token against a body that estimates higher, so the TPM
	// path would refuse with 429 if it ran first.
	key.RateLimitTPM = new(1)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, budgetRequest(key))
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402 (budget is checked before TPM)", rec.Code)
	}
}

// TestBudgetKeyColumnOverridesPlan proves the key's own budget columns win over
// its plan's, the same precedence rateLimitRPM already uses. The plan is the
// template; the key column is the override.
func TestBudgetKeyColumnOverridesPlan(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	reader := &stubBudget{spend: budget.Spend{Micros: 1_500_000, Tokens: 0}}
	rl.SetBudgetReader(reader)
	rl.SetPlanSource(stubPlan{plan: planWithBudget(10_000_000, budget.PeriodTotal)})
	handler := RequireRateLimit(rl, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	// The key's own $1 monthly ceiling must win over the plan's $10 lifetime
	// one, so this request is refused rather than served.
	key := budgetKey(1_000_000, 0, budget.PeriodMonthly, 0, true)
	key.PlanID = new("p1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, budgetRequest(key))
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402 (the key's own ceiling wins)", rec.Code)
	}
}

// TestBudgetPlanAppliesWhenKeyHasNone proves the plan's budget governs a key
// that sets no columns of its own, which is the whole point of a template.
func TestBudgetPlanAppliesWhenKeyHasNone(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	rl.SetBudgetReader(&stubBudget{spend: budget.Spend{Micros: 2_000_000, Tokens: 0}})
	rl.SetPlanSource(stubPlan{plan: planWithBudget(1_000_000, budget.PeriodMonthly)})
	handler := RequireRateLimit(rl, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	key := &models.APIKey{ID: "planonly", Key: "sk-planonly-abcdefghij", PlanID: new("p1")}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, budgetRequest(key))
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402 (the plan's ceiling governs)", rec.Code)
	}
}
