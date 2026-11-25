package dashboard

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/service"
)

func TestHandleRestartGateway(t *testing.T) {
	prevDelay, prevSelf, prevSupported := restartDelay, restartSelf, restartSelfSupported
	prevStatus, prevStop := serviceStatus, requestStop
	restartDelay = 0
	t.Cleanup(func() {
		restartDelay, restartSelf, restartSelfSupported = prevDelay, prevSelf, prevSupported
		serviceStatus, requestStop = prevStatus, prevStop
	})

	type seams struct {
		self chan struct{}
		stop chan struct{}
	}
	// stub wires both mechanisms; supervised selects the supervisor branch.
	stub := func(supervised bool) seams {
		s := seams{self: make(chan struct{}, 1), stop: make(chan struct{}, 1)}
		restartSelf = func() { s.self <- struct{}{} }
		requestStop = func() { s.stop <- struct{}{} }
		restartSelfSupported = func() error { return nil }
		if supervised {
			serviceStatus = func() (service.Info, bool) {
				return service.Info{Backend: service.BackendSystemdUser, Active: true}, true
			}
		} else {
			serviceStatus = func() (service.Info, bool) { return service.Info{}, false }
		}
		return s
	}

	expectIdle := func(t *testing.T, s seams) {
		t.Helper()
		select {
		case <-s.self:
			t.Fatal("the process replaced itself for a request that must not restart it")
		case <-s.stop:
			t.Fatal("the process stopped for a request that must not restart it")
		case <-time.After(50 * time.Millisecond):
		}
	}
	expectSignal := func(t *testing.T, ch chan struct{}, what string) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(2 * time.Second):
			t.Fatalf("%s was never requested", what)
		}
	}
	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/system/restart", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		(&DashboardHandler{}).HandleRestartGateway(rec, req)
		return rec
	}

	t.Run("refuses a body without the confirmation word", func(t *testing.T) {
		s := stub(false)
		defer expectIdle(t, s)

		for _, body := range []string{"", "{}", `{"confirm":"yes"}`, "confirm=restart"} {
			if rec := post(body); rec.Code != http.StatusBadRequest {
				t.Errorf("body %q: status = %d, want %d", body, rec.Code, http.StatusBadRequest)
			}
		}
	})

	t.Run("a supervisor that restarts on exit owns the restart", func(t *testing.T) {
		s := stub(true)

		if rec := post(`{"confirm":"restart"}`); rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		expectSignal(t, s.stop, "a stop request")
		select {
		case <-s.self:
			t.Fatal("the process spawned a replacement under a supervisor: two processes would race for the port")
		default:
		}
	})

	t.Run("an unsupervised process replaces itself", func(t *testing.T) {
		s := stub(false)

		if rec := post(`{"confirm":"restart"}`); rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		expectSignal(t, s.self, "a self restart")
		select {
		case <-s.stop:
			t.Fatal("the process asked to stop with no supervisor to restart it")
		default:
		}
	})

	t.Run("refuses a non-JSON body", func(t *testing.T) {
		s := stub(false)
		defer expectIdle(t, s)

		for _, tt := range []struct{ ct, body string }{
			{"text/plain", `{"confirm":"restart"}`},
			{"application/x-www-form-urlencoded", "confirm=restart"},
		} {
			req := httptest.NewRequest(http.MethodPost, "/api/system/restart", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", tt.ct)
			rec := httptest.NewRecorder()
			(&DashboardHandler{}).HandleRestartGateway(rec, req)
			if rec.Code != http.StatusUnsupportedMediaType {
				t.Errorf("%s: status = %d, want %d", tt.ct, rec.Code, http.StatusUnsupportedMediaType)
			}
		}
	})

	t.Run("refuses trailing data", func(t *testing.T) {
		s := stub(false)
		defer expectIdle(t, s)

		req := httptest.NewRequest(http.MethodPost, "/api/system/restart",
			strings.NewReader(`{"confirm":"restart"} {"confirm":"restart"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		(&DashboardHandler{}).HandleRestartGateway(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})

	t.Run("an installed-but-inactive supervisor does not own the restart", func(t *testing.T) {
		s := stub(false)
		defer expectIdle(t, s)
		// A unit file on disk is not a running supervisor: with the service not
		// active, a stop request would leave the gateway down.
		serviceStatus = func() (service.Info, bool) {
			return service.Info{Backend: service.BackendSystemdUser, Active: false}, true
		}

		if rec := post(`{"confirm":"restart"}`); rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		expectSignal(t, s.self, "a self restart")
	})

	t.Run("an unknown active state does not own the restart", func(t *testing.T) {
		s := stub(false)
		defer expectIdle(t, s)
		serviceStatus = func() (service.Info, bool) {
			return service.Info{Backend: service.BackendSystemdUser, Active: true, ActiveUnknown: true}, true
		}

		if rec := post(`{"confirm":"restart"}`); rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		expectSignal(t, s.self, "a self restart")
	})

	t.Run("refuses when the binary cannot be re-exec'd", func(t *testing.T) {
		s := stub(false)
		defer expectIdle(t, s)
		restartSelfSupported = func() error { return errors.New("binary gone") }

		if rec := post(`{"confirm":"restart"}`); rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
		}
	})

	t.Run("answers before restarting", func(t *testing.T) {
		stub(true)

		rec := post(`{"confirm":"restart"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if !strings.Contains(rec.Body.String(), `"restarting":true`) {
			t.Errorf("body = %q, want a restarting flag", rec.Body.String())
		}
	})
}
