package media

import (
	"net/http"
	"testing"
)

func TestAggregateProviderResults(t *testing.T) {
	pass := modelTestModelResult{Model: "p/live", State: testPass, OK: true, LatencyMs: 10, HTTPStatus: 200}
	fail := modelTestModelResult{Model: "p/dead", State: testFail, LatencyMs: 20, HTTPStatus: 501, Error: "upstream 501"}
	blocked := modelTestModelResult{Model: "p/parked", State: testBlocked, LatencyMs: 30, Blocked: true, Error: "rate-limited", ResetAt: "soon"}

	t.Run("all pass", func(t *testing.T) {
		out := aggregateProviderResults([]modelTestModelResult{pass, pass})
		if !out.OK || out.Note != "" {
			t.Errorf("aggregate = %+v, want OK with no note", out.testModelResult)
		}
		if out.LatencyMs != 20 {
			t.Errorf("latency = %d, want the sweep total 20", out.LatencyMs)
		}
	})

	t.Run("one live model passes the provider", func(t *testing.T) {
		out := aggregateProviderResults([]modelTestModelResult{fail, pass})
		if !out.OK {
			t.Fatalf("aggregate not OK: %+v", out.testModelResult)
		}
		if out.Note != "1 of 2 models answered" {
			t.Errorf("note = %q, want the partial answer spelled out", out.Note)
		}
		if out.Status != http.StatusOK {
			t.Errorf("status = %d, want 200", out.Status)
		}
	})

	t.Run("all fail keeps the first failure's wording", func(t *testing.T) {
		out := aggregateProviderResults([]modelTestModelResult{fail, blocked})
		if out.OK || out.Blocked {
			t.Fatalf("aggregate = %+v, want a plain fail", out.testModelResult)
		}
		if out.Error != "upstream 501" || out.Status != 501 {
			t.Errorf("aggregate error/status = %q/%d, want the first failure", out.Error, out.Status)
		}
	})

	t.Run("all blocked stays blocked", func(t *testing.T) {
		out := aggregateProviderResults([]modelTestModelResult{blocked})
		if !out.Blocked || out.ResetAt != "soon" {
			t.Errorf("aggregate = %+v, want blocked with its reset time", out.testModelResult)
		}
	})

	t.Run("nothing probed is an error, not a pass", func(t *testing.T) {
		out := aggregateProviderResults(nil)
		if out.OK || out.Error == "" {
			t.Errorf("aggregate = %+v, want an error", out.testModelResult)
		}
	})
}

func TestModelBreakdown(t *testing.T) {
	row := modelBreakdown("p/m", testModelResult{OK: true, Status: 200, LatencyMs: 3})
	if row.State != testPass || row.Model != "p/m" {
		t.Errorf("row = %+v, want a pass row for p/m", row)
	}
	row = modelBreakdown("p/m", testModelResult{Blocked: true, Error: "parked"})
	if row.State != testBlocked {
		t.Errorf("row state = %q, want blocked", row.State)
	}
}
