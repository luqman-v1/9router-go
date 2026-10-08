package guardrails

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// chunk builds one OpenAI streaming delta event.
func chunk(content string) string {
	return `data: {"choices":[{"index":0,"delta":{"content":"` + content + `"},"finish_reason":null}]}` + "\n\n"
}

// TestScanResponseBodyMasksAndBlocks covers the buffered half of the outbound
// tap: a non-streamed answer can be rewritten in full before a byte is written.
func TestScanResponseBodyMasksAndBlocks(t *testing.T) {
	tests := []struct {
		name        string
		action      Action
		body        string
		hasEmail    bool
		wantBlocked bool
		wantMasked  bool
	}{
		{
			name:       "mask redacts an email in a chat completion",
			action:     ActionMask,
			body:       `{"choices":[{"message":{"content":"mail billing@acme-corp.com"}}]}`,
			hasEmail:   true,
			wantMasked: true,
		},
		{
			name:        "block rejects a chat completion",
			action:      ActionBlock,
			body:        `{"choices":[{"message":{"content":"mail billing@acme-corp.com"}}]}`,
			hasEmail:    true,
			wantBlocked: true,
		},
		{
			name:   "clean traffic is relayed byte for byte",
			action: ActionMask,
			body:   `{"choices":[{"message":{"content":"commit 9f2c4e7a1b8d3f6a"}}]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := NewEngine([]string{"pii"}, tt.action)
			out, decision := ScanResponseBody(engine, []byte(tt.body))

			if decision.Blocked() != tt.wantBlocked {
				t.Fatalf("blocked = %v, want %v", decision.Blocked(), tt.wantBlocked)
			}
			if decision.WasMutated() != tt.wantMasked {
				t.Fatalf("mutated = %v, want %v", decision.WasMutated(), tt.wantMasked)
			}
			// A blocked body is returned unwritten on purpose: the caller
			// discards it, so only a mask or an allow has content to check.
			if tt.hasEmail && !tt.wantBlocked && strings.Contains(string(out), "billing@acme-corp.com") {
				t.Fatalf("email survived a %s action: %s", tt.action, out)
			}
			if !tt.hasEmail && string(out) != tt.body {
				t.Fatalf("clean body was rewritten:\n got %s\nwant %s", out, tt.body)
			}
		})
	}
}

// TestNewOutboundReturnsNilWhenDisabled is what keeps the feature free: with no
// policy the caller gets no wrapper at all, so the response path is unchanged.
func TestNewOutboundReturnsNilWhenDisabled(t *testing.T) {
	rec := httptest.NewRecorder()
	for _, engine := range []*Engine{nil, NewEngine(nil, ActionMask), NewEngine([]string{"pii"}, ActionAllow)} {
		if got := NewOutboundFor(rec, engine, nil, FormatOpenAI); got != nil {
			t.Errorf("NewOutboundFor returned a wrapper for a disabled engine %+v", engine)
		}
	}
}

// TestOutboundMasksAcrossFrameBoundary is the case a per-frame scanner gets
// wrong, and the one this whole tap exists for. The address arrives in two
// deltas, so neither event alone contains a matchable value: judging the frames
// separately would send both halves to the client.
func TestOutboundMasksAcrossFrameBoundary(t *testing.T) {
	rec := httptest.NewRecorder()
	out := NewOutboundFor(rec, NewEngine([]string{"pii"}, ActionMask), nil, FormatOpenAI)

	out.Write([]byte(chunk("Send it to billing@acme-")))
	out.Write([]byte(chunk("corp.com now.")))
	out.Close()

	got := rec.Body.String()
	for _, leak := range []string{"billing@acme-", "corp.com", "billing@acme-corp.com"} {
		if strings.Contains(got, leak) {
			t.Errorf("split address survived the tap (%q present): %q", leak, got)
		}
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Errorf("address was not masked: %q", got)
	}
	if !strings.Contains(got, "Send it to") || !strings.Contains(got, "now.") {
		t.Errorf("redaction destroyed surrounding text: %q", got)
	}
}

// TestOutboundMasksAcrossManyFrames is the same case at a length where a naive
// "hold the last frame" rule would still leak: the value is spread over three
// deltas with ordinary text between them.
func TestOutboundMasksAcrossManyFrames(t *testing.T) {
	rec := httptest.NewRecorder()
	out := NewOutboundFor(rec, NewEngine([]string{"pii"}, ActionMask), nil, FormatOpenAI)

	for _, c := range []string{
		"the invoice went to ",
		"billing",
		"@acme-corp",
		".com last tuesday",
	} {
		out.Write([]byte(chunk(c)))
	}
	out.Close()

	got := rec.Body.String()
	if strings.Contains(got, "@acme-corp") || strings.Contains(got, "billing") {
		t.Errorf("split address survived the tap: %q", got)
	}
	if !strings.Contains(got, "last tuesday") {
		t.Errorf("redaction destroyed text after the value: %q", got)
	}
}

// TestOutboundStreamsBeforeClose proves the tap does not buffer the whole
// answer. A client waiting on tokens must see them arrive while the stream is
// still open; a tap that only decided at Close would break streaming while
// still passing the redaction tests above.
func TestOutboundStreamsBeforeClose(t *testing.T) {
	rec := httptest.NewRecorder()
	out := NewOutboundFor(rec, NewEngine([]string{"pii"}, ActionMask), nil, FormatOpenAI)

	for i := range 200 {
		out.Write([]byte(chunk("ordinary output line " + strings.Repeat("x", 20) + " ")))
		if i == 100 && rec.Body.Len() == 0 {
			t.Fatal("nothing reached the client mid-stream: the tap buffers the whole response")
		}
	}
	if rec.Body.Len() == 0 {
		t.Fatal("nothing reached the client before Close")
	}
}

// TestOutboundBlocksAndStops proves a block cuts the stream: nothing after it
// reaches the client.
func TestOutboundBlocksAndStops(t *testing.T) {
	rec := httptest.NewRecorder()
	out := NewOutboundFor(rec, NewEngine([]string{"pii"}, ActionBlock), nil, FormatOpenAI)

	out.Write([]byte(chunk("here it is: billing@acme-corp.com ")))
	out.Write([]byte(chunk("SECRET-UPSTREAM-PAYLOAD")))
	out.Close()

	if !out.Blocked() {
		t.Fatal("outbound tap did not latch the block")
	}
	got := rec.Body.String()
	if strings.Contains(got, "SECRET-UPSTREAM-PAYLOAD") {
		t.Errorf("content after the block reached the client: %q", got)
	}
	if strings.Contains(got, "billing@acme-corp.com") {
		t.Errorf("blocked address reached the client: %q", got)
	}
}

// TestOutboundBlockTerminatesCommittedStream is the half of block that only
// exists once the status line is gone: the client cannot be given a 4xx, so
// the turn has to be closed in-band with frames its own parser recognises.
// Without them a strict client waits forever for a completion that is never
// coming.
func TestOutboundBlockTerminatesCommittedStream(t *testing.T) {
	tests := []struct {
		name   string
		format StreamFormat
		want   []string
	}{
		{
			name:   "openai stream is closed with a terminal frame and DONE",
			format: FormatOpenAI,
			want:   []string{`"finish_reason":"stop"`, "data: [DONE]"},
		},
		{
			name:   "claude stream is closed with message_stop",
			format: FormatClaude,
			want:   []string{`"type":"message_stop"`},
		},
		{
			name:   "responses stream is closed with response.failed",
			format: FormatResponses,
			want:   []string{"response.failed"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			out := NewOutboundFor(rec, NewEngine([]string{"pii"}, ActionBlock), nil, tt.format)
			// The gateway writes SSE headers before the first token, so the
			// status line is spent long before a block can fire.
			out.WriteHeader(200)
			out.Write([]byte(chunk("here it is: billing@acme-corp.com ")))

			if !out.Blocked() {
				t.Fatal("block did not latch on a committed stream")
			}
			got := rec.Body.String()
			if strings.Contains(got, "billing@acme-corp.com") {
				t.Errorf("blocked address reached the client: %q", got)
			}
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("stream was not terminated with %q: %q", want, got)
				}
			}
			// Close runs on the way out of the handler and must not append a
			// second set of closing frames.
			out.Close()
			if got := rec.Body.String(); strings.Count(got, "data: [DONE]") > 1 {
				t.Errorf("closing frames were written twice: %q", got)
			}
		})
	}
}

// TestOutboundBlockBeforeStatusUsesRealStatus is the other half: nothing has
// been sent yet, so the client can be told why with an actual status code
// instead of an in-band frame.
func TestOutboundBlockBeforeStatusUsesRealStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	out := NewOutboundFor(rec, NewEngine([]string{"pii"}, ActionBlock), nil, FormatOpenAI)

	out.Write([]byte(chunk("billing@acme-corp.com")))

	if rec.Code != 502 {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "billing@acme-corp.com") {
		t.Errorf("blocked address reached the client: %q", rec.Body.String())
	}
}

// TestOutboundPassesCleanStreamThrough guards against the tap damaging ordinary
// traffic, which is the failure mode that would make the feature unusable.
func TestOutboundPassesCleanStreamThrough(t *testing.T) {
	rec := httptest.NewRecorder()
	out := NewOutboundFor(rec, NewEngine([]string{"pii", "injection"}, ActionMask), nil, FormatOpenAI)

	for _, c := range []string{
		"commit 9f2c4e7a1b8d3f6a",
		" on 127.0.0.1:8080",
		" with uuid 1b4e28ba-2fa1-11d2-883f-0016d3cca427",
	} {
		out.Write([]byte(chunk(c)))
	}
	out.Write([]byte("data: [DONE]\n\n"))
	out.Close()

	got := rec.Body.String()
	for _, want := range []string{
		"9f2c4e7a1b8d3f6a", "127.0.0.1:8080", "1b4e28ba-2fa1-11d2-883f-0016d3cca427",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("clean value %q was altered: %q", want, got)
		}
	}
}

// TestOutboundRelaysFramingIntact pins the property that makes the tap safe to
// leave in the path: with nothing found, every byte written reaches the client
// in the same order and the same count. A masking or holdback bug that
// duplicated, dropped, or reordered content would pass every redaction test
// above while breaking every real client.
func TestOutboundRelaysFramingIntact(t *testing.T) {
	rec := httptest.NewRecorder()
	out := NewOutboundFor(rec, NewEngine([]string{"pii", "injection"}, ActionMask), nil, FormatOpenAI)

	want := strings.Repeat(chunk("ordinary output line"), 100) +
		"data: {\"id\":\"chatcmpl-stream\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":13,\"total_tokens\":18}}\n\n" +
		"data: [DONE]\n\n"
	out.Write([]byte(want))
	out.Close()

	got := rec.Body.String()
	if got != want {
		t.Errorf("clean stream was altered:\n got %d bytes\nwant %d bytes", len(got), len(want))
	}
}
