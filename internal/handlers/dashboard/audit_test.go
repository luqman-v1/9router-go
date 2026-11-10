package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/audit"
	"9router/proxy/internal/db"
	"github.com/go-chi/chi/v5"
)

// auditTestRecorder starts a recorder over the test repo and attaches it. It
// does NOT register its own cleanup: the caller must defer stop() AHEAD of the
// database cleanup(), because a deferred func runs before t.Cleanup — the
// recorder's drain must finish while the database is still open, or its last
// batch hits "database is closed".
func auditTestRecorder(t *testing.T, repo *db.Repo) (rec *audit.Recorder, stop func()) {
	t.Helper()
	rec = audit.New(repo.Audit(), nil, audit.Config{
		Key:             audit.DeriveKey([]byte("test-master-key")),
		DefaultTenantID: db.AuditTenant,
	})
	repo.SetAuditRecorder(rec)
	return rec, func() {
		rec.Stop(3 * time.Second)
		repo.SetAuditRecorder(nil)
	}
}

// auditRow is the JSON shape the list endpoint returns for one row.
type auditRow struct {
	ID         string            `json:"id"`
	Action     string            `json:"action"`
	Outcome    string            `json:"outcome"`
	ActorType  string            `json:"actor_type"`
	Method     string            `json:"method"`
	Route      string            `json:"route"`
	StatusCode int               `json:"status_code"`
	ResourceID string            `json:"resource_id"`
	ChainID    string            `json:"chain_id"`
	ChainSeq   int64             `json:"chain_seq"`
	Hash       string            `json:"hash"`
	Details    map[string]string `json:"details"`
}

// doGET issues a GET against h and returns the recorder.
func doGET(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// post hits path with a small JSON body.
func post(t *testing.T, h http.Handler, path string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"name":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(httptest.NewRecorder(), req)
}

// listAuditRows reads every stored row directly from the repository (bypassing
// the handler), parsing the detail JSON so a test can assert on the stored
// fields. Ordered by chain sequence (the recorder's monotonic write order),
// which is deterministic — created_at is microsecond precision and two fast
// writes can land in the same microsecond.
func listAuditRows(t *testing.T, repo *db.Repo) []auditRow {
	t.Helper()
	entries, err := repo.Audit().List(context.Background(), db.AuditFilter{Limit: 500})
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	rows := make([]auditRow, 0, len(entries))
	for _, e := range entries {
		details := map[string]string{}
		if e.Detail != "" {
			_ = json.Unmarshal([]byte(e.Detail), &details)
		}
		rows = append(rows, auditRow{
			ID: e.ID, Action: e.Action, Outcome: e.Outcome, ActorType: e.ActorType,
			Method: e.HTTPMethod, Route: e.HTTPRoute, StatusCode: e.StatusCode,
			ResourceID: e.Target, ChainID: e.ChainID, ChainSeq: e.ChainSeq, Hash: e.Hash,
			Details: details,
		})
	}
	// Sort by chain sequence ascending = the order the recorder wrote them.
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && rows[j-1].ChainSeq > rows[j].ChainSeq; j-- {
			rows[j-1], rows[j] = rows[j], rows[j-1]
		}
	}
	return rows
}

// TestAuditMiddlewareRecordsMutation verifies the middleware records a POST on
// the dashboard route group: the route pattern, the resource id read from the
// create response, and the JSON field names — never their values.
func TestAuditMiddlewareRecordsMutation(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	rec, stop := auditTestRecorder(t, repo)
	defer stop()

	h := NewDashboardHandler(repo)
	r := chi.NewRouter()
	r.Use(AuditMiddleware(repo))
	r.Post("/api/connections", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"conn_abc","name":"deepseek"}`))
	})
	r.Get("/api/audit", h.HandleAuditList)

	req := httptest.NewRequest(http.MethodPost, "/api/connections",
		strings.NewReader(`{"name":"deepseek","data":{"baseUrl":"https://x"}}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("create status = %d", rr.Code)
	}
	rec.Flush(context.Background())

	rows := listAuditRows(t, repo)
	if len(rows) != 1 {
		t.Fatalf("expected 1 audit row, got %d", len(rows))
	}
	got := rows[0]
	if got.Action != "connections.create" {
		t.Errorf("action = %q, want connections.create", got.Action)
	}
	if got.ResourceID != "conn_abc" {
		t.Errorf("resource id = %q, want conn_abc (from the create response)", got.ResourceID)
	}
	if got.Method != "POST" || got.Route != "/api/connections" {
		t.Errorf("method/route = %s %s", got.Method, got.Route)
	}
	if got.Outcome != audit.OutcomeSuccess {
		t.Errorf("outcome = %q, want %s", got.Outcome, audit.OutcomeSuccess)
	}
	fields, ok := got.Details["fields"]
	if !ok {
		t.Fatalf("details missing fields: %+v", got.Details)
	}
	if !strings.Contains(fields, "name") || !strings.Contains(fields, "data") {
		t.Errorf("fields = %q, want it to contain name and data", fields)
	}
	if strings.Contains(fields, "deepseek") {
		t.Errorf("fields leaked a value: %q", fields)
	}
}

