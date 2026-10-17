package dashboard

import (
	"bytes"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"
)

// planRequest is this file's own write shape: it must not depend on the
// handler's struct, or a rename there would keep these tests green.
func planBody(fields map[string]any) *bytes.Reader {
	raw, _ := json.Marshal(fields)
	return bytes.NewReader(raw)
}

func doJSON(t *testing.T, router http.Handler, method, path string, body *bytes.Reader) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == nil {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, body)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func decodePlan(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal plan response: %v (body %s)", err, rec.Body.String())
	}
	return out
}

// TestPlansCreateListUpdateDelete drives the whole CRUD surface the dashboard
// uses, including the boolean and the allowlist array surviving the round trip.
func TestPlansCreateListUpdateDelete(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	if err := repo.UpdateSettingsRaw(map[string]any{"requireLogin": false}); err != nil {
		t.Fatalf("disable requireLogin: %v", err)
	}
	router := setupTestRouter(repo)

	rec := doJSON(t, router, http.MethodPost, "/api/plans", planBody(map[string]any{
		"name":             "Pro",
		"description":      "paid tier",
		"limitMicros":      5000000,
		"limitTokens":      1000000,
		"rpmLimit":         120,
		"tpmLimit":         90000,
		"concurrencyLimit": 4,
		"period":           "weekly",
		"alertPct":         75,
		"hardCutoff":       true,
		"allowedModels":    []string{"gpt-4o", "claude-*"},
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("create plan expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	created := decodePlan(t, rec)
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatal("created plan has no id")
	}
	if created["hardCutoff"] != true {
		t.Errorf("created hardCutoff = %v, want true", created["hardCutoff"])
	}
	if created["rpmLimit"] != float64(120) {
		t.Errorf("created rpmLimit = %v, want 120", created["rpmLimit"])
	}
	models, _ := created["allowedModels"].([]any)
	if len(models) != 2 {
		t.Errorf("created allowedModels = %v, want two entries", created["allowedModels"])
	}

	// The seeded default plan is listed alongside it.
	rec = doJSON(t, router, http.MethodGet, "/api/plans", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list plans expected 200, got %d", rec.Code)
	}
	var list struct {
		Plans []map[string]any `json:"plans"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if len(list.Plans) != 2 {
		t.Fatalf("list returned %d plans, want 2 (default + created)", len(list.Plans))
	}

	// A partial update keeps the fields it does not mention.
	rec = doJSON(t, router, http.MethodPut, "/api/plans/"+id, planBody(map[string]any{
		"name":     "Pro Plus",
		"rpmLimit": 300,
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("update plan expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	updated := decodePlan(t, rec)
	if updated["name"] != "Pro Plus" || updated["rpmLimit"] != float64(300) {
		t.Errorf("updated plan = %v, want name Pro Plus and rpmLimit 300", updated)
	}
	if updated["tpmLimit"] != float64(90000) {
		t.Errorf("tpmLimit = %v after update, want the stored 90000 preserved", updated["tpmLimit"])
	}

	// Deleting a plan nothing references succeeds.
	rec = doJSON(t, router, http.MethodDelete, "/api/plans/"+id, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete plan expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, router, http.MethodGet, "/api/plans/"+id, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get deleted plan expected 404, got %d", rec.Code)
	}
}

// TestDeletePlanInUseIsRefused proves the 409 guard: a plan keys still
// reference cannot be deleted, so their limits can never change silently.
func TestDeletePlanInUseIsRefused(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	if err := repo.UpdateSettingsRaw(map[string]any{"requireLogin": false}); err != nil {
		t.Fatalf("disable requireLogin: %v", err)
	}
	router := setupTestRouter(repo)

	rec := doJSON(t, router, http.MethodPost, "/api/plans", planBody(map[string]any{"name": "Team"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("create plan expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	planID, _ := decodePlan(t, rec)["id"].(string)

	id, _ := createTestKey(t, router, "Planned Key")
	rec = doJSON(t, router, http.MethodPut, "/api/keys/"+id, planBody(map[string]any{"planId": planID}))
	if rec.Code != http.StatusOK {
		t.Fatalf("assign plan expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got, _ := decodePlan(t, rec)["planId"].(string); got != planID {
		t.Errorf("assign response planId = %q, want %q", got, planID)
	}

	rec = doJSON(t, router, http.MethodGet, "/api/plans/"+planID+"/keys", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("plan keys expected 200, got %d", rec.Code)
	}
	if got := decodePlan(t, rec)["keys"]; got != float64(1) {
		t.Errorf("plan keys = %v, want 1", got)
	}

	rec = doJSON(t, router, http.MethodDelete, "/api/plans/"+planID, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("delete in-use plan expected 409, got %d: %s", rec.Code, rec.Body.String())
	}

	// Detaching the key unblocks it.
	rec = doJSON(t, router, http.MethodPut, "/api/keys/"+id, planBody(map[string]any{"planId": ""}))
	if rec.Code != http.StatusOK {
		t.Fatalf("detach plan expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, router, http.MethodDelete, "/api/plans/"+planID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete after detach expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestAssignUnknownPlanIsRefused guards the dangling-id case: storing a plan
// that does not exist would show a template in the dashboard while the key
// kept resolving to no plan at all.
func TestAssignUnknownPlanIsRefused(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	if err := repo.UpdateSettingsRaw(map[string]any{"requireLogin": false}); err != nil {
		t.Fatalf("disable requireLogin: %v", err)
	}
	router := setupTestRouter(repo)

	id, _ := createTestKey(t, router, "Unplanned Key")
	rec := doJSON(t, router, http.MethodPut, "/api/keys/"+id, planBody(map[string]any{"planId": "ghost"}))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown plan expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestPlanValidationRejectsBadInput covers the two bounds an operator can get
// wrong without noticing: an unknown period and an alert percentage over 100.
func TestPlanValidationRejectsBadInput(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	if err := repo.UpdateSettingsRaw(map[string]any{"requireLogin": false}); err != nil {
		t.Fatalf("disable requireLogin: %v", err)
	}
	router := setupTestRouter(repo)

	tests := []struct {
		name string
		body map[string]any
	}{
		{"unknown period", map[string]any{"name": "P", "period": "yearly"}},
		{"alert over 100", map[string]any{"name": "P", "alertPct": 101}},
		{"negative rpm", map[string]any{"name": "P", "rpmLimit": -1}},
		{"missing name", map[string]any{"rpmLimit": 10}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := doJSON(t, router, http.MethodPost, "/api/plans", planBody(tt.body))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}
