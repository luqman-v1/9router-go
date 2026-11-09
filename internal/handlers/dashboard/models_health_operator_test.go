package dashboard

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/dbtest"
	"9router/proxy/internal/modelhealth"
)

// The disabled-models write must tell the model-health loop what the operator
// decided, model by model. Without it a model the operator re-enables stays
// marked scheduler-owned, so the loop either refuses to disable it again after
// fresh failures or silently re-enables a model the operator switched off by
// hand — both the cases the panel promises not to do.
func TestSaveDisabledModelsRecordsOperatorActions(t *testing.T) {
	repo, cleanup := setupDisabledModelsRepo(t)
	defer cleanup()

	loop := modelhealth.NewLoop(
		modelhealth.RepoStore{Repo: repo},
		func(ctx context.Context, provider, model string) modelhealth.Verdict {
			return modelhealth.Verdict{OK: true}
		},
		func() []modelhealth.Candidate { return nil },
		nil,
	)
	h := NewDashboardHandler(repo)
	h.ModelHealth = loop

	// First write: two models off.
	rec := putDisabledWith(t, repo, h, "vsllm", []string{"m1", "m2"})
	if rec.Code != http.StatusOK {
		t.Fatalf("first PUT = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	// Second write: m2 comes back on, m3 goes off. Only those two are operator
	// actions; m1 was already off and is untouched.
	rec = putDisabledWith(t, repo, h, "vsllm", []string{"m1", "m3"})
	if rec.Code != http.StatusOK {
		t.Fatalf("second PUT = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}

	// AppendAudit PREPENDS, so the log is newest-first and the FIRST entry for
	// a model is the operator's most recent decision. Overwriting in a map
	// would keep the oldest and report the earlier write instead.
	last := map[string]modelhealth.AuditEntry{}
	for _, e := range readAudit(t, repo) {
		if e.Provider == "vsllm" {
			if _, seen := last[e.Model]; !seen {
				last[e.Model] = e
			}
		}
	}
	if last["m2"].Action != modelhealth.ActionEnabled {
		t.Errorf("m2 action = %q, want %q (the operator re-enabled it)", last["m2"].Action, modelhealth.ActionEnabled)
	}
	if last["m3"].Action != modelhealth.ActionDisabled {
		t.Errorf("m3 action = %q, want %q (the operator disabled it)", last["m3"].Action, modelhealth.ActionDisabled)
	}
	// m1 was off before the second write and stays off: no operator action.
	if last["m1"].At != "" && last["m1"].Action == "" {
		t.Errorf("m1 recorded with no action: %+v", last["m1"])
	}
	// The second write must have recorded exactly two actions.
	secondWrite := 0
	for _, e := range readAudit(t, repo) {
		if e.Provider == "vsllm" && (e.Model == "m2" || e.Model == "m3") {
			secondWrite++
		}
	}
	if secondWrite != 3 {
		t.Errorf("m2/m3 entries = %d, want 3 (m2 disabled in write 1, m2 enabled and m3 disabled in write 2)", secondWrite)
	}
	for _, e := range readAudit(t, repo) {
		if e.Provider == "vsllm" && e.Actor != modelhealth.ActorOperator {
			t.Errorf("actor = %q, want %q", e.Actor, modelhealth.ActorOperator)
		}
	}
}

// A manual re-enable must clear the scheduler mark. That is what stops the loop
// from silently re-enabling a model the operator switched off by hand.
func TestSaveDisabledModelsClearsSchedulerMarkOnReEnable(t *testing.T) {
	repo, cleanup := setupDisabledModelsRepo(t)
	defer cleanup()
	store := modelhealth.RepoStore{Repo: repo}
	// The scheduler disabled it, so it is in the stored disabled list AND
	// carries the scheduler mark. Both halves matter: the operator's re-enable
	// is a diff against the stored list, so a model that is not in it is not
	// something the operator can be said to have re-enabled.
	if err := store.SetKV(modelhealth.ScopeFailures, "vsllm/m1", encodeFailure(t, true)); err != nil {
		t.Fatalf("seed failure state: %v", err)
	}
	if err := store.SaveDisabledModels("vsllm", []string{"m1"}); err != nil {
		t.Fatalf("seed disabled list: %v", err)
	}
	h := NewDashboardHandler(repo)
	h.ModelHealth = modelhealth.NewLoop(store, nil, nil, nil)

	// The operator re-enables it: the list goes from ["m1"] to [].
	rec := putDisabledWith(t, repo, h, "vsllm", []string{})
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	raw, err := store.GetKV(modelhealth.ScopeFailures, "vsllm/m1")
	if err != nil {
		t.Fatalf("read failure state: %v", err)
	}
	f := decodeFailuresForTest(t, raw)
	if f.DisabledByScheduler {
		t.Error("DisabledByScheduler still set after the operator re-enabled the model")
	}
}

// A handler with no loop wired must still write the disabled list: the
// recording is an addition, not a precondition.
func TestSaveDisabledModelsWithoutLoopStillWrites(t *testing.T) {
	repo, cleanup := setupDisabledModelsRepo(t)
	defer cleanup()
	rec := putDisabled(t, repo, "vsllm", []string{"m1"})
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	kv, err := repo.GetKVScope("disabledModels")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if kv["vsllm"] != `["m1"]` {
		t.Errorf("stored = %q, want [\"m1\"]", kv["vsllm"])
	}
}

// putDisabled drives the real route table rather than the handler directly, so
// the {provider} path param is populated the way production populates it.
func putDisabled(t *testing.T, repo *db.Repo, provider string, models []string) *httptest.ResponseRecorder {
	t.Helper()
	return putDisabledWith(t, repo, NewDashboardHandler(repo), provider, models)
}

func putDisabledWith(t *testing.T, repo *db.Repo, h *DashboardHandler, provider string, models []string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(models)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/models/disabled/"+provider, bytes.NewReader(body))
	rec := httptest.NewRecorder()
	setupTestRouterWith(repo, h).ServeHTTP(rec, req)
	return rec
}

func setupDisabledModelsRepo(t *testing.T) (*db.Repo, func()) {
	t.Helper()
	database, err := db.OpenDatabase(t.TempDir() + "/disabled-models.sqlite")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	cleanup := func() { _ = database.Close() }
	if err := dbtest.CreateTables(database); err != nil {
		cleanup()
		t.Fatalf("schema: %v", err)
	}
	return db.NewRepo(database), cleanup
}

func readAudit(t *testing.T, repo *db.Repo) []modelhealth.AuditEntry {
	t.Helper()
	raw, err := modelhealth.RepoStore{Repo: repo}.GetKV(modelhealth.ScopeAudit, "log")
	if err != nil || raw == "" {
		return nil
	}
	var entries []modelhealth.AuditEntry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		t.Fatalf("decode audit: %v", err)
	}
	return entries
}

func encodeFailure(t *testing.T, schedulerOwned bool) string {
	t.Helper()
	b, err := json.Marshal(modelhealth.Failures{
		Count:               3,
		DisabledByScheduler: schedulerOwned, EverProbed: true,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// decodeFailuresForTest reads a stored counter row the way the loop does.
func decodeFailuresForTest(t *testing.T, raw string) modelhealth.Failures {
	t.Helper()
	var f modelhealth.Failures
	if err := json.Unmarshal([]byte(raw), &f); err != nil {
		t.Fatalf("decode failures %q: %v", raw, err)
	}
	return f
}
