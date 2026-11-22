package db

import (
	"testing"
)

func TestPruneComboModels(t *testing.T) {
	repo := openEphemeralSchemaDB(t)
	dbRaw := repo.RawDB()

	seeds := []struct {
		id        string
		models    string
		updatedAt string
	}{
		{
			id:        "plain-combo",
			models:    `["openai/gpt-4o","ag/claude-sonnet-4-6","opencode/gpt-4o"]`,
			updatedAt: "2026-01-01T00:00:00Z",
		},
		{
			id:        "weighted-combo",
			models:    `[{"model":"openai/gpt-4o","weight":3},{"model":"groq/llama-3.3-70b","weight":1}]`,
			updatedAt: "2026-01-01T00:00:00Z",
		},
		{
			id:        "all-pruned-combo",
			models:    `["openai/gpt-4o"]`,
			updatedAt: "2026-01-01T00:00:00Z",
		},
		{
			id:        "untouched-combo",
			models:    `["ag/claude-sonnet-4-6","opencode/gpt-4o"]`,
			updatedAt: "2026-01-01T00:00:00Z",
		},
	}
	for _, s := range seeds {
		if _, err := dbRaw.Exec(
			`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES (?, ?, 'fallback', ?, '2026-01-01T00:00:00Z', ?)`,
			s.id, s.id, s.models, s.updatedAt,
		); err != nil {
			t.Fatalf("seed combo %s: %v", s.id, err)
		}
	}

	updated, err := repo.PruneComboModels(t.Context(), func(entry string) bool {
		return entry == "openai/gpt-4o"
	})
	if err != nil {
		t.Fatalf("PruneComboModels error = %v", err)
	}
	if updated != 3 {
		t.Fatalf("updated = %d, want 3", updated)
	}

	cPlain, err := repo.GetComboById("plain-combo")
	if err != nil {
		t.Fatalf("GetComboById plain-combo: %v", err)
	}
	if want := `["ag/claude-sonnet-4-6","opencode/gpt-4o"]`; cPlain.Models != want {
		t.Errorf("plain-combo models = %s, want %s", cPlain.Models, want)
	}

	cWeighted, err := repo.GetComboById("weighted-combo")
	if err != nil {
		t.Fatalf("GetComboById weighted-combo: %v", err)
	}
	if want := `[{"model":"groq/llama-3.3-70b","weight":1}]`; cWeighted.Models != want {
		t.Errorf("weighted-combo models = %s, want %s", cWeighted.Models, want)
	}

	cEmpty, err := repo.GetComboById("all-pruned-combo")
	if err != nil {
		t.Fatalf("GetComboById all-pruned-combo: %v", err)
	}
	if cEmpty.Models != `[]` {
		t.Errorf("all-pruned-combo models = %s, want []", cEmpty.Models)
	}

	cUntouched, err := repo.GetComboById("untouched-combo")
	if err != nil {
		t.Fatalf("GetComboById untouched-combo: %v", err)
	}
	if cUntouched.UpdatedAt != "2026-01-01T00:00:00Z" {
		t.Errorf("untouched-combo updatedAt = %s, want unchanged 2026-01-01T00:00:00Z", cUntouched.UpdatedAt)
	}
	if want := `["ag/claude-sonnet-4-6","opencode/gpt-4o"]`; cUntouched.Models != want {
		t.Errorf("untouched-combo models = %s, want %s", cUntouched.Models, want)
	}
}

func TestDeleteProviderNodePrunesComboEntriesAtomically(t *testing.T) {
	t.Run("prunes node id and prefix entries while preserving other providers", func(t *testing.T) {
		repo := openEphemeralSchemaDB(t)
		dbRaw := repo.RawDB()

		if _, err := repo.CreateProviderNode("node-a", "openai-compatible", "Node A",
			`{"prefix":"amd","apiType":"openai-compatible","baseUrl":"https://amd.test"}`); err != nil {
			t.Fatalf("CreateProviderNode node-a: %v", err)
		}
		if _, err := repo.CreateProviderNode("node-b", "openai-compatible", "Node B",
			`{"prefix":"other","apiType":"openai-compatible","baseUrl":"https://other.test"}`); err != nil {
			t.Fatalf("CreateProviderNode node-b: %v", err)
		}
		if _, err := dbRaw.Exec(
			`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES
			 ('c1', 'c1', 'fallback', '["amd/m1","node-a/m2","other/m1","ag/claude"]', '2026-01-01', '2026-01-01')`,
		); err != nil {
			t.Fatalf("seed combo: %v", err)
		}

		if err := repo.DeleteProviderNode(t.Context(), "node-a"); err != nil {
			t.Fatalf("DeleteProviderNode: %v", err)
		}
		got, err := repo.GetComboById("c1")
		if err != nil {
			t.Fatalf("GetComboById: %v", err)
		}
		if want := `["other/m1","ag/claude"]`; got.Models != want {
			t.Fatalf("combo models = %s, want %s", got.Models, want)
		}
	})

	t.Run("rolls back combo prune when delete trigger fails", func(t *testing.T) {
		repo := openEphemeralSchemaDB(t)
		dbRaw := repo.RawDB()

		if _, err := repo.CreateProviderNode("node-a", "openai-compatible", "Node A",
			`{"prefix":"amd","apiType":"openai-compatible","baseUrl":"https://amd.test"}`); err != nil {
			t.Fatalf("CreateProviderNode node-a: %v", err)
		}
		if _, err := dbRaw.Exec(
			`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES
			 ('c1', 'c1', 'fallback', '["amd/m1","ag/claude"]', '2026-01-01', '2026-01-01')`,
		); err != nil {
			t.Fatalf("seed combo: %v", err)
		}
		if _, err := dbRaw.Exec(
			`CREATE TRIGGER fail_combo_update BEFORE UPDATE ON combos BEGIN SELECT RAISE(ABORT, 'forced combo update failure'); END;`,
		); err != nil {
			t.Fatalf("create trigger: %v", err)
		}

		if err := repo.DeleteProviderNode(t.Context(), "node-a"); err == nil {
			t.Fatal("expected DeleteProviderNode error on trigger abort, got nil")
		}
		node, _, err := repo.GetProviderNodeByID("node-a")
		if err != nil || node == nil {
			t.Fatalf("expected node-a to roll back and survive, got node=%v err=%v", node, err)
		}
	})
}

