package chat

import (
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/models"
)

// A key on a plan is restricted by the union of its own rows and the plan's
// rows. If the plan read fails, that union is unknown, and returning the
// key's own rows alone would drop the plan's grant without saying so.
//
// The dangerous shape is a key whose only rows came from its plan: its own
// rows are empty, so an empty plan half leaves the key with no allowlist at
// all, and "no allowlist" reads as "no constraint". The key that was
// restricted by its plan would be unrestricted.
//
// So a failed plan read has to fail closed, the same way the own-rows read
// above it already does.
func TestModelAccess_PlanReadFailureFailsClosed(t *testing.T) {
	_, repo, c := setupFailClosedTestDB(t)
	defer c()
	h := NewChatHandler(repo)

	planID := "plan-restrictive"
	// The key has no rows of its own: the plan is the only restriction.
	if err := repo.SetAllowedModels("k-plan", nil); err != nil {
		t.Fatalf("seed own rows: %v", err)
	}
	if err := repo.CreatePlan(db.Plan{
		ID:            planID,
		Name:          "restrictive",
		AllowedModels: []string{"gpt-4o"},
	}); err != nil {
		t.Fatalf("seed plan: %v", err)
	}

	// Sanity: with the plan readable, the key is restricted to the plan's
	// models, and a model outside the plan is denied.
	if err := h.checkModelAccess(&models.APIKey{ID: "k-plan", PlanID: &planID}, "gpt-4o"); err != nil {
		t.Fatalf("plan model must be allowed: %v", err)
	}
	err := h.checkModelAccess(&models.APIKey{ID: "k-plan", PlanID: &planID}, "claude-opus-4")
	if err == nil || !IsModelNotAllowed(err) {
		t.Fatalf("model outside the plan must be denied, got %v", err)
	}

	// Break the plan read while leaving the table in place: the column the
	// read selects is gone, so the query fails. That is a storage failure,
	// not a database without plans.
	if _, err := repo.RawDB().Exec(`ALTER TABLE plans DROP COLUMN allowed_models`); err != nil {
		t.Fatalf("break plans read: %v", err)
	}

	// The plan half is unknown, so the union is unknown: fail closed with a
	// policy failure rather than letting the key through unrestricted.
	err = h.checkModelAccess(&models.APIKey{ID: "k-plan", PlanID: &planID}, "gpt-4o")
	if err == nil || !IsPolicyUnavailable(err) {
		t.Fatalf("want ErrPolicyUnavailable on a failed plan read, got %v", err)
	}
}

// The deep-dispatch loader holds only a key id, so it reads the plan with the
// joined query. It has to fail closed the same way: an empty allowlist there
// reads as "no constraint" too.
func TestModelAccess_PlanJoinFailureFailsClosed(t *testing.T) {
	_, repo, c := setupFailClosedTestDB(t)
	defer c()
	h := NewChatHandler(repo)

	if err := repo.SetAllowedModels("k-join", nil); err != nil {
		t.Fatalf("seed own rows: %v", err)
	}
	if err := repo.CreatePlan(db.Plan{
		ID:            "plan-join",
		Name:          "restrictive",
		AllowedModels: []string{"gpt-4o"},
	}); err != nil {
		t.Fatalf("seed plan: %v", err)
	}
	seedAPIKeyRow(t, repo, "k-join")
	if err := repo.SetApiKeyPlan("k-join", "plan-join"); err != nil {
		t.Fatalf("assign plan: %v", err)
	}

	// With the plan readable, the key is restricted.
	patterns := h.allowedModelPatterns("k-join")
	if len(patterns) == 0 {
		t.Fatal("a key on a plan must have an allowlist")
	}

	if _, err := repo.RawDB().Exec(`ALTER TABLE plans DROP COLUMN allowed_models`); err != nil {
		t.Fatalf("break plans read: %v", err)
	}

	// The joined read fails, so the plan half is unknown: deny everything
	// rather than hand back an empty allowlist, which reads as "no
	// constraint".
	patterns = h.allowedModelPatterns("k-join")
	if len(patterns) != 1 || patterns[0] != SentinelDenyAll {
		t.Fatalf("want [%s] on a failed plan read, got %v", SentinelDenyAll, patterns)
	}
}

// A plan that no longer exists is not a failure. A key can name a plan that
// was deleted after the key was pointed at it, and such a key grants nothing
// beyond its own rows — that is a policy, not an outage.
func TestModelAccess_DeletedPlanIsNotAFailure(t *testing.T) {
	_, repo, c := setupFailClosedTestDB(t)
	defer c()
	h := NewChatHandler(repo)

	planID := "plan-gone"
	if err := repo.SetAllowedModels("k-gone", []string{"gpt-4o"}); err != nil {
		t.Fatalf("seed own rows: %v", err)
	}
	if err := repo.CreatePlan(db.Plan{
		ID:            planID,
		Name:          "gone",
		AllowedModels: []string{"claude-opus-4"},
	}); err != nil {
		t.Fatalf("seed plan: %v", err)
	}
	if _, err := repo.RawDB().Exec(`DELETE FROM plans WHERE id = ?`, planID); err != nil {
		t.Fatalf("delete plan: %v", err)
	}

	// The plan is gone, so the key's own rows are all that is left. That is
	// a policy, not a storage failure, so it must not be a 503.
	if err := h.checkModelAccess(&models.APIKey{ID: "k-gone", PlanID: &planID}, "gpt-4o"); err != nil {
		t.Fatalf("own rows must still apply after the plan is deleted: %v", err)
	}
	err := h.checkModelAccess(&models.APIKey{ID: "k-gone", PlanID: &planID}, "claude-opus-4")
	if err == nil || !IsModelNotAllowed(err) {
		t.Fatalf("a deleted plan must not keep granting its models, got %v", err)
	}
}

