//go:build integration

package integration

import (
	"net/http"
	"testing"

	"9router/proxy/internal/db"
)

// The smart-combo routes are registered in two tables: dashboard.RegisterRoutes,
// which the dashboard package's own tests mount, and SetupDashboardRoutes, which
// the binary builds. They are separate registrations, so a route added to one
// alone is reachable in a unit test and 404s on the real server. That is how the
// rules editor shipped answering "404 page not found" on every preview while
// every unit test stayed green — the same failure the node-limit pair had in
// #432.
//
// This drives both routes through the router the binary actually mounts, and
// reads the membership back through the listing the dashboard renders from.
func TestSmartComboRulesAreReachableThroughTheRouter(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, chatCompletionResponder())
	seedNaraNode(t, env, upstream, db.ProviderLimits{})

	if err := env.Repo.SetKV("customModels", "nara|deepseek-v4|llm",
		`{"providerAlias":"nara","id":"deepseek-v4","type":"llm"}`); err != nil {
		t.Fatalf("seed custom model: %v", err)
	}
	env.AddCombo(t, "combo-ds", "ds-all", []string{})

	// The dry run the editor calls as the operator types.
	preview := env.Post(t, "/api/combos/combo-ds/smart/preview", map[string]any{
		"matchRules": []any{map[string]any{"pattern": "nara/deepseek*", "mode": "glob"}},
	})
	if preview.Status != http.StatusOK {
		t.Fatalf("POST smart preview = %d, want 200 (body: %s)", preview.Status, truncate(preview.Body))
	}
	var dry struct {
		Added []string `json:"added"`
		Rules []struct {
			Matched int `json:"matched"`
			Applied int `json:"applied"`
		} `json:"rules"`
	}
	preview.Decode(t, &dry)
	if len(dry.Added) != 1 || dry.Added[0] != "nara/deepseek-v4" {
		t.Fatalf("preview added = %v, want the one matching model", dry.Added)
	}
	if len(dry.Rules) != 1 || dry.Rules[0].Applied != 1 {
		t.Fatalf("preview rules = %+v, want one rule applying one model", dry.Rules)
	}

	// A dry run must not have written anything.
	stored, err := env.Repo.GetComboRules(t.Context(), "combo-ds")
	if err != nil {
		t.Fatalf("GetComboRules: %v", err)
	}
	if stored != "" && stored != "[]" {
		t.Errorf("the preview wrote rules: %q", stored)
	}

	// The rules are stored directly rather than through the combo write, so the
	// manual apply below is the only thing that could materialize the member.
	if err := env.Repo.SetComboRules(t.Context(), "combo-ds",
		`[{"id":"r1","pattern":"nara/deepseek*","mode":"glob","effect":"include","enabled":true,"maxMembers":20,"allKinds":false,"resolved":[]}]`); err != nil {
		t.Fatalf("store rules: %v", err)
	}

	// The manual apply, which is the half that catches what no model write
	// announces.
	reconcile := env.Post(t, "/api/combos/reconcile", nil)
	if reconcile.Status != http.StatusOK {
		t.Fatalf("POST reconcile = %d, want 200 (body: %s)", reconcile.Status, truncate(reconcile.Body))
	}
	var applied struct {
		Reconciled int `json:"reconciled"`
	}
	reconcile.Decode(t, &applied)
	if applied.Reconciled != 1 {
		t.Fatalf("reconciled = %d, want 1", applied.Reconciled)
	}

	// The listing the dashboard renders from must show the member the rule owns.
	res := env.Get(t, "/api/combos")
	if res.Status != http.StatusOK {
		t.Fatalf("GET /api/combos = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	var combos []struct {
		ID          string   `json:"id"`
		Models      string   `json:"models"`
		MatchRules  string   `json:"matchRules"`
		AutoMembers []string `json:"autoMembers"`
	}
	res.Decode(t, &combos)
	var found bool
	for _, c := range combos {
		if c.ID != "combo-ds" {
			continue
		}
		found = true
		if c.MatchRules == "" {
			t.Error("the combo must carry its rules so the editor can show them")
		}
		if len(c.AutoMembers) != 1 || c.AutoMembers[0] != "nara/deepseek-v4" {
			t.Errorf("autoMembers = %v, want the rule-owned member", c.AutoMembers)
		}
	}
	if !found {
		t.Fatalf("combo-ds is absent from GET /api/combos: %s", truncate(res.Body))
	}
}