func TestProviderActiveStatus(t *testing.T) {
	repo := openEphemeralSchemaDB(t)
	dbRaw := repo.RawDB()

	for _, c := range []struct {
		id, prov string
		active   int
	}{
		{"c-act", "openai", 1},
		{"c-mix-1", "antigravity", 0},
		{"c-mix-2", "antigravity", 1},
		{"c-off-1", "deepseek", 0},
		{"c-off-2", "deepseek", 0},
	} {
		if _, err := dbRaw.Exec(
			`INSERT INTO providerConnections (id, provider, authType, isActive, data, createdAt, updatedAt) VALUES (?, ?, 'apikey', ?, '{}', '2026-01-01', '2026-01-01')`,
			c.id, c.prov, c.active,
		); err != nil {
			t.Fatalf("seed conn %s: %v", c.id, err)
		}
	}

	got, err := repo.ProviderActiveStatus(t.Context())
	if err != nil {
		t.Fatalf("ProviderActiveStatus error = %v", err)
	}
	if !got["openai"] {
		t.Errorf("got[openai] = %v, want true", got["openai"])
	}
	if !got["antigravity"] {
		t.Errorf("got[antigravity] = %v, want true (one active among two)", got["antigravity"])
	}
	if active, exists := got["deepseek"]; !exists || active {
		t.Errorf("got[deepseek] = (%v, %v), want (false, true)", active, exists)
	}
	if _, exists := got["opencode"]; exists {
		t.Errorf("expected opencode (no rows) to be absent from status map")
	}
}

func TestDeleteProviderConnectionWithCount(t *testing.T) {
	repo := openEphemeralSchemaDB(t)
	dbRaw := repo.RawDB()

	for _, c := range []struct {
		id, prov string
	}{
		{"c-oai-1", "openai"},
		{"c-oai-2", "openai"},
		{"c-ds-1", "deepseek"},
	} {
		if _, err := dbRaw.Exec(
			`INSERT INTO providerConnections (id, provider, authType, isActive, data, createdAt, updatedAt) VALUES (?, ?, 'apikey', 1, '{}', '2026-01-01', '2026-01-01')`,
			c.id, c.prov,
		); err != nil {
			t.Fatalf("seed conn %s: %v", c.id, err)
		}
	}

	prov, rem, err := repo.DeleteProviderConnectionWithCount(t.Context(), "c-oai-1")
	if err != nil {
		t.Fatalf("delete c-oai-1 error = %v", err)
	}
	if prov != "openai" || rem != 1 {
		t.Fatalf("delete c-oai-1 = (%q, %d), want (openai, 1)", prov, rem)
	}

	prov, rem, err = repo.DeleteProviderConnectionWithCount(t.Context(), "c-oai-2")
	if err != nil {
		t.Fatalf("delete c-oai-2 error = %v", err)
	}
	if prov != "openai" || rem != 0 {
		t.Fatalf("delete c-oai-2 = (%q, %d), want (openai, 0)", prov, rem)
	}

	prov, rem, err = repo.DeleteProviderConnectionWithCount(t.Context(), "missing-id")
	if err != nil {
		t.Fatalf("delete missing-id error = %v", err)
	}
	if prov != "" || rem != 0 {
		t.Fatalf("delete missing-id = (%q, %d), want (\"\", 0)", prov, rem)
	}

	prov, rem, err = repo.DeleteProviderConnectionWithCount(t.Context(), "c-ds-1")
	if err != nil {
		t.Fatalf("delete c-ds-1 error = %v", err)
	}
	if prov != "deepseek" || rem != 0 {
		t.Fatalf("delete c-ds-1 = (%q, %d), want (deepseek, 0)", prov, rem)
	}
}
