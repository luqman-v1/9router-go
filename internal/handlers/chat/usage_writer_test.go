package chat

import (
	"sync"
	"testing"
	"time"
)

// asyncModeForTest flips the package into the production asynchronous mode and
// restores the suite's inline mode afterwards. Every async test uses it.
func asyncModeForTest(t *testing.T) {
	t.Helper()
	SetUsageWritesSynchronousForTest(false)
	t.Cleanup(func() { SetUsageWritesSynchronousForTest(true) })
}

// TestUsageWriter_JobsLandAfterFlush pins the production contract: a job
// enqueued on the response path has landed once FlushUsageWrites returns.
func TestUsageWriter_JobsLandAfterFlush(t *testing.T) {
	asyncModeForTest(t)

	var mu sync.Mutex
	landed := make([]int, 0, 3)
	for i := 0; i < 3; i++ {
		i := i
		enqueueUsageWrite(func() {
			mu.Lock()
			defer mu.Unlock()
			landed = append(landed, i)
		})
	}

	if !FlushUsageWrites(2 * time.Second) {
		t.Fatal("flush timed out with an idle queue")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(landed) != 3 {
		t.Fatalf("jobs landed = %d, want 3", len(landed))
	}
	for i, got := range landed {
		if got != i {
			t.Fatalf("job %d landed at position %d: FIFO order lost", i, got)
		}
	}
}

// TestUsageWriter_FullQueueDropsInsteadOfBlocking pins the overload contract:
// when the queue is full the enqueue returns immediately and drops, so the
// response path never inherits the writer's backlog.
func TestUsageWriter_FullQueueDropsInsteadOfBlocking(t *testing.T) {
	asyncModeForTest(t)

	// A job that parks until released occupies the worker; the queue then
	// fills behind it and every further enqueue must drop, not block.
	release := make(chan struct{})
	blocked := make(chan struct{})
	enqueueUsageWrite(func() {
		close(blocked)
		<-release
	})
	<-blocked

	filled := 0
	for {
		select {
		case usageWriteQueue <- func() {}:
			filled++
			continue
		default:
		}
		break
	}
	if filled == 0 {
		t.Fatal("queue never accepted any job; the worker is not parked as arranged")
	}

	before := UsageWritesDropped()
	done := make(chan struct{})
	go func() {
		enqueueUsageWrite(func() {})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("enqueue blocked on a full queue")
	}
	if UsageWritesDropped() != before+1 {
		t.Fatalf("dropped = %d, want %d", UsageWritesDropped(), before+1)
	}

	close(release)
	if !FlushUsageWrites(2 * time.Second) {
		t.Fatal("flush timed out after unblocking the worker")
	}
}

// TestUsageWriter_SynchronousModeRunsInline pins the harness mode: with sync
// mode on, a job has run before enqueue returns, so callers never need to
// flush.
func TestUsageWriter_SynchronousModeRunsInline(t *testing.T) {
	if !usageSynchronous.Load() {
		t.Skip("suite default changed; this test pins the inline mode")
	}

	ran := false
	enqueueUsageWrite(func() { ran = true })
	if !ran {
		t.Fatal("synchronous mode deferred the job")
	}
	if !FlushUsageWrites(time.Second) {
		t.Fatal("flush failed in synchronous mode")
	}
}

// TestUsageWriter_FlushWithoutWorker pins the cold path: a process that never
// served a request has no worker, and the drain must report success rather
// than waiting out its timeout.
func TestUsageWriter_FlushWithoutWorker(t *testing.T) {
	asyncModeForTest(t)

	usageWriteOnce = sync.Once{}
	usageWriteQueue = nil
	if !FlushUsageWrites(50 * time.Millisecond) {
		t.Fatal("flush waited on a queue that was never created")
	}
}