// TestAuditMiddlewareSkipsPlainGET verifies a plain dashboard GET is not
// recorded, while a sensitive read from the auditedReads set is.
func TestAuditMiddlewareSkipsPlainGET(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	rec, stop := auditTestRecorder(t, repo)
	defer stop()

	h := NewDashboardHandler(repo)
	r := chi.NewRouter()
	r.Use(AuditMiddleware(repo))
	r.Get("/api/connections", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	r.Get("/api/usage/export", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	r.Get("/api/audit", h.HandleAuditList)

	// Plain GET: not recorded.
	doGET(t, r, "/api/connections")
	rec.Flush(context.Background())
	if got := listAuditRows(t, repo); len(got) != 0 {
		t.Fatalf("plain GET should not be recorded, got %d rows", len(got))
	}

	// Sensitive GET: recorded.
	doGET(t, r, "/api/usage/export")
	rec.Flush(context.Background())
	rows := listAuditRows(t, repo)
	if len(rows) != 1 {
		t.Fatalf("sensitive GET should be recorded, got %d rows", len(rows))
	}
	if !strings.HasPrefix(rows[0].Action, "usage.export") {
		t.Errorf("sensitive-read action = %q, want usage.export*", rows[0].Action)
	}
}

// TestAuditVerifyEndpoint verifies GET /api/audit/verify recomputes the chain
// and reports a clean result after a recorded write.
func TestAuditVerifyEndpoint(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	rec, stop := auditTestRecorder(t, repo)
	defer stop()

	h := NewDashboardHandler(repo)
	r := chi.NewRouter()
	r.Use(AuditMiddleware(repo))
	r.Post("/api/connections", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"conn_1"}`))
	})
	r.Get("/api/audit/verify", h.HandleAuditVerify)

	post(t, r, "/api/connections")
	rec.Flush(context.Background())

	rr := doGET(t, r, "/api/audit/verify")
	if rr.Code != http.StatusOK {
		t.Fatalf("verify status = %d, body %s", rr.Code, rr.Body.String())
	}
	var rep audit.VerifyReport
	if err := json.Unmarshal(rr.Body.Bytes(), &rep); err != nil {
		t.Fatalf("unmarshal verify report: %v", err)
	}
	if !rep.OK {
		t.Errorf("verify ok = false: %+v", rep.Breaks)
	}
	if rep.Entries < 1 {
		t.Errorf("verify entries = %d, want >= 1", rep.Entries)
	}
}

// TestAuditVerifyDisabled returns 503 when no recorder is attached, so an
// operator can tell "auditing is off" from "auditing is on".
func TestAuditVerifyDisabled(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	h := NewDashboardHandler(repo)
	r := chi.NewRouter()
	r.Get("/api/audit/verify", h.HandleAuditVerify)

	rr := doGET(t, r, "/api/audit/verify")
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("verify with no recorder = %d, want 503", rr.Code)
	}
}

// TestAuditStatusEndpoint reports the recorder counters and the retention.
func TestAuditStatusEndpoint(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	_, stop := auditTestRecorder(t, repo)
	defer stop()

	h := NewDashboardHandler(repo)
	r := chi.NewRouter()
	r.Get("/api/audit/status", h.HandleAuditStatus)

	rr := doGET(t, r, "/api/audit/status")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var body struct {
		Stats         audit.Stats `json:"stats"`
		RetentionDays int         `json:"retention_days"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal status: %v", err)
	}
	if !body.Stats.Enabled {
		t.Errorf("stats.enabled = false, want true")
	}
	if body.Stats.ChainID == "" {
		t.Errorf("stats.chain_id empty")
	}
}

// TestAuditExportEndpoint streams rows as JSON Lines; the set of actions is
// what is asserted (the two writes share a microsecond, so order is racy).
func TestAuditExportEndpoint(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	rec, stop := auditTestRecorder(t, repo)
	defer stop()

	h := NewDashboardHandler(repo)
	r := chi.NewRouter()
	r.Use(AuditMiddleware(repo))
	r.Post("/api/connections", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"conn_1"}`))
	})
	r.Post("/api/plans", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"plan_1"}`))
	})
	r.Get("/api/audit/export", h.HandleAuditExport)

	post(t, r, "/api/connections")
	post(t, r, "/api/plans")
	rec.Flush(context.Background())

	rr := doGET(t, r, "/api/audit/export")
	if rr.Code != http.StatusOK {
		t.Fatalf("export = %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "ndjson") {
		t.Errorf("export content-type = %q, want ndjson", ct)
	}
	actions := map[string]bool{}
	lines := 0
	dec := json.NewDecoder(rr.Body)
	for dec.More() {
		var row auditRow
		if err := dec.Decode(&row); err != nil {
			t.Fatalf("decode export line: %v", err)
		}
		lines++
		actions[row.Action] = true
	}
	if lines != 2 {
		t.Fatalf("export lines = %d, want 2", lines)
	}
	if !actions["connections.create"] || !actions["plans.create"] {
		t.Errorf("export actions = %v, want both connections.create and plans.create", actions)
	}
}

// TestAuditListFilters narrows the list by an action prefix.
func TestAuditListFilters(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	rec, stop := auditTestRecorder(t, repo)
	defer stop()

	h := NewDashboardHandler(repo)
	r := chi.NewRouter()
	r.Use(AuditMiddleware(repo))
	r.Post("/api/connections", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"c1"}`))
	})
	r.Post("/api/plans", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"p1"}`))
	})
	r.Get("/api/audit", h.HandleAuditList)

	post(t, r, "/api/connections")
	post(t, r, "/api/plans")
	rec.Flush(context.Background())

	rr := doGET(t, r, "/api/audit?action=plans.*")
	if rr.Code != http.StatusOK {
		t.Fatalf("list = %d", rr.Code)
	}
	var body struct {
		Entries []auditRow `json:"entries"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if len(body.Entries) != 1 {
		t.Fatalf("filtered entries = %d, want 1", len(body.Entries))
	}
	if body.Entries[0].Action != "plans.create" {
		t.Errorf("filtered action = %q, want plans.create", body.Entries[0].Action)
	}
}
