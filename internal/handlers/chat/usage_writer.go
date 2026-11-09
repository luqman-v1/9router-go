package chat

import (
	"sync"
	"sync/atomic"
	"time"

	"9router/proxy/internal/log"
)

// Off-path usage writes.
//
// logUsage used to run every SQLite write it needed — the usageHistory row,
// the requestDetails row, the compression-analytics row, and the daily
// aggregate read-modify-write — on the goroutine that had just finished
// streaming the response. All of them share one WAL writer, so under load each
// completion waited behind every other completion and behind the dashboard's
// own writes; and the daily blob RMW alone costs ~21 ms once a busy day has
// grown it past 400 KB, paid per request on a global mutex.
//
// None of that work changes what the client sees on the wire, so it runs on a
// single background worker instead. Jobs keep FIFO order (row → detail → daily
// aggregate, per request), which is the order the synchronous path used. The
// queue is bounded: a gateway that cannot write usage rows as fast as requests
// finish drops the surplus with a warning rather than pushing the delay back
// onto responses — a usage ledger may lose a row under overload, but the
// response path must not inherit the ledger's backlog.

// usageWriteQueueDepth bounds the pending usage jobs. At the ~20 KB a
// requestDetails payload can reach, a full queue is a few MB of retained
// payloads at worst.
const usageWriteQueueDepth = 256

// usageWriteJob is one self-contained unit of usage persistence. It captures
// the repo and the already-built payloads; the worker runs it verbatim.
type usageWriteJob func()

var (
	usageWriteOnce     sync.Once
	usageWriteQueue    chan usageWriteJob
	usageWritesDropped atomic.Int64
	// usageSynchronous runs every job inline at enqueue time. The integration
	// harness sets it: its assertions read usage rows straight after the HTTP
	// response, and teaching every one of them to drain would hide the exact
	// ordering the production path guarantees anyway.
	usageSynchronous atomic.Bool
)

func ensureUsageWorker() {
	usageWriteOnce.Do(func() {
		usageWriteQueue = make(chan usageWriteJob, usageWriteQueueDepth)
		go func() {
			for job := range usageWriteQueue {
				job()
			}
		}()
	})
}

// enqueueUsageWrite persists one usage unit off the response goroutine. A full
// queue drops the job: the warning carries the running total so an operator
// can tell a one-off burst from a sustained backlog.
func enqueueUsageWrite(job usageWriteJob) {
	if job == nil {
		return
	}
	if usageSynchronous.Load() {
		job()
		return
	}
	ensureUsageWorker()
	select {
	case usageWriteQueue <- job:
	default:
		dropped := usageWritesDropped.Add(1)
		log.Warn("usage", "write queue full, dropping usage row", "dropped_total", dropped)
	}
}

// FlushUsageWrites waits until every usage write enqueued before the call has
// landed, or the timeout expires. It is the drain hook for graceful shutdown
// and for tests that opt into the asynchronous path. Reports whether the
// drain completed.
func FlushUsageWrites(timeout time.Duration) bool {
	if usageSynchronous.Load() {
		return true
	}
	// A gateway that never served a request has no worker and nothing to drain.
	if usageWriteQueue == nil {
		return true
	}
	done := make(chan struct{})
	select {
	case usageWriteQueue <- func() { close(done) }:
	case <-time.After(timeout):
		return false
	}
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// SetUsageWritesSynchronousForTest toggles inline execution. The integration
// harness turns it on at boot; the async-specific tests turn it off and flush
// explicitly.
func SetUsageWritesSynchronousForTest(sync bool) {
	usageSynchronous.Store(sync)
}

// UsageWritesDropped reports how many usage jobs were discarded because the
// queue was full since process start.
func UsageWritesDropped() int64 {
	return usageWritesDropped.Load()
}
