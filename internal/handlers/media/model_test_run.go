package media

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"9router/proxy/internal/handlerutil"
)

// The dashboard's "test models" page probes many models at once. The per-model
// button in ProviderDetailView can run its probe inside the click's request and
// render the response directly; a page that sweeps a selection cannot, because
// the operator navigates away, reloads, or closes the tab while the sweep is
// still talking to providers. This runner owns that sweep:
//
//   - it lives on the MediaHandler, so its lifetime is the process's, not a
//     request's, and a run started from one page load is still reported to the
//     next;
//   - it reuses probeModel, so the batch verdict and the button verdict come
//     from exactly the same dispatch and the same pass/fail rules (model_test_ping.go);
//   - it probes sequentially, because the probes share the provider accounts
//     the selector rotates through, and firing a pageful of them in parallel
//     would race that rotation rather than measure it.
//
// State is in-process only. A restart ends an in-flight run; the page then sees
// no run and lets the operator start again. Persisting a run to SQLite would
// buy recovery from a restart that also failed every in-flight probe, which is
// not worth a schema.

var (
	errModelTestRunning   = errors.New("a model test run is already in progress")
	errModelTestNoTargets = errors.New("no models selected")
	errModelTestBadKind   = errors.New("unsupported model kind")
)

// modelTestTarget is one probe the dashboard asked for. Either a model —
// "test this model on its kind's endpoint" — or a provider (Model empty):
// "test this provider across every chat model it serves", the
// connected-provider mode.
type modelTestTarget struct {
	Model    string `json:"model"`
	Kind     string `json:"kind"`
	Provider string `json:"provider"`
}

// modelTestState is a row's lifecycle. A canceled run marks every row it did
// not finish, so a stopped sweep never leaves rows reading "pending" forever.
type modelTestState string

const (
	testPending  modelTestState = "pending"
	testRunning  modelTestState = "running"
	testPass     modelTestState = "pass"
	testFail     modelTestState = "fail"
	testBlocked  modelTestState = "blocked"
	testCanceled modelTestState = "canceled"
)

// modelTestModelResult is one model's verdict inside a provider row, so a
// dead model reads as a dead model rather than as a dead provider.
type modelTestModelResult struct {
	Model      string         `json:"model"`
	State      modelTestState `json:"state"`
	OK         bool           `json:"ok"`
	LatencyMs  int64          `json:"latencyMs"`
	HTTPStatus int            `json:"httpStatus,omitempty"`
	Error      string         `json:"error,omitempty"`
	Blocked    bool           `json:"blocked,omitempty"`
	ResetAt    string         `json:"resetAt,omitempty"`
}

// providerProbeResult is a provider sweep's outcome: the aggregate verdict
// the provider row reports, plus the per-model breakdown it came from.
type providerProbeResult struct {
	testModelResult
	Models []modelTestModelResult
}

// modelTestEntry is one row of a run, carrying the probe verdict verbatim. A
// provider row sets Provider and leaves Model/Kind empty, and carries the
// per-model breakdown of its sweep in Models; a model row does the reverse.
type modelTestEntry struct {
	Model      string                 `json:"model"`
	Kind       string                 `json:"kind"`
	Provider   string                 `json:"provider,omitempty"`
	State      modelTestState         `json:"state"`
	OK         bool                   `json:"ok"`
	LatencyMs  int64                  `json:"latencyMs"`
	HTTPStatus int                    `json:"httpStatus,omitempty"`
	Note       string                 `json:"note,omitempty"`
	Error      string                 `json:"error,omitempty"`
	Blocked    bool                   `json:"blocked,omitempty"`
	ResetAt    string                 `json:"resetAt,omitempty"`
	Models     []modelTestModelResult `json:"models,omitempty"`
}

// modelTestRunSnapshot is what the status endpoint serves. Counts are derived
// so the page never has to walk the rows to render a summary.
type modelTestRunSnapshot struct {
	ID         string           `json:"id"`
	Running    bool             `json:"running"`
	StartedAt  string           `json:"startedAt"`
	FinishedAt string           `json:"finishedAt,omitempty"`
	Total      int              `json:"total"`
	Done       int              `json:"done"`
	Passed     int              `json:"passed"`
	Failed     int              `json:"failed"`
	Blocked    int              `json:"blocked"`
	Entries    []modelTestEntry `json:"entries"`
}

