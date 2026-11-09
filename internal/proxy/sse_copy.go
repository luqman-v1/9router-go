package proxy

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
)

// Frames appended when an upstream OpenAI SSE stream ends without the
// terminal frame a strict client requires.
const (
	// sseDoneFrame closes every stream.
	sseDoneFrame = "data: [DONE]\n\n"
	// sseStopTerminal completes a turn the upstream deliberately ended
	// ([DONE] received) but never closed with finish_reason. DONE is an
	// intentional end, so the turn completed: "stop".
	sseStopTerminal = "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"
	// sseTruncatedTerminal marks a stream that died mid-turn (EOF or read
	// error before [DONE]): the turn did not complete.
	sseTruncatedTerminal = "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"network_error\"}]}\n\n"
)

const (
	// maxSSEHoldback caps the unterminated line fragment held back between
	// reads. A real sentinel line (`data: [DONE]`) is a few dozen bytes, so
	// anything longer is mid-line content streaming through.
	maxSSEHoldback = 4096
	// sseTailSize is the rolling window used to catch a terminal token split
	// across two reads.
	sseTailSize = 64
)

// SSECopy reads from upstream in a raw loop and writes each chunk to the client.
// A simplified passthrough that does NOT parse SSE framing — use when translation is not needed.
// onChunk is called for each chunk before writing (for metrics/TTFT tracking).
// Optional patchers rewrite a `data:` payload needing repair before it reaches
// the client (e.g. the reasoning-alias lift); a patched payload is re-serialized
// in place of the original line. Returns the first upstream or write error so a
// truncated stream is not reported as a successful completion.
//
// The stream is kept OpenAI-compliant for strict clients (Oh My Pi):
//   - a bare `data: [DONE]` arriving without a prior terminal frame gains a
//     `finish_reason: "stop"` frame first, otherwise the client fails with
//     "stream closed before a finish_reason was received";
//   - EOF or a read error before [DONE] gains an in-band error frame.
//
// A provider-injected error event (OpenRouter relaying a dead downstream as
// `choices: []` plus `error: {...}` on HTTP 200) is swallowed instead of
// relayed and fails the turn: without this the stream reads as a served
// success, usage is logged as success, and the sick connection is never
// locked out of rotation. Detection is inline per line — no holdback, so the
// first content byte still reaches the client as fast as before.
func SSECopy(w http.ResponseWriter, upstream io.Reader, flusher http.Flusher, onChunk func([]byte), patchers ...func([]byte) []byte) error {
	// Allocate a local buffer instead of using the shared pool. The buffer is
	// alive for the entire read loop, so there is no safe point to return it
	// to the pool, and the pool would add a race window between ReleaseByteSlice
	// and the next iteration's write/flush completing.
	buf := make([]byte, 4096)
	c := &sseCopier{w: w, flusher: flusher, trailingNL: 2, toolFill: NewStreamToolCallIDFill(), patchers: patchers}
	for {
		n, err := upstream.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			if onChunk != nil {
				onChunk(chunk)
			}
			if bytes.Contains(chunk, []byte("data:")) {
				c.seenSSE = true
			}
			if ferr := c.feed(chunk); ferr != nil {
				return fmt.Errorf("write stream to client: %w", ferr)
			}
			if c.hasDone {
				return c.inbandFailure()
			}
		}
		if err != nil {
			if err == io.EOF {
				if ferr := c.finish(); ferr != nil {
					return ferr
				}
				return nil
			}
			// HTTP 200 is already on the wire, so the abort cannot be
			// reported as a status code. Close it in-band instead: an
			// error frame the client can parse, never a fabricated
			// successful finish_reason (upstream 93001213).
			c.abort(err)
			return fmt.Errorf("read upstream stream: %w", err)
		}
	}
}

// sseCopier forwards raw SSE bytes while tracking line boundaries. It exists
// because the previous blind passthrough had two defects: it honored [DONE]
// as a raw substring (so "[DONE]" inside content cut the stream early), and
// it forwarded a bare [DONE] with no terminal frame, which strict clients
// (Oh My Pi) reject with "stream closed before a finish_reason was received".
// Line tracking honors the sentinel only as a real event line, so a terminal
// frame can be injected ahead of it.
type sseCopier struct {
	w           http.ResponseWriter
	flusher     http.Flusher
	pending     []byte // unterminated trailing line fragment
	tail        [sseTailSize]byte
	tailLen     int
	trailingNL  int // trailing '\n' count of everything written, capped at 2
	hasTerminal bool
	hasDone     bool
	seenSSE     bool
	// toolFill mints ids for tool-call fragments that open a new index
	// without one; nil when the relay carries no chat chunks worth patching.
	toolFill *StreamToolCallIDFill
	// patchers rewrite a complete data line's payload before it is relayed
	// (e.g. the reasoning-alias lift); nil slices relay verbatim.
	patchers []func([]byte) []byte
	// seenInbandError tracks a provider-injected error event swallowed from
	// the relay. inbandCode/Msg hold the first one seen; the turn fails only
	// when no completion preceded it (inbandFailure).
	seenInbandError bool
	inbandCode      int
	inbandMsg       string
}

