package dashboard

import (
	"bytes"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/budget"
	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
)

// budgetRead is the shape budgetReadPayload renders, read back into a struct so
// every case below asserts on explicit fields rather than "the body is
// non-empty".
type budgetRead struct {
	Armed           bool   `json:"armed"`
	LimitMicros     int64  `json:"limitMicros"`
	LimitTokens     int64  `json:"limitTokens"`
	SpentMicros     int64  `json:"spentMicros"`
	SpentTokens     int64  `json:"spentTokens"`
	RemainingMicros int64  `json:"remainingMicros"`
	ResetAt         string `json:"resetAt"`
	Status          string `json:"status"`
	Note            string `json:"note"`
}

// getBudget reads GET /api/keys/{id}/budget and decodes the payload.
func getBudget(t *testing.T, router http.Handler, id string) (int, budgetRead) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/keys/"+id+"/budget", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		return rec.Code, budgetRead{}
	}
	var got budgetRead
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal budget read: %v: %s", err, rec.Body.String())
	}
	return rec.Code, got
}

// ensureLedger applies the core schema to the test database. The budget read
// measures spend from the usageHistory ledger, so a spend assertion is only
// honest against the real table and its indexes — the read-only tables a bare
// setupTestDB creates leave every ledger read failing with "no such table".
func ensureLedger(t *testing.T, repo *db.Repo) {
	t.Helper()
	if err := db.EnsureCoreSchema(repo.RawDB()); err != nil {
		t.Fatalf("apply core schema: %v", err)
	}
}

// armKeyBudget sets a key's own budget columns through the PUT the dashboard
// uses, so the read endpoint is tested against the same stored shape a real
// configuration produces rather than a hand-written row.
func armKeyBudget(t *testing.T, router http.Handler, id string, body map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, "/api/keys/"+id, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("arm budget: status %d: %s", rec.Code, rec.Body.String())
	}
}

// insertSpend attributes one ledger row to the test key. It goes through the
// masked key as clientKey, which is the spelling the live request path writes
// (#365), so the read must find it exactly the same way the 402 decision does.
func insertSpend(t *testing.T, repo *db.Repo, masked string, cost float64, prompt, completion int) {
	t.Helper()
	if err := repo.InsertUsageHistory("openai", "gpt-4o", "c1", masked, "/v1/chat/completions",
		prompt, completion, cost, "success", prompt+completion, "{}", "{}", masked); err != nil {
		t.Fatalf("insert usage history: %v", err)
	}
}

// TestGetApiKeyBudgetUnknownID proves an id that is not in the table is a 404
// rather than an empty 200: a budget read for a mistyped key must not read as
// "armed:false, spent 0", which would look exactly like a healthy fresh key.
func TestGetApiKeyBudgetUnknownID(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	if err := repo.UpdateSettingsRaw(map[string]any{"requireLogin": false}); err != nil {
		t.Fatalf("disable requireLogin: %v", err)
	}
	router := setupTestRouter(repo)

	code, _ := getBudget(t, router, "does-not-exist")
	if code != http.StatusNotFound {
		t.Fatalf("unknown id status = %d, want 404", code)
	}
}

// TestGetApiKeyBudgetUnarmed proves a key with no budget configured reads as
// unarmed and untouched: no limit, no spend, no reader call. The dashboard uses
// armed:false to hide the budget panel, so the unarmed read must stay cheap and
// must not report a configured ceiling that does not exist.
func TestGetApiKeyBudgetUnarmed(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	if err := repo.UpdateSettingsRaw(map[string]any{"requireLogin": false}); err != nil {
		t.Fatalf("disable requireLogin: %v", err)
	}
	router := setupTestRouter(repo)
	id, _ := createTestKey(t, router, "Unarmed Budget Key")

	code, got := getBudget(t, router, id)
	if code != http.StatusOK {
		t.Fatalf("unarmed read status = %d, want 200", code)
	}
	if got.Armed {
		t.Errorf("armed = true, want false for a key with no budget set")
	}
	if got.LimitMicros != 0 || got.LimitTokens != 0 {
		t.Errorf("limits = %d micros / %d tokens, want 0/0", got.LimitMicros, got.LimitTokens)
	}
	if got.SpentMicros != 0 || got.SpentTokens != 0 {
		t.Errorf("spent = %d micros / %d tokens, want 0/0", got.SpentMicros, got.SpentTokens)
	}
	if got.Status != string(budget.StatusOK) {
		t.Errorf("status = %q, want %q", got.Status, budget.StatusOK)
	}
}

