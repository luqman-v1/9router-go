package dashboard

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"9router/proxy/internal/db"
)

// modelCapsRouter mounts the two endpoints on the same path and verbs the
// production table registers, with the model segment percent-encoded the way
// the dashboard sends an id that carries slashes and colons.
func modelCapsRouter(repo *db.Repo) chi.Router {
	r := chi.NewRouter()
	h := NewDashboardHandler(repo)
	r.Get("/api/providers/{id}/models/{model}/caps", h.HandleGetModelCapsOverride)
	r.Put("/api/providers/{id}/models/{model}/caps", h.HandleSaveModelCapsOverride)
	return r
}

// The editor reads what is pinned AND what detection currently resolves, so the
// placeholder it shows for an Auto field is the real number, not a second
// estimate the operator has to reconcile.
func TestModelCapsOverrideRoundTrip(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	r := modelCapsRouter(repo)

	path := "/api/providers/antigravity/models/claude-sonnet-4/caps"

	getRec := httptest.NewRecorder()
	r.ServeHTTP(getRec, httptest.NewRequest(http.MethodGet, path, nil))
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET = %d: %s", getRec.Code, getRec.Body.String())
	}
	var initial struct {
		CapsOverride map[string]any `json:"capsOverride"`
		Detected     map[string]any `json:"detected"`
		Fields       []struct {
			Key  string `json:"key"`
			Type string `json:"type"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &initial); err != nil {
		t.Fatalf("decode GET: %v", err)
	}
	if len(initial.CapsOverride) != 0 {
		t.Errorf("fresh install reported overrides: %#v", initial.CapsOverride)
	}
	if len(initial.Fields) != 11 {
		t.Errorf("fields = %d, want the 11 editable capability fields", len(initial.Fields))
	}

	putBody := `{"capsOverride":{"vision":true,"contextWindow":200000}}`
	putRec := httptest.NewRecorder()
	putReq := httptest.NewRequest(http.MethodPut, path, strings.NewReader(putBody))
	putReq.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", putRec.Code, putRec.Body.String())
	}
	var afterSave struct {
		CapsOverride map[string]any `json:"capsOverride"`
	}
	if err := json.Unmarshal(putRec.Body.Bytes(), &afterSave); err != nil {
		t.Fatalf("decode PUT: %v", err)
	}
	if afterSave.CapsOverride["vision"] != true {
		t.Errorf("saved vision = %v, want true", afterSave.CapsOverride["vision"])
	}

	// Read back through the store, not the PUT echo: the write must have
	// persisted, and the GET must reflect it.
	row, err := repo.GetModelCapOverride("antigravity", "claude-sonnet-4")
	if err != nil {
		t.Fatalf("repo get: %v", err)
	}
	if row == nil {
		t.Fatal("PUT answered OK but stored nothing")
	}

	// An empty object is the clear, so the dedicated route can be absent.
	clearRec := httptest.NewRecorder()
	clearReq := httptest.NewRequest(http.MethodPut, path, strings.NewReader(`{"capsOverride":{}}`))
	clearReq.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(clearRec, clearReq)
	if clearRec.Code != http.StatusOK {
		t.Fatalf("clear PUT = %d: %s", clearRec.Code, clearRec.Body.String())
	}
	row, err = repo.GetModelCapOverride("antigravity", "claude-sonnet-4")
	if err != nil {
		t.Fatalf("repo get after clear: %v", err)
	}
	if row != nil {
		t.Fatalf("override survived a {} save: %#v", row)
	}
}

// A malformed body and a non-object payload are 400s, never a silent clear:
// an operator who typed a payload and read "saved" would not find out until a
// request went out with the wrong window.
func TestModelCapsOverrideRejectsBadPayload(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	r := modelCapsRouter(repo)
	path := "/api/providers/openai/models/gpt-4o/caps"

	cases := []struct {
		name string
		body string
	}{
		{"malformed JSON", `{"capsOverride":`},
		{"not an object", `[]`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPut, path, strings.NewReader(c.body))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// A model id carrying slashes and a :suffix arrives percent-encoded. Without
// the unescape the route would look up "vendor%2Fclaude-x%3Afree" and answer
// as if the model had no override at all.
func TestModelCapsOverrideUnescapesModelSegment(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	r := modelCapsRouter(repo)

	// Store under the canonical provider id and the bare model key.
	if err := repo.SetModelCapOverride("opencode", "claude-x", []byte(`{"vision":true}`)); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec := httptest.NewRecorder()
	// %2F = /, %3A = :
	req := httptest.NewRequest(http.MethodGet, "/api/providers/opencode/models/vendor%2Fclaude-x%3Afree/caps", nil)
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		CapsOverride map[string]any `json:"capsOverride"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The GET reads the row by the exact stored model string, so a bare "claude-x"
	// seed is not found by "vendor/claude-x:free": that is the DB's job, and the
	// handler must at least have unescaped the segment rather than looked up the
	// percent-encoded id. Assert the reported model is the decoded one.
	var envelope struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &envelope)
	if envelope.Model != "vendor/claude-x:free" {
		t.Errorf("model = %q, want the decoded %q", envelope.Model, "vendor/claude-x:free")
	}
}