type modelTestRun struct {
	id         string
	startedAt  time.Time
	finishedAt time.Time
	running    bool
	entries    []modelTestEntry
}

// modelTestRunner serialises batch runs. Only one run is live at a time: the
// probes contend for the same provider accounts, and two concurrent sweeps
// would rotate those accounts against each other.
type modelTestRunner struct {
	probe         func(context.Context, string, string) testModelResult
	probeProvider func(context.Context, string) providerProbeResult

	mu     sync.Mutex
	run    *modelTestRun
	cancel context.CancelFunc
	seq    int64
}

func newModelTestRunner(probe func(context.Context, string, string) testModelResult, probeProvider func(context.Context, string) providerProbeResult) *modelTestRunner {
	return &modelTestRunner{probe: probe, probeProvider: probeProvider}
}

// Start validates the selection and launches the run. It returns the initial
// snapshot, every row pending. A second Start while one is live is refused with
// errModelTestRunning rather than superseding: the operator's first sweep is
// still writing verdicts, and silently abandoning it would lose them.
func (m *modelTestRunner) Start(targets []modelTestTarget) (*modelTestRunSnapshot, error) {
	cleaned, err := validateTargets(targets)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.run != nil && m.run.running {
		return nil, errModelTestRunning
	}

	m.seq++
	run := &modelTestRun{
		id:        fmt.Sprintf("mt-%d-%d", time.Now().UnixMilli(), m.seq),
		startedAt: time.Now(),
		running:   true,
		entries:   make([]modelTestEntry, len(cleaned)),
	}
	for i, t := range cleaned {
		entry := modelTestEntry{State: testPending}
		if t.Provider != "" {
			entry.Provider = t.Provider
		} else {
			entry.Model, entry.Kind = t.Model, t.Kind
		}
		run.entries[i] = entry
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.run = run
	m.cancel = cancel
	go m.execute(ctx, run)
	return run.snapshot(), nil
}

// Snapshot reports the current or last run. With no run yet it returns an idle
// snapshot, so the page renders "not started" instead of having to handle null.
func (m *modelTestRunner) Snapshot() *modelTestRunSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.run == nil {
		return &modelTestRunSnapshot{Entries: []modelTestEntry{}}
	}
	return m.run.snapshot()
}

// Cancel stops the live run. The request currently in flight is abandoned via
// its context; execute finalises the snapshot on its way out.
func (m *modelTestRunner) Cancel() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		m.cancel()
	}
}

// execute walks the rows once. It re-checks that the run it started is still the
// current one after every probe, so a run that Cancel already finalised cannot
// have verdicts written into it.
func (m *modelTestRunner) execute(ctx context.Context, run *modelTestRun) {
	defer func() {
		if r := recover(); r != nil {
			m.finalize(run, fmt.Errorf("runner panicked: %v", r))
		}
	}()

	for i := range run.entries {
		if ctx.Err() != nil {
			break
		}
		entry := run.entries[i]

		m.mu.Lock()
		if m.run != run {
			m.mu.Unlock()
			return
		}
		entry.State = testRunning
		run.entries[i] = entry
		m.mu.Unlock()

		verdict, breakdown := m.probeEntry(ctx, entry)

		m.mu.Lock()
		if m.run == run {
			if ctx.Err() != nil && !verdict.OK {
				entry.State = testCanceled
			} else {
				entry.applyVerdict(verdict)
			}
			if breakdown != nil {
				entry.Models = breakdown
			}
			run.entries[i] = entry
		}
		m.mu.Unlock()
	}

	m.finalize(run, nil)
}

// finalize closes the run: rows that never finished read "canceled", and the
// snapshot stops advertising the run as live.
func (m *modelTestRunner) finalize(run *modelTestRun, cause error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.run != run {
		return
	}
	for i := range run.entries {
		if run.entries[i].State == testPending || run.entries[i].State == testRunning {
			run.entries[i].State = testCanceled
			if cause != nil && run.entries[i].Error == "" {
				run.entries[i].Error = cause.Error()
			}
		}
	}
	run.running = false
	run.finishedAt = time.Now()
	m.cancel = nil
}

