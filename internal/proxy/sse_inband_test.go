package proxy

import (
	"errors"
	"strings"
	"testing"
)

// stealthErrorLine is the exact in-band error event OpenRouter relayed for
// its dead Stealth downstream (from production requestDetails): HTTP 200,
// an empty choices array, and the failure inside `error`.
const stealthErrorLine = `data: {"id":"gen-1791132819-r2vSMPOQN6gHjNzP3jCk","object":"chat.completion.chunk","created":1791132819,"model":"stealth/space-bunny-alpha","provider":"Stealth","choices":[],"error":{"code":502,"message":"JSON error injected into SSE stream","metadata":{"error_type":"provider_unavailable"}}}`

func TestDetectInbandSSEError(t *testing.T) {
	tests := []struct {
		name        string
		line        string
		wantFound   bool
		wantCode    int
		wantMsgPart string
	}{
		{
			name:        "OpenRouter Stealth provider_unavailable chunk fails the turn",
			line:        stealthErrorLine,
			wantFound:   true,
			wantCode:    502,
			wantMsgPart: "JSON error injected into SSE stream",
		},
		{
			name:        "Claude error event payload fails the turn",
			line:        `data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
			wantFound:   true,
			wantCode:    502,
			wantMsgPart: "Overloaded",
		},
		{
			name:        "string error payload fails the turn",
			line:        `data: {"error":"upstream overloaded"}`,
			wantFound:   true,
			wantCode:    502,
			wantMsgPart: "upstream overloaded",
		},
		{
			name:        "numeric string code is honored",
			line:        `data: {"error":{"code":"503","message":"busy"}}`,
			wantFound:   true,
			wantCode:    503,
			wantMsgPart: "busy",
		},
		{
			name:        "status key backs up a missing code",
			line:        `data: {"error":{"status":429,"message":"slow down"}}`,
			wantFound:   true,
			wantCode:    429,
			wantMsgPart: "slow down",
		},
		{
			name:        "non-numeric code falls back to 502",
			line:        `data: {"error":{"code":"upstream_error","message":"bad gateway"}}`,
			wantFound:   true,
			wantCode:    502,
			wantMsgPart: "bad gateway",
		},
		{
			name:        "bare JSON error without data prefix still fails",
			line:        `{"error":{"message":"bare envelope"}}`,
			wantFound:   true,
			wantCode:    502,
			wantMsgPart: "bare envelope",
		},
		{
			name:      "DONE sentinel is not an error",
			line:      "data: [DONE]",
			wantFound: false,
		},
		{
			name:      "normal content delta passes through",
			line:      `data: {"choices":[{"delta":{"content":"hello"}}]}`,
			wantFound: false,
		},
		{
			name:      "content mentioning the word error passes through",
			line:      `data: {"choices":[{"delta":{"content":"there was an \"error\": null in the log"}}]}`,
			wantFound: false,
		},
		{
			name:      "null error passes through",
			line:      `data: {"choices":[],"error":null}`,
			wantFound: false,
		},
		{
			name:      "heartbeat comment passes through",
			line:      ": keep-alive",
			wantFound: false,
		},
		{
			name:      "event label alone carries no payload",
			line:      "event: error",
			wantFound: false,
		},
		{
			name:      "non-JSON data passes through",
			line:      "data: hello",
			wantFound: false,
		},
		{
			name:      "empty line passes through",
			line:      "",
			wantFound: false,
		},
		{
			name:      "unparseable body with an error key passes through",
			line:      `data: {"error":`,
			wantFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, msg, found := DetectInbandSSEError([]byte(tt.line))
			if found != tt.wantFound {
				t.Fatalf("DetectInbandSSEError() found = %v, want %v", found, tt.wantFound)
			}
			if !tt.wantFound {
				return
			}
			if code != tt.wantCode {
				t.Errorf("DetectInbandSSEError() code = %d, want %d", code, tt.wantCode)
			}
			if !strings.Contains(msg, tt.wantMsgPart) {
				t.Errorf("DetectInbandSSEError() message = %q, want it to contain %q", msg, tt.wantMsgPart)
			}
			if !strings.HasPrefix(msg, InbandStreamErrorMessage) {
				t.Errorf("DetectInbandSSEError() message = %q, want gateway wording with prefix %q", msg, InbandStreamErrorMessage)
			}
		})
	}
}

// A provider that injects an error into a stream must have that error seen.
// encoding/json/v2 rejects a repeated member name and invalid UTF-8 where v1
// accepted them, so a frame carrying both an error key and such a payload would
// otherwise parse as nothing and the turn would end as a truncated success.
func TestDetectInbandSSEError_LenientAboutProviderFrames(t *testing.T) {
	tests := []struct {
		name string
		line string
	}{
		{
			name: "repeated member name beside the error",
			line: `data: {"error":{"message":"dead"},"error":{"message":"dead"}}`,
		},
		{
			name: "invalid utf-8 beside the error",
			line: "data: {\"content\":\"x\xff\",\"error\":{\"message\":\"dead\",\"code\":503}}",
		},
		{
			name: "unpaired surrogate escape beside the error",
			line: `data: {"error":{"message":"dead"},"delta":"\ud83d"}`,
		},
		{
			name: "error key spelled with different casing",
			line: `data: {"error":{"Message":"dead","Code":503}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, msg, found := DetectInbandSSEError([]byte(tt.line))
			if !found {
				t.Fatal("DetectInbandSSEError() found = false, want true: the frame carries an error")
			}
			if code < 400 || code >= 600 {
				t.Errorf("code = %d, want a usable error status", code)
			}
			if !strings.Contains(msg, "dead") {
				t.Errorf("message = %q, want it to carry the provider text", msg)
			}
		})
	}
}

func TestIsEventErrorLine(t *testing.T) {
	tests := []struct {
		name string
		line string
		want bool
	}{
		{name: "error label matches", line: "event: error", want: true},
		{name: "error label with spacing matches", line: "  event:   error  ", want: true},
		{name: "other labels do not match", line: "event: message", want: false},
		{name: "data lines do not match", line: stealthErrorLine, want: false},
		{name: "comments do not match", line: ": keep-alive", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isEventErrorLine([]byte(tt.line)); got != tt.want {
				t.Errorf("isEventErrorLine(%q) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

// An error-only stream must fail loudly: the provider chunk is swallowed
// (never relayed), the gateway's own error frame closes the stream, and
// SSECopy reports the failure so the combo locks the sick connection out of
// rotation instead of logging a served success.
func TestSSECopy_InbandErrorOnlyStreamFails(t *testing.T) {
	tests := []struct {
		name   string
		stream string
	}{
		{
			name:   "error chunk then upstream closes without DONE",
			stream: stealthErrorLine + "\n\n",
		},
		{
			name:   "error chunk then DONE sentinel",
			stream: stealthErrorLine + "\n\ndata: [DONE]\n\n",
		},
		{
			name:   "Claude error event then close",
			stream: "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &mockResponseWriter{}
			err := SSECopy(rec, strings.NewReader(tt.stream), rec, nil)
			if err == nil {
				t.Fatal("expected the in-band provider error to be reported, got nil")
			}
			var ue *UpstreamError
			if !errors.As(err, &ue) {
				t.Fatalf("error type = %T, want *UpstreamError", err)
			}
			if ue.StatusCode != 502 {
				t.Errorf("StatusCode = %d, want 502 so the router fails over", ue.StatusCode)
			}

			out := rec.String()
			// The raw provider chunk is gone: its identifying fields
			// (provider name, metadata) never reach the client. The gateway
			// frame deliberately quotes the provider message text, so that
			// substring alone is not asserted absent.
			if strings.Contains(out, "Stealth") || strings.Contains(out, "provider_unavailable") {
				t.Errorf("provider error chunk must be swallowed, got %q", out)
			}
			frame, ok := errorFrame(t, out)
			if !ok {
				t.Fatalf("no gateway error frame in %q", out)
			}
			if frame.Error.Code != "upstream_error" {
				t.Errorf("frame code = %q, want upstream_error", frame.Error.Code)
			}
			if strings.Contains(out, "network_error") || strings.Contains(out, "finish_reason") {
				t.Errorf("error-only turn must not fabricate a terminal: %q", out)
			}
			if !strings.HasSuffix(out, sseDoneFrame) {
				t.Errorf("stream must still close with [DONE], got %q", out)
			}
		})
	}
}

// A turn that completed before the provider errored keeps its answer: the
// error beside real output is not a failure (codex stream rule).
func TestSSECopy_CompletedTurnIgnoresTrailingInbandError(t *testing.T) {
	stream := "data: {\"choices\":[{\"delta\":{\"content\":\"forty-two\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		stealthErrorLine + "\n\n" +
		"data: [DONE]\n\n"

	rec := &mockResponseWriter{}
	if err := SSECopy(rec, strings.NewReader(stream), rec, nil); err != nil {
		t.Fatalf("completed turn with trailing provider error must stay successful, got %v", err)
	}
	out := rec.String()
	if !strings.Contains(out, "forty-two") {
		t.Errorf("completed content must survive, got %q", out)
	}
}

// A healthy stream relays byte-for-byte semantics: content, terminal and DONE
// all pass through and no error is reported.
func TestSSECopy_HealthyStreamUnaffectedByInbandDetection(t *testing.T) {
	stream := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	rec := &mockResponseWriter{}
	if err := SSECopy(rec, strings.NewReader(stream), rec, nil); err != nil {
		t.Fatalf("healthy stream must stay successful, got %v", err)
	}
	out := rec.String()
	if !strings.Contains(out, `"content":"hi"`) {
		t.Errorf("content must pass through, got %q", out)
	}
	if _, ok := errorFrame(t, out); ok {
		t.Errorf("healthy stream must not gain an error frame: %q", out)
	}
}
