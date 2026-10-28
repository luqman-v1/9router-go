package proxy

import (
	"strings"
	"testing"
)

// TestScanStreamEvents_PreservesEventAndEarlyStop covers the two things the
// chunk-level scanner threw away: the `event:` and `id:` labels that a
// provider-injected error frame relies on, and the ability to stop reading
// after the first event the caller cares about.
//
// The early stop is what makes a peek cheap. Without it the only way to look at
// the head of a stream was to buffer the whole thing, which is exactly what a
// long turn must not do.
func TestScanStreamEvents_PreservesEventAndEarlyStop(t *testing.T) {
	stream := "event: error\n" +
		"id: evt-1\n" +
		"data: {\"message\":\"model overloaded\"}\n" +
		"\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n" +
		"\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"bye\"}}]}\n" +
		"\n"

	var got []SSEEvent
	err := ScanStreamEvents(strings.NewReader(stream), func(ev SSEEvent) bool {
		got = append(got, ev)
		return false // stop after the first
	})
	if err != nil {
		t.Fatalf("ScanStreamEvents returned an error: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("expected the callback to stop the scan after 1 event, got %d: %q", len(got), got)
	}
	if got[0].Event != "error" {
		t.Errorf("expected event label %q, got %q", "error", got[0].Event)
	}
	if got[0].ID != "evt-1" {
		t.Errorf("expected event id %q, got %q", "evt-1", got[0].ID)
	}
	if want := `{"message":"model overloaded"}`; string(got[0].Data) != want {
		t.Errorf("expected data %q, got %q", want, string(got[0].Data))
	}
}

// TestScanStreamEvents_ReadsAllEventsAndResetsLabels pins that the labels are
// per event: a `data:`-only frame after an `event:` frame must not inherit it,
// or every later chunk would look like a second error.
func TestScanStreamEvents_ReadsAllEventsAndResetsLabels(t *testing.T) {
	stream := "event: message_start\n" +
		"data: {\"a\":1}\n" +
		"\n" +
		"data: {\"b\":2}\n" +
		"\n"

	var got []SSEEvent
	err := ScanStreamEvents(strings.NewReader(stream), func(ev SSEEvent) bool {
		got = append(got, ev)
		return true
	})
	if err != nil {
		t.Fatalf("ScanStreamEvents returned an error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 events, got %d", len(got))
	}
	if got[0].Event != "message_start" {
		t.Errorf("expected first event labelled, got %q", got[0].Event)
	}
	if got[1].Event != "" {
		t.Errorf("expected the label to reset between events, got %q", got[1].Event)
	}
}

// TestScanStream_SkipsLabelOnlyAndKeepAliveFrames keeps the existing contract
// honest through the new shared body: a comment, a bare `data:` keep-alive and
// an `event:` label with no data each carry nothing a client can read, so
// neither scanner reports them.
func TestScanStreamEvents_SkipsFramesWithoutData(t *testing.T) {
	stream := ": keep-alive\n" +
		"\n" +
		"data:\n" +
		"\n" +
		"event: ping\n" +
		"\n" +
		"data: [DONE]\n" +
		"\n"

	var got []SSEEvent
	if err := ScanStreamEvents(strings.NewReader(stream), func(ev SSEEvent) bool {
		got = append(got, ev)
		return true
	}); err != nil {
		t.Fatalf("ScanStreamEvents returned an error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected only the frame carrying data, got %d: %q", len(got), got)
	}
	if string(got[0].Data) != "[DONE]" {
		t.Errorf("expected [DONE], got %q", string(got[0].Data))
	}
}
