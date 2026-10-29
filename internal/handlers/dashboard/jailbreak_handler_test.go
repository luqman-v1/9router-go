package dashboard

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

// The toggle must survive a reload. Model ids contain "/" and ":"
// ("tkh/mimo-v2.6-flash:free"), and an id passed through the URL path came
// back still-encoded — the write answered 200 under a key that the read path
// never matched, so the switch looked on and then reverted. The id travels in
// the body now; this pins that it lands verbatim.
func TestJailbreakToggleStoresModelIDVerbatim(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	r := setupTestRouter(repo)

	const modelID = "tkh/mimo-v2.6-flash:free"
	payload, _ := json.Marshal(map[string]any{"id": modelID, "enabled": true})
	req := httptest.NewRequest(http.MethodPut, "/api/jailbreak/models", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	// Exactly the id the request asked for, no percent-escapes anywhere.
	on, err := repo.JailbreakModelEnabled(modelID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !on {
		t.Fatal("toggle did not persist under the id the client sent")
	}
	list, err := repo.ListJailbreakModels()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !list[modelID] {
		t.Fatalf("list = %v, want the verbatim id", list)
	}
	for id := range list {
		if id != modelID {
			t.Fatalf("stored key %q is not the verbatim id", id)
		}
	}

	// And off removes it again, so the next reload reads off.
	payload, _ = json.Marshal(map[string]any{"id": modelID, "enabled": false})
	req = httptest.NewRequest(http.MethodPut, "/api/jailbreak/models", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT off status = %d, want 200", rec.Code)
	}
	on, _ = repo.JailbreakModelEnabled(modelID)
	if on {
		t.Fatal("toggle stayed on after being switched off")
	}
}

// An empty id must be refused rather than writing a key nobody can match.
func TestJailbreakToggleRejectsEmptyID(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	r := setupTestRouter(repo)
	payload, _ := json.Marshal(map[string]any{"id": "", "enabled": true})
	req := httptest.NewRequest(http.MethodPut, "/api/jailbreak/models", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// The prompt is usually an uploaded .md file, so the document has to carry the
// file's name alongside its text, and the body ceiling has to admit a real
// markdown document rather than truncate it at a typed-line size.
func TestJailbreakDocStoresUploadedMarkdownFile(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	r := setupTestRouter(repo)

	// ~120 KB of markdown: comfortably past the old 64 KB body cap.
	big := "# Agents\n\n" + strings.Repeat("- rule line with `code` and **bold**\n", 3000)
	payload, _ := json.Marshal(map[string]any{
		"prompt":       big,
		"geminiSafety": true,
		"source":       "agents.md",
	})
	req := httptest.NewRequest(http.MethodPut, "/api/jailbreak", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	// GET is what the page reloads from: everything must come back intact.
	req = httptest.NewRequest(http.MethodGet, "/api/jailbreak", nil)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode GET: %v", err)
	}
	if got["source"] != "agents.md" {
		t.Errorf("source = %v, want agents.md", got["source"])
	}
	if got["geminiSafety"] != true {
		t.Errorf("geminiSafety = %v, want true", got["geminiSafety"])
	}
	prompt, _ := got["prompt"].(string)
	if len(prompt) != len(big) {
		t.Errorf("prompt came back %d bytes, sent %d — truncated", len(prompt), len(big))
	}

	doc, err := repo.GetJailbreakDoc()
	if err != nil {
		t.Fatalf("repo read: %v", err)
	}
	if doc.Source != "agents.md" || doc.Prompt != big {
		t.Errorf("stored doc mismatch: source=%q len(prompt)=%d", doc.Source, len(doc.Prompt))
	}
}

func TestJailbreakPresetCRUDRoutes(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	r := setupTestRouter(repo)

	// Create preset via PUT /api/jailbreak/presets
	payload, _ := json.Marshal(map[string]any{
		"id":           "expert-coder",
		"name":         "Expert Coder",
		"prompt":       "You are an expert coder.",
		"geminiSafety": true,
		"source":       "expert.md",
	})
	req := httptest.NewRequest(http.MethodPut, "/api/jailbreak/presets", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT preset status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	// List presets GET /api/jailbreak/presets
	req = httptest.NewRequest(http.MethodGet, "/api/jailbreak/presets", nil)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET presets status = %d, want 200", rec.Code)
	}
	var list []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode presets: %v", err)
	}
	found := false
	for _, p := range list {
		if p["id"] == "expert-coder" {
			found = true
			if p["name"] != "Expert Coder" || p["prompt"] != "You are an expert coder." {
				t.Fatalf("preset content mismatch: %+v", p)
			}
		}
	}
	if !found {
		t.Fatalf("created preset not in list: %+v", list)
	}

	// Bind model to preset
	bindPayload, _ := json.Marshal(map[string]any{
		"id":       "anthropic/claude-3-7-sonnet",
		"presetId": "expert-coder",
	})
	req = httptest.NewRequest(http.MethodPut, "/api/jailbreak/models", bytes.NewReader(bindPayload))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT model bind status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	// GET /api/jailbreak/models
	req = httptest.NewRequest(http.MethodGet, "/api/jailbreak/models", nil)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET models status = %d, want 200", rec.Code)
	}
	var modelConfigs map[string]map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &modelConfigs); err != nil {
		t.Fatalf("decode models: %v", err)
	}
	cfg := modelConfigs["anthropic/claude-3-7-sonnet"]
	if cfg["enabled"] != true || cfg["presetId"] != "expert-coder" {
		t.Fatalf("model config mismatch: %+v", cfg)
	}

	// Delete preset DELETE /api/jailbreak/presets/{id}
	req = httptest.NewRequest(http.MethodDelete, "/api/jailbreak/presets/expert-coder", nil)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE preset status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	// Verify that models previously bound to expert-coder are automatically unbound to off
	req = httptest.NewRequest(http.MethodGet, "/api/jailbreak/models", nil)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET models status = %d, want 200", rec.Code)
	}
	var modelConfigsAfter map[string]map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &modelConfigsAfter); err != nil {
		t.Fatalf("decode models: %v", err)
	}
	if _, stillArmed := modelConfigsAfter["anthropic/claude-3-7-sonnet"]; stillArmed {
		t.Fatalf("model should be unbound to off after preset deletion, got: %+v", modelConfigsAfter["anthropic/claude-3-7-sonnet"])
	}
}