// feed observes a raw read, forwards complete lines, and holds back the
// trailing unterminated fragment for the next read.
func (c *sseCopier) feed(chunk []byte) error {
	c.observe(chunk)
	data := chunk
	if len(c.pending) > 0 {
		combined := make([]byte, 0, len(c.pending)+len(chunk))
		combined = append(combined, c.pending...)
		combined = append(combined, chunk...)
		c.pending = c.pending[:0]
		data = combined
	}
	if idx := bytes.LastIndexByte(data, '\n'); idx >= 0 {
		if err := c.writeEvents(data[:idx+1]); err != nil {
			return err
		}
		if c.hasDone {
			return nil
		}
		return c.hold(data[idx+1:])
	}
	return c.hold(data)
}

// hold retains an unterminated line fragment. Over-long fragments stream
// their head through: a real sentinel line is short, so an over-long
// fragment is mid-line content by construction.
func (c *sseCopier) hold(frag []byte) error {
	if len(frag) == 0 {
		return nil
	}
	if len(frag) > maxSSEHoldback {
		if err := c.writeRaw(frag[:len(frag)-maxSSEHoldback]); err != nil {
			return err
		}
		frag = frag[len(frag)-maxSSEHoldback:]
	}
	c.pending = append(c.pending, frag...)
	return nil
}

// writeEvents forwards lines that all end with '\n'. The first real [DONE]
// sentinel line ends the stream: when no terminal frame preceded it, a
// `finish_reason: "stop"` frame is injected first. Bytes after the sentinel
// are dropped — nothing valid follows [DONE].
//
// Provider-injected error lines are swallowed, not relayed, so a dead
// downstream never masquerades as content. Forwarding stays line-at-a-time:
// segStart only ever trails already-written bytes, so the first content byte
// is flushed exactly as fast as before (no holdback).
func (c *sseCopier) writeEvents(complete []byte) error {
	start, segStart := 0, 0
	for start < len(complete) {
		eol := bytes.IndexByte(complete[start:], '\n') + start
		line := complete[start : eol+1]
		if ssePayloadIsDone(line) {
			if err := c.writeRaw(complete[segStart:start]); err != nil {
				return err
			}
			return c.writeDoneLine(line)
		}
		if c.swallowInbandError(line) {
			if err := c.writeRaw(complete[segStart:start]); err != nil {
				return err
			}
			segStart = eol + 1
			start = eol + 1
			continue
		}
		if patched, ok := c.patchLine(line); ok {
			if err := c.writeRaw(complete[segStart:start]); err != nil {
				return err
			}
			if err := c.writeRaw(patched); err != nil {
				return err
			}
			segStart = eol + 1
		}
		start = eol + 1
	}
	return c.writeRaw(complete[segStart:])
}

// patchLine repairs one complete data line (including its terminator): the
// configured patchers rewrite the payload first (e.g. the reasoning-alias
// lift), then the tool-call id fill runs on the result. Returns the rebuilt
// line when anything changed.
func (c *sseCopier) patchLine(line []byte) ([]byte, bool) {
	trimmed := bytes.TrimLeft(line, " \t")
	if !bytes.HasPrefix(trimmed, []byte("data:")) {
		return nil, false
	}
	payload := bytes.TrimSpace(trimmed[len("data:"):])
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		return nil, false
	}
	changed := false
	for _, patch := range c.patchers {
		if next := patch(payload); !bytes.Equal(next, payload) {
			payload = next
			changed = true
		}
	}
	if c.toolFill != nil {
		if filled, ok := c.toolFill.Fill(payload); ok {
			payload = filled
			changed = true
		}
	}
	if !changed {
		return nil, false
	}
	term := []byte("\n")
	if bytes.HasSuffix(line, []byte("\r\n")) {
		term = []byte("\r\n")
	}
	out := make([]byte, 0, len("data: ")+len(payload)+len(term))
	out = append(out, []byte("data: ")...)
	out = append(out, payload...)
	out = append(out, term...)
	return out, true
}

// writeDoneLine closes the stream on the [DONE] sentinel. An error-only
// stream (provider error, no completion) closes with the gateway's own error
// frame instead of a fabricated successful terminal.
func (c *sseCopier) writeDoneLine(line []byte) error {
	if c.seenInbandError && !c.hasTerminal {
		return c.writeErrorClose()
	}
	if !c.hasTerminal {
		if err := c.ensureBlank(); err != nil {
			return err
		}
		if err := c.writeRaw([]byte(sseStopTerminal)); err != nil {
			return err
		}
		c.hasTerminal = true
	}
	if err := c.writeRaw(line); err != nil {
		return err
	}
	c.hasDone = true
	return nil
}

