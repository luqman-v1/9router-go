package dashboard

import (
	"context"
	"database/sql"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"9router/proxy/internal/combos/smart"
)

// seedSmartNode inserts a custom provider node, a connection for it and one
// custom model under its prefix. That is the shape the feature exists for: a
// model the operator registered by hand on their own endpoint, addressed as
// "<prefix>/<model>".
func seedSmartNode(t *testing.T, rawDB *sql.DB, nodeID, prefix, modelID string) {
	t.Helper()
	if _, err := rawDB.Exec(
		`INSERT INTO providerNodes (id, type, name, data, createdAt, updatedAt)
		 VALUES (?, 'custom', ?, ?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		nodeID, nodeID,
		fmt.Sprintf(`{"prefix":%q,"apiType":"openai-compatible","baseUrl":"http://127.0.0.1:1"}`, prefix),
	); err != nil {
		t.Fatalf("seed provider node: %v", err)
	}
	seedConnection(t, rawDB, nodeID)
	if _, err := rawDB.Exec(
		`INSERT INTO kv (scope, key, value) VALUES ('customModels', ?, ?)`,
		prefix+"|"+modelID+"|llm",
		fmt.Sprintf(`{"providerAlias":%q,"id":%q,"type":"llm"}`, prefix, modelID),
	); err != nil {
		t.Fatalf("seed custom model: %v", err)
	}
}

func TestCreateComboWithRulesMaterializesMembers(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)
	seedSmartNode(t, repo.RawDB(), "node-kenari", "kenari", "deepseek-v4")

	rec := postJSON(t, router, "/api/combos", map[string]any{
		"id":         "ds-all",
		"name":       "ds-all",
		"models":     []string{},
		"matchRules": []any{map[string]any{"pattern": "kenari/deepseek*", "mode": "glob"}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create combo = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	combo := mustCombo(t, repo, "ds-all")
	members, err := comboModels(combo.Models)
	if err != nil {
		t.Fatalf("comboModels: %v", err)
	}
	if !slices.Contains(members, "kenari/deepseek-v4") {
		t.Fatalf("members = %v, want the matching model materialized", members)
	}
	stored, err := repo.GetComboRules(context.Background(), "ds-all")
	if err != nil {
		t.Fatalf("GetComboRules: %v", err)
	}
	rules, err := smart.ParseRules(stored)
	if err != nil {
		t.Fatalf("ParseRules: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("stored %d rules, want 1", len(rules))
	}
	if !slices.Contains(rules[0].Resolved, "kenari/deepseek-v4") {
		t.Errorf("rule snapshot = %v, want the model it contributed", rules[0].Resolved)
	}
}

func TestSaveCustomModelAddsToCombo(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)
	seedSmartNode(t, repo.RawDB(), "node-kenari", "kenari", "placeholder")

	rec := postJSON(t, router, "/api/combos", map[string]any{
		"id":         "ds-all",
		"name":       "ds-all",
		"models":     []string{},
		"matchRules": []any{map[string]any{"pattern": "kenari/deepseek*", "mode": "glob"}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create combo = %d: %s", rec.Code, rec.Body.String())
	}
	if members, _ := comboModels(mustCombo(t, repo, "ds-all").Models); len(members) != 0 {
		t.Fatalf("members = %v, want none before the model exists", members)
	}

	rec = postJSON(t, router, "/api/models/custom", map[string]any{
		"providerAlias": "kenari",
		"id":            "deepseek-v4",
		"type":          "llm",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("save custom model = %d: %s", rec.Code, rec.Body.String())
	}

	members, err := comboModels(mustCombo(t, repo, "ds-all").Models)
	if err != nil {
		t.Fatalf("comboModels: %v", err)
	}
	if !slices.Contains(members, "kenari/deepseek-v4") {
		t.Fatalf("members = %v, want the newly registered model", members)
	}
}

func TestBulkImportAddsToCombo(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)
	seedSmartNode(t, repo.RawDB(), "node-kenari", "kenari", "placeholder")

	rec := postJSON(t, router, "/api/combos", map[string]any{
		"id":         "ds-all",
		"name":       "ds-all",
		"models":     []string{},
		"matchRules": []any{map[string]any{"pattern": "kenari/deepseek*", "mode": "glob"}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create combo = %d: %s", rec.Code, rec.Body.String())
	}

	// The array form is what "Import from /models" sends: one request for a
	// whole pasted list, which is the path most likely to hand a rule members.
	rec = postJSON(t, router, "/api/models/custom", map[string]any{
		"providerAlias": "kenari",
		"models":        []any{map[string]any{"id": "deepseek-v4"}, map[string]any{"id": "deepseek-v4-flash"}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("bulk save = %d: %s", rec.Code, rec.Body.String())
	}

	members, err := comboModels(mustCombo(t, repo, "ds-all").Models)
	if err != nil {
		t.Fatalf("comboModels: %v", err)
	}
	for _, want := range []string{"kenari/deepseek-v4", "kenari/deepseek-v4-flash"} {
		if !slices.Contains(members, want) {
			t.Fatalf("members = %v, want %s from the import", members, want)
		}
	}
}

func TestBulkDeleteRemovesFromCombo(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)
	seedSmartNode(t, repo.RawDB(), "node-kenari", "kenari", "deepseek-v4")

	rec := postJSON(t, router, "/api/combos", map[string]any{
		"id":         "ds-all",
		"name":       "ds-all",
		"models":     []string{},
		"matchRules": []any{map[string]any{"pattern": "kenari/deepseek*", "mode": "glob"}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create combo = %d: %s", rec.Code, rec.Body.String())
	}
	if members, _ := comboModels(mustCombo(t, repo, "ds-all").Models); len(members) != 1 {
		t.Fatalf("members = %v, want the model materialized first", members)
	}

	// The route is keyed by the provider alias the models are stored under,
	// which for a node is its prefix.
	req := httptest.NewRequest(http.MethodDelete, "/api/providers/kenari/models", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("bulk delete = %d: %s", rec.Code, rec.Body.String())
	}

	members, err := comboModels(mustCombo(t, repo, "ds-all").Models)
	if err != nil {
		t.Fatalf("comboModels: %v", err)
	}
	if slices.Contains(members, "kenari/deepseek-v4") {
		t.Fatalf("members = %v, want the bulk-deleted model pruned", members)
	}
}

func TestDeleteCustomModelRemovesFromCombo(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)
	seedSmartNode(t, repo.RawDB(), "node-kenari", "kenari", "deepseek-v4")

	rec := postJSON(t, router, "/api/combos", map[string]any{
		"id":         "ds-all",
		"name":       "ds-all",
		"models":     []string{},
		"matchRules": []any{map[string]any{"pattern": "kenari/deepseek*", "mode": "glob"}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create combo = %d: %s", rec.Code, rec.Body.String())
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/models/custom/kenari|deepseek-v4|llm", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete custom model = %d: %s", rec.Code, rec.Body.String())
	}

	members, err := comboModels(mustCombo(t, repo, "ds-all").Models)
	if err != nil {
		t.Fatalf("comboModels: %v", err)
	}
	if slices.Contains(members, "kenari/deepseek-v4") {
		t.Fatalf("members = %v, want the deleted model pruned", members)
	}
}

func TestDisableModelRemovesFromComboAndReEnableRestoresIt(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)
	seedSmartNode(t, repo.RawDB(), "node-kenari", "kenari", "deepseek-v4")

	rec := postJSON(t, router, "/api/combos", map[string]any{
		"id":         "ds-all",
		"name":       "ds-all",
		"models":     []string{},
		"matchRules": []any{map[string]any{"pattern": "kenari/deepseek*", "mode": "glob"}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create combo = %d: %s", rec.Code, rec.Body.String())
	}

	rec = putJSON(t, router, "/api/models/disabled/kenari", map[string]any{"disabledModels": []string{"deepseek-v4"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("disable models = %d: %s", rec.Code, rec.Body.String())
	}
	members, err := comboModels(mustCombo(t, repo, "ds-all").Models)
	if err != nil {
		t.Fatalf("comboModels: %v", err)
	}
	if slices.Contains(members, "kenari/deepseek-v4") {
		t.Fatalf("members = %v, want the disabled model pruned", members)
	}

	rec = putJSON(t, router, "/api/models/disabled/kenari", map[string]any{"disabledModels": []string{}})
	if rec.Code != http.StatusOK {
		t.Fatalf("re-enable models = %d: %s", rec.Code, rec.Body.String())
	}
	members, err = comboModels(mustCombo(t, repo, "ds-all").Models)
	if err != nil {
		t.Fatalf("comboModels: %v", err)
	}
	if !slices.Contains(members, "kenari/deepseek-v4") {
		t.Fatalf("members = %v, want the re-enabled model back", members)
	}
}

func TestUpdateComboReplacesRules(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)
	seedSmartNode(t, repo.RawDB(), "node-kenari", "kenari", "deepseek-v4")
	seedSmartNode(t, repo.RawDB(), "node-other", "other", "glm-5")

	rec := postJSON(t, router, "/api/combos", map[string]any{
		"id":         "mixed",
		"name":       "mixed",
		"models":     []string{},
		"matchRules": []any{map[string]any{"pattern": "kenari/*", "mode": "glob"}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create combo = %d: %s", rec.Code, rec.Body.String())
	}
	members, _ := comboModels(mustCombo(t, repo, "mixed").Models)
	if !slices.Contains(members, "kenari/deepseek-v4") || slices.Contains(members, "other/glm-5") {
		t.Fatalf("members = %v, want only the kenari model", members)
	}

	rec = putJSON(t, router, "/api/combos/mixed", map[string]any{
		"matchRules": []any{map[string]any{"pattern": "other/*", "mode": "glob"}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("update combo = %d: %s", rec.Code, rec.Body.String())
	}
	members, err := comboModels(mustCombo(t, repo, "mixed").Models)
	if err != nil {
		t.Fatalf("comboModels: %v", err)
	}
	if slices.Contains(members, "kenari/deepseek-v4") {
		t.Errorf("members = %v, want the old rule's model gone", members)
	}
	if !slices.Contains(members, "other/glm-5") {
		t.Errorf("members = %v, want the new rule's model", members)
	}
}

func TestSmartPreviewDoesNotWrite(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)
	seedSmartNode(t, repo.RawDB(), "node-kenari", "kenari", "deepseek-v4")

	rec := postJSON(t, router, "/api/combos", map[string]any{"id": "ds-all", "name": "ds-all", "models": []string{}})
	if rec.Code != http.StatusOK {
		t.Fatalf("create combo = %d: %s", rec.Code, rec.Body.String())
	}
	before := mustCombo(t, repo, "ds-all").Models

	rec = postJSON(t, router, "/api/combos/ds-all/smart/preview", map[string]any{
		"matchRules": []any{map[string]any{"pattern": "kenari/*", "mode": "glob"}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("preview = %d: %s", rec.Code, rec.Body.String())
	}
	var res map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	added, _ := res["added"].([]any)
	if len(added) != 1 || added[0] != "kenari/deepseek-v4" {
		t.Fatalf("added = %v, want the one matching model", added)
	}
	rules, _ := res["rules"].([]any)
	if len(rules) != 1 {
		t.Fatalf("rules = %v, want one rule preview", rules)
	}
	if after := mustCombo(t, repo, "ds-all").Models; after != before {
		t.Fatalf("preview wrote to the row: %q -> %q", before, after)
	}
}

func TestReconcileEndpointPicksUpUnannouncedModel(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)
	seedSmartNode(t, repo.RawDB(), "node-kenari", "kenari", "placeholder")

	rec := postJSON(t, router, "/api/combos", map[string]any{
		"id":         "ds-all",
		"name":       "ds-all",
		"models":     []string{},
		"matchRules": []any{map[string]any{"pattern": "kenari/deepseek*", "mode": "glob"}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create combo = %d: %s", rec.Code, rec.Body.String())
	}

	// A model that appeared without any write announcing it: a catalogue sync,
	// a restore, another process. Only the manual pass can see it.
	if _, err := repo.RawDB().Exec(
		`INSERT INTO kv (scope, key, value) VALUES ('customModels', 'kenari|deepseek-v4|llm', ?)`,
		`{"providerAlias":"kenari","id":"deepseek-v4","type":"llm"}`,
	); err != nil {
		t.Fatalf("seed custom model: %v", err)
	}

	rec = postJSON(t, router, "/api/combos/reconcile", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("reconcile = %d: %s", rec.Code, rec.Body.String())
	}
	var res map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if reconciled, _ := res["reconciled"].(float64); reconciled != 1 {
		t.Fatalf("reconciled = %v, want 1", res["reconciled"])
	}
	members, err := comboModels(mustCombo(t, repo, "ds-all").Models)
	if err != nil {
		t.Fatalf("comboModels: %v", err)
	}
	if !slices.Contains(members, "kenari/deepseek-v4") {
		t.Fatalf("members = %v, want the unannounced model picked up", members)
	}
}

func TestInvalidRuleRejectedAndNothingWritten(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	rec := postJSON(t, router, "/api/combos", map[string]any{
		"id":         "broken",
		"name":       "broken",
		"models":     []string{},
		"matchRules": []any{map[string]any{"pattern": "re:^kenari/(unclosed", "mode": "regex"}},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create combo = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	var res map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if res["code"] != "INVALID_REGEX" {
		t.Errorf("code = %v, want INVALID_REGEX", res["code"])
	}
	if combo, _ := repo.GetComboById("broken"); combo != nil {
		t.Fatalf("a refused combo must not be persisted: %+v", combo)
	}
}

func TestGetCombosCarriesRulesAndAutoMembers(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)
	seedSmartNode(t, repo.RawDB(), "node-kenari", "kenari", "deepseek-v4")

	rec := postJSON(t, router, "/api/combos", map[string]any{
		"id":         "ds-all",
		"name":       "ds-all",
		"models":     []string{},
		"matchRules": []any{map[string]any{"pattern": "kenari/deepseek*", "mode": "glob"}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create combo = %d: %s", rec.Code, rec.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/api/combos", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("get combos = %d: %s", rec.Code, rec.Body.String())
	}
	var combos []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &combos); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(combos) != 1 {
		t.Fatalf("got %d combos, want 1", len(combos))
	}
	if _, ok := combos[0]["matchRules"]; !ok {
		t.Error("the combo must carry its rules so the editor can show them")
	}
	auto, _ := combos[0]["autoMembers"].([]any)
	if len(auto) != 1 || auto[0] != "kenari/deepseek-v4" {
		t.Errorf("autoMembers = %v, want the rule-owned member", auto)
	}
}

func TestNoRulesMeansNoWork(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)
	seedSmartNode(t, repo.RawDB(), "node-kenari", "kenari", "deepseek-v4")

	// A combo with no rules at all: the early-out must leave it exactly as it
	// is, so an installation that never uses the feature pays nothing.
	rec := postJSON(t, router, "/api/combos", map[string]any{
		"id": "plain", "name": "plain", "models": []string{"kenari/deepseek-v4"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create combo = %d: %s", rec.Code, rec.Body.String())
	}
	before := mustCombo(t, repo, "plain").Models

	rec = postJSON(t, router, "/api/combos/reconcile", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("reconcile = %d: %s", rec.Code, rec.Body.String())
	}
	if after := mustCombo(t, repo, "plain").Models; after != before {
		t.Fatalf("a combo without rules was touched: %q -> %q", before, after)
	}
}

var _ = fmt.Sprintf
