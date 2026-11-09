package handlers

import (
	"bufio"
	"context"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/usagetracker"
)

func TestHandleUsageStream(t *testing.T) {
	tracker := usagetracker.GetTracker()

	handler := HandleUsageStream(nil)
	server := httptest.NewServer(handler)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/usage/stream", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("expected text/event-stream, got %s", resp.Header.Get("Content-Type"))
	}

	reader := bufio.NewReader(resp.Body)

	// 1. Read initial state
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read line: %v", err)
	}
	if !strings.HasPrefix(line, "data: ") {
		t.Fatalf("expected data: prefix, got %s", line)
	}

	var initPayload usagetracker.StreamPayload
	dataBytes := strings.TrimPrefix(strings.TrimSpace(line), "data: ")
	if err := json.Unmarshal([]byte(dataBytes), &initPayload); err != nil {
		t.Fatalf("unmarshal init payload: %v", err)
	}

	// 2. Trigger pending request in tracker
	tracker.TrackPending("gemini-2.5-flash", "antigravity", "conn_test", true, false)

	// Read empty line
	_, _ = reader.ReadString('\n')

	// Read next data event
	line2, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read line2: %v", err)
	}
	if !strings.HasPrefix(line2, "data: ") {
		t.Fatalf("expected data: prefix on line2, got %s", line2)
	}

	var updatePayload usagetracker.StreamPayload
	dataBytes2 := strings.TrimPrefix(strings.TrimSpace(line2), "data: ")
	if err := json.Unmarshal([]byte(dataBytes2), &updatePayload); err != nil {
		t.Fatalf("unmarshal update payload: %v", err)
	}

	if len(updatePayload.ActiveRequests) == 0 {
		t.Fatalf("expected at least 1 active request in update payload")
	}

	tracker.TrackPending("gemini-2.5-flash", "antigravity", "conn_test", false, false)
}

// The usage stream carries the resilience layer's per-egress breaker state, so
// an operator watching the request flow sees an egress go OPEN beside the
// requests that stopped being served instead of inferring it from a dry list.
//
// It is also the only place the wiring is exercised: without the closure the
// boot installs, the field is silently absent and the dashboard's panel stays
// empty while the breaker blocks traffic.
func TestUsageStreamCarriesBreakerSnapshots(t *testing.T) {
	tracker := usagetracker.GetTracker()
	// Install the same closure SetupRoutes installs. A nil controller must
	// yield no snapshots rather than panicking on the stream's read lock.
	tracker.SetBreakerStatus(func() []usagetracker.BreakerSnapshot { return nil })
	t.Cleanup(func() { tracker.SetBreakerStatus(nil) })

	server := httptest.NewServer(HandleUsageStream(nil))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/usage/stream", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read line: %v", err)
	}
	var payload usagetracker.StreamPayload
	if err := json.Unmarshal([]byte(strings.TrimPrefix(strings.TrimSpace(line), "data: ")), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	// A nil controller omits the field entirely, so the dashboard's existing
	// consumers see no change and no empty array.
	if payload.Breakers != nil {
		t.Fatalf("Breakers = %v, want nil when no resilience layer is wired", payload.Breakers)
	}
	// With snapshots present, they must survive the round trip with their
	// state legible — the field is what the operator reads. SetBreakerStatus
	// only installs the hook: it does not broadcast, so the hook's first
	// appearance on the wire is the next scheduled broadcast, which
	// TrackPending triggers below.
	tracker.SetBreakerStatus(func() []usagetracker.BreakerSnapshot {
		return []usagetracker.BreakerSnapshot{{
			Name:         "deepseek|pool-a",
			State:        "OPEN",
			FailureCount: 5,
			SuccessCount: 0,
			RetryAfterMs: 12345,
		}}
	})
	tracker.TrackPending("m", "p", "c", true, false)
	// SSE events are "data: ...\n\n": the blank line terminates each event.
	// The broadcast is debounced by 50ms, so read only after that window —
	// otherwise the reader blocks on a frame the tracker has not sent yet.
	time.Sleep(150 * time.Millisecond)
	// Event 2 is terminated by its own blank line.
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatalf("read event 2 terminator: %v", err)
	}
	line2, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read event 2 data: %v", err)
	}
	var update usagetracker.StreamPayload
	if err := json.Unmarshal([]byte(strings.TrimPrefix(strings.TrimSpace(line2), "data: ")), &update); err != nil {
		t.Fatalf("unmarshal update: %v", err)
	}
	if len(update.Breakers) != 1 {
		t.Fatalf("breakers = %d, want 1", len(update.Breakers))
	}
	b := update.Breakers[0]
	if b.Name != "deepseek|pool-a" || b.State != "OPEN" || b.RetryAfterMs != 12345 {
		t.Errorf("breaker = %+v, want deepseek|pool-a OPEN retryAfter 12345", b)
	}
	if b.FailureCount != 5 {
		t.Errorf("failureCount = %d, want 5", b.FailureCount)
	}
	tracker.TrackPending("m", "p", "c", false, false)
}
