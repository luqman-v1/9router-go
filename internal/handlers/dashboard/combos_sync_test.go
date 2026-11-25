package dashboard

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

func TestDeleteCustomModelPrunesPlainCombos(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)
	seedSmartNode(t, repo.RawDB(), "node-kenari", "kenari", "deepseek-v4")
	seedSmartNode(t, repo.RawDB(), "node-other", "other", "deepseek-v4")

	if rec := postJSON(t, router, "/api/combos", map[string]any{
		"id":     "plain-c",
		"name":   "plain-c",
		"models": []string{"kenari/deepseek-v4", "other/deepseek-v4", "ag/claude-sonnet-4-6"},
	}); rec.Code != http.StatusOK {
		t.Fatalf("create combo: %d %s", rec.Code, rec.Body.String())
	}
	// Seed a legacy weighted object-array combo directly in SQLite (POST /api/combos normalizes objects to plain strings).
	if _, err := repo.RawDB().Exec(
		`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES
		 ('weighted-c', 'weighted-c', 'fallback', '[{"model":"kenari/deepseek-v4","weight":3},{"model":"ag/claude-sonnet-4-6","weight":2}]', '2026-01-01', '2026-01-01')`,
	); err != nil {
		t.Fatalf("seed weighted combo: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/models/custom/kenari|deepseek-v4|llm", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete custom model = %d: %s", rec.Code, rec.Body.String())
	}

	plainMembers, err := comboModels(mustCombo(t, repo, "plain-c").Models)
	if err != nil {
		t.Fatalf("comboModels plain-c: %v", err)
	}
	wantPlain := []string{"other/deepseek-v4", "ag/claude-sonnet-4-6"}
	if !slices.Equal(plainMembers, wantPlain) {
		t.Fatalf("plain-c members = %v, want %v", plainMembers, wantPlain)
	}

	weightedRaw := mustCombo(t, repo, "weighted-c").Models
	if wantWeighted := `[{"model":"ag/claude-sonnet-4-6","weight":2}]`; weightedRaw != wantWeighted {
		t.Fatalf("weighted-c models = %s, want %s", weightedRaw, wantWeighted)
	}
}

func TestDeleteProviderModelsPrunesPlainCombos(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)
	seedSmartNode(t, repo.RawDB(), "node-kenari", "kenari", "deepseek-v4")
	if _, err := repo.RawDB().Exec(
		`INSERT INTO kv (scope, key, value) VALUES ('customModels', 'kenari|deepseek-v4-flash|llm', '{"providerAlias":"kenari","id":"deepseek-v4-flash","type":"llm"}')`,
	); err != nil {
		t.Fatalf("seed second custom model: %v", err)
	}

	if rec := postJSON(t, router, "/api/combos", map[string]any{
		"id":     "plain-bulk",
		"name":   "plain-bulk",
		"models": []string{"kenari/deepseek-v4", "kenari/deepseek-v4-flash", "openai/deepseek-v4"},
	}); rec.Code != http.StatusOK {
		t.Fatalf("create combo = %d: %s", rec.Code, rec.Body.String())
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/providers/kenari/models", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("bulk delete = %d: %s", rec.Code, rec.Body.String())
	}

	members, err := comboModels(mustCombo(t, repo, "plain-bulk").Models)
	if err != nil {
		t.Fatalf("comboModels: %v", err)
	}
	if !slices.Equal(members, []string{"openai/deepseek-v4"}) {
		t.Fatalf("members = %v, want only [openai/deepseek-v4]", members)
	}
}

func TestDeleteProviderNodePrunesPlainCombosViaAPI(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)
	seedSmartNode(t, repo.RawDB(), "node-kenari", "kenari", "deepseek-v4")

	if rec := postJSON(t, router, "/api/combos", map[string]any{
		"id":     "plain-node",
		"name":   "plain-node",
		"models": []string{"kenari/deepseek-v4", "node-kenari/other-model", "ag/claude-sonnet-4-6"},
	}); rec.Code != http.StatusOK {
		t.Fatalf("create combo = %d: %s", rec.Code, rec.Body.String())
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/provider-nodes/node-kenari", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete provider node = %d: %s", rec.Code, rec.Body.String())
	}

	members, err := comboModels(mustCombo(t, repo, "plain-node").Models)
	if err != nil {
		t.Fatalf("comboModels: %v", err)
	}
	if !slices.Equal(members, []string{"ag/claude-sonnet-4-6"}) {
		t.Fatalf("members = %v, want only [ag/claude-sonnet-4-6]", members)
	}
}

