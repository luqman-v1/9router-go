package executor

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strings"
	"time"
)

// geminiWebLineReader yields the stream's lines while enforcing the two
// timeouts a streamed site turn needs and a single overall deadline cannot give:
// the wait before the first line, and the gap between lines once the answer is
// under way.
//
// A site turn can spend a long time thinking before its first frame, which is
// why the total duration is not bounded — a slow answer is not a failure. A
// stream that stops producing frames mid-answer is, and both cases arrive as a
// silent connection, so only these two clocks tell them apart.
type geminiWebLineReader struct {
	ctx      context.Context
	lines    chan string
	failures chan error
	// buffered holds lines handed over in the same read that produced them, so a
	// burst does not get re-queued through the channel.
	buffered []string
	started  bool
}

func newGeminiWebLineReader(ctx context.Context, body io.Reader) *geminiWebLineReader {
	reader := &geminiWebLineReader{
		ctx:      ctx,
		lines:    make(chan string, 64),
		failures: make(chan error, 1),
	}
	go reader.read(body)
	return reader
}

// read drains the body into the channel. The body is closed by the caller, which
// is what unblocks this goroutine when the consumer stopped early — so it never
// outlives the turn.
func (r *geminiWebLineReader) read(body io.Reader) {
	scanner := bufio.NewScanner(body)
	// One frame repeats the whole answer so far, so the longest line grows with
	// the answer. The buffer is sized for the largest observed turn rather than
	// left at the scanner's 64 KB default, which a long answer exceeds.
	scanner.Buffer(make([]byte, 64*1024), 8<<20)
	defer close(r.lines)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		select {
		case r.lines <- line:
		case <-r.ctx.Done():
			return
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, context.Canceled) {
		select {
		case r.failures <- err:
		default:
		}
	}
}

// next returns the next line, or io.EOF when the stream ended cleanly.
func (r *geminiWebLineReader) next() (string, error) {
	if len(r.buffered) > 0 {
		line := r.buffered[0]
		r.buffered = r.buffered[1:]
		return line, nil
	}
	timeout := geminiWebIdleTimeout
	if !r.started {
		timeout = geminiWebFirstByteTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case line, ok := <-r.lines:
		if !ok {
			if err := r.failure(); err != nil {
				return "", err
			}
			return "", io.EOF
		}
		r.started = true
		return strings.TrimSpace(line), nil
	case err := <-r.failures:
		return "", err
	case <-timer.C:
		if !r.started {
			return "", errors.New("gemini-web: gemini.google.com sent no stream bytes within 60s")
		}
		return "", errors.New("gemini-web: the answer stream stalled — no frames for 90s")
	case <-r.ctx.Done():
		return "", r.ctx.Err()
	}
}

// failure drains a reader error that was recorded when the channel closed.
func (r *geminiWebLineReader) failure() error {
	select {
	case err := <-r.failures:
		return err
	default:
		return nil
	}
}