// probeEntry isolates a single probe from a panic. A MediaHandler built for the
// relay routes has a nil ChatH, and a media or provider probe reaches it; a
// panic there must fail the row, not take down the process (zero-panic policy).
func (m *modelTestRunner) probeEntry(ctx context.Context, entry modelTestEntry) (res testModelResult, breakdown []modelTestModelResult) {
	defer func() {
		if r := recover(); r != nil {
			res = testModelResult{Error: fmt.Sprintf("internal probe error: %v", r)}
			breakdown = nil
		}
	}()
	if err := ctx.Err(); err != nil {
		return testModelResult{Error: err.Error()}, nil
	}
	if entry.Provider != "" {
		out := m.probeProvider(ctx, entry.Provider)
		return out.testModelResult, out.Models
	}
	return m.probe(ctx, entry.Model, entry.Kind), nil
}

func validateTargets(targets []modelTestTarget) ([]modelTestTarget, error) {
	if len(targets) == 0 {
		return nil, errModelTestNoTargets
	}
	cleaned := make([]modelTestTarget, 0, len(targets))
	for _, t := range targets {
		provider := strings.TrimSpace(t.Provider)
		if provider != "" {
			cleaned = append(cleaned, modelTestTarget{Provider: provider})
			continue
		}
		model := strings.TrimSpace(t.Model)
		if model == "" {
			continue
		}
		if !isProbeKind(t.Kind) {
			return nil, fmt.Errorf("%w: %q", errModelTestBadKind, t.Kind)
		}
		cleaned = append(cleaned, modelTestTarget{Model: model, Kind: t.Kind})
	}
	if len(cleaned) == 0 {
		return nil, errModelTestNoTargets
	}
	return cleaned, nil
}

func (e *modelTestEntry) applyVerdict(r testModelResult) {
	e.OK = r.OK
	e.LatencyMs = r.LatencyMs
	e.HTTPStatus = r.Status
	e.Note = r.Note
	e.Error = r.Error
	e.Blocked = r.Blocked
	e.ResetAt = r.ResetAt
	switch {
	case r.Blocked:
		e.State = testBlocked
	case r.OK:
		e.State = testPass
	default:
		e.State = testFail
	}
}

func (r *modelTestRun) snapshot() *modelTestRunSnapshot {
	snap := &modelTestRunSnapshot{
		ID:        r.id,
		Running:   r.running,
		StartedAt: r.startedAt.UTC().Format(time.RFC3339),
		Total:     len(r.entries),
		Entries:   append([]modelTestEntry(nil), r.entries...),
	}
	if !r.finishedAt.IsZero() {
		snap.FinishedAt = r.finishedAt.UTC().Format(time.RFC3339)
	}
	for _, e := range r.entries {
		switch e.State {
		case testPass:
			snap.Passed++
		case testFail:
			snap.Failed++
		case testBlocked:
			snap.Blocked++
		}
		if e.State != testPending && e.State != testRunning {
			snap.Done++
		}
	}
	return snap
}

// HandleStartModelTestRun handles POST /api/model-tests/runs. Body:
// {"targets":[{"model":"<provider>/<model>","kind":"<service kind>"}]} for
// per-model testing, or {"targets":[{"provider":"<provider id>"}]} for the
// connected-provider mode (each provider probed across all its chat models).
// A 202
// carries the initial snapshot; 409 means a run is already live; 400 covers an
// empty selection or an unknown kind.
func (h *MediaHandler) HandleStartModelTestRun(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var req struct {
		Targets []modelTestTarget `json:"targets"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	snap, err := h.testRunner.Start(req.Targets)
	switch {
	case errors.Is(err, errModelTestRunning):
		handlerutil.WriteJSONError(w, http.StatusConflict, err.Error())
	case err != nil:
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
	default:
		handlerutil.WriteJSON(w, http.StatusAccepted, snap)
	}
}

// HandleModelTestRunStatus handles GET /api/model-tests/runs/current.
func (h *MediaHandler) HandleModelTestRunStatus(w http.ResponseWriter, r *http.Request) {
	handlerutil.WriteJSON(w, http.StatusOK, h.testRunner.Snapshot())
}

// HandleCancelModelTestRun handles DELETE /api/model-tests/runs/current.
func (h *MediaHandler) HandleCancelModelTestRun(w http.ResponseWriter, r *http.Request) {
	h.testRunner.Cancel()
	handlerutil.WriteJSON(w, http.StatusOK, h.testRunner.Snapshot())
}