// A preset's keyword and exclude rules travel through the same PUT as its
// prompt, so the dashboard can arm a whole model family in one save.
func TestJailbreakPresetStoresKeywordRules(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	r := setupTestRouter(repo)

	payload, _ := json.Marshal(map[string]any{
		"id":     "deepseek",
		"name":   "Deepseek",
		"prompt": "DS RULE",
		"keywords": []map[string]any{
			{"text": "DeepSeek"},
			{"text": "^providerx/", "mode": "regex"},
		},
		"excludes": []map[string]any{
			{"text": "deepseek-4-pro"},
		},
	})
	req := httptest.NewRequest(http.MethodPut, "/api/jailbreak/presets", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT preset status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/jailbreak/presets", nil)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	var list []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode presets: %v", err)
	}
	var preset map[string]any
	for _, p := range list {
		if p["id"] == "deepseek" {
			preset = p
		}
	}
	if preset == nil {
		t.Fatalf("preset not found: %+v", list)
	}
	keywords, _ := preset["keywords"].([]any)
	if len(keywords) != 2 {
		t.Fatalf("keywords = %+v, want 2 rules", preset["keywords"])
	}
	// A word rule is stored lowercased; a regex is stored verbatim.
	first, _ := keywords[0].(map[string]any)
	if first["text"] != "deepseek" {
		t.Errorf("word keyword = %v, want the lowercased form", first["text"])
	}
	second, _ := keywords[1].(map[string]any)
	if second["text"] != "^providerx/" || second["mode"] != "regex" {
		t.Errorf("regex keyword = %+v, want it verbatim", second)
	}
	excludes, _ := preset["excludes"].([]any)
	if len(excludes) != 1 {
		t.Fatalf("excludes = %+v, want 1 rule", preset["excludes"])
	}
}

