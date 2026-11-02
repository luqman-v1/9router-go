package oauth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// These tests pin the OAuth-01 contract: every outbound call in the device
// flow carries the caller's context, so a client that hangs up — or a gateway
// shutting down — aborts the request instead of leaving a goroutine and an
// upstream connection parked until the client timeout expires.
//
// They drive the shared helpers rather than one provider's poll because those
// helpers are what every provider family funnels through: postJSON, postForm
// and getJSON are the three primitives, and the per-provider start/poll
// functions are thin wrappers over them. Proving the primitives honour
// cancellation proves the whole family, without a test needing to reach a real
// provider host.

// cancelledContext returns a context that is cancelled after a short delay,
// which is the shape a disconnecting client produces.
func cancelledContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	return ctx
}

// hangingServer never answers, so the only way a request against it returns is
// the context firing. The handler blocks on a channel that is closed when the
// server shuts down, rather than on the request's own context: a POST whose
// body has not been read keeps the connection half-open, and waiting on
// r.Context().Done() there lets the client's own read block instead of
// returning the transport error the test is looking for.
func hangingServer(t *testing.T) *httptest.Server {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Drain whatever the client sent so the transport can complete the
		// write side and surface the cancellation as a Do error.
		_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 1<<20))
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() {
		close(release)
		srv.Close()
	})
	return srv
}

// assertAborted checks that a call returned an error and did so promptly. A
// call that blocks past the grace window is the failure this test exists to
// catch: the context never reached the request.
func assertAborted(t *testing.T, name string, err error, elapsed time.Duration) {
	t.Helper()
	if err == nil {
		t.Errorf("%s returned no error; a cancelled context must surface one", name)
	}
	if elapsed > 2*time.Second {
		t.Errorf("%s took %v after cancellation; the context is not reaching the request", name, elapsed)
	}
}

func TestGetJSONHonoursCancelledContext(t *testing.T) {
	srv := hangingServer(t)
	start := time.Now()
	_, _, err := getJSON(cancelledContext(t), srv.URL, nil)
	assertAborted(t, "getJSON", err, time.Since(start))
}

func TestPostJSONHonoursCancelledContext(t *testing.T) {
	srv := hangingServer(t)
	start := time.Now()
	_, _, err := postJSON(cancelledContext(t), srv.URL, map[string]any{"x": 1}, nil)
	assertAborted(t, "postJSON", err, time.Since(start))
}

func TestPostFormHonoursCancelledContext(t *testing.T) {
	srv := hangingServer(t)
	start := time.Now()
	_, _, err := postForm(cancelledContext(t), srv.URL, map[string][]string{"a": {"b"}}, nil)
	assertAborted(t, "postForm", err, time.Since(start))
}

// TestHelpersHonourDeadline is the deadline half of the same contract: a
// context with a timeout bounds the call even when the upstream never answers.
func TestHelpersHonourDeadline(t *testing.T) {
	srv := hangingServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, _, err := getJSON(ctx, srv.URL, nil)
	assertAborted(t, "getJSON", err, time.Since(start))
}

// TestHelpersSucceedOnLiveContext is the control: the same helpers against a
// server that answers must still work. A test that only proves cancellation
// would pass just as well if every request were broken.
func TestHelpersSucceedOnLiveContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	data, status, err := getJSON(context.Background(), srv.URL, nil)
	if err != nil {
		t.Fatalf("getJSON against a live server: %v", err)
	}
	if status != http.StatusOK {
		t.Errorf("status = %d, want 200", status)
	}
	if v, _ := data["ok"].(bool); !v {
		t.Errorf("body = %v, want ok:true", data)
	}
}