// TestGetApiKeyBudgetMeasuredSpend proves the read reports what the engine
// would enforce: a key armed with a $2 total ceiling and $0.50 of attributed
// spend reads back 500000 micros spent, 150 tokens, 1500000 remaining, and a
// status that is ok because the spend is well under the 80% alert.
func TestGetApiKeyBudgetMeasuredSpend(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ensureLedger(t, repo)
	if err := repo.UpdateSettingsRaw(map[string]any{"requireLogin": false}); err != nil {
		t.Fatalf("disable requireLogin: %v", err)
	}
	router := setupTestRouter(repo)
	id, full := createTestKey(t, router, "Measured Key")

	armKeyBudget(t, router, id, map[string]any{
		"limitMicros":  2_000_000,
		"budgetPeriod": "total",
		"alertPct":     80,
		"hardCutoff":   true,
	})
	insertSpend(t, repo, handlerutil.MaskAPIKey(full), 0.50, 100, 50)

	code, got := getBudget(t, router, id)
	if code != http.StatusOK {
		t.Fatalf("measured read status = %d, want 200", code)
	}
	if !got.Armed {
		t.Fatalf("armed = false, want true for a budgeted key")
	}
	if got.SpentMicros != 500_000 {
		t.Errorf("spentMicros = %d, want 500000 ($0.50)", got.SpentMicros)
	}
	if got.SpentTokens != 150 {
		t.Errorf("spentTokens = %d, want 150 (100 prompt + 50 completion)", got.SpentTokens)
	}
	if want := int64(2_000_000) - 500_000; got.RemainingMicros != want {
		t.Errorf("remainingMicros = %d, want %d", got.RemainingMicros, want)
	}
	if got.Status != string(budget.StatusOK) {
		t.Errorf("status = %q, want %q at 25%% of the ceiling", got.Status, budget.StatusOK)
	}
	if got.ResetAt != "" {
		t.Errorf("resetAt = %q, want empty for a total-period budget", got.ResetAt)
	}
}

// TestGetApiKeyBudgetPlanInheritance proves the read resolves the budget the
// same way the limiter does — plan first, key override where set. A key with no
// columns of its own must report its plan's ceiling rather than an unarmed zero,
// or the dashboard would show "no budget" for a key the engine is actively
// enforcing.
func TestGetApiKeyBudgetPlanInheritance(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ensureLedger(t, repo)
	if err := repo.UpdateSettingsRaw(map[string]any{"requireLogin": false}); err != nil {
		t.Fatalf("disable requireLogin: %v", err)
	}
	router := setupTestRouter(repo)

	plan := db.Plan{
		ID:          "plan-budget-read",
		TenantID:    "default",
		Name:        "Read Plan",
		LimitMicros: 1_000_000,
		Period:      "monthly",
		AlertPct:    50,
	}
	if err := repo.CreatePlan(plan); err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	id, full := createTestKey(t, router, "Plan Child Key")
	planID := plan.ID
	armKeyBudget(t, router, id, map[string]any{"planId": planID})

	insertSpend(t, repo, handlerutil.MaskAPIKey(full), 0.50, 10, 10)

	code, got := getBudget(t, router, id)
	if code != http.StatusOK {
		t.Fatalf("plan read status = %d, want 200", code)
	}
	if !got.Armed {
		t.Fatalf("armed = false, want true: the plan arms the budget")
	}
	if got.LimitMicros != plan.LimitMicros {
		t.Errorf("limitMicros = %d, want %d (inherited from the plan)", got.LimitMicros, plan.LimitMicros)
	}
	if want := int64(1_000_000) - 500_000; got.RemainingMicros != want {
		t.Errorf("remainingMicros = %d, want %d", got.RemainingMicros, want)
	}
	// 500000 of the 1000000 ceiling is exactly the plan's 50% alert threshold,
	// and pctReached tests >=, so the engine reports "approaching" rather than
	// "ok" here even though nothing is exhausted yet.
	if got.Status != string(budget.StatusApproaching) {
		t.Errorf("status = %q, want %q at exactly the plan's 50%% alert threshold", got.Status, budget.StatusApproaching)
	}
	if got.ResetAt == "" {
		t.Errorf("resetAt empty, want the monthly roll for an anchored plan")
	}
}

