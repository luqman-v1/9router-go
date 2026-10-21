package media

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestValidateTargets_WithProviderTargets(t *testing.T) {
	tests := []struct {
		name    string
		targets []modelTestTarget
		wantErr error
	}{
		{
			name:    "provider ids are accepted",
			targets: []modelTestTarget{{Provider: "openai"}},
			wantErr: nil,
		},
		{
			name:    "blank provider is dropped",
			targets: []modelTestTarget{{Provider: "   "}},
			wantErr: errModelTestNoTargets,
		},
		{
			name:    "unknown kind still rejected on model entries",
			targets: []modelTestTarget{{Model: "x/y", Kind: "hologram"}},
			wantErr: errModelTestBadKind,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newTestRunner(testModelResult{OK: true}, testModelResult{OK: true}, nil)
			_, err := r.Start(tt.targets)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Start() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestModelTestRunner_ProviderTargets(t *testing.T) {
	var seen []string
	r := newTestRunner(testModelResult{OK: true}, testModelResult{OK: true, Status: 200, LatencyMs: 7}, &seen)
	targets := []modelTestTarget{
		{Provider: "openai"},
		{Provider: "  "},
		{Model: "openai/gpt-4o"},
	}
	if _, err := r.Start(targets); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	snap := waitForRunDone(t, r)

	if snap.Total != 2 {
		t.Fatalf("total = %d, want 2 (blank provider dropped)", snap.Total)
	}
	first := snap.Entries[0]
	if first.Provider != "openai" {
		t.Errorf("entry provider = %q, want openai", first.Provider)
	}
	if first.Model != "" || first.Kind != "" {
		t.Errorf("provider entry carries model %q kind %q, want both empty", first.Model, first.Kind)
	}
	if first.State != testPass {
		t.Errorf("provider state = %q, want pass", first.State)
	}
	second := snap.Entries[1]
	if second.Model != "openai/gpt-4o" || second.Provider != "" {
		t.Errorf("model entry = %+v, want model row without provider", second)
	}
	if len(seen) != 2 || seen[0] != "openai" {
		t.Errorf("provider probe saw %v, want [openai ...]", seen)
	}
}

func TestModelTestRunner_ProviderFailVerdict(t *testing.T) {
	r := newTestRunner(
		testModelResult{OK: true},
		testModelResult{Status: 502, Error: "no active connections for provider: openai"},
		nil,
	)
	if _, err := r.Start([]modelTestTarget{{Provider: "openai"}}); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	snap := waitForRunDone(t, r)
	if snap.Entries[0].State != testFail {
		t.Errorf("state = %q, want fail", snap.Entries[0].State)
	}
	if snap.Failed != 1 {
		t.Errorf("failed = %d, want 1", snap.Failed)
	}
}

func TestModelTestRunner_ProviderBreakdown(t *testing.T) {
	probe := func(_ context.Context, provider string) providerProbeResult {
		return providerProbeResult{
			testModelResult: testModelResult{OK: true, Status: 200, Note: "1 of 2 models answered"},
			Models: []modelTestModelResult{
				{Model: provider + "/live", State: testPass, OK: true, LatencyMs: 5},
				{Model: provider + "/dead", State: testFail, Error: "upstream 501", HTTPStatus: 501},
			},
		}
	}
	r := newModelTestRunner(fakeProbe(testModelResult{OK: true}, nil), probe)
	if _, err := r.Start([]modelTestTarget{{Provider: "omop"}}); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	snap := waitForRunDone(t, r)
	entry := snap.Entries[0]
	if entry.State != testPass {
		t.Errorf("provider state = %q, want pass when any model answers", entry.State)
	}
	if len(entry.Models) != 2 {
		t.Fatalf("breakdown rows = %d, want 2", len(entry.Models))
	}
	if entry.Models[1].State != testFail || entry.Models[1].Error != "upstream 501" {
		t.Errorf("dead model row = %+v, want the failure kept verbatim", entry.Models[1])
	}
	if snap.Passed != 1 || snap.Failed != 0 {
		t.Errorf("counts passed/failed = %d/%d, want 1/0 (the row is the provider)", snap.Passed, snap.Failed)
	}
}

func TestModelTestRunner_ProviderPanicFailsRowOnly(t *testing.T) {
	probe := func(_ context.Context, provider string) providerProbeResult {
		if provider == "boom" {
			panic("kaboom")
		}
		return providerProbeResult{testModelResult: testModelResult{OK: true, Status: 200}}
	}
	r := newModelTestRunner(fakeProbe(testModelResult{OK: true}, nil), probe)
	if _, err := r.Start([]modelTestTarget{{Provider: "boom"}, {Provider: "ok"}}); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	snap := waitForRunDone(t, r)
	if snap.Entries[0].State != testFail {
		t.Errorf("panicking provider state = %q, want fail", snap.Entries[0].State)
	}
	if snap.Entries[1].State != testPass {
		t.Errorf("healthy provider state = %q, want pass", snap.Entries[1].State)
	}
}

func TestStartModelTestRunHandler_ProviderTargets(t *testing.T) {
	h := &MediaHandler{
		testRunner: newTestRunner(testModelResult{OK: true}, testModelResult{OK: true}, nil),
	}
	req := httptest.NewRequest(http.MethodPost, "/api/model-tests/runs",
		bytes.NewBufferString(`{"targets":[{"provider":"openai"},{"provider":"claude"}]}`))
	rec := httptest.NewRecorder()
	h.HandleStartModelTestRun(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}
	var snap modelTestRunSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatalf("response not a snapshot: %v", err)
	}
	if snap.Total != 2 {
		t.Errorf("total = %d, want 2", snap.Total)
	}
	for i, want := range []string{"openai", "claude"} {
		if snap.Entries[i].Provider != want {
			t.Errorf("entry %d provider = %q, want %q", i, snap.Entries[i].Provider, want)
		}
	}
}
