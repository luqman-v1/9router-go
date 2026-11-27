package media

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"9router/proxy/internal/db"
	"9router/proxy/internal/dbtest"
	"9router/proxy/internal/headroom"
)

type headroomRoundTripFunc func(*http.Request) (*http.Response, error)

func (f headroomRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type headroomCountingBody struct {
	remaining int64
	readBytes int64
}

func (b *headroomCountingBody) Read(p []byte) (int, error) {
	if b.remaining <= 0 {
		return 0, io.EOF
	}
	n := len(p)
	if int64(n) > b.remaining {
		n = int(b.remaining)
	}
	for i := range n {
		p[i] = 'a'
	}
	b.remaining -= int64(n)
	b.readBytes += int64(n)
	return n, nil
}

func (b *headroomCountingBody) Close() error { return nil }

func newHeadroomTestRepo(t *testing.T, headroomURL string) *db.Repo {
	t.Helper()
	database, err := db.OpenDatabase(t.TempDir() + "/headroom.sqlite")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := dbtest.CreateTables(database); err != nil {
		t.Fatalf("create tables: %v", err)
	}
	repo := db.NewRepo(database)
	if headroomURL != "" {
		if err := repo.UpdateSettingsRaw(map[string]any{"headroomUrl": headroomURL}); err != nil {
			t.Fatalf("seed headroomUrl: %v", err)
		}
	}
	return repo
}

func TestHeadroomHandler_NilRepoFallsBackToDefaults(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil Repo panicked: %v", r)
		}
	}()

	h := &HeadroomHandler{
		Client: &http.Client{
			Transport: headroomRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
				}, nil
			}),
		},
	}

	if got := h.headroomURL(); got != headroom.DefaultURL {
		t.Fatalf("headroomURL() = %q, want %q", got, headroom.DefaultURL)
	}
	if codeAware, kompress := h.headroomFlags(); codeAware || !kompress {
		t.Fatalf("headroomFlags() = (%v, %v), want (false, true)", codeAware, kompress)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/headroom/proxy/health", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("*", "health")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	h.HandleHeadroomProxy(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func TestNewHeadroomHandler_SetsClientTimeout(t *testing.T) {
	h := NewHeadroomHandler(nil)
	if h.Client == nil || h.Client.Timeout <= 0 {
		t.Fatalf("expected bounded Client.Timeout on NewHeadroomHandler, got %+v", h.Client)
	}
}

func TestHandleHeadroomProxy_RejectsHostlessHeadroomURL(t *testing.T) {
	repo := newHeadroomTestRepo(t, "http:///only-path")
	h := NewHeadroomHandler(repo)

	req := httptest.NewRequest(http.MethodGet, "/api/headroom/proxy/health", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("*", "health")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	h.HandleHeadroomProxy(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 for hostless headroomUrl: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleHeadroomProxy_NilClientUsesBoundedFallback(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil Client panicked: %v", r)
		}
	}()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	repo := newHeadroomTestRepo(t, srv.URL)
	h := &HeadroomHandler{Repo: repo}

	req := httptest.NewRequest(http.MethodGet, "/api/headroom/proxy/health", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("*", "health")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	h.HandleHeadroomProxy(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleHeadroomProxy_CapsOversizedDashboardHTML(t *testing.T) {
	const upstreamBytes = 10 << 20
	body := &headroomCountingBody{remaining: upstreamBytes}

	repo := newHeadroomTestRepo(t, "http://127.0.0.1:8787")
	h := &HeadroomHandler{
		Repo: repo,
		Client: &http.Client{
			Transport: headroomRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
					Body:       body,
				}, nil
			}),
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/headroom/proxy/dashboard", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("*", "dashboard")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	h.HandleHeadroomProxy(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if body.readBytes > 8<<20 {
		t.Fatalf("read %d bytes from dashboard response, exceeds 8 MiB cap (%d)", body.readBytes, 8<<20)
	}
}