// TestGetApiKeyBudgetHashedSecret proves a key whose secret was hashed away
// (#199) reports what it cannot measure instead of a zero that reads as a fresh
// key: the note is present and the spend stays unmeasured.
func TestGetApiKeyBudgetHashedSecret(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	if err := repo.UpdateSettingsRaw(map[string]any{"requireLogin": false}); err != nil {
		t.Fatalf("disable requireLogin: %v", err)
	}
	router := setupTestRouter(repo)
	id, _ := createTestKey(t, router, "Hashed Key")

	// Hash the row the way issue #199 does: the key column becomes the
	// "\x00argon2id:<lookup>" sentinel and the secret survives only as a
	// verifier. This is the production shape — a plain `UPDATE key = ''` is a
	// state no code path writes, so the test goes through HashApiKeyRow.
	if err := repo.HashApiKeyRow(id, "argon2id-verifier", "lookup-hash-123", "sk-6••123"); err != nil {
		t.Fatalf("HashApiKeyRow: %v", err)
	}
	// The guard must fire on the production predicate, not on a blank column:
	// assert the row really is a hashed row before expecting the note.
	hashed, err := repo.GetApiKeyByID(id)
	if err != nil {
		t.Fatalf("GetApiKeyByID: %v", err)
	}
	if !db.IsHashedKeyRow(hashed) {
		t.Fatalf("fixture is not a production-shaped hashed row (IsHashedKeyRow = false); the test would pass for the wrong reason")
	}

	armKeyBudget(t, router, id, map[string]any{
		"limitMicros":  1_000_000,
		"budgetPeriod": "total",
		"alertPct":     80,
		"hardCutoff":   true,
	})

	code, got := getBudget(t, router, id)
	if code != http.StatusOK {
		t.Fatalf("hashed read status = %d, want 200", code)
	}
	if got.Note == "" {
		t.Errorf("note empty, want the hashed-secret caveat")
	}
	if got.SpentMicros != 0 {
		t.Errorf("spentMicros = %d, want 0: the spend is not measured for a hashed key", got.SpentMicros)
	}
}

// TestGetApiKeysCarriesPlanId proves the list row of a plan-armed key reports
// its planId. The list returns the key's own columns, which are all zero for a
// key whose budget comes from a plan — without the planId the dashboard cannot
// tell that key is budgeted at all, and the policy modal skips the spend read
// for exactly the keys the engine is enforcing.
func TestGetApiKeysCarriesPlanId(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	if err := repo.UpdateSettingsRaw(map[string]any{"requireLogin": false}); err != nil {
		t.Fatalf("disable requireLogin: %v", err)
	}
	router := setupTestRouter(repo)
	plan := db.Plan{
		ID:          "plan-list-pin",
		TenantID:    "default",
		Name:        "List Pin Plan",
		LimitMicros: 1_000_000,
		Period:      "monthly",
	}
	if err := repo.CreatePlan(plan); err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	id, _ := createTestKey(t, router, "List Pin Key")
	planID := plan.ID
	armKeyBudget(t, router, id, map[string]any{"planId": planID})
	req := httptest.NewRequest(http.MethodGet, "/api/keys", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	for _, row := range rows {
		if row["id"] != id {
			continue
		}
		if row["planId"] != planID {
			t.Errorf("list row planId = %v, want %q: a plan-armed key must carry its plan so the dashboard knows it is budgeted", row["planId"], planID)
		}
		return
	}
	t.Fatalf("key %q not found in the list", id)
}
