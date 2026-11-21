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
