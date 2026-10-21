package media

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// fakeProbe returns a probe that answers every model with verdict, recording
// the models it saw. It never touches the network, so the runner tests stay
// offline.
func fakeProbe(verdict testModelResult, seen *[]string) func(context.Context, string, string) testModelResult {
	return func(_ context.Context, model, kind string) testModelResult {
		if seen != nil {
			*seen = append(*seen, model+"/"+kind)
		}
		return verdict
	}
}

// fakeProviderProbe answers every provider with verdict, recording what it saw.
func fakeProviderProbe(verdict testModelResult, seen *[]string) func(context.Context, string) providerProbeResult {
	return func(_ context.Context, provider string) providerProbeResult {
		if seen != nil {
			*seen = append(*seen, provider)
		}
		return providerProbeResult{testModelResult: verdict}
	}
}

// newTestRunner builds a runner with both a model probe and a provider probe,
// so a single test covers the per-model and per-provider paths through the same
// runner. All fake probes return verdict; only model ones log a model string.
func newTestRunner(modelVerdict testModelResult, providerVerdict testModelResult, seen *[]string) *modelTestRunner {
	return newModelTestRunner(
		fakeProbe(modelVerdict, seen),
		fakeProviderProbe(providerVerdict, seen),
	)
}

// waitForRunDone polls until the run stops being live, failing the test if it
// never does. The runner executes on its own goroutine, so this is the only
// honest way to observe completion without a probe channel.
func waitForRunDone(t *testing.T, r *modelTestRunner) *modelTestRunSnapshot {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snap := r.Snapshot()
		if !snap.Running {
			return snap
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("run never finished")
	return nil
}

func TestModelTestRunner_StartValidation(t *testing.T) {
	tests := []struct {
		name    string
		targets []modelTestTarget
		wantErr error
	}{
		{
			name:    "empty selection is refused",
			targets: nil,
			wantErr: errModelTestNoTargets,
		},
		{
			name:    "blank models are refused",
			targets: []modelTestTarget{{Model: "   "}},
			wantErr: errModelTestNoTargets,
		},
		{
			name:    "unknown kind is refused",
			targets: []modelTestTarget{{Model: "openai/gpt-4o", Kind: "hologram"}},
			wantErr: errModelTestBadKind,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newModelTestRunner(fakeProbe(testModelResult{OK: true}, nil), fakeProviderProbe(testModelResult{OK: true}, nil))
			_, err := r.Start(tt.targets)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Start() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestModelTestRunner_SuccessMatchesVerdict(t *testing.T) {
	tests := []struct {
		name      string
		verdict   testModelResult
		wantState modelTestState
	}{
		{
			name:      "ok is a pass",
			verdict:   testModelResult{OK: true, Status: 200, LatencyMs: 42},
			wantState: testPass,
		},
		{
			name:      "error is a fail",
			verdict:   testModelResult{Status: 500, Error: "HTTP 500"},
			wantState: testFail,
		},
		{
			name:      "blocked is distinct from fail",
			verdict:   testModelResult{Status: 502, Blocked: true, ResetAt: "2026-01-01T00:00:00Z"},
			wantState: testBlocked,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var seen []string
			r := newModelTestRunner(fakeProbe(tt.verdict, &seen), fakeProviderProbe(testModelResult{OK: true}, nil))
			if _, err := r.Start([]modelTestTarget{{Model: "openai/gpt-4o"}}); err != nil {
				t.Fatalf("Start() error = %v", err)
			}
			snap := waitForRunDone(t, r)

			if len(snap.Entries) != 1 {
				t.Fatalf("entries = %d, want 1", len(snap.Entries))
			}
			if got := snap.Entries[0].State; got != tt.wantState {
				t.Errorf("state = %q, want %q", got, tt.wantState)
			}
			if snap.Done != 1 {
				t.Errorf("done = %d, want 1", snap.Done)
			}
			if len(seen) != 1 || seen[0] != "openai/gpt-4o/" {
				t.Errorf("probe saw %v, want [openai/gpt-4o/]", seen)
			}
		})
	}
}

func TestModelTestRunner_CountsAndOrder(t *testing.T) {
	verdicts := map[string]testModelResult{
		"a/one":   {OK: true, Status: 200},
		"b/two":   {Status: 404, Error: "HTTP 404"},
		"c/three": {Blocked: true, Status: 502},
	}
	probe := func(_ context.Context, model, _ string) testModelResult {
		return verdicts[model]
	}
	r := newModelTestRunner(probe, fakeProviderProbe(testModelResult{OK: true}, nil))
	targets := []modelTestTarget{{Model: "a/one"}, {Model: "b/two"}, {Model: "c/three"}}
	if _, err := r.Start(targets); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	snap := waitForRunDone(t, r)

	if snap.Total != 3 || snap.Done != 3 {
		t.Fatalf("total/done = %d/%d, want 3/3", snap.Total, snap.Done)
	}
	if snap.Passed != 1 || snap.Failed != 1 || snap.Blocked != 1 {
		t.Errorf("passed/failed/blocked = %d/%d/%d, want 1/1/1", snap.Passed, snap.Failed, snap.Blocked)
	}
	wantOrder := []string{"a/one", "b/two", "c/three"}
	for i, want := range wantOrder {
		if snap.Entries[i].Model != want {
			t.Errorf("entry %d model = %q, want %q", i, snap.Entries[i].Model, want)
		}
	}
}

func TestModelTestRunner_RefusesConcurrentRun(t *testing.T) {
	release := make(chan struct{})
	probe := func(ctx context.Context, _, _ string) testModelResult {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return testModelResult{OK: true, Status: 200}
	}
	r := newModelTestRunner(probe, fakeProviderProbe(testModelResult{OK: true}, nil))
	if _, err := r.Start([]modelTestTarget{{Model: "openai/gpt-4o"}}); err != nil {
		t.Fatalf("first Start() error = %v", err)
	}

	if _, err := r.Start([]modelTestTarget{{Model: "openai/gpt-4o"}}); !errors.Is(err, errModelTestRunning) {
		t.Fatalf("second Start() error = %v, want errModelTestRunning", err)
	}

	close(release)
	waitForRunDone(t, r)
}

func TestModelTestRunner_CancelFinalisesRemainingRows(t *testing.T) {
	release := make(chan struct{})
	probe := func(ctx context.Context, _, _ string) testModelResult {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return testModelResult{OK: true, Status: 200}
	}
	r := newModelTestRunner(probe, fakeProviderProbe(testModelResult{OK: true}, nil))
	targets := []modelTestTarget{{Model: "a/one"}, {Model: "b/two"}, {Model: "c/three"}}
	if _, err := r.Start(targets); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	r.Cancel()
	// Unblock the in-flight probe so execute can finalise.
	close(release)
	snap := waitForRunDone(t, r)

	for i, e := range snap.Entries {
		if e.State != testCanceled {
			t.Errorf("entry %d state = %q, want %q", i, e.State, testCanceled)
		}
	}
	if snap.Passed != 0 || snap.Failed != 0 {
		t.Errorf("a canceled run scored passed=%d failed=%d, want 0/0", snap.Passed, snap.Failed)
	}
}

func TestModelTestRunner_ProbePanicFailsRowOnly(t *testing.T) {
	probe := func(_ context.Context, model, _ string) testModelResult {
		if model == "boom/x" {
			panic("kaboom")
		}
		return testModelResult{OK: true, Status: 200}
	}
	r := newModelTestRunner(probe, fakeProviderProbe(testModelResult{OK: true}, nil))
	targets := []modelTestTarget{{Model: "boom/x"}, {Model: "ok/y"}}
	if _, err := r.Start(targets); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	snap := waitForRunDone(t, r)

	if snap.Entries[0].State != testFail {
		t.Errorf("panicking row state = %q, want %q", snap.Entries[0].State, testFail)
	}
	if snap.Entries[1].State != testPass {
		t.Errorf("healthy row state = %q, want %q", snap.Entries[1].State, testPass)
	}
}

func TestModelTestRunner_SnapshotIdleWhenNoRun(t *testing.T) {
	r := newModelTestRunner(fakeProbe(testModelResult{OK: true}, nil), fakeProviderProbe(testModelResult{OK: true}, nil))
	snap := r.Snapshot()
	if snap.Running || snap.Total != 0 || snap.Entries == nil {
		t.Fatalf("idle snapshot = %+v, want non-nil empty entries", snap)
	}
}

func TestStartModelTestRunHandler(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{
			name:       "valid selection is accepted",
			body:       `{"targets":[{"model":"openai/gpt-4o"}]}`,
			wantStatus: http.StatusAccepted,
		},
		{
			name:       "malformed body is a 400",
			body:       `{not json`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "empty selection is a 400",
			body:       `{"targets":[]}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "unknown kind is a 400",
			body:       `{"targets":[{"model":"x/y","kind":"hologram"}]}`,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &MediaHandler{testRunner: newModelTestRunner(fakeProbe(testModelResult{OK: true}, nil), fakeProviderProbe(testModelResult{OK: true}, nil))}
			req := httptest.NewRequest(http.MethodPost, "/api/model-tests/runs", bytes.NewBufferString(tt.body))
			rec := httptest.NewRecorder()
			h.HandleStartModelTestRun(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.wantStatus == http.StatusAccepted {
				var snap modelTestRunSnapshot
				if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
					t.Fatalf("response not a snapshot: %v", err)
				}
				if snap.Total != 1 {
					t.Errorf("snapshot total = %d, want 1", snap.Total)
				}
			}
		})
	}
}

func TestModelTestRunHandlers_StatusAndCancel(t *testing.T) {
	release := make(chan struct{})
	probe := func(ctx context.Context, _, _ string) testModelResult {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return testModelResult{OK: true, Status: 200}
	}
	h := &MediaHandler{testRunner: newModelTestRunner(probe, fakeProviderProbe(testModelResult{OK: true}, nil))}
	start := httptest.NewRequest(http.MethodPost, "/api/model-tests/runs",
		bytes.NewBufferString(`{"targets":[{"model":"a/one"},{"model":"b/two"}]}`))
	h.HandleStartModelTestRun(httptest.NewRecorder(), start)

	rec := httptest.NewRecorder()
	h.HandleModelTestRunStatus(rec, httptest.NewRequest(http.MethodGet, "/api/model-tests/runs/current", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200", rec.Code)
	}
	var snap modelTestRunSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatalf("status body not a snapshot: %v", err)
	}
	if !snap.Running || snap.Total != 2 {
		t.Fatalf("snapshot running/total = %v/%d, want true/2", snap.Running, snap.Total)
	}

	close(release)
	cancelRec := httptest.NewRecorder()
	h.HandleCancelModelTestRun(cancelRec, httptest.NewRequest(http.MethodDelete, "/api/model-tests/runs/current", nil))
	if cancelRec.Code != http.StatusOK {
		t.Fatalf("cancel status = %d, want 200", cancelRec.Code)
	}
	waitForRunDone(t, h.testRunner)
}
