package handlers

import (
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

// The Details list endpoint used to return the whole stored payload per row —
// request messages and response body included — while the table renders only
// timestamp, provider, model, latency and token counts. That made a 20-row page
// ~409 KB for 3.8 KB of visible data, and the header's "Total recorded" waited
// behind all of it. These tests pin the collapse, because the failure mode is
// silent: the page still renders, just slower.

func seedDetailRow(t *testing.T, repo *db.Repo, id string, body string) {
	t.Helper()
	err := repo.InsertRequestDetail(id, "openai", "gpt-5.5", "conn-1", "success", body)
	if err != nil {
		t.Fatalf("seed request detail: %v", err)
	}
}

func detailsList(t *testing.T, repo *db.Repo, query string) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/usage/request-details"+query, nil)
	HandleRequestDetails(repo)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body)
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v (raw %s)", err, rec.Body)
	}
	return payload
}

func detailByID(t *testing.T, repo *db.Repo, id string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/usage/request-details/"+id, nil)
	req.SetPathValue("id", id)
	HandleRequestDetail(repo)(rec, req)
	if rec.Code != http.StatusOK {
		return rec.Code, nil
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v (raw %s)", err, rec.Body)
	}
	detail, _ := payload["detail"].(map[string]any)
	return rec.Code, detail
}

// A row that carries request and response text must come back from the list
// without them. The payload is the whole cost of this endpoint, so its presence
// in the list response is exactly the regression.
func TestRequestDetails_ListOmitsRequestAndResponseBody(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	body := `{"id":"req-1","provider":"openai","model":"gpt-5.5","status":"success",` +
		`"timestamp":"2026-10-07T06:00:00.000Z","latency":{"ttft":120,"total":900},` +
		`"tokens":{"prompt_tokens":1000,"completion_tokens":200,"cached_tokens":64,"reasoning_tokens":12},` +
		`"request":{"messages":[{"role":"user","content":"` + strings.Repeat("q", 500) + `"}]},` +
		`"response":{"content":"` + strings.Repeat("r", 10000) + `"}}`
	seedDetailRow(t, repo, "req-1", body)

	payload := detailsList(t, repo, "?limit=20&offset=0")
	details, _ := payload["details"].([]any)
	if len(details) != 1 {
		t.Fatalf("details length = %d, want 1", len(details))
	}
	row, _ := details[0].(map[string]any)
	if row == nil {
		t.Fatalf("details[0] is not an object: %#v", details[0])
	}

	if _, present := row["request"]; present {
		t.Error("list row carries `request`: the collapsed list must omit the request body")
	}
	if _, present := row["response"]; present {
		t.Error("list row carries `response`: the collapsed list must omit the response body")
	}

	// The values the table does render must survive the collapse.
	if got := row["model"]; got != "gpt-5.5" {
		t.Errorf("model = %v, want gpt-5.5", got)
	}
	if got := row["provider"]; got != "openai" {
		t.Errorf("provider = %v, want openai", got)
	}
	latency, _ := row["latency"].(map[string]any)
	if latency == nil {
		t.Fatalf("latency missing from list row: %#v", row)
	}
	if ttft, _ := latency["ttft"].(float64); ttft != 120 {
		t.Errorf("latency.ttft = %v, want 120", ttft)
	}
	if total, _ := latency["total"].(float64); total != 900 {
		t.Errorf("latency.total = %v, want 900", total)
	}
	tokens, _ := row["tokens"].(map[string]any)
	if tokens == nil {
		t.Fatalf("tokens missing from list row: %#v", row)
	}
	if prompt, _ := tokens["prompt_tokens"].(float64); prompt != 1000 {
		t.Errorf("tokens.prompt_tokens = %v, want 1000", prompt)
	}
	if cached, _ := tokens["cached_tokens"].(float64); cached != 64 {
		t.Errorf("tokens.cached_tokens = %v, want 64: the cached-token normalization the full path applied must still run", cached)
	}
}

