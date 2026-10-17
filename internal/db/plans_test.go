package db

import (
	"database/sql"
	"errors"
	"testing"
)

// TestPlansDefaultIsSeeded proves a bootstrapped database always has the
// default plan, which is what keeps a key with an empty planId and a key on
// "default" reading the same limits.
func TestPlansDefaultIsSeeded(t *testing.T) {
	repo := openEphemeralSchemaDB(t)

	plan, err := repo.GetPlan("default")
	if err != nil {
		t.Fatalf("GetPlan(default): %v", err)
	}
	if plan.Name != "Default" {
		t.Errorf("default plan name = %q, want Default", plan.Name)
	}
	if plan.Period != PlanPeriodMonthly {
		t.Errorf("default plan period = %q, want %q", plan.Period, PlanPeriodMonthly)
	}
	// The seed ships hard_cutoff off so every key that predates plans keeps
	// exactly the behaviour it had.
	if plan.HardCutoff {
		t.Error("default plan must not hard-cut off: it would change existing installs")
	}
	if plan.RPMLimit != 0 || plan.TPMLimit != 0 || plan.ConcurrencyLimit != 0 {
		t.Errorf("default plan limits = %d/%d/%d, want all zero (unlimited)",
			plan.RPMLimit, plan.TPMLimit, plan.ConcurrencyLimit)
	}
}

// TestPlansCRUDRoundTrip proves the plan columns survive a write and read back
// unmangled, including the comma-joined allowlist and the boolean.
func TestPlansCRUDRoundTrip(t *testing.T) {
	repo := openEphemeralSchemaDB(t)

	want := Plan{
		ID:               "pro",
		TenantID:         "default",
		Name:             "Pro",
		Description:      "paid tier",
		LimitMicros:      5_000_000,
		LimitTokens:      1_000_000,
		RPMLimit:         120,
		TPMLimit:         90_000,
		ConcurrencyLimit: 4,
		Period:           PlanPeriodWeekly,
		AlertPct:         75,
		HardCutoff:       true,
		AllowedModels:    []string{"gpt-4o", "claude-*"},
	}
	if err := repo.CreatePlan(want); err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}

	got, err := repo.GetPlan("pro")
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if got.Name != want.Name || got.Description != want.Description ||
		got.LimitMicros != want.LimitMicros || got.LimitTokens != want.LimitTokens ||
		got.RPMLimit != want.RPMLimit || got.TPMLimit != want.TPMLimit ||
		got.ConcurrencyLimit != want.ConcurrencyLimit || got.Period != want.Period ||
		got.AlertPct != want.AlertPct || got.HardCutoff != want.HardCutoff {
		t.Fatalf("GetPlan() = %+v, want %+v", got, want)
	}
	if len(got.AllowedModels) != 2 || got.AllowedModels[0] != "gpt-4o" || got.AllowedModels[1] != "claude-*" {
		t.Fatalf("AllowedModels = %v, want [gpt-4o claude-*]", got.AllowedModels)
	}

	// The seeded default is still listed alongside the new plan.
	plans, err := repo.ListPlans("default")
	if err != nil {
		t.Fatalf("ListPlans: %v", err)
	}
	if len(plans) != 2 {
		t.Fatalf("ListPlans returned %d plans, want 2 (default + pro)", len(plans))
	}

	got.Name = "Pro Plus"
	got.RPMLimit = 300
	got.HardCutoff = false
	got.AllowedModels = []string{"gpt-4o"}
	if err := repo.UpdatePlan(got); err != nil {
		t.Fatalf("UpdatePlan: %v", err)
	}
	updated, err := repo.GetPlan("pro")
	if err != nil {
		t.Fatalf("GetPlan after update: %v", err)
	}
	if updated.Name != "Pro Plus" || updated.RPMLimit != 300 || updated.HardCutoff {
		t.Fatalf("updated plan = %+v, want name/rpm/hardCutoff changed", updated)
	}
	if len(updated.AllowedModels) != 1 || updated.AllowedModels[0] != "gpt-4o" {
		t.Fatalf("updated AllowedModels = %v, want [gpt-4o]", updated.AllowedModels)
	}
	// A field the update did not touch keeps its value: UpdatePlan writes the
	// whole row, so the caller must hand back what it read.
	if updated.LimitTokens != want.LimitTokens {
		t.Errorf("LimitTokens = %d after update, want %d", updated.LimitTokens, want.LimitTokens)
	}

	if err := repo.DeletePlan("pro"); err != nil {
		t.Fatalf("DeletePlan: %v", err)
	}
	if _, err := repo.GetPlan("pro"); err == nil {
		t.Error("GetPlan succeeded after DeletePlan")
	}
}

