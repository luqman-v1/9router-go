package db

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// seedPerfRow writes one requestDetails row carrying a latency payload.
func seedPerfRow(t *testing.T, database *sql.DB, id, provider, model, payload string) {
	t.Helper()
	if _, err := database.Exec(
		`INSERT INTO requestDetails (id, timestamp, provider, model, connectionId, status, data)
		 VALUES (?, '2026-10-06T01:00:00Z', ?, ?, 'conn-1', 'success', ?)`,
		id, provider, model, payload,
	); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

// GetModelLatency aggregates the requestDetails ring into one mean row per
// provider/model pair. The cases that matter are the ones a sort depends on:
// a mean over several observations, a model whose responses never reported a
// TTFT, a row with no latency at all, and a payload that is not JSON.
func TestGetModelLatency(t *testing.T) {
	database, err := OpenDatabase(filepath.Join(t.TempDir(), "latency.sqlite"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = database.Close() }()
	if err := EnsureCoreSchema(database); err != nil {
		t.Fatalf("schema: %v", err)
	}
	// Two observations of the same model: the mean is what a sort reads, so a
	// sum would rank a chatty model as a slow one.
	seedPerfRow(t, database, "r1", "ds", "deepseek-chat",
		`{"latency":{"ttft":300,"total":1000}}`)
	seedPerfRow(t, database, "r2", "ds", "deepseek-chat",
		`{"latency":{"ttft":500,"total":2000}}`)
	// A model that never reported a first-token time: the total is honest, the
	// TTFT is absent, and the two must not be conflated.
	seedPerfRow(t, database, "r3", "oc", "gpt-4o",
		`{"latency":{"total":4000}}`)
	// A row with no latency block contributes nothing rather than a zero.
	seedPerfRow(t, database, "r4", "oc", "no-latency", `{"tokens":{"output_tokens":10}}`)
	// A payload that is not JSON reads as "no observation" rather than taking
	// the whole read down.
	seedPerfRow(t, database, "r5", "oc", "broken", `not json at all`)

	repo := NewRepo(database)
	rows, err := repo.GetModelLatency()
	if err != nil {
		t.Fatalf("GetModelLatency: %v", err)
	}
	byKey := make(map[string]ModelLatencyAgg, len(rows))
	for _, r := range rows {
		byKey[r.Provider+"|"+r.Model] = r
	}

	ds, ok := byKey["ds|deepseek-chat"]
	if !ok {
		t.Fatalf("ds|deepseek-chat missing from %v", byKey)
	}
	if ds.Samples != 2 {
		t.Errorf("samples = %d, want 2", ds.Samples)
	}
	if ds.TTFTMs != 400 {
		t.Errorf("ttft = %d, want the mean 400", ds.TTFTMs)
	}
	if ds.TotalMs != 1500 {
		t.Errorf("total = %d, want the mean 1500", ds.TotalMs)
	}

	oc, ok := byKey["oc|gpt-4o"]
	if !ok {
		t.Fatalf("oc|gpt-4o missing from %v", byKey)
	}
	if oc.Samples != 1 || oc.TotalMs != 4000 {
		t.Errorf("oc|gpt-4o = %+v, want 1 sample and total 4000", oc)
	}
	if oc.TTFTMs != 0 {
		t.Errorf("ttft = %d, want 0 when never observed", oc.TTFTMs)
	}

	// The two rows without a usable latency must not appear at all: a sort
	// treats "absent" as unproven, and a zero-total row would read as the
	// fastest model in the catalogue.
	if _, exists := byKey["oc|no-latency"]; exists {
		t.Error("a row with no latency block must not produce an aggregate")
	}
	if _, exists := byKey["oc|broken"]; exists {
		t.Error("a row whose payload is not JSON must not produce an aggregate")
	}
}

// An empty ring aggregates to nothing rather than to an error, so a fresh
// install's sort dropdown degrades to "no measurements".
func TestGetModelLatencyEmptyRing(t *testing.T) {
	database, err := OpenDatabase(filepath.Join(t.TempDir(), "latency-empty.sqlite"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = database.Close() }()
	if err := EnsureCoreSchema(database); err != nil {
		t.Fatalf("schema: %v", err)
	}
	rows, err := NewRepo(database).GetModelLatency()
	if err != nil {
		t.Fatalf("GetModelLatency: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("rows = %v, want none", rows)
	}
}
