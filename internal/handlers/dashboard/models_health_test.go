package dashboard

import (
	"bytes"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"
)

// getModelHealth reads GET /api/models/health and decodes the envelope.
func getModelHealth(t *testing.T, router http.Handler) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/models/health", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/models/health = %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return body
}

// putModelHealth sends one PUT and returns the recorder.
func putModelHealth(t *testing.T, router http.Handler, payload map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/models/health", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// TestModelHealthDefaultsToOff is the safety property: a fresh install must
// answer with the loop switched off, so nothing probes upstream models until
// an operator asks.
func TestModelHealthDefaultsToOff(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	body := getModelHealth(t, setupTestRouter(repo))

	cfg, ok := body["config"].(map[string]any)
	if !ok {
		t.Fatalf("config missing or wrong shape: %v", body["config"])
	}
	if cfg["enabled"] != false {
		t.Errorf("enabled = %v, want false on a fresh install", cfg["enabled"])
	}
	if cfg["threshold"] != float64(3) {
		t.Errorf("threshold = %v, want the default 3", cfg["threshold"])
	}
	if cfg["scope"] != "history" {
		t.Errorf("scope = %v, want \"history\"", cfg["scope"])
	}
}

// TestModelHealthSaveMerges covers the merge contract: a payload naming one
// field must not wipe the others, which is how a half-written settings request
// used to switch the loop off.
func TestModelHealthSaveMerges(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	rec := putModelHealth(t, router, map[string]any{
		"enabled":     true,
		"intervalMin": float64(30),
		"threshold":   float64(5),
		"scope":       "failed",
		"autoEnable":  true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("first save = %d: %s", rec.Code, rec.Body.String())
	}

	// A second payload naming only the threshold.
	rec = putModelHealth(t, router, map[string]any{"threshold": float64(4)})
	if rec.Code != http.StatusOK {
		t.Fatalf("second save = %d: %s", rec.Code, rec.Body.String())
	}

	cfg := getModelHealth(t, router)["config"].(map[string]any)
	if cfg["threshold"] != float64(4) {
		t.Errorf("threshold = %v, want 4", cfg["threshold"])
	}
	if cfg["enabled"] != true {
		t.Error("enabled must survive a payload that did not mention it")
	}
	if cfg["intervalMin"] != float64(30) {
		t.Errorf("intervalMin = %v, want 30 carried over", cfg["intervalMin"])
	}
	if cfg["scope"] != "failed" {
		t.Errorf("scope = %v, want \"failed\" carried over", cfg["scope"])
	}
	if cfg["autoEnable"] != true {
		t.Error("autoEnable must survive a payload that did not mention it")
	}
}

// TestModelHealthRejectsUnknownScope covers validation on write rather than on
// read, so a typo in the dashboard cannot reach the loop.
func TestModelHealthRejectsUnknownScope(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	rec := putModelHealth(t, setupTestRouter(repo), map[string]any{"scope": "everything"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

// TestModelHealthStoresEffectiveThreshold pins that what the dashboard reads
// back is what the loop runs with: a threshold of 1 is stored as the minimum.
func TestModelHealthStoresEffectiveThreshold(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	rec := putModelHealth(t, router, map[string]any{"threshold": float64(1)})
	if rec.Code != http.StatusOK {
		t.Fatalf("save = %d: %s", rec.Code, rec.Body.String())
	}
	cfg := getModelHealth(t, router)["config"].(map[string]any)
	if cfg["threshold"] != float64(3) {
		t.Errorf("threshold = %v, want the effective default 3", cfg["threshold"])
	}
}

// TestModelHealthAuditRoundTrip covers the audit half of the endpoint: a line
// written into the KV scope has to come back out, because the panel renders
// the scheduler's history from this one response.
func TestModelHealthAuditRoundTrip(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	if err := repo.SetKV("modelHealth.audit", "log",
		`[{"provider":"vsllm","model":"m1","action":"disabled","reason":"boom","actor":"scheduler","at":"2026-10-25T12:00:00Z"}]`); err != nil {
		t.Fatalf("seed audit: %v", err)
	}

	body := getModelHealth(t, setupTestRouter(repo))
	audit, ok := body["audit"].([]any)
	if !ok {
		t.Fatalf("audit missing or wrong shape: %v", body["audit"])
	}
	if len(audit) != 1 {
		t.Fatalf("audit has %d entries, want 1", len(audit))
	}
	entry := audit[0].(map[string]any)
	if entry["model"] != "m1" || entry["action"] != "disabled" || entry["actor"] != "scheduler" {
		t.Errorf("audit entry = %v, want the seeded line", entry)
	}
}

// TestModelHealthReportsFailureCounters covers the third half: the panel shows
// "2 of 3 failures" beside a model, which needs the counters in the same
// response.
func TestModelHealthReportsFailureCounters(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	if err := repo.SetKV("modelHealth.failures", "vsllm/m1",
		`{"count":2,"lastError":"boom","disabledByScheduler":true}`); err != nil {
		t.Fatalf("seed failures: %v", err)
	}

	body := getModelHealth(t, setupTestRouter(repo))
	failures, ok := body["failures"].(map[string]any)
	if !ok {
		t.Fatalf("failures missing or wrong shape: %v", body["failures"])
	}
	entry, ok := failures["vsllm/m1"].(map[string]any)
	if !ok {
		t.Fatalf("vsllm/m1 missing from failures: %v", failures)
	}
	if entry["count"] != float64(2) {
		t.Errorf("count = %v, want 2", entry["count"])
	}
	if entry["disabledByScheduler"] != true {
		t.Error("disabledByScheduler must be reported, or the panel cannot tell a scheduler disable from an operator's")
	}
}
