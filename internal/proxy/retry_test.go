package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// The delays are seconds upstream, so a test that exercises them for real would
// run for minutes. The policy table is exercised directly; only the attempt
// counting and the client-gone path run against a live server.
func TestTransientRetryPolicy_MatchesUpstreamDefaults(t *testing.T) {
	// open-sse/config/runtimeConfig.js DEFAULT_RETRY_CONFIG.
	want := map[int]struct {
		attempts int
		delay    time.Duration
	}{
		http.StatusBadGateway:         {3, 3 * time.Second},
		http.StatusServiceUnavailable: {3, 2 * time.Second},
		http.StatusGatewayTimeout:     {2, 3 * time.Second},
	}

	for status, expected := range want {
		policy, ok := transientRetryPolicy[status]
		if !ok {
			t.Fatalf("status %d must have a retry policy", status)
		}
		if policy.attempts != expected.attempts || policy.delay != expected.delay {
			t.Errorf("status %d policy = %d attempts @%v, want %d @%v",
				status, policy.attempts, policy.delay, expected.attempts, expected.delay)
		}
	}

	// A 429 is an account-level window, not a transient blip: repeating it here
	// would deepen the throttle on the same credential that account fallback is
	// about to hand off to the next connection.
	if _, ok := transientRetryPolicy[http.StatusTooManyRequests]; ok {
		t.Error("429 must not be retried here; it belongs to the account fallback path")
	}
	// A 400 is our bug and must never be replayed.
	if _, ok := transientRetryPolicy[400]; ok {
		t.Error("400 must not be retried")
	}
}

// A temporarily-unavailable upstream must be re-sent inside the same client turn:
// the gateway's only answer to opencode-zen's 503 service_overloaded used to be
// "fail this request", even though the same upstream served the very next attempt.
func TestDoRequest_RetriesTransientUpstreamUntilServed(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The upstream answers 503 for the first three attempts, then serves.
		if calls.Add(1) <= 3 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `{"error":{"code":"service_overloaded","message":"The backend is temporarily overloaded. Please retry."}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"ok\":true}\n\n")
	}))
	defer srv.Close()

	resp, err := DoRequest(context.Background(), srv.Client(), "POST", srv.URL, nil, []byte(`{"model":"muse-spark-1.3-contributor-free"}`))
	if err != nil {
		t.Fatalf("a recovered upstream must serve the request, got %v", err)
	}
	defer resp.Body.Close()

	if got := calls.Load(); got != 4 {
		t.Errorf("expected 4 upstream attempts (3 failing + 1 served), got %d", got)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

// When the upstream never recovers, the caller must receive the real status and
// body — not a generic "upstream request failed" — so failover sees a 503 and
// can pick another account.
func TestDoRequest_ReportsRealStatusWhenRetriesExhausted(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"error":{"code":"service_overloaded","message":"still overloaded"}}`)
	}))
	defer srv.Close()

	_, err := DoRequest(context.Background(), srv.Client(), "POST", srv.URL, nil, []byte(`{"model":"muse-spark-1.3-contributor-free"}`))
	if err == nil {
		t.Fatal("expected an error when the upstream never recovers")
	}

	var ue *UpstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UpstreamError so failover can branch on the status, got %T: %v", err, err)
	}
	if ue.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected status 503 to survive, got %d", ue.StatusCode)
	}
	// 1 initial attempt + 3 retries for a 503.
	if got := calls.Load(); got != 4 {
		t.Errorf("expected 4 attempts before giving up, got %d", got)
	}
}

func TestDoRequest_DoesNotRetryClientErrors(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusTooManyRequests} {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			// An explicit JSON content type, like every provider rejection
			// carries. A bare text/plain 403 is read by isProxyFailure as a local
			// proxy refusing the tunnel and re-dials directly — a pre-existing
			// rule about transport, not about retrying.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			fmt.Fprint(w, `{"error":{"message":"nope"}}`)
		}))

		_, err := DoRequest(context.Background(), srv.Client(), "POST", srv.URL, nil, []byte(`{"bad":true}`))
		srv.Close()

		var ue *UpstreamError
		if !errors.As(err, &ue) {
			t.Fatalf("status %d: expected *UpstreamError, got %T", status, err)
		}
		if ue.StatusCode != status {
			t.Errorf("status %d: expected it reported verbatim, got %d", status, ue.StatusCode)
		}
		if got := calls.Load(); got != 1 {
			t.Errorf("status %d: must not be retried, got %d attempts", status, got)
		}
	}
}

// An upstream that goes from 503 to 400 on the retry must report the 400: the
// later answer describes the current state, and locking the account for a
// request-scoped error would keep a healthy credential out of rotation.
func TestDoRequest_RetryStopsWhenStatusStopsBeingTransient(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"model not supported"}}`)
	}))
	defer srv.Close()

	_, err := DoRequest(context.Background(), srv.Client(), "POST", srv.URL, nil, []byte(`{"model":"x"}`))

	var ue *UpstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UpstreamError, got %T", err)
	}
	if ue.StatusCode != http.StatusBadRequest {
		t.Errorf("expected the final 400, got %d", ue.StatusCode)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("expected the retry to stop after the 400, got %d attempts", got)
	}
}

// A client that disconnects mid-backoff must not keep the upstream answering for
// a turn nobody reads: the original failure is reported and the wait is cut short.
func TestDoRequest_AbandonsRetryWhenClientIsGone(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"error":{"code":"service_overloaded"}}`)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the client hung up before the request went out

	start := time.Now()
	_, err := DoRequest(ctx, srv.Client(), "POST", srv.URL, nil, []byte(`{"model":"x"}`))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error for a cancelled client context")
	}
	// The 503 policy waits 2s per retry; a cancelled context must not pay it.
	if elapsed > time.Second {
		t.Errorf("cancelled client waited %v for a retry backoff it cannot use", elapsed)
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("expected no upstream call for an already-cancelled context, got %d", got)
	}
}

func TestSleepCtx_ReturnsImmediatelyForNonPositiveDelay(t *testing.T) {
	if err := sleepCtx(context.Background(), 0); err != nil {
		t.Errorf("zero delay should not report a context error, got %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepCtx(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}
