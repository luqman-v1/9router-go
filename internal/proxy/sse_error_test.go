package proxy

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

func errorFrame(t *testing.T, stream string) (sseErrorPayload, bool) {
	t.Helper()
	for _, line := range strings.Split(stream, "\n") {
		payload, ok := strings.CutPrefix(strings.TrimSpace(line), "data:")
		if !ok {
			continue
		}
		var frame sseErrorPayload
		if err := json.Unmarshal([]byte(strings.TrimSpace(payload)), &frame); err == nil && frame.Error.Message != "" {
			return frame, true
		}
	}
	return sseErrorPayload{}, false
}

// Issue #57: a stream that dies after HTTP 200 used to be closed silently, so
// clients kept the truncated text and reported success. The abort must arrive
// as a parseable in-band error frame instead (upstream decolua/9router 93001213).
func TestSSECopy_AbortEmitsInBandErrorFrame(t *testing.T) {
	const partial = "data: {\"choices\":[{\"delta\":{\"content\":\"the answer is\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\" forty-two and\"}}]}\n\n"

	tests := []struct {
		name        string
		cause       error
		wantMessage string
		wantCode    string
	}{
		{
			name:        "socket died mid-turn reports upstream connection lost",
			cause:       errors.New("connection reset by peer"),
			wantMessage: "upstream connection lost",
			wantCode:    "upstream_error",
		},
		{
			name:        "stall watchdog reports a timeout, not a lost socket",
			cause:       ErrStreamStall,
			wantMessage: "stream stall timeout",
			wantCode:    "gateway_timeout",
		},
		{
			name:        "context cancellation is not a gateway failure",
			cause:       context.Canceled,
			wantMessage: "upstream request aborted",
			wantCode:    "request_aborted",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &mockResponseWriter{}
			upstream := io.MultiReader(strings.NewReader(partial), iotest.ErrReader(tt.cause))
			if err := SSECopy(rec, upstream, rec, nil); err == nil {
				t.Fatal("expected the read error to be reported")
			}

			out := rec.String()
			if !strings.Contains(out, `"content":" forty-two and"`) {
				t.Fatalf("content streamed before the abort must survive, got %q", out)
			}
			frame, ok := errorFrame(t, out)
			if !ok {
				t.Fatalf("no in-band error frame in %q", out)
			}
			if frame.Error.Message != tt.wantMessage {
				t.Errorf("message = %q, want %q", frame.Error.Message, tt.wantMessage)
			}
			if frame.Error.Code != tt.wantCode {
				t.Errorf("code = %q, want %q", frame.Error.Code, tt.wantCode)
			}
			// The client must not be told the turn finished.
			if strings.Contains(out, "finish_reason") {
				t.Errorf("abort fabricated a finish_reason: %q", out)
			}
			if !strings.HasSuffix(out, sseDoneFrame) {
				t.Errorf("stream must still close with [DONE], got %q", out)
			}
		})
	}
}
