package handlers

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/db"
)

// exportTestRepo builds a repository with the tables the export walks and a
// handful of request-details rows spanning two days, so a windowed export can
// be told apart from an unbounded one.
func exportTestRepo(t *testing.T) (*db.Repo, func()) {
	t.Helper()
	database, cleanup := setupTestDB(t)
	repo := db.NewRepo(database)
	now := time.Now().UTC()
	rows := []struct {
		id      string
		age     time.Duration
		status  string
		payload string
	}{
		{"req-recent-1", -time.Hour, "success", `{"latency":{"ttft":120,"total":900},"tokens":{"prompt_tokens":100,"completion_tokens":50},"cost":0.0042,"account":"acct-a","combo":"","protocol":"openai"}`},
		{"req-recent-2", -2 * time.Hour, "error", `{"latency":{"ttft":0,"total":50},"tokens":{"prompt_tokens":10,"completion_tokens":0},"cost":0.0,"account":"acct-b","combo":"my-combo","protocol":"claude"}`},
		{"req-old-1", -72 * time.Hour, "success", `{"latency":{"ttft":80,"total":400},"tokens":{"prompt_tokens":999,"completion_tokens":999},"cost":9.9,"account":"acct-a","combo":"","protocol":"openai"}`},
	}
	for _, row := range rows {
		ts := now.Add(row.age).Format("2006-01-02T15:04:05.000Z")
		if err := repo.InsertRequestDetail(row.id, "openai", "gpt-4o", "conn-1", row.status, row.payload); err != nil {
			cleanup()
			t.Fatalf("InsertRequestDetail(%s): %v", row.id, err)
		}
		// InsertRequestDetail stamps "now", so the age is applied by hand.
		if _, err := database.Exec(`UPDATE requestDetails SET timestamp = ? WHERE id = ?`, ts, row.id); err != nil {
			cleanup()
			t.Fatalf("stamp %s: %v", row.id, err)
		}
	}
	return repo, cleanup
}

func TestHandleUsageExport_CSV(t *testing.T) {
	repo, cleanup := exportTestRepo(t)
	defer cleanup()
	handler := HandleUsageExport(repo)
	req := httptest.NewRequest(http.MethodGet, "/api/usage/export?format=csv", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/csv; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/csv", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") || !strings.Contains(cd, ".csv") {
		t.Errorf("Content-Disposition = %q, want a .csv attachment", cd)
	}
	records, err := csv.NewReader(rec.Body).ReadAll()
	if err != nil {
		t.Fatalf("parse CSV: %v", err)
	}
	// Header plus the three seeded rows.
	if len(records) != 4 {
		t.Fatalf("got %d CSV records, want 4 (header + 3 rows)", len(records))
	}
	if records[0][0] != "timestamp" {
		t.Errorf("first header = %q, want timestamp", records[0][0])
	}
	// The recent row's cost must survive as a decimal, and its token counts
	// must come out of the stored JSON rather than as zeroes.
	found := false
	for _, rec := range records[1:] {
		if rec[0] == "" {
			continue
		}
		if rec[4] == "success" && rec[9] == "0.004200" {
			found = true
			if rec[7] != "100" || rec[8] != "50" {
				t.Errorf("tokens = %s/%s, want 100/50", rec[7], rec[8])
			}
		}
	}
	if !found {
		t.Error("the recent successful row with its cost was not exported")
	}
}

func TestHandleUsageExport_JSON(t *testing.T) {
	repo, cleanup := exportTestRepo(t)
	defer cleanup()
	handler := HandleUsageExport(repo)
	req := httptest.NewRequest(http.MethodGet, "/api/usage/export?format=json", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var rows []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("parse JSON: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}
	for _, row := range rows {
		for _, key := range []string{"timestamp", "provider", "model", "status", "cost_usd"} {
			if _, ok := row[key]; !ok {
				t.Errorf("row missing %q: %v", key, row)
			}
		}
	}
}

// TestHandleUsageExport_WindowNarrows proves ?period is honoured: a 24h window
// must exclude the row seeded three days ago.
func TestHandleUsageExport_WindowNarrows(t *testing.T) {
	repo, cleanup := exportTestRepo(t)
	defer cleanup()
	handler := HandleUsageExport(repo)
	req := httptest.NewRequest(http.MethodGet, "/api/usage/export?format=json&period=24h", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	var rows []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("parse JSON: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows in a 24h window, want 2 (the 72h-old row excluded)", len(rows))
	}
}

// TestHandleUsageExport_RejectsBadFormat proves an unknown format is a client
// error rather than a silent fallback to CSV.
func TestHandleUsageExport_RejectsBadFormat(t *testing.T) {
	repo, cleanup := exportTestRepo(t)
	defer cleanup()
	handler := HandleUsageExport(repo)
	req := httptest.NewRequest(http.MethodGet, "/api/usage/export?format=xml", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// TestHandleUsageExport_EmptyLedger proves a fresh install exports a well-formed
// empty file rather than an error: a CSV with only its header, a JSON with an
// empty array.
func TestHandleUsageExport_EmptyLedger(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	handler := HandleUsageExport(db.NewRepo(database))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/usage/export?format=csv", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("CSV status = %d, want 200", rec.Code)
	}
	records, err := csv.NewReader(rec.Body).ReadAll()
	if err != nil {
		t.Fatalf("parse CSV: %v", err)
	}
	if len(records) != 1 {
		t.Errorf("got %d CSV records, want 1 (header only)", len(records))
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/usage/export?format=json", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("JSON status = %d, want 200", rec.Code)
	}
	var rows []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("parse JSON: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("got %d rows, want 0", len(rows))
	}
}

// TestExportTokensField proves the token counts are read out of the stored
// usage JSON, and that an unreadable blob degrades to 0 rather than failing
// the whole export.
func TestExportTokensField(t *testing.T) {
	tests := []struct {
		name  string
		input string
		field string
		want  string
	}{
		{"float count", `{"prompt_tokens":1234}`, "prompt_tokens", "1234"},
		{"string count", `{"prompt_tokens":"77"}`, "prompt_tokens", "77"},
		{"missing field", `{"completion_tokens":5}`, "prompt_tokens", "0"},
		{"empty json", ``, "prompt_tokens", "0"},
		{"not json", `nope`, "prompt_tokens", "0"},
		{"null value", `{"prompt_tokens":null}`, "prompt_tokens", "0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := exportTokensField(tt.input, tt.field); got != tt.want {
				t.Errorf("exportTokensField(%q, %q) = %q, want %q", tt.input, tt.field, got, tt.want)
			}
		})
	}
}
