package executor

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"9router/proxy/internal/translator"
)

// stalledUpstream streams the answer and its finish_reason — the two chunks a
// client needs — and then holds the connection open forever, which is what a
// broken chat provider does when the usage trailer never comes. It implements
// io.Closer because the scan's idle bound releases a stalled read by closing
// the reader.
type stalledUpstream struct {
	frames chan []byte
	once   sync.Once
	done   chan struct{}
}

func newStalledUpstream(frames ...string) *stalledUpstream {
	u := &stalledUpstream{frames: make(chan []byte, len(frames)), done: make(chan struct{})}
	for _, f := range frames {
		u.frames <- []byte(f)
	}
	return u
}

func (u *stalledUpstream) Read(p []byte) (int, error) {
	select {
	case frame, ok := <-u.frames:
		if !ok {
			return 0, io.EOF
		}
		return copy(p, frame), nil
	case <-u.done:
		return 0, io.EOF
	}
}

// Close releases the stalled read. Idempotent: the scan closes the reader when
// the idle bound elapses and the test closes it again on the way out.
func (u *stalledUpstream) Close() error {
	u.once.Do(func() { close(u.done) })
	return nil
}

const (
	contentFrame = `data: {"id":"c1","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"}}]}` + "\n\n"
	finishFrame  = `data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n"
	// usageFrame is the trailer a healthy upstream sends after finish_reason.
	usageFrame = `data: {"id":"c1","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":4}}` + "\n\n"
)

// countCompleted counts the terminal events that reached the client. Two is a
// contract violation: a Responses client that receives response.completed
// twice has to decide which one won.
func countCompleted(body string) int {
	return strings.Count(body, `"type":"response.completed"`)
}

// withFlushTimeout shrinks the watchdog window so the test drives the bound
// instead of sleeping through the production one.
func withFlushTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := PendingCompletionFlushTimeout
	PendingCompletionFlushTimeout = d
	t.Cleanup(func() { PendingCompletionFlushTimeout = prev })
}

// TestStreamChatToResponses_WatchdogFlushesStalledCompletion is the stall the
// deferral created: finish_reason arrives, nothing ever completes it, and the
// client is left holding a turn that already ended. The watchdog has to end
// the wait, emit exactly one response.completed, and release the stalled read.
func TestStreamChatToResponses_WatchdogFlushesStalledCompletion(t *testing.T) {
	withFlushTimeout(t, 50*time.Millisecond)

	upstream := newStalledUpstream(contentFrame, finishFrame)
	defer upstream.Close()

	rec := httptest.NewRecorder()
	done := make(chan error, 1)
	go func() {
		done <- StreamChatToResponses(translator.WithRequestedModel(context.Background(), "gpt-x"),
			rec, upstream, time.Time{}, nil, nil)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the stalled upstream was never released; the client would wait forever")
	}

	body := rec.Body.String()
	if got := countCompleted(body); got != 1 {
		t.Errorf("response.completed count = %d, want exactly 1:\n%s", got, body)
	}
	if !strings.Contains(body, `"status":"completed"`) {
		t.Errorf("the flushed event must report the completed status:\n%s", body)
	}
	if strings.Contains(body, `"usage"`) {
		t.Errorf("no usage was ever reported, so the flushed event must not invent one:\n%s", body)
	}
	if !strings.Contains(body, `"text":"Hello"`) {
		t.Errorf("the flushed event must carry the answer that already streamed:\n%s", body)
	}
}

// TestStreamChatToResponses_WatchdogDoesNotDoubleComplete covers the usage
// trailer arriving inside the window: the real completion wins and the timer
// must not have fired and added a second one behind it.
func TestStreamChatToResponses_WatchdogDoesNotDoubleComplete(t *testing.T) {
	withFlushTimeout(t, 50*time.Millisecond)

	upstream := newStalledUpstream(contentFrame, finishFrame, usageFrame)
	defer upstream.Close()

	rec := httptest.NewRecorder()
	if err := StreamChatToResponses(translator.WithRequestedModel(context.Background(), "gpt-x"),
		rec, upstream, time.Time{}, nil, nil); err != nil {
		t.Fatalf("StreamChatToResponses: %v", err)
	}

	// Well past the watchdog window: anything it might still emit shows up here.
	time.Sleep(300 * time.Millisecond)

	body := rec.Body.String()
	if got := countCompleted(body); got != 1 {
		t.Errorf("response.completed count = %d, want exactly 1:\n%s", got, body)
	}
	if !strings.Contains(body, `"input_tokens":12`) || !strings.Contains(body, `"output_tokens":4`) {
		t.Errorf("the usage trailer must reach the terminal event:\n%s", body)
	}
}

// TestStreamChatToResponses_WatchdogDoesNotCutOffAQuietUpstream proves the
// bound only applies once the terminal event is owed. A provider that pauses
// before its first token is slow, not broken, and cutting it off would empty
// the answer for every slow upstream.
func TestStreamChatToResponses_WatchdogDoesNotCutOffAQuietUpstream(t *testing.T) {
	withFlushTimeout(t, 30*time.Millisecond)

	upstream := newStalledUpstream()
	defer upstream.Close()

	rec := httptest.NewRecorder()
	go func() {
		// The upstream stays silent well past the window, then finally answers.
		time.Sleep(200 * time.Millisecond)
		upstream.frames <- []byte(contentFrame)
		upstream.frames <- []byte(finishFrame + usageFrame)
		close(upstream.frames)
	}()

	if err := StreamChatToResponses(translator.WithRequestedModel(context.Background(), "gpt-x"),
		rec, upstream, time.Time{}, nil, nil); err != nil {
		t.Fatalf("StreamChatToResponses: %v", err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, `"text":"Hello"`) {
		t.Errorf("a slow upstream must not be cut off before its answer:\n%s", body)
	}
	if got := countCompleted(body); got != 1 {
		t.Errorf("response.completed count = %d, want exactly 1:\n%s", got, body)
	}
}

// frameCollector collects what the bridge writes. It locks because the
// watchdog writes from its own goroutine, which is the whole point of it.
type frameCollector struct {
	mu   sync.Mutex
	body strings.Builder
}

func newFrameCollector() *frameCollector { return &frameCollector{} }

func (c *frameCollector) write(frame []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.body.Write(frame)
	return nil
}

func (c *frameCollector) text() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.body.String()
}

func (c *frameCollector) countCompleted() int { return countCompleted(c.text()) }

// TestResponsesBridgeWatchdogCancelsWhenTheProducerCloses covers the two
// emitters racing: the watchdog and the producer must not both complete the
// stream, and whichever loses must leave nothing armed behind it.
func TestResponsesBridgeWatchdogCancelsWhenTheProducerCloses(t *testing.T) {
	withFlushTimeout(t, 40*time.Millisecond)

	frames := newFrameCollector()
	bridge := NewResponsesBridge("gpt-x", nil, frames.write)

	bridge.Feed([]byte(`{"id":"c1","choices":[{"index":0,"delta":{"content":"hi"}}]}`))
	bridge.Feed([]byte(`{"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`))
	if !bridge.CompletionPending() {
		t.Fatal("a deferred completion must be reported as pending")
	}

	// The producer reaches the end of the stream on its own. Its Close must
	// win, and nothing the watchdog was holding may survive it.
	bridge.Close()
	time.Sleep(200 * time.Millisecond)

	if got := frames.countCompleted(); got != 1 {
		t.Errorf("response.completed count = %d after the stream ended, want exactly 1:\n%s", got, frames.text())
	}
}