// TestSetApiKeyPlanAndCount proves a key's plan assignment is persisted, is
// counted for the delete guard, and comes back in the batch read.
func TestSetApiKeyPlanAndCount(t *testing.T) {
	repo := openEphemeralSchemaDB(t)

	if err := repo.CreateApiKey("k1", "sk-plan-test-0001", "Planned", "m1"); err != nil {
		t.Fatalf("CreateApiKey: %v", err)
	}
	if err := repo.CreatePlan(Plan{ID: "team", TenantID: "default", Name: "Team", Period: PlanPeriodMonthly}); err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}

	if n, err := repo.CountPlanKeys("team"); err != nil || n != 0 {
		t.Fatalf("CountPlanKeys before assign = %d, %v; want 0", n, err)
	}
	if err := repo.SetApiKeyPlan("k1", "team"); err != nil {
		t.Fatalf("SetApiKeyPlan: %v", err)
	}
	if n, err := repo.CountPlanKeys("team"); err != nil || n != 1 {
		t.Fatalf("CountPlanKeys after assign = %d, %v; want 1", n, err)
	}

	key, err := repo.GetApiKeyByID("k1")
	if err != nil {
		t.Fatalf("GetApiKeyByID: %v", err)
	}
	if key.PlanID == nil || *key.PlanID != "team" {
		t.Fatalf("key.PlanID = %v, want team", key.PlanID)
	}

	plans, err := repo.GetPlansForKeys([]string{"k1", "absent"})
	if err != nil {
		t.Fatalf("GetPlansForKeys: %v", err)
	}
	if _, ok := plans["k1"]; !ok {
		t.Error("GetPlansForKeys omitted the key that has a plan")
	}
	if got := plans["absent"]; got.ID != "" {
		t.Errorf("GetPlansForKeys[\"absent\"] = %+v, want the zero plan", got)
	}

	// Clearing the assignment returns the key to its own columns.
	if err := repo.SetApiKeyPlan("k1", ""); err != nil {
		t.Fatalf("SetApiKeyPlan(clear): %v", err)
	}
	if n, err := repo.CountPlanKeys("team"); err != nil || n != 0 {
		t.Fatalf("CountPlanKeys after clear = %d, %v; want 0", n, err)
	}
}

// TestPlanUpdateMissingRowIsAnError guards the update path: a plan id that no
// longer exists must not report success, or the dashboard would show a saved
// template that was never written.
func TestPlanUpdateMissingRowIsAnError(t *testing.T) {
	repo := openEphemeralSchemaDB(t)

	err := repo.UpdatePlan(Plan{ID: "ghost", TenantID: "default", Name: "Ghost", Period: PlanPeriodMonthly})
	if err == nil {
		t.Fatal("UpdatePlan on a missing row succeeded")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("UpdatePlan error = %v, want sql.ErrNoRows", err)
	}
}

// TestIsValidPlanPeriod pins the enum the budget engine will read.
func TestIsValidPlanPeriod(t *testing.T) {
	for _, ok := range []string{PlanPeriodDaily, PlanPeriodWeekly, PlanPeriodMonthly, PlanPeriodTotal} {
		if !IsValidPlanPeriod(ok) {
			t.Errorf("IsValidPlanPeriod(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "yearly", "MONTHLY", "daily "} {
		if IsValidPlanPeriod(bad) {
			t.Errorf("IsValidPlanPeriod(%q) = true, want false", bad)
		}
	}
}
