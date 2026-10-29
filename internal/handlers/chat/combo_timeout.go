package chat

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// comboResponseTimeout serializes timeout expiry with response commitment. A
// canceled candidate can never write into the next candidate's response. Once
// committed, the timer is disarmed without canceling the live upstream stream.
type comboResponseTimeout struct {
	http.ResponseWriter
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	timer     *time.Timer
	committed bool
	expired   bool
	header    http.Header
}

func newComboResponseTimeout(ctx context.Context, w http.ResponseWriter, timeout time.Duration) *comboResponseTimeout {
	ctx, cancel := context.WithCancel(ctx)
	t := &comboResponseTimeout{ResponseWriter: w, ctx: ctx, cancel: cancel, header: make(http.Header)}
	t.timer = time.AfterFunc(timeout, t.expire)
	return t
}

func (t *comboResponseTimeout) expire() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.committed {
		t.expired = true
		t.cancel()
	}
}

func (t *comboResponseTimeout) Header() http.Header {
	return t.header
}

func (t *comboResponseTimeout) commitLocked() bool {
	if t.expired {
		return false
	}
	if !t.committed {
		for key, values := range t.header {
			t.ResponseWriter.Header()[key] = values
		}
	}
	t.committed = true
	t.timer.Stop()
	return true
}

func (t *comboResponseTimeout) WriteHeader(code int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.commitLocked() {
		t.ResponseWriter.WriteHeader(code)
	}
}

func (t *comboResponseTimeout) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.commitLocked() {
		return 0, context.DeadlineExceeded
	}
	return t.ResponseWriter.Write(b)
}

func (t *comboResponseTimeout) Flush() {
	t.mu.Lock()
	defer t.mu.Unlock()
	// Flush alone is not usable output and must not disarm the timer.
	if t.committed {
		if f, ok := t.ResponseWriter.(http.Flusher); ok {
			f.Flush()
		}
	}
}

func (t *comboResponseTimeout) finish() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.timer.Stop()
	t.cancel()
	return t.expired
}

func (t *comboResponseTimeout) timedOut() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.expired
}

// comboTimeoutPolicy returns the per-candidate response timeout and the grace
// period a timed-out model sits out. Both are zero when the timeout is off.
func (h *ChatHandler) comboTimeoutPolicy(comboName string) (timeout, grace time.Duration) {
	settings, err := h.Repo.GetSettings()
	if err != nil || settings == nil {
		return 0, 0
	}
	cfg := settings.ComboStrategies[comboName]
	if cfg.FirstTokenTimeoutMs <= 0 {
		return 0, 0
	}
	return time.Duration(cfg.FirstTokenTimeoutMs) * time.Millisecond,
		time.Duration(cfg.FirstTokenGrace()) * time.Millisecond
}

func comboBenchKey(comboName, entry string) string {
	return comboName + "\x00" + entry
}

// benchTimedOutModel keeps a model that missed its response timeout out of the
// combo chain until the grace period has elapsed. The state is in memory on
// purpose: a restart is a fresh start, and a stale bench must never outlive
// the process that observed the slowness.
func (h *ChatHandler) benchTimedOutModel(comboName, entry string, grace time.Duration) {
	if grace <= 0 {
		return
	}
	now := time.Now()
	h.timeoutBenchMu.Lock()
	defer h.timeoutBenchMu.Unlock()
	if h.timeoutBench == nil {
		h.timeoutBench = make(map[string]time.Time)
	}
	for k, until := range h.timeoutBench {
		if !until.After(now) {
			delete(h.timeoutBench, k)
		}
	}
	h.timeoutBench[comboBenchKey(comboName, entry)] = now.Add(grace)
}

// withoutBenchedModels drops models still inside their grace period. If that
// would leave nothing to try, the chain is returned unchanged: a combo whose
// every member is benched must keep answering rather than fail for minutes.
func (h *ChatHandler) withoutBenchedModels(comboName string, models []string, grace time.Duration) []string {
	if grace <= 0 {
		return models
	}
	now := time.Now()
	h.timeoutBenchMu.Lock()
	defer h.timeoutBenchMu.Unlock()
	if len(h.timeoutBench) == 0 {
		return models
	}
	kept := make([]string, 0, len(models))
	for _, m := range models {
		if until, ok := h.timeoutBench[comboBenchKey(comboName, m)]; ok && until.After(now) {
			continue
		}
		kept = append(kept, m)
	}
	if len(kept) == 0 {
		return models
	}
	return kept
}

func comboTimeoutError() *upstreamError {
	return &upstreamError{StatusCode: http.StatusGatewayTimeout, Body: []byte(`{"error":{"message":"model response timeout","type":"timeout_error","code":504}}`)}
}