// Opening a row has to yield the whole payload again — the collapse is a
// transport optimization, not a loss of data.
func TestRequestDetailByID_ReturnsFullPayload(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	body := `{"id":"req-1","provider":"openai","model":"gpt-5.5","status":"success",` +
		`"timestamp":"2026-10-07T06:00:00.000Z","latency":{"ttft":120,"total":900},` +
		`"tokens":{"prompt_tokens":1000,"completion_tokens":200,"cached_tokens":64},` +
		`"request":{"messages":[{"role":"user","content":"secret-prompt"}]},` +
		`"response":{"content":"secret-answer"}}`
	seedDetailRow(t, repo, "req-1", body)

	code, detail := detailByID(t, repo, "req-1")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	req, _ := detail["request"].(map[string]any)
	if req == nil {
		t.Fatalf("by-id response has no `request`: %#v", detail)
	}
	messages, _ := req["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(messages))
	}
	msg, _ := messages[0].(map[string]any)
	if msg["content"] != "secret-prompt" {
		t.Errorf("message content = %v, want secret-prompt", msg["content"])
	}
	resp, _ := detail["response"].(map[string]any)
	if resp == nil || resp["content"] != "secret-answer" {
		t.Errorf("by-id response lost the response body: %#v", detail["response"])
	}
}

// A row that retention has already pruned, or an id that never existed, is a
// 404. Answering 500 would tell the dashboard the gateway is broken.
func TestRequestDetailByID_UnknownIDIs404(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	code, _ := detailByID(t, repo, "does-not-exist")
	if code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", code)
	}
}

// The total in the header and the page of rows are one response, and the total
// must still describe the whole table rather than the page.
func TestRequestDetails_TotalCoversWholeTable(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	for i := range 7 {
		seedDetailRow(t, repo, fmt.Sprintf("req-%d", i),
			fmt.Sprintf(`{"id":"req-%d","model":"gpt-5.5","status":"success","tokens":{"prompt_tokens":%d}}`, i, 100*(i+1)))
	}

	first := detailsList(t, repo, "?limit=20&offset=0")
	second := detailsList(t, repo, "?limit=3&offset=3")

	if got, _ := first["total"].(float64); got != 7 {
		t.Errorf("first page total = %v, want 7", got)
	}
	if got, _ := second["total"].(float64); got != 7 {
		t.Errorf("second page total = %v, want 7: paging changed the reported total", got)
	}
	rows, _ := second["details"].([]any)
	if len(rows) != 3 {
		t.Errorf("second page rows = %d, want 3", len(rows))
	}
	if off, _ := second["offset"].(float64); off != 3 {
		t.Errorf("second page offset = %v, want 3", off)
	}
}

// The table paginates newest-first, so a page must not repeat or skip a row as
// pages advance — the collapse changed which query produces the ordering.
func TestRequestDetails_PagesDoNotRepeatRows(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	const total = 10
	for i := range total {
		seedDetailRow(t, repo, fmt.Sprintf("req-%02d", i),
			fmt.Sprintf(`{"id":"req-%02d","model":"m%d","status":"success","tokens":{"prompt_tokens":10}}`, i, i))
	}

	seen := map[string]int{}
	for offset := 0; offset < total; offset += 4 {
		payload := detailsList(t, repo, fmt.Sprintf("?limit=4&offset=%d", offset))
		details, _ := payload["details"].([]any)
		if len(details) == 0 {
			t.Fatalf("offset %d returned no rows", offset)
		}
		for _, raw := range details {
			row, _ := raw.(map[string]any)
			id, _ := row["id"].(string)
			if id == "" {
				t.Fatalf("row without an id: %#v", row)
			}
			seen[id]++
		}
	}

	for id, count := range seen {
		if count != 1 {
			t.Errorf("row %s appeared %d times across pages, want 1", id, count)
		}
	}
	if len(seen) != total {
		t.Errorf("paged through %d distinct rows, want %d", len(seen), total)
	}
}
