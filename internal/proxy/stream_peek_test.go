package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// readAll is the assertion every replay case needs: whatever Peek returns as
// the reader must reproduce the upstream byte-for-byte, because the forwarder
// relays those bytes to the client and a re-serialised frame would change line
// endings and spacing the client can see.
func readAll(t *testing.T, r io.Reader) string {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read replayed stream: %v", err)
	}
	return string(b)
}

// TestPeekFirstStreamEvent_ReplaysExactly pins the property the whole design
// rests on: after a successful peek the caller still sees the original bytes,
// including CRLF, the keep-alive comment and the blank-line framing.
func TestPeekFirstStreamEvent_ReplaysExactly(t *testing.T) {
	stream := ": keep-alive\r\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\r\n\r\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"bye\"}}]}\r\n\r\n" +
		"data: [DONE]\r\n\r\n"

	peeked, rest, err := PeekFirstStreamEvent(context.Background(), strings.NewReader(stream), 0)
	if err != nil {
		t.Fatalf("expected a valid first event, got error: %v", err)
	}
	if !strings.Contains(string(peeked), "hi") {
		t.Errorf("peeked bytes should hold the first event, got %q", peeked)
	}

	// `rest` already replays the peeked bytes ahead of the upstream, so the
	// whole stream has to come out of it alone.
	if got := readAll(t, rest); got != stream {
		t.Errorf("replay changed the stream\nwant %q\ngot  %q", stream, got)
	}
}

// TestPeekFirstStreamEvent_SkipsRoleOnlyPreamble is the case that makes a
// one-event peek useless on its own: a real OpenAI stream opens with a
// role-only chunk that carries no answer, so a peek that stopped there would
// call every healthy stream empty.
func TestPeekFirstStreamEvent_SkipsRoleOnlyPreamble(t *testing.T) {
	stream := "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n" +
		"data: [DONE]\n\n"

	peeked, rest, err := PeekFirstStreamEvent(context.Background(), strings.NewReader(stream), 0)
	if err != nil {
		t.Fatalf("a role-only preamble must not be read as an empty turn: %v", err)
	}
	if !strings.Contains(string(peeked), "hello") {
		t.Errorf("peek should continue past the preamble to the first carrier, got %q", peeked)
	}
	if got := readAll(t, rest); got != stream {
		t.Errorf("replay changed the stream\nwant %q\ngot  %q", stream, got)
	}
}

// TestPeekFirstStreamEvent_RefusesEmptyTurn covers the four shapes of a 200
// that carries no answer. Each must come back as a 502 the fallback layer
// retries on, and each must be refused before the caller commits headers.
func TestPeekFirstStreamEvent_RefusesEmptyTurn(t *testing.T) {
	cases := []struct {
		name   string
		stream string
	}{
		{name: "immediate EOF", stream: ""},
		{name: "keep-alive comments only then EOF", stream: ": keep-alive\n\n: keep-alive\n\n"},
		{name: "DONE with no token before it", stream: "data: [DONE]\n\n"},
		{name: "blank frames only", stream: "\n\n\n\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := PeekFirstStreamEvent(context.Background(), strings.NewReader(tc.stream), 0)
			if err == nil {
				t.Fatal("expected the empty turn to be refused")
			}
			var ue *UpstreamError
			if !errors.As(err, &ue) {
				t.Fatalf("expected *UpstreamError so fallback can act on it, got %T: %v", err, err)
			}
			if ue.StatusCode != http.StatusBadGateway {
				t.Errorf("expected 502, got %d", ue.StatusCode)
			}
		})
	}
}

// TestPeekFirstStreamEvent_RefusesErrorEvent pins that a provider-injected
// error frame is caught while it is still reversible. After the headers are
// written the same frame can only be relayed to the client.
func TestPeekFirstStreamEvent_RefusesErrorEvent(t *testing.T) {
	stream := "event: error\n" +
		"data: {\"error\":{\"code\":429,\"message\":\"model overloaded\"}}\n\n"

	_, _, err := PeekFirstStreamEvent(context.Background(), strings.NewReader(stream), 0)
	if err == nil {
		t.Fatal("expected an error event to be refused")
	}
	var ue *UpstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UpstreamError, got %T: %v", err, err)
	}
	if ue.StatusCode != http.StatusTooManyRequests {
		t.Errorf("expected the provider's own 429 to survive, got %d", ue.StatusCode)
	}
}

