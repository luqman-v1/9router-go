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

// overridesRouter is the production route table for these two endpoints, with
// the same paths and verbs routes.go registers.
func overridesRouter(repo *db.Repo) chi.Router {
	r := chi.NewRouter()
	h := NewDashboardHandler(repo)
	r.Get("/api/providers/{id}/overrides", h.HandleGetProviderOverrides)
	r.Put("/api/providers/{id}/overrides", h.HandleSaveProviderOverrides)
	return r
}

// The operator's stored headers are what the request must carry; the blocked
// list is what makes Authorization impossible to re-point at another account.
func TestProviderOverridesRoundTrip(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	r := overridesRouter(repo)

	// A fresh install reports the registry's own headers so the UI has a
	// baseline, and nothing marked as an override.
	getRec := httptest.NewRecorder()
	r.ServeHTTP(getRec, httptest.NewRequest(http.MethodGet, "/api/providers/deepseek/overrides", nil))
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET status = %d: %s", getRec.Code, getRec.Body.String())
	}
	var initial struct {
		Headers        map[string]string `json:"headers"`
		BuiltinHeaders map[string]string `json:"builtinHeaders"`
		BlockedHeaders []string          `json:"blockedHeaders"`
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &initial); err != nil {
		t.Fatalf("decode GET: %v", err)
	}
	if len(initial.Headers) != 0 {
		t.Errorf("a fresh provider must have no overrides, got %v", initial.Headers)
	}
	if len(initial.BlockedHeaders) == 0 {
		t.Error("GET must tell the UI which headers it may not offer")
	}

	putRec := httptest.NewRecorder()
	body := strings.NewReader(`{"headers":{"X-Tenant":"acme","x-trace":"abc"}}`)
	req := httptest.NewRequest(http.MethodPut, "/api/providers/deepseek/overrides", body)
	overridesRouter(repo).ServeHTTP(putRec, req)
	if putRec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d: %s", putRec.Code, putRec.Body.String())
	}

	afterRec := httptest.NewRecorder()
	overridesRouter(repo).ServeHTTP(afterRec, httptest.NewRequest(http.MethodGet, "/api/providers/deepseek/overrides", nil))
	var after struct {
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(afterRec.Body.Bytes(), &after); err != nil {
		t.Fatalf("decode GET after PUT: %v", err)
	}
	if after.Headers["X-Tenant"] != "acme" || after.Headers["X-Trace"] != "abc" {
		t.Errorf("stored headers = %v, want the pair just written", after.Headers)
	}

	// An empty payload clears it — the endpoint needs no DELETE for that.
	clearRec := httptest.NewRecorder()
	clearReq := httptest.NewRequest(http.MethodPut, "/api/providers/deepseek/overrides", strings.NewReader(`{"headers":{}}`))
	overridesRouter(repo).ServeHTTP(clearRec, clearReq)
	if clearRec.Code != http.StatusOK {
		t.Fatalf("clear PUT status = %d: %s", clearRec.Code, clearRec.Body.String())
	}
	clearedRec := httptest.NewRecorder()
	overridesRouter(repo).ServeHTTP(clearedRec, httptest.NewRequest(http.MethodGet, "/api/providers/deepseek/overrides", nil))
	var cleared struct {
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(clearedRec.Body.Bytes(), &cleared); err != nil {
		t.Fatalf("decode after clear: %v", err)
	}
	if len(cleared.Headers) != 0 {
		t.Errorf("an empty PUT must clear the override, got %v", cleared.Headers)
	}
}

// An alias in the URL must address the same entry as the canonical id, because
// the dashboard links by alias while a request arrives as provider/model.
func TestProviderOverridesAliasAndCanonicalAreOneEntry(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	putRec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/providers/ds/overrides", strings.NewReader(`{"headers":{"X-Origin":"alias"}}`))
	overridesRouter(repo).ServeHTTP(putRec, req)
	if putRec.Code != http.StatusOK {
		t.Fatalf("PUT by alias status = %d: %s", putRec.Code, putRec.Body.String())
	}

	getRec := httptest.NewRecorder()
	overridesRouter(repo).ServeHTTP(getRec, httptest.NewRequest(http.MethodGet, "/api/providers/deepseek/overrides", nil))
	var got struct {
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Headers["X-Origin"] != "alias" {
		t.Errorf("GET by canonical id = %v, want the entry written through the alias", got.Headers)
	}
}

func TestProviderOverridesRejectAuthAndFramingHeaders(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	cases := []struct {
		name string
		body string
	}{
		{"authorization", `{"headers":{"Authorization":"Bearer stolen"}}`},
		{"cookie", `{"headers":{"Cookie":"session=x"}}`},
		{"host", `{"headers":{"Host":"evil.example"}}`},
		{"content-type", `{"headers":{"Content-Type":"text/plain"}}`},
		{"content-length", `{"headers":{"Content-Length":"0"}}`},
		{"connection", `{"headers":{"Connection":"close"}}`},
		{"transfer-encoding", `{"headers":{"Transfer-Encoding":"chunked"}}`},
		{"injected newline", `{"headers":{"X-Ok":"a\r\nX-Evil: b"}}`},
		{"space in name", `{"headers":{"X Bad":"v"}}`},
		{"colon in name", `{"headers":{"X:Bad":"v"}}`},
		{"malformed json", `{"headers":`},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPut, "/api/providers/deepseek/overrides", strings.NewReader(tt.body))
			overridesRouter(repo).ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
			}
		})
	}
}

// A rejected write must leave the stored entry untouched.
func TestProviderOverridesRejectedWriteKeepsPrevious(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	okRec := httptest.NewRecorder()
	overridesRouter(repo).ServeHTTP(okRec,
		httptest.NewRequest(http.MethodPut, "/api/providers/deepseek/overrides", strings.NewReader(`{"headers":{"X-Keep":"yes"}}`)))
	if okRec.Code != http.StatusOK {
		t.Fatalf("seed PUT status = %d: %s", okRec.Code, okRec.Body.String())
	}
	badRec := httptest.NewRecorder()
	overridesRouter(repo).ServeHTTP(badRec,
		httptest.NewRequest(http.MethodPut, "/api/providers/deepseek/overrides", strings.NewReader(`{"headers":{"Authorization":"Bearer stolen"}}`)))
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("rejected write status = %d, want 400", badRec.Code)
	}

	getRec := httptest.NewRecorder()
	overridesRouter(repo).ServeHTTP(getRec, httptest.NewRequest(http.MethodGet, "/api/providers/deepseek/overrides", nil))
	var got struct {
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Headers["X-Keep"] != "yes" || len(got.Headers) != 1 {
		t.Errorf("headers = %v, want only the accepted X-Keep", got.Headers)
	}
}