// A rule the gateway would reject must be refused here, with the reason, so
// the operator never saves a preset that silently stops matching.
func TestJailbreakPresetRejectsBadKeywordRule(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	r := setupTestRouter(repo)

	payload, _ := json.Marshal(map[string]any{
		"id":       "broken",
		"name":     "Broken",
		"prompt":   "x",
		"keywords": []map[string]any{{"text": "(unclosed", "mode": "regex"}},
	})
	req := httptest.NewRequest(http.MethodPut, "/api/jailbreak/presets", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "regex") {
		t.Errorf("error body = %s, want the reason", rec.Body.String())
	}
}

// The global exclude list round-trips through its own endpoints and denies
// every preset, including an exact binding.
func TestJailbreakGlobalExcludeRoutes(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	r := setupTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/api/jailbreak/excludes", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET excludes status = %d, want 200", rec.Code)
	}
	var empty map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &empty); err != nil {
		t.Fatalf("decode empty excludes: %v", err)
	}
	if rules, _ := empty["excludes"].([]any); len(rules) != 0 {
		t.Fatalf("excludes = %+v, want an empty list", empty["excludes"])
	}

	payload, _ := json.Marshal(map[string]any{
		"excludes": []map[string]any{{"text": "providerx/deepseek-4-pro"}},
	})
	req = httptest.NewRequest(http.MethodPut, "/api/jailbreak/excludes", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT excludes status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/jailbreak/excludes", nil)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode excludes: %v", err)
	}
	rules, _ := got["excludes"].([]any)
	if len(rules) != 1 {
		t.Fatalf("excludes = %+v, want 1 rule", got["excludes"])
	}

	// The deny list outranks an exact binding.
	if err := repo.SaveJailbreakPreset(&db.JailbreakDoc{ID: "p", Name: "P", Prompt: "P RULE"}); err != nil {
		t.Fatalf("save preset: %v", err)
	}
	if err := repo.SetJailbreakModelPreset("providerx/deepseek-4-pro", "p", true); err != nil {
		t.Fatalf("bind model: %v", err)
	}
	doc, err := repo.ResolveJailbreakDoc("providerx/deepseek-4-pro")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if doc != nil {
		t.Errorf("globally excluded id resolved to %+v, want no injection", doc)
	}
}