// TestPeekFirstStreamEvent_HonoursCancellation keeps a client that hung up
// from buying the gateway a blocked read: the caller reports 499 and must be
// able to tell cancellation apart from an unusable upstream.
func TestPeekFirstStreamEvent_HonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := PeekFirstStreamEvent(ctx, strings.NewReader("data: {\"choices\":[]}\n\n"), 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %T: %v", err, err)
	}
}

// TestPeekFirstStreamEvent_CommitsAtTheBudgetBound pins the escape hatch: a
// stream that never carries an answer but keeps sending is a slow model, not a
// broken one, so once the peek budget is spent the caller commits instead of
// waiting forever or refusing a healthy turn.
func TestPeekFirstStreamEvent_CommitsAtTheBudgetBound(t *testing.T) {
	var sb strings.Builder
	for range 64 {
		sb.WriteString("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n")
	}

	peeked, rest, err := PeekFirstStreamEvent(context.Background(), strings.NewReader(sb.String()), 256)
	if err != nil {
		t.Fatalf("expected the budget bound to commit, got error: %v", err)
	}
	if len(peeked) == 0 {
		t.Fatal("expected the peeked bytes to be replayed, got none")
	}
	if len(peeked) > 256 {
		t.Errorf("peek exceeded its budget: %d bytes for a 256-byte cap", len(peeked))
	}
	if got := len(readAll(t, rest)); got != sb.Len() {
		t.Errorf("replay lost bytes: want %d, got %d", sb.Len(), got)
	}
}

// TestPeekFirstStreamEvent_CapsAnUnboundedRequest stops a caller from asking
// for an unbounded buffer: the spec's ceiling is what keeps a hostile or
// broken upstream from pinning memory.
//
// The stream must be larger than MaxPeekBytes for the ceiling to be observable.
// An earlier draft used eight small events and only logged its outcome, so
// removing the clamp entirely left it green: with a short stream both a 1 GiB
// and a 10 MiB budget read to EOF, and the test asserted nothing about either.
func TestPeekFirstStreamEvent_CapsAnUnboundedRequest(t *testing.T) {
	event := []byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n")
	stream := bytes.Repeat(event, MaxPeekBytes/len(event)+2)
	if len(stream) <= MaxPeekBytes {
		t.Fatalf("fixture must exceed the ceiling: %d <= %d", len(stream), MaxPeekBytes)
	}

	peeked, rest, err := PeekFirstStreamEvent(context.Background(), bytes.NewReader(stream), 1<<30)
	if err != nil {
		t.Fatalf("a clamped peek must commit at the ceiling, not refuse: %v", err)
	}
	if len(peeked) > MaxPeekBytes {
		t.Errorf("peek held %d bytes for a 1 GiB request; the ceiling is %d", len(peeked), MaxPeekBytes)
	}
	// The clamp must not cost the client a byte: rest replays the peeked bytes
	// ahead of the untouched remainder, so it alone must rebuild the stream.
	n, copyErr := io.Copy(io.Discard, rest)
	if copyErr != nil {
		t.Fatalf("replay read: %v", copyErr)
	}
	if n != int64(len(stream)) {
		t.Errorf("replay lost bytes: replayed %d, stream is %d (peeked %d)", n, len(stream), len(peeked))
	}
}

// TestPeekFirstStreamEvent_PropagatesReadError keeps an upstream that dies
// mid-peek distinguishable from one that simply said nothing.
func TestPeekFirstStreamEvent_PropagatesReadError(t *testing.T) {
	boom := errors.New("connection reset by peer")
	_, _, err := PeekFirstStreamEvent(context.Background(), &failingReader{err: boom}, 0)
	if !errors.Is(err, boom) {
		t.Fatalf("expected the read error to survive, got %T: %v", err, err)
	}
}

type failingReader struct{ err error }

func (f *failingReader) Read([]byte) (int, error) { return 0, f.err }

var _ = bytes.MinRead