// swallowInbandError reports whether line is a provider-injected error event
// that must not reach the client, recording the first one seen. An error
// arriving after a terminal frame is left alone: the turn already completed,
// so the answer stands (mirrors the codex stream rule that an upstream error
// beside real output is not a failure).
func (c *sseCopier) swallowInbandError(line []byte) bool {
	if c.hasTerminal {
		return false
	}
	if isEventErrorLine(line) {
		return true
	}
	code, msg, ok := DetectInbandSSEError(line)
	if !ok {
		return false
	}
	if !c.seenInbandError {
		c.seenInbandError = true
		c.inbandCode, c.inbandMsg = code, msg
	}
	return true
}

// inbandFailure converts a recorded provider error into the turn failure the
// fallback layer locks on — but only when the stream carried no completion.
// A nil return keeps every healthy stream (and every completed turn) exactly
// as successful as before.
func (c *sseCopier) inbandFailure() error {
	if c.seenInbandError && !c.hasTerminal {
		return UpstreamFailure(c.inbandCode, c.inbandMsg)
	}
	return nil
}

// writeErrorClose ends an error-only turn with the gateway's own error frame
// (which already carries the closing [DONE]) instead of a fabricated
// successful finish_reason. OpenAI clients raise on a `data:` payload
// carrying `error` rather than keeping truncated text (upstream 93001213).
func (c *sseCopier) writeErrorClose() error {
	if err := c.ensureBlank(); err != nil {
		return err
	}
	if err := c.writeRaw(BuildStreamErrorBytes(c.inbandCode, c.inbandMsg, SSEFormatOpenAI)); err != nil {
		return err
	}
	// Build the failure before marking the stream closed: inbandFailure only
	// fires while no completion was recorded, and the flags below would
	// disarm it.
	failure := UpstreamFailure(c.inbandCode, c.inbandMsg)
	c.hasTerminal = true
	c.hasDone = true
	return failure
}

// finish flushes any held fragment and closes an unterminated SSE stream the
// same way the old EOF path did (PR #4079): blank-line separation, then
// `finish_reason: "network_error"` unless a terminal frame was seen, then
// [DONE]. A held `data: [DONE]` without trailing newline is a deliberate end
// and gains the "stop" terminal instead.
//
// An error-only stream (provider error, no completion) closes with the
// gateway's error frame and reports the failure, so the turn is locked out
// of rotation instead of being logged as a served success.
func (c *sseCopier) finish() error {
	if len(c.pending) > 0 {
		frag := c.pending
		c.pending = nil
		if ssePayloadIsDone(frag) {
			if c.seenInbandError && !c.hasTerminal {
				return c.writeErrorClose()
			}
			if !c.hasTerminal {
				_ = c.ensureBlank()
				_ = c.writeRaw([]byte(sseStopTerminal))
				c.hasTerminal = true
			}
			_ = c.writeRaw([]byte(sseDoneFrame))
			c.hasDone = true
			return nil
		}
		if !c.swallowInbandError(frag) {
			_ = c.writeRaw(frag)
		}
	}
	if c.seenSSE && !c.hasDone {
		if c.seenInbandError && !c.hasTerminal {
			return c.writeErrorClose()
		}
		_ = c.ensureBlank()
		if !c.hasTerminal {
			_ = c.writeRaw([]byte(sseTruncatedTerminal))
		}
		_ = c.writeRaw([]byte(sseDoneFrame))
		c.hasDone = true
	}
	return c.inbandFailure()
}

// abort closes a stream that died after HTTP 200 with an in-band error frame
// instead of a terminal finish_reason. A fabricated "stop" or "network_error"
// finish_reason is worse than nothing: OpenAI clients treat both as a normal
// completion and keep the truncated text, so a stall timeout and a dropped
// socket are indistinguishable from a finished answer. The error frame is
// what makes the difference visible (upstream decolua/9router 93001213).
func (c *sseCopier) abort(cause error) {
	if len(c.pending) > 0 {
		frag := c.pending
		c.pending = nil
		_ = c.writeRaw(frag)
	}
	if !c.seenSSE || c.hasDone {
		return
	}
	code, message := ClassifyStreamAbort(cause)
	_ = c.ensureBlank()
	// OpenAI shape only: SSECopy only ever serves OpenAI-format clients. The
	// Claude-named path terminates in the executor that already owns that
	// translation, and sends its own event: error frame.
	_ = c.writeRaw(BuildStreamErrorBytes(code, message, SSEFormatOpenAI))
	c.hasTerminal = true
	c.hasDone = true
}

