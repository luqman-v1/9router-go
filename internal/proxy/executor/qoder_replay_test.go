package executor

import (
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
