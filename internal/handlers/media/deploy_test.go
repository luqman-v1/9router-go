package media

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestPollDeployStatus verifies the deployment status polling loop against a
// fixture server: it must keep polling while "building" and stop on "succeeded".
func TestPollDeployStatus(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "Bearer tok" {
			t.Errorf("expected Bearer auth, got %q", auth)
		}
		calls++
		if calls < 3 {
			w.Write([]byte(`{"status":"building"}`))
			return
		}
		w.Write([]byte(`{"status":"succeeded","url":"x"}`))
	}))
	defer srv.Close()

	got, err := pollDeployStatus(t.Context(), &http.Client{Timeout: time.Second}, srv.URL, "tok", time.Millisecond, 10, func(data map[string]any) (bool, error) {
		return data["status"] == "succeeded", nil
	})
	if err != nil {
		t.Fatalf("pollDeployStatus errored: %v", err)
	}
	if got["url"] != "x" {
		t.Errorf("expected url x in result, got %v", got)
	}
	if calls < 3 {
		t.Errorf("expected multiple polls, got %d", calls)
	}

	// check returning an error surfaces immediately.
	if _, err := pollDeployStatus(t.Context(), &http.Client{Timeout: time.Second}, srv.URL, "tok", time.Millisecond, 3, func(data map[string]any) (bool, error) {
		return false, &pollErr{}
	}); err == nil || err.Error() != "boom" {
		t.Errorf("expected boom, got %v", err)
	}

	// Zero attempts: immediate timeout.
	if _, err := pollDeployStatus(t.Context(), &http.Client{}, "http://x", "tok", time.Millisecond, 0, func(map[string]any) (bool, error) { return false, nil }); err == nil || err.Error() != "deployment timed out" {
		t.Errorf("expected deployment timed out, got %v", err)
	}
}

// TestIsDenoTerminalStatus pins which revision states stop the wait. "queued"
// and "building" (plus an absent status, which means the field was missing)
// must keep polling; everything else is terminal. Treating a rejected build as
// still-running is what made the handler burn its full 60s budget before
// reporting "deployment timed out" instead of the actual failure.
func TestIsDenoTerminalStatus(t *testing.T) {
	tests := []struct {
		status string
		want   bool
	}{
		{"succeeded", true},
		{"failed", true},
		{"cancelled", true},
		{"canceled", true},
		{"error", true},
		{"queued", false},
		{"building", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run("status="+tt.status, func(t *testing.T) {
			if got := isDenoTerminalStatus(tt.status); got != tt.want {
				t.Errorf("isDenoTerminalStatus(%q) = %v, want %v", tt.status, got, tt.want)
			}
		})
	}
}

// TestAwaitDenoRevisionSkipsPollWhenAlreadyTerminal pins the fast path: a
// revision the deploy call already answered as terminal must resolve without a
// single request. A client pointed at a URL that refuses connections proves it
// — any poll at all would surface an error instead of the status.
func TestAwaitDenoRevisionSkipsPollWhenAlreadyTerminal(t *testing.T) {
	for _, status := range []string{"failed", "cancelled", "error"} {
		t.Run(status, func(t *testing.T) {
			got, err := awaitDenoRevision(t.Context(), &http.Client{}, "http://127.0.0.1:1/revisions/x", "tok", status)
			if err != nil {
				t.Fatalf("awaitDenoRevision(%q) errored: %v", status, err)
			}
			if got != status {
				t.Errorf("awaitDenoRevision(%q) = %q, want it echoed back", status, got)
			}
		})
	}
}

// TestAwaitDenoRevisionPollsUntilTerminal covers the in-flight path: a queued
// revision is polled, and the loop stops the moment a terminal status arrives
// rather than running out its attempts.
func TestAwaitDenoRevisionPollsUntilTerminal(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls < 3 {
			w.Write([]byte(`{"status":"building"}`))
			return
		}
		w.Write([]byte(`{"status":"succeeded"}`))
	}))
	defer srv.Close()

	got, err := awaitDenoRevision(t.Context(), srv.Client(), srv.URL, "tok", "queued")
	if err != nil {
		t.Fatalf("awaitDenoRevision: %v", err)
	}
	if got != "succeeded" {
		t.Errorf("awaitDenoRevision = %q, want succeeded", got)
	}
	if calls < 3 {
		t.Errorf("expected the loop to keep polling while building, got %d calls", calls)
	}
}

// TestAwaitDenoRevisionReportsFailureNotTimeout is the regression this change
// exists for: a revision Deno rejects mid-poll must surface its real status,
// not a timeout after the full attempt budget.
func TestAwaitDenoRevisionReportsFailureNotTimeout(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Write([]byte(`{"status":"failed"}`))
	}))
	defer srv.Close()

	got, err := awaitDenoRevision(t.Context(), srv.Client(), srv.URL, "tok", "building")
	if err != nil {
		t.Fatalf("awaitDenoRevision: %v", err)
	}
	if got != "failed" {
		t.Errorf("awaitDenoRevision = %q, want failed", got)
	}
	if calls != 1 {
		t.Errorf("expected to stop at the first terminal status, got %d polls", calls)
	}
}

type pollErr struct{}

func (*pollErr) Error() string { return "boom" }