// TestJailbreakResolveRoute pins the test-match endpoint to the table the
// dashboard tests exercise. The production table is covered by
// TestSetupServerRouter_JailbreakRoutesAreLive.
func TestJailbreakResolveRoute(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	r := setupTestRouter(repo)

	// Arm a preset by keyword.
	payload, _ := json.Marshal(map[string]any{
		"id":       "deepseek",
		"name":     "Deepseek",
		"prompt":   "DS RULE",
		"keywords": []map[string]any{{"text": "deepseek"}},
	})
	req := httptest.NewRequest(http.MethodPut, "/api/jailbreak/presets", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("seed preset status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	// A matching id reports the keyword rule.
	body, _ := json.Marshal(map[string]any{"id": "providerx/deepseek-4-pro"})
	req = httptest.NewRequest(http.MethodPost, "/api/jailbreak/resolve", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("resolve status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var match map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &match); err != nil {
		t.Fatalf("decode resolve: %v", err)
	}
	if match["injected"] != true {
		t.Errorf("injected = %v, want true", match["injected"])
	}
	if match["reason"] != "keyword" {
		t.Errorf("reason = %v, want keyword", match["reason"])
	}
	if match["presetId"] != "deepseek" {
		t.Errorf("presetId = %v, want deepseek", match["presetId"])
	}

	// A non-matching id reports no-match.
	body, _ = json.Marshal(map[string]any{"id": "gpt-4o"})
	req = httptest.NewRequest(http.MethodPost, "/api/jailbreak/resolve", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("resolve status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	match = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &match); err != nil {
		t.Fatalf("decode resolve: %v", err)
	}
	if match["injected"] != false {
		t.Errorf("injected = %v, want false", match["injected"])
	}
	if match["reason"] != "no-match" {
		t.Errorf("reason = %v, want no-match", match["reason"])
	}
}

// TestJailbreakDuplicateAndReorder pins the clone and arrangement endpoints to
// the table the dashboard tests exercise.
func TestJailbreakDuplicateAndReorder(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	r := setupTestRouter(repo)

	seed := func(id, name string) {
		payload, _ := json.Marshal(map[string]any{
			"id": id, "name": name, "prompt": name + " RULE",
			"keywords": []map[string]any{{"text": id}},
		})
		req := httptest.NewRequest(http.MethodPut, "/api/jailbreak/presets", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("seed %s status = %d, want 200: %s", id, rec.Code, rec.Body.String())
		}
	}
	seed("alpha", "Alpha")
	seed("beta", "Beta")

	// Duplicate alpha → alpha-copy, keeping its prompt and keyword.
	req := httptest.NewRequest(http.MethodPost, "/api/jailbreak/presets/alpha/duplicate", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("duplicate status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var dup struct {
		Ok     bool            `json:"ok"`
		Preset db.JailbreakDoc `json:"preset"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &dup); err != nil {
		t.Fatalf("decode duplicate: %v", err)
	}
	if dup.Preset.ID != "alpha-copy" {
		t.Errorf("duplicate id = %q, want alpha-copy", dup.Preset.ID)
	}
	if dup.Preset.Prompt != "Alpha RULE" {
		t.Errorf("duplicate prompt = %q, want the source prompt", dup.Preset.Prompt)
	}
	if len(dup.Preset.Keywords) != 1 || dup.Preset.Keywords[0].Text != "alpha" {
		t.Errorf("duplicate keywords = %+v, want the source keyword", dup.Preset.Keywords)
	}

	// Reorder: beta first, then alpha-copy, then alpha.
	order, _ := json.Marshal(map[string]any{"order": []string{"beta", "alpha-copy", "alpha"}})
	req = httptest.NewRequest(http.MethodPut, "/api/jailbreak/presets/order", bytes.NewReader(order))
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("reorder status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	presets, err := repo.ListJailbreakPresets()
	if err != nil {
		t.Fatalf("list presets: %v", err)
	}
	if len(presets) != 3 {
		t.Fatalf("preset count = %d, want 3", len(presets))
	}
	want := []string{"beta", "alpha-copy", "alpha"}
	for i, id := range want {
		if presets[i].ID != id {
			t.Errorf("preset[%d] = %q, want %q (full order %s)", i, presets[i].ID, id, presetIDs(presets))
		}
	}
}

func presetIDs(list []*db.JailbreakDoc) []string {
	out := make([]string, 0, len(list))
	for _, d := range list {
		out = append(out, d.ID)
	}
	return out
}

// TestJailbreakUsageCounter pins the per-preset injection counter.
func TestJailbreakUsageCounter(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	if err := repo.RecordJailbreakUsage("deepseek"); err != nil {
		t.Fatalf("record usage: %v", err)
	}
	if err := repo.RecordJailbreakUsage("deepseek"); err != nil {
		t.Fatalf("record usage again: %v", err)
	}
	if err := repo.RecordJailbreakUsage("soul"); err != nil {
		t.Fatalf("record soul usage: %v", err)
	}
	usage, err := repo.JailbreakUsage()
	if err != nil {
		t.Fatalf("read usage: %v", err)
	}
	if usage["deepseek"] != 2 {
		t.Errorf("deepseek = %d, want 2", usage["deepseek"])
	}
	if usage["soul"] != 1 {
		t.Errorf("soul = %d, want 1", usage["soul"])
	}
	if _, ok := usage["never-used"]; ok {
		t.Errorf("never-used present, want absent")
	}
}