// observe records terminal tokens from raw bytes, including tokens split
// across two reads via the rolling tail window.
func (c *sseCopier) observe(chunk []byte) {
	if !c.hasTerminal {
		if sseHasTerminalToken(chunk) {
			c.hasTerminal = true
		} else if c.tailLen > 0 {
			head := chunk
			if len(head) > 32 {
				head = head[:32]
			}
			win := make([]byte, 0, c.tailLen+len(head))
			win = append(win, c.tail[:c.tailLen]...)
			win = append(win, head...)
			if sseHasTerminalToken(win) {
				c.hasTerminal = true
			}
		}
	}
	if len(chunk) >= len(c.tail) {
		copy(c.tail[:], chunk[len(chunk)-len(c.tail):])
		c.tailLen = len(c.tail)
		return
	}
	if total := c.tailLen + len(chunk); total > len(c.tail) {
		drop := total - len(c.tail)
		if drop >= c.tailLen {
			copy(c.tail[:], chunk[drop-c.tailLen:])
		} else {
			copy(c.tail[:], c.tail[drop:c.tailLen])
			copy(c.tail[c.tailLen-drop:], chunk)
		}
		c.tailLen = len(c.tail)
		return
	}
	copy(c.tail[c.tailLen:], chunk)
	c.tailLen += len(chunk)
}

// writeRaw writes bytes and flushes, tracking trailing newlines for event
// separation.
func (c *sseCopier) writeRaw(p []byte) error {
	if len(p) == 0 {
		return nil
	}
	if _, err := c.w.Write(p); err != nil {
		return err
	}
	if c.flusher != nil {
		c.flusher.Flush()
	}
	c.noteWritten(p)
	return nil
}

// ensureBlank emits the newlines needed so the next frame starts a fresh SSE
// event instead of merging with the prior line.
func (c *sseCopier) ensureBlank() error {
	switch c.trailingNL {
	case 0:
		return c.writeRaw([]byte("\n\n"))
	case 1:
		return c.writeRaw([]byte("\n"))
	default:
		return nil
	}
}

func (c *sseCopier) noteWritten(p []byte) {
	trail := 0
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] != '\n' {
			break
		}
		trail++
		if trail == 2 {
			break
		}
	}
	if trail == len(p) {
		c.trailingNL += trail
		if c.trailingNL > 2 {
			c.trailingNL = 2
		}
		return
	}
	c.trailingNL = trail
}

// ssePayloadIsDone reports whether a single SSE line is the [DONE] sentinel:
// either a bare "[DONE]" line or a `data:` line whose payload is "[DONE]".
// A "[DONE]" substring inside JSON content is not a sentinel.
func ssePayloadIsDone(line []byte) bool {
	trimmed := bytes.TrimSpace(line)
	if bytes.Equal(trimmed, []byte("[DONE]")) {
		return true
	}
	if !bytes.HasPrefix(trimmed, []byte("data:")) {
		return false
	}
	payload := bytes.TrimSpace(trimmed[len("data:"):])
	return bytes.Equal(payload, []byte("[DONE]"))
}

// sseHasTerminalToken reports whether bytes carry a non-null terminal token:
// a real finish_reason, a Claude stop_reason, or message_stop. Explicit null
// values do not count.
func sseHasTerminalToken(p []byte) bool {
	if bytes.Contains(p, []byte(`"message_stop"`)) {
		return true
	}
	return sseHasNonNullValue(p, `"stop_reason"`) || sseHasNonNullValue(p, `"finish_reason"`)
}

// sseHasNonNullValue reports whether key (with quotes, e.g. `"finish_reason"`)
// appears with a value other than null. The needle is key + ":", not
// key + `":` — key already ends in the closing quote, so the latter searched
// for `"finish_reason":` (two quotes before the colon), which no JSON
// contains. hasTerminal was therefore always false, and every stream the
// upstream had already terminated correctly gained a second injected
// terminal before its [DONE].
//
// Content that quotes the field back needs no extra guard: inside a JSON
// string every quote is escaped, so it arrives as \"finish_reason\": and the
// unescaped needle cannot match it.
func sseHasNonNullValue(p []byte, key string) bool {
	needle := []byte(key + ":")
	for len(p) > 0 {
		idx := bytes.Index(p, needle)
		if idx < 0 {
			return false
		}
		rest := p[idx+len(needle):]
		for len(rest) > 0 && (rest[0] == ' ' || rest[0] == '\t') {
			rest = rest[1:]
		}
		if len(rest) == 0 {
			// Value split across a read boundary; the joined tail window
			// check in observe resolves it.
			return false
		}
		if rest[0] != 'n' {
			return true
		}
		p = rest
	}
	return false
}
