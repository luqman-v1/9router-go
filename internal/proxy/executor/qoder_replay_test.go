package executor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"9router/proxy/internal/proxy"
)

// A queued model announces itself with a 10605 envelope; the executor must
// wait it out and replay the same request instead of failing it as billing.
func TestForwardQoder_ReplaysQueuedModel(t *testing.T) {
	var inferenceCalls, statusCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			// The queue-status endpoint reports ready, so the replay is
			// immediate and the test does not sleep.
			statusCalls.Add(1)
			w.Write([]byte(`{"isQueued":false,"queueCount":0,"retryAfterSeconds":2}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if inferenceCalls.Add(1) == 1 {
			inner := `{"code":"10605","message":"{\"isQueued\":true,\"queueType\":\"p3\",\"retryAfterSeconds\":30,\"subCode\":\"RESOURCE_LIMIT\"}","subCode":"RESOURCE_LIMIT"}`
			w.Write([]byte(qoderEnvelopeError(http.StatusInternalServerError, inner)))
			return
		}
		w.Write([]byte(qoderSSE(qoderChunk(`{"content":"ok"}`, "null"), qoderChunk("{}", "stop"))))
	}))
	defer srv.Close()

	rec := httptest.NewRecorder()
	if err := ForwardQoder(rec, qoderRequest(t, srv)); err != nil {
		t.Fatalf("ForwardQoder: %v", err)
	}
	if inferenceCalls.Load() != 2 {
		t.Errorf("inference calls = %d, want the original plus one replay", inferenceCalls.Load())
	}
	if statusCalls.Load() != 1 {
		t.Errorf("status polls = %d, want 1", statusCalls.Load())
	}
	if !strings.Contains(rec.Body.String(), "ok") {
		t.Errorf("replayed completion lost: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "qoder_billing_block") {
		t.Errorf("queue signal surfaced as billing: %s", rec.Body.String())
	}
}

// When the queue never drains inside the budget the request fails with a
// retryable rate-limit error, not a billing block.
func TestForwardQoder_QueueBudgetExceeded(t *testing.T) {
	t.Setenv(QoderModelQueueMaxWaitEnv, "1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// Outpace the 1ms budget so the first queue response already trips it
		// and the test does not sleep out a retry interval.
		time.Sleep(5 * time.Millisecond)
		w.Header().Set("Content-Type", "text/event-stream")
		inner := `{"code":"10605","message":"{\"isQueued\":true,\"queueType\":\"p3\",\"retryAfterSeconds\":30,\"subCode\":\"RESOURCE_LIMIT\"}","subCode":"RESOURCE_LIMIT"}`
		w.Write([]byte(qoderEnvelopeError(http.StatusInternalServerError, inner)))
	}))
	defer srv.Close()

	rec := httptest.NewRecorder()
	err := ForwardQoder(rec, qoderRequest(t, srv))
	var ue *proxy.UpstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UpstreamError, got %v", err)
	}
	if ue.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", ue.StatusCode)
	}
	if !strings.Contains(string(ue.Body), "still queued") {
		t.Errorf("body = %s", ue.Body)
	}
}

// A caller with a shorter budget than the queue wait must still learn why its
// request stopped. The dashboard probe wraps its call in a 45s deadline while
// the queue answers in 30s windows, so the expiry lands inside the sleep; it
// used to surface as a bare "context canceled" that the chat handler reported
// as HTTP 499 "client closed request" — a hangup the client never made.
func TestForwardQoder_CallerDeadlineReportsQueue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		inner := `{"code":"10605","message":"{\"isQueued\":true,\"queueType\":\"p3\",\"retryAfterSeconds\":30,\"subCode\":\"RESOURCE_LIMIT\"}","subCode":"RESOURCE_LIMIT"}`
		w.Write([]byte(qoderEnvelopeError(http.StatusInternalServerError, inner)))
	}))
	defer srv.Close()

	req := qoderRequest(t, srv)
	// Expire the caller's context while the executor sleeps out the 30s
	// retry interval, which is what a 45s probe budget does against a 30s
	// queue window.
	ctx, cancel := context.WithTimeout(req.Ctx, 50*time.Millisecond)
	defer cancel()
	req.Ctx = ctx

	rec := httptest.NewRecorder()
	err := ForwardQoder(rec, req)
	var ue *proxy.UpstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UpstreamError, got %v", err)
	}
	if ue.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 (a queued model is retryable, not a client hangup)", ue.StatusCode)
	}
	if !strings.Contains(string(ue.Body), "still queued") {
		t.Errorf("body = %s, want the queue state named", ue.Body)
	}
	if strings.Contains(string(ue.Body), "context canceled") {
		t.Errorf("body = %s, must not report the caller's deadline as an upstream cancel", ue.Body)
	}
}

// A queue window longer than the caller's remaining budget must be refused at
// once, not slept through. Qoder answers retryAfterSeconds=30 and the dashboard
// probe allows 45s, so the old loop spent the whole budget to report a timeout
// the first refusal had already established.
func TestForwardQoder_QueueWindowBeyondBudgetFailsFast(t *testing.T) {
	var inferenceCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		inferenceCalls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		inner := `{"code":"10605","message":"{\"isQueued\":true,\"queueType\":\"p3\",\"retryAfterSeconds\":30,\"subCode\":\"RESOURCE_LIMIT\"}","subCode":"RESOURCE_LIMIT"}`
		w.Write([]byte(qoderEnvelopeError(http.StatusInternalServerError, inner)))
	}))
	defer srv.Close()

	req := qoderRequest(t, srv)
	// A 5s budget cannot absorb a 30s window.
	ctx, cancel := context.WithTimeout(req.Ctx, 5*time.Second)
	defer cancel()
	req.Ctx = ctx

	start := time.Now()
	rec := httptest.NewRecorder()
	err := ForwardQoder(rec, req)
	elapsed := time.Since(start)

	var ue *proxy.UpstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UpstreamError, got %v", err)
	}
	if ue.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", ue.StatusCode)
	}
	if !strings.Contains(string(ue.Body), "still queued") {
		t.Errorf("body = %s, want the queue state named", ue.Body)
	}
	if elapsed > 2*time.Second {
		t.Errorf("elapsed = %v, want an immediate refusal (no sleep into the deadline)", elapsed)
	}
	if n := inferenceCalls.Load(); n != 1 {
		t.Errorf("inference calls = %d, want exactly 1", n)
	}
}

// A machine id derived from the user id is sent instead of a fresh uuid, so a
// connection without a stored machine id keeps its queue position.
func TestBuildQoderCosyHeaders_StableMachineID(t *testing.T) {
	build := func() map[string]string {
		headers, err := buildQoderCosyHeaders([]byte("body"), "https://api3.qoder.sh/algo/x", QoderCosyCreds{
			UserID: "user-1", AuthToken: "tok",
		})
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		return headers
	}
	first := build()["Cosy-Machineid"]
	if first == "" || first != build()["Cosy-Machineid"] {
		t.Errorf("machine id not stable across calls: %q", first)
	}
	if first != QoderDeriveMachineID("user-1") {
		t.Errorf("machine id %q is not the derived one", first)
	}
}

// A billing refusal on the first frame is the upstream answer, not stream
// content: it must reach chatCore as the 403 upstream means so it reads as
// insufficient_quota and the failover loop can try another account, locking
// only the refused model. Relaying the marker inside a 200 stream read as
// success, so a dfmodel out of credits was never retried elsewhere and the
// client saw the raw marker. Streaming and non-streaming requests are
// classified the same way.
func TestForwardQoder_FirstFrameBillingRefusalIs403(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "nonstream"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			var inferenceCalls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				inferenceCalls.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				inner := `{"code":"112","message":"daily limit exceeded","pricingUrl":"https://qoder.com/pricing"}`
				// The envelope rides a 500 to prove the billing verdict sets
				// the status, not the envelope's own code.
				w.Write([]byte(qoderEnvelopeError(http.StatusInternalServerError, inner)))
			}))
			defer srv.Close()

			req := qoderRequest(t, srv)
			req.IsStream = stream
			rec := httptest.NewRecorder()
			err := ForwardQoder(rec, req)
			var ue *proxy.UpstreamError
			if !errors.As(err, &ue) {
				t.Fatalf("expected UpstreamError, got %v", err)
			}
			if ue.StatusCode != http.StatusForbidden {
				t.Errorf("status = %d, want 403", ue.StatusCode)
			}
			if !strings.Contains(string(ue.Body), "pricingUrl") {
				t.Errorf("body = %s, want the refusal text", ue.Body)
			}
			if n := inferenceCalls.Load(); n != 1 {
				t.Errorf("inference calls = %d, want 1 (a refusal is not replayed)", n)
			}
		})
	}
}

// A non-billing first-frame failure keeps the envelope's status when it is a
// 4xx/5xx and falls back to 502 otherwise, matching upstream wrapQoderSSE.
func TestForwardQoder_FirstFrameErrorStatusMapping(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		wantStatus int
	}{
		{"upstream 500 keeps its status", http.StatusInternalServerError, http.StatusInternalServerError},
		{"status outside 4xx/5xx maps to 502", 999, http.StatusBadGateway},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := qoderTestServer(t, qoderEnvelopeError(tc.status, `{"code":"500","message":"boom"}`))
			req := qoderRequest(t, srv)
			req.IsStream = true
			rec := httptest.NewRecorder()
			err := ForwardQoder(rec, req)
			var ue *proxy.UpstreamError
			if !errors.As(err, &ue) {
				t.Fatalf("expected UpstreamError, got %v", err)
			}
			if ue.StatusCode != tc.wantStatus {
				t.Errorf("status = %d, want %d", ue.StatusCode, tc.wantStatus)
			}
		})
	}
}
