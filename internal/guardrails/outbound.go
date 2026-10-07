package guardrails

import (
	"bytes"
	json "encoding/json/v2"
	"net/http"
	"strings"
)

// scanWindowChars is how much model text an outbound scanner keeps back before
// deciding on it.
//
// A value is routinely split across two deltas — "billing@acme-" then
// "corp.com" — so judging each frame alone would miss every address that landed
// on a boundary. The window is the largest plausible single value: anything
// longer is not one entity, and keeping more would mean re-scanning an
// ever-growing buffer on every chunk.
const scanWindowChars = 512

// BlockedMessage is what the client is told when a response is cut by a
// policy. It names no detector and no matched value: a message that said what
// was found would confirm to a prober exactly which patterns this install runs.
const BlockedMessage = "Response blocked by a configured guardrail policy"

// sseDoneFrame closes every event stream the gateway writes. A stream cut by a
// policy must end the way any other completed stream does, or strict clients
// (Oh My Pi, the OpenAI SDKs) wait for a terminal frame that never comes.
const sseDoneFrame = "data: [DONE]\n\n"

// sseStopTerminal completes a turn that ended deliberately. A blocked turn did
// end deliberately — the policy ended it — so "stop" is honest here in a way
// the network_error frame used for an upstream death is not.
const sseStopTerminal = `data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n"

// claudeBlockFrame is the Anthropic end-of-turn event a /v1/messages client
// waits for.
const claudeBlockFrame = `data: {"type":"message_stop"}` + "\n\n"

// responsesBlockFrames ends a /v1/responses stream. response.failed is the
// terminal event a Responses client raises on; without it the client waits for
// a completion that will never come.
const responsesBlockFrames = "event: response.failed\n" +
	`data: {"type":"response.failed","response":{"id":"guardrail","status":"failed","error":{"code":"guardrail_blocked","message":"` + BlockedMessage + `"}}}` + "\n\n"

// StreamFormat names the wire format a stream is written in, which decides the
// frames a block has to close it with.
type StreamFormat uint8

const (
	// FormatOpenAI is Chat Completions: the default for /v1/chat/completions
	// and every other relayed stream.
	FormatOpenAI StreamFormat = iota
	// FormatClaude is the Anthropic Messages event shape.
	FormatClaude
	// FormatResponses is the /v1/responses event shape, which the gateway
	// produces by translating a Chat Completions stream.
	FormatResponses
)

// pendingFrame is one complete event held back while its text may still grow.
//
// A frame with no judged text — a comment, a "[DONE]" sentinel, a usage-only
// chunk — is queued too, with empty text. It is never masked, but it still has
// to reach the client in the order it arrived, so it waits its turn in the same
// queue rather than being written straight through: writing it early would put
// it ahead of every model frame still sitting in the window.
type pendingFrame struct {
	// prefix is the event's non-data lines, kept so a masked payload can be
	// written back into the same envelope.
	prefix []byte
	// payload is the decoded data field; nil for a frame that carried none.
	payload map[string]any
	// raw is the event exactly as it arrived. A frame nothing was masked in is
	// relayed from here rather than re-encoded: re-encoding a decoded map
	// reorders its keys, so every clean frame of a masked stream would reach
	// the client reordered — still valid JSON, but no longer byte-identical to
	// what the provider sent, and not something a filter should cause.
	raw []byte
	// text is this frame's contribution to the window.
	text string
}

// Outbound applies an engine to a streamed response on its way to the client.
//
// A buffered rewrite is impossible once bytes are on the wire, and for an event
// stream it does not even help on the buffer: the text a policy matches spans
// the decoded string values of several frames, so concatenating the raw frames
// never yields a matchable value. So each event is decoded, its model text is
// appended to a sliding window, and only frames whose text has settled are
// re-encoded and released. The rest stay held until enough has arrived to
// decide them — which is what makes a value split across frames catchable.
type Outbound struct {
	http.ResponseWriter
	engine *Engine
	audit  Audit
	// format names the wire format a blocked stream has to be terminated in.
	format StreamFormat

	// heldFrames are the events withheld from the client because their text
	// might still turn out to be the start of a match. They are masked and
	// written as one window, so a value spanning several of them is redacted
	// once, as a single value.
	heldFrames []pendingFrame
	// frame accumulates raw bytes until a complete event is available.
	frame bytes.Buffer
	// blocked latches once a block action fires: the stream is cut and no
	// further upstream bytes reach the client.
	blocked bool
	// terminated records that the closing frames have been written.
	terminated bool
	// committed records that the status line has already gone out, so a block
	// can no longer become a 4xx and must instead end the stream.
	committed bool
}

// NewOutboundFor wraps w. It returns nil when the engine does nothing, so a
// disabled policy costs the caller nothing.
func NewOutboundFor(w http.ResponseWriter, e *Engine, audit Audit, format StreamFormat) *Outbound {
	if !e.Enabled() {
		return nil
	}
	return &Outbound{ResponseWriter: w, engine: e, audit: audit, format: format}
}

// Blocked reports whether the response was cut short.
func (o *Outbound) Blocked() bool { return o != nil && o.blocked }

// WriteHeader records that the status line is spent.
func (o *Outbound) WriteHeader(code int) {
	o.committed = true
	o.ResponseWriter.WriteHeader(code)
}

// Write filters complete SSE events and withholds a partial one.
func (o *Outbound) Write(p []byte) (int, error) {
	if o.blocked {
		// Swallow: the client must not receive upstream content after a block.
		return len(p), nil
	}
	o.frame.Write(p)
	for !o.blocked {
		event, rest, ok := nextSSEEvent(o.frame.Bytes())
		if !ok {
			break
		}
		o.frame.Reset()
		o.frame.Write(rest)
		if err := o.handleEvent(event); err != nil {
			return len(p), err
		}
	}
	return len(p), nil
}

// Flush forwards to the underlying writer so SSE streaming is not broken by the
// wrapper. Held text is deliberately not released: emitting it would defeat
// the window. Close is what discharges it.
func (o *Outbound) Flush() {
	if o.blocked {
		return
	}
	if f, ok := o.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Close releases the held text and terminates a blocked stream, so the last
// tokens are not lost and a cut turn does not hang the client.
func (o *Outbound) Close() {
	if !o.blocked {
		// A trailing event with no blank line still belongs to the client; the
		// stream has ended, so nothing can extend the text any more.
		if pending := o.frame.Bytes(); len(bytes.TrimSpace(pending)) > 0 {
			o.frame.Reset()
			if event, _, _ := nextSSEEvent(append(pending, '\n', '\n')); event != nil {
				if err := o.handleEvent(event); err != nil {
					return
				}
			}
		}
		if err := o.flushHeld(); err != nil {
			return
		}
	}
	o.writeTerminalFrames()
	if f, ok := o.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// handleEvent takes one complete event: relays it untouched, holds it into the
// window, or releases settled frames.
func (o *Outbound) handleEvent(event []byte) error {
	prefix, _ := splitEvent(event)
	frame := pendingFrame{prefix: prefix, raw: bytes.Clone(event)}

	// A comment, an event name, a "[DONE]" sentinel, a usage-only chunk, or a
	// provider error frame: none carry model text a policy judges, so nothing
	// is masked. It still joins the queue, because a frame written straight
	// through would overtake the model frames still sitting in the window.
	payload, isData := ssePayload(event)
	if decoded, ok := decodeOrNil(payload); isData && ok {
		frame.payload = decoded
		frame.text = joinText(payloadText(decoded))
	}

	// The block is judged over the whole window, not just the new frame: the
	// value that matched may have started several frames ago.
	if decision := o.engine.Scan(windowText(o.heldFrames) + frame.text); decision.Blocked() {
		return o.block(decision)
	}

	o.heldFrames = append(o.heldFrames, frame)
	return o.release(false)
}

// release writes the frames whose text can no longer change and keeps the rest
// held. final is set on stream end, when nothing can arrive to extend the
// window.
func (o *Outbound) release(final bool) error {
	if len(o.heldFrames) == 0 {
		return nil
	}
	// At end of stream nothing can extend the window, so all of it settles.
	keep := 0
	if !final {
		keep = tailFrames(o.heldFrames)
		if keep == len(o.heldFrames) {
			// Nothing has settled yet, so there is nothing to release.
			return nil
		}
	}
	if err := o.writeFrames(0, len(o.heldFrames)-keep); err != nil {
		return err
	}
	o.heldFrames = append([]pendingFrame(nil), o.heldFrames[len(o.heldFrames)-keep:]...)
	return nil
}

// writeFrames masks and writes frames[from:to] of the window, treating them as
// one piece of text so a value that spans them is redacted once.
//
// When nothing in the window matched, every frame is relayed from its original
// bytes. Re-encoding an untouched payload would be indistinguishable to a
// client and visibly different to anyone reading a stream: Go maps do not keep
// field order, so every clean frame of a filtered stream would come back with
// its keys reshuffled.
func (o *Outbound) writeFrames(from, to int) error {
	if to <= from {
		return nil
	}
	window := o.heldFrames[from:to]
	decision := o.engine.Scan(windowText(window))
	if !decision.WasMutated() {
		return o.writeRawFrames(window)
	}
	if o.audit != nil {
		o.audit(*decision, Target{})
	}
	original := windowText(window)
	spans := make([]int, len(window))
	for i, f := range window {
		spans[i] = len(f.text)
	}
	parts := remap(original, decision.Mutated, spans)
	for i, f := range window {
		// A frame the redaction did not touch is relayed untouched; only the
		// one carrying the replacement is re-encoded.
		frame := f.raw
		if parts[i] != f.text {
			frame = rebuildEvent(f.prefix, encodePayload(f.payload, parts[i]))
		}
		if err := o.writeFrame(frame); err != nil {
			return err
		}
	}
	return nil
}

// writeRawFrames relays frames exactly as they arrived.
func (o *Outbound) writeRawFrames(window []pendingFrame) error {
	for _, f := range window {
		if err := o.writeFrame(f.raw); err != nil {
			return err
		}
	}
	return nil
}

// encodePayload re-encodes a decoded payload carrying one judged string.
func encodePayload(payload map[string]any, text string) []byte {
	setPayloadText(payload, []string{text})
	encoded, err := json.Marshal(payload)
	if err != nil {
		// A payload that decoded cleanly always re-encodes, so this would be a
		// bug. The client is better served by an empty frame than by a panic in
		// the middle of a stream.
		return []byte("null")
	}
	return encoded
}

// windowText is the joined model text of a held window.
func windowText(frames []pendingFrame) string {
	var b strings.Builder
	for _, f := range frames {
		b.WriteString(f.text)
	}
	return b.String()
}

// flushHeld releases the window at end of stream, where nothing can extend it.
func (o *Outbound) flushHeld() error {
	return o.release(true)
}

// tailFrames is how many of the held frames cover the last scanWindowChars of
// text. Those frames may still be part of a value that continues, so they stay
// held; everything before them has settled.
func tailFrames(held []pendingFrame) int {
	keep := 0
	budget := 0
	for i := len(held) - 1; i >= 0; i-- {
		budget += len(held[i].text)
		keep++
		if budget >= scanWindowChars {
			break
		}
	}
	return keep
}

// block cuts the stream. Whatever is still held belongs to the same verdict and
// must not be released afterwards.
func (o *Outbound) block(decision *Decision) error {
	o.blocked = true
	o.heldFrames = nil
	o.frame.Reset()
	if o.audit != nil {
		o.audit(*decision, Target{})
	}
	// Before the status line is spent the client can still be told why in a
	// real status; after it, the turn is ended inside the stream instead.
	if !o.committed {
		o.committed = true
		o.terminated = true
		http.Error(o.ResponseWriter, BlockedMessage+".", http.StatusBadGateway)
		return nil
	}
	// The status is spent, so the error can only be reported in-band. Frames go
	// out now rather than waiting for Close, so a client reading an unclosed
	// stream learns the turn ended right away.
	return o.writeTerminalFrames()
}

// writeTerminalFrames closes a blocked stream in the wire format the client is
// reading. An unterminated stream reads as a hung request to strict clients
// (Oh My Pi, the OpenAI SDKs), so a policy that blocks has to end the turn as
// deliberately as the gateway ends every other one.
func (o *Outbound) writeTerminalFrames() error {
	if o.terminated || !o.blocked {
		return nil
	}
	o.terminated = true
	switch o.format {
	case FormatClaude:
		return o.writeFrame([]byte(claudeBlockFrame))
	case FormatResponses:
		return o.writeFrame([]byte(responsesBlockFrames))
	default:
		if err := o.writeFrame([]byte(sseStopTerminal)); err != nil {
			return err
		}
		return o.writeFrame([]byte(sseDoneFrame))
	}
}

// writeFrame writes one event to the client verbatim.
func (o *Outbound) writeFrame(event []byte) error {
	if len(event) == 0 {
		return nil
	}
	if _, err := o.ResponseWriter.Write(event); err != nil {
		return err
	}
	if f, ok := o.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}

// ScanResponseBody applies the engine to a non-streamed response body. It is
// the JSON half of the outbound tap: a streamed response goes through Outbound,
// a buffered one can be rewritten in full before a single byte is written.
func ScanResponseBody(engine *Engine, body []byte) ([]byte, *Decision) {
	if !engine.Enabled() || len(body) == 0 {
		return body, &Decision{Action: ActionAllow}
	}
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		// Not JSON — scan it as plain text so a bare-text body is still covered.
		decision := engine.Scan(string(body))
		if decision.Blocked() || !decision.WasMutated() {
			return body, decision
		}
		return []byte(decision.Mutated), decision
	}
	scanned, decision := engine.ScanJSON(payload)
	if !decision.WasMutated() {
		return body, decision
	}
	out, err := json.Marshal(scanned)
	if err != nil {
		// The scan mutated the payload into something that will not re-marshal.
		// A payload that decoded cleanly always re-marshals, so this is a
		// defensive path; the original body is the only thing left to write.
		return body, decision
	}
	return out, decision
}