func TestDisableModelPreservesPlainComboEntries(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)
	seedSmartNode(t, repo.RawDB(), "node-kenari", "kenari", "deepseek-v4")

	if rec := postJSON(t, router, "/api/combos", map[string]any{
		"id":     "plain-keep",
		"name":   "plain-keep",
		"models": []string{"kenari/deepseek-v4", "ag/claude-sonnet-4-6"},
	}); rec.Code != http.StatusOK {
		t.Fatalf("create combo = %d: %s", rec.Code, rec.Body.String())
	}

	if rec := putJSON(t, router, "/api/models/disabled/kenari", map[string]any{
		"disabledModels": []string{"deepseek-v4"},
	}); rec.Code != http.StatusOK {
		t.Fatalf("disable models = %d: %s", rec.Code, rec.Body.String())
	}

	members, err := comboModels(mustCombo(t, repo, "plain-keep").Models)
	if err != nil {
		t.Fatalf("comboModels: %v", err)
	}
	want := []string{"kenari/deepseek-v4", "ag/claude-sonnet-4-6"}
	if !slices.Equal(members, want) {
		t.Fatalf("plain-keep members = %v, want preserved %v when disabled", members, want)
	}
}

func TestDeleteConnectionPrunesCombosOnlyWhenLastRemoved(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	for _, c := range []struct {
		id, prov string
	}{
		{"conn-oai-1", "openai"},
		{"conn-oai-2", "openai"},
		{"conn-ds-1", "deepseek"},
		{"conn-oc-1", "opencode"}, // NoAuth keyless provider
	} {
		if _, err := repo.RawDB().Exec(
			`INSERT INTO providerConnections (id, provider, authType, isActive, data, createdAt, updatedAt) VALUES (?, ?, 'apikey', 1, '{}', '2026-01-01', '2026-01-01')`,
			c.id, c.prov,
		); err != nil {
			t.Fatalf("seed conn %s: %v", c.id, err)
		}
	}

	if rec := postJSON(t, router, "/api/combos", map[string]any{
		"id":     "plain-conn",
		"name":   "plain-conn",
		"models": []string{"openai/gpt-4o", "deepseek/deepseek-chat", "opencode/gpt-5", "ag/claude-sonnet-4-6"},
	}); rec.Code != http.StatusOK {
		t.Fatalf("create combo = %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := repo.RawDB().Exec(
		`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES
		 ('weighted-conn', 'weighted-conn', 'fallback', '[{"model":"openai/gpt-4o","weight":3},{"model":"opencode/gpt-5","weight":1}]', '2026-01-01', '2026-01-01')`,
	); err != nil {
		t.Fatalf("seed weighted combo: %v", err)
	}

	deleteConn := func(id string) {
		req := httptest.NewRequest(http.MethodDelete, "/api/connections/"+id, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("DELETE /api/connections/%s = %d: %s", id, rec.Code, rec.Body.String())
		}
	}

	// Deleting the first of two openai connections keeps openai members intact.
	deleteConn("conn-oai-1")
	members, err := comboModels(mustCombo(t, repo, "plain-conn").Models)
	if err != nil {
		t.Fatalf("comboModels after first openai delete: %v", err)
	}
	wantAfterFirst := []string{"openai/gpt-4o", "deepseek/deepseek-chat", "opencode/gpt-5", "ag/claude-sonnet-4-6"}
	if !slices.Equal(members, wantAfterFirst) {
		t.Fatalf("after first openai delete = %v, want %v", members, wantAfterFirst)
	}

	// Deleting the last openai and deepseek connections prunes both from combos.
	deleteConn("conn-oai-2")
	deleteConn("conn-ds-1")
	// Deleting the keyless opencode connection must preserve opencode entries.
	deleteConn("conn-oc-1")

	members, err = comboModels(mustCombo(t, repo, "plain-conn").Models)
	if err != nil {
		t.Fatalf("comboModels after last deletes: %v", err)
	}
	wantFinal := []string{"opencode/gpt-5", "ag/claude-sonnet-4-6"}
	if !slices.Equal(members, wantFinal) {
		t.Fatalf("after last deletes = %v, want %v", members, wantFinal)
	}

	weightedRaw := mustCombo(t, repo, "weighted-conn").Models
	if wantWeighted := `[{"model":"opencode/gpt-5","weight":1}]`; weightedRaw != wantWeighted {
		t.Fatalf("weighted-conn models = %s, want %s", weightedRaw, wantWeighted)
	}
}
