package chat

import (
	"9router/proxy/internal/connlimit"
	"context"
	"net/http"
	"testing"
	"time"
)

// The free Antigravity backend refuses the fourth concurrent call per account,
// and the IDE never sends more than one. Serialising keeps a client that fans
// out parallel tool calls from parking the account.
func TestAcquireAntigravitySlot_Serialises(t *testing.T) {
	conn := "ag-test-serial"
	release, err := acquireAntigravitySlot(context.Background(), conn)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := acquireAntigravitySlot(ctx, conn); err == nil {
		t.Fatal("a second call must wait while the first is in flight")
	}
	release()

	second, err := acquireAntigravitySlot(context.Background(), conn)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	second()

	// Different connections do not serialise against each other.
	a, err := acquireAntigravitySlot(context.Background(), conn+"a")
	if err != nil {
		t.Fatalf("acquire a: %v", err)
	}
	defer a()
	if _, err := acquireAntigravitySlot(context.Background(), conn+"b"); err != nil {
		t.Fatalf("another connection must not wait: %v", err)
	}
}

// The returned release must free both slots exactly once. A closure that
// captured the result variable instead of a copy pointed at itself and
// overflowed the stack when the handler released it.
func TestAdmitAntigravity_ReleaseDoesNotRecurse(t *testing.T) {
	conn := "ag-test-release"
	release, err := admitAntigravity(context.Background(), conn)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	release()
	again, err := admitAntigravity(context.Background(), conn)
	if err != nil {
		t.Fatalf("re-admit after release: %v", err)
	}
	again()
}

// A breaker opened while a request waited on the serial slot must still refuse
// it: the slot is taken first, so the verdict is read after the queue, not
// before it. A refusal must also hand the slot back, or the connection stalls.
func TestAdmitAntigravity_RefusesWhenBreakerOpensWhileQueued(t *testing.T) {
	conn := "ag-test-breaker-queue"
	for i := 0; i < connlimit.DefaultFailureThreshold; i++ {
		connlimit.RecordFailure(conn, http.StatusTooManyRequests, nil)
	}
	release, err := admitAntigravity(context.Background(), conn)
	if err == nil {
		release()
		t.Fatal("an open breaker must refuse the request")
	}
	// The slot must be free again: a fresh connection-scoped acquire cannot
	// block on the refused request's leftovers.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	slot, err := acquireAntigravitySlot(ctx, conn)
	if err != nil {
		t.Fatalf("the refused request leaked its serial slot: %v", err)
	}
	slot()
}

// The queue behind the single slot must be bounded: connlimit exists to fail
// fast, and an unbounded backlog would let a fan-out client exhaust memory.
func TestAcquireAntigravitySlot_QueueIsBounded(t *testing.T) {
	conn := "ag-test-queue-depth"
	release, err := acquireAntigravitySlot(context.Background(), conn)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer release()

	var held []func()
	for i := 0; i < antigravityQueueDepth; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			if r, err := acquireAntigravitySlot(ctx, conn); err == nil {
				held = append(held, r)
			}
		}()
		time.Sleep(2 * time.Millisecond) // let it enqueue
		cancel()
		<-done
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := acquireAntigravitySlot(ctx, conn); err == nil {
		t.Fatal("a queue at depth must refuse rather than block")
	}
}

// A queue-full 429 is our own admission control, not an upstream verdict: it
// must exclude the connection for this pass without cooling the account.
func TestIsLocalQueueRefusal(t *testing.T) {
	upstream429 := &upstreamError{StatusCode: http.StatusTooManyRequests,
		Body: []byte(`{"error":{"message":"Resource has been exhausted","status":"RESOURCE_EXHAUSTED"}}`)}
	if isLocalQueueRefusal(upstream429) {
		t.Error("an upstream 429 must stay an upstream verdict")
	}
	local429 := &upstreamError{StatusCode: http.StatusTooManyRequests, Local: true,
		Body: []byte(`{"error":{"message":"antigravity connection queue is full","type":"queue_full","code":429}}`)}
	if !isLocalQueueRefusal(local429) {
		t.Error("our own queue refusal must be recognised")
	}
	// An upstream that echoes the same payload must still take its cooldown:
	// the flag is the only trusted evidence of local origin.
	echoed := &upstreamError{StatusCode: http.StatusTooManyRequests,
		Body: []byte(`{"error":{"message":"antigravity connection queue is full","type":"queue_full","code":429}}`)}
	if isLocalQueueRefusal(echoed) {
		t.Error("the body must not be trusted as proof of local origin")
	}
	if isLocalQueueRefusal(nil) {
		t.Error("nil must not match")
	}
}