// The union still works: the plan adds to the key's own rows, and the key's
// own rows still apply on their own.
func TestModelAccess_PlanUnionsWithOwnRows(t *testing.T) {
	_, repo, c := setupFailClosedTestDB(t)
	defer c()
	h := NewChatHandler(repo)

	planID := "plan-union"
	if err := repo.SetAllowedModels("k-union", []string{"gpt-4o"}); err != nil {
		t.Fatalf("seed own rows: %v", err)
	}
	if err := repo.CreatePlan(db.Plan{
		ID:            planID,
		Name:          "union",
		AllowedModels: []string{"claude-opus-4"},
	}); err != nil {
		t.Fatalf("seed plan: %v", err)
	}
	key := &models.APIKey{ID: "k-union", PlanID: &planID}
	if err := h.checkModelAccess(key, "gpt-4o"); err != nil {
		t.Fatalf("own row must be allowed: %v", err)
	}
	if err := h.checkModelAccess(key, "claude-opus-4"); err != nil {
		t.Fatalf("plan row must be allowed: %v", err)
	}
	if err := h.checkModelAccess(key, "gemini-2.5-pro"); err == nil || !IsModelNotAllowed(err) {
		t.Fatalf("a model in neither must be denied, got %v", err)
	}
}

// A key with no plan keeps behaving exactly as it did: the plan read is not
// attempted, so a broken plans table does not affect it.
//
// A key that is not on a plan carries an empty plan id, not a missing one. A
// missing plan id means "this caller does not know the key's plan", which is
// a different thing and is covered by the joined-read case above.
func TestModelAccess_NoPlanIsUnaffected(t *testing.T) {
	_, repo, c := setupFailClosedTestDB(t)
	defer c()
	h := NewChatHandler(repo)

	if err := repo.SetAllowedModels("k-noplan", []string{"gpt-4o"}); err != nil {
		t.Fatalf("seed own rows: %v", err)
	}
	if _, err := repo.RawDB().Exec(`ALTER TABLE plans DROP COLUMN allowed_models`); err != nil {
		t.Fatalf("break plans read: %v", err)
	}
	emptyPlanID := ""
	key := &models.APIKey{ID: "k-noplan", PlanID: &emptyPlanID}
	if err := h.checkModelAccess(key, "gpt-4o"); err != nil {
		t.Fatalf("a key with no plan must keep its own rows: %v", err)
	}
	if err := h.checkModelAccess(key, "gemini-2.5-pro"); err == nil || !IsModelNotAllowed(err) {
		t.Fatalf("a key with no plan must still be restricted, got %v", err)
	}
}

// A database created before the plans feature has no plans table, and a key
// in it cannot be on a plan. That is not an outage, so it must not answer
// every request with a 503.
func TestModelAccess_PlanlessDatabaseIsNotAnOutage(t *testing.T) {
	d, repo, c := setupFailClosedTestDB(t)
	defer c()
	h := NewChatHandler(repo)

	if err := repo.SetAllowedModels("k-legacy", []string{"gpt-4o"}); err != nil {
		t.Fatalf("seed own rows: %v", err)
	}
	if _, err := d.Exec(`DROP TABLE plans`); err != nil {
		t.Fatalf("drop plans: %v", err)
	}
	if repo.HasPlansTable() {
		t.Fatal("plans table must be gone")
	}
	key := &models.APIKey{ID: "k-legacy"}
	if err := h.checkModelAccess(key, "gpt-4o"); err != nil {
		t.Fatalf("a planless database must not fail closed for a key with no plan: %v", err)
	}
	if err := h.checkModelAccess(key, "gemini-2.5-pro"); err == nil || !IsModelNotAllowed(err) {
		t.Fatalf("a planless database must still be restricted, got %v", err)
	}
}

// A closed database is a storage failure on every read, and the plan read is
// no exception: it must not be mistaken for "no plan".
func TestModelAccess_ClosedDatabaseIsNotAPlanlessDatabase(t *testing.T) {
	_, repo, c := setupFailClosedTestDB(t)
	defer c()
	h := NewChatHandler(repo)

	planID := "plan-closed"
	if err := repo.SetAllowedModels("k-closed", nil); err != nil {
		t.Fatalf("seed own rows: %v", err)
	}
	if err := repo.CreatePlan(db.Plan{ID: planID, Name: "closed"}); err != nil {
		t.Fatalf("seed plan: %v", err)
	}
	repo.RawDB().Close()

	err := h.checkModelAccess(&models.APIKey{ID: "k-closed", PlanID: &planID}, "gpt-4o")
	if err == nil || !IsPolicyUnavailable(err) {
		t.Fatalf("want ErrPolicyUnavailable on a closed database, got %v", err)
	}
}
