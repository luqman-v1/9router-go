package db

import (
	"context"
	"path/filepath"
	"testing"
)

// TestListGuardrailPoliciesForScope pins the dashboard listing contract: a
// scope read without a subject returns subject-scoped rows too, while the
// resolver's exact match keeps returning only the scope-wide policy.
func TestListGuardrailPoliciesForScope(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.sqlite")
	database, err := OpenDatabase(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer database.Close()
	if err := EnsureCoreSchema(database); err != nil {
		t.Fatalf("core schema: %v", err)
	}
	repo := NewRepo(database)
	for _, policy := range []*GuardrailPolicy{
		{ID: "p-global", Scope: "global", ScopeID: "", Name: "global", Enabled: true, Config: `{"detectors":["pii"]}`},
		{ID: "p-model", Scope: "model", ScopeID: "gpt-4o-mini", Name: "model", Enabled: true, Config: `{"detectors":["injection"]}`},
		{ID: "p-provider", Scope: "provider", ScopeID: "openai", Name: "provider", Enabled: true, Config: `{"detectors":["pii"]}`},
	} {
		if err := repo.CreateGuardrailPolicy(policy); err != nil {
			t.Fatalf("create %s: %v", policy.ID, err)
		}
	}

	all, err := repo.ListGuardrailPoliciesForScope(context.Background(), "model")
	if err != nil {
		t.Fatalf("list for scope: %v", err)
	}
	if len(all) != 1 || all[0].ID != "p-model" {
		t.Fatalf("scope listing = %v, want only p-model", ids(all))
	}

	// The resolver's exact read must not start returning subject rows.
	exact, err := repo.ListGuardrailPolicies("model", "gpt-4o-mini")
	if err != nil {
		t.Fatalf("exact list: %v", err)
	}
	if len(exact) != 1 || exact[0].ID != "p-model" {
		t.Fatalf("exact listing = %v, want only p-model", ids(exact))
	}
	wide, err := repo.ListGuardrailPolicies("model", "")
	if err != nil {
		t.Fatalf("scope-wide list: %v", err)
	}
	if len(wide) != 0 {
		t.Fatalf("scope-wide listing = %v, want none", ids(wide))
	}
}

func ids(policies []*GuardrailPolicy) []string {
	out := make([]string, 0, len(policies))
	for _, p := range policies {
		out = append(out, p.ID)
	}
	return out
}
