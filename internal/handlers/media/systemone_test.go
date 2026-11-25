package media

import (
	"context"

	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

func TestHandleSystemone_MissingModel(t *testing.T) {
	sqlDB, cleanup := setupEmbeddingsTestDB(t)
	defer cleanup()
	repo := db.NewRepo(sqlDB)
	handler := newTestMediaHandler(repo)

	for _, body := range []string{`{}`, `{"state":"x"}`, `not-json`} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/systemone", strings.NewReader(body))
		handler.HandleSystemone(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %q: got status %d, want 400", body, rec.Code)
		}
	}
}

func TestHandleSystemone_NoConnection(t *testing.T) {
	sqlDB, cleanup := setupEmbeddingsTestDB(t)
	defer cleanup()
	repo := db.NewRepo(sqlDB)
	handler := newTestMediaHandler(repo)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/systemone",
		strings.NewReader(`{"model":"nosuchprovider/jev-1.13","state":"x","questions":{}}`),
	)
	handler.HandleSystemone(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("got status %d, want 404", rec.Code)
	}
}

func TestHandleSystemone_Opencode_Mock(t *testing.T) {
	var gotSession string
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSession = r.Header.Get("x-opencode-session")
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"model":"jev-1.13-free","answers":{"is_urgent":{"type":"noul","noul":0.04}}}`))
	}))
	defer server.Close()

	sqlDB, cleanup := setupEmbeddingsTestDB(t)
	defer cleanup()
	repo := db.NewRepo(sqlDB)
	p := 1
	err := repo.CreateProviderConnectionFull(context.Background(), "conn-oc-1", "opencode", "none", "OpenCode Free", &p, `{"baseUrl":"`+server.URL+`"}`)
	if err != nil {
		t.Fatalf("failed to create connection: %v", err)
	}

	handler := newTestMediaHandler(repo)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/systemone",
		strings.NewReader(`{"model":"oc/jev-1.13-free","state":"The server is responding normally.","questions":{"is_urgent":{"type":"noul","instructions":"Urgent?"}}}`),
	)
	handler.HandleSystemone(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200, body: %s", rec.Code, rec.Body.String())
	}
	if !strings.HasPrefix(gotSession, "ses_") {
		t.Errorf("expected session header starting with ses_, got %q", gotSession)
	}
	if gotAuth != "Bearer public" {
		t.Errorf("expected Bearer public auth header, got %q", gotAuth)
	}
	if !strings.Contains(rec.Body.String(), "jev-1.13-free") {
		t.Errorf("expected body to contain jev-1.13-free, got %s", rec.Body.String())
	}
}

func TestHandleSystemone_Live_Opencode(t *testing.T) {
	// This one talks to a real provider, so it is gated the way the other live
	// tests in this package are (see e2e_media_test.go): off unless asked for.
	// It used to run on every `go test`, and the free tier it targets now
	// answers 429 for callers outside its own client, which turned CI red on
	// main without saying anything about this code.
	if testing.Short() {
		t.Skip("skipping live test in short mode")
	}
	if os.Getenv("ROUTER_LIVE_E2E") == "" {
		t.Skip("skipping live systemone network test (set ROUTER_LIVE_E2E=1 to run)")
	}
	sqlDB, cleanup := setupEmbeddingsTestDB(t)
	defer cleanup()
	repo := db.NewRepo(sqlDB)
	handler := newTestMediaHandler(repo)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/systemone",
		strings.NewReader(`{"model":"oc/jev-1.13-free","state":"The server is responding normally with low latency.","questions":{"is_urgent":{"type":"noul","instructions":"Does this request require urgent attention?"}}}`),
	)
	handler.HandleSystemone(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("live systemone request failed: status %d, body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "answers") {
		t.Errorf("expected response containing 'answers', got: %s", rec.Body.String())
	}
}
