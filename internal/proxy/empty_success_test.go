package proxy

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

// A 200 that carries no answer must be reported as a retryable failure, or
// the router reads it as a served turn and never leaves the model.
func TestEmptyUpstreamError(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantFail bool
	}{
		{
			name:     "blank body",
			body:     "",
			wantFail: true,
		},
		{
			name:     "whitespace only body",
			body:     "  \n\t ",
			wantFail: true,
		},
		{
			name:     "html error page served with 200",
			body:     "<!DOCTYPE html><html><body>502 Bad Gateway</body></html>",
			wantFail: true,
		},
		{
			name:     "bare text",
			body:     "upstream unavailable",
			wantFail: true,
		},
		{
			name:     "truncated json",
			body:     `{"choices":[{"message":{"content":"hi"`,
			wantFail: true,
		},
		{
			name:     "empty choices",
			body:     `{"choices":[]}`,
			wantFail: true,
		},
		{
			name:     "choice with no content",
			body:     `{"choices":[{"message":{"content":""},"finish_reason":"stop"}]}`,
			wantFail: true,
		},
		{
			name:     "empty responses output",
			body:     `{"output":[]}`,
			wantFail: true,
		},
		{
			name:     "error envelope served with 200",
			body:     `{"error":{"message":"upstream overloaded","type":"server_error"}}`,
			wantFail: true,
		},
		{
			name:     "error envelope with a null message",
			body:     `{"error":{"message":null},"choices":[]}`,
			wantFail: true,
		},
		{
			name:     "content plus tool calls is an answer",
			body:     `{"choices":[{"message":{"content":"","tool_calls":[{"id":"1"}]}}]}`,
			wantFail: false,
		},
		{
			name:     "content plus reasoning is an answer",
			body:     `{"choices":[{"message":{"content":"","reasoning_content":"step"}}]}`,
			wantFail: false,
		},
		{
			name:     "refusal is an answer",
			body:     `{"choices":[{"message":{"content":"","refusal":"cannot help"}}]}`,
			wantFail: false,
		},
		{
			name:     "plain content is an answer",
			body:     `{"choices":[{"message":{"role":"assistant","content":"hello"}}]}`,
			wantFail: false,
		},
		{
			name:     "claude messages shape is an answer",
			body:     `{"content":[{"type":"text","text":"hello"}]}`,
			wantFail: false,
		},
		{
			name:     "responses output with a message is an answer",
			body:     `{"output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}]}`,
			wantFail: false,
		},
		{
			name:     "responses completed with output is an answer",
			body:     `{"status":"completed","output":[{"type":"message"}]}`,
			wantFail: false,
		},
		{
			name:     "error field explicitly null is not an error",
			body:     `{"error":null,"choices":[{"message":{"content":"hello"}}]}`,
			wantFail: false,
		},
		{
			name:     "an unfolded event stream is left to the caller",
			body:     "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n",
			wantFail: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := EmptyUpstreamError([]byte(tt.body))
			if !tt.wantFail {
				if err != nil {
					t.Fatalf("EmptyUpstreamError() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("EmptyUpstreamError() = nil, want a failure")
			}
			var ue *UpstreamError
			if !errors.As(err, &ue) {
				t.Fatalf("EmptyUpstreamError() = %T, want *UpstreamError", err)
			}
			if ue.StatusCode != http.StatusBadGateway {
				t.Errorf("StatusCode = %d, want 502 so fallback retries", ue.StatusCode)
			}
			if !strings.Contains(string(ue.Body), "error") {
				t.Errorf("Body = %s, want an error envelope", ue.Body)
			}
		})
	}
}

// encoding/json/v2 rejects an object that repeats a member name and rejects
// invalid UTF-8, while the v1 behavior this probe was written against accepted
// both. A provider body is not JSON this gateway produced, so neither may turn
// a 200 carrying a real answer into the 502 that ends combo fallback.
func TestEmptyUpstreamError_LenientAboutProviderBodies(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "repeated member name in the message",
			body: `{"choices":[{"message":{"content":"hi","content":"there"}}]}`,
		},
		{
			name: "repeated member name at the envelope",
			body: `{"choices":[],"choices":[{"message":{"content":"hi"}}]}`,
		},
		{
			name: "invalid utf-8 in the content",
			body: "{\"choices\":[{\"message\":{\"content\":\"caf\xc3\xa9 \xff\"}}]}",
		},
		{
			name: "unpaired surrogate escape",
			body: `{"choices":[{"message":{"content":"ok \ud83d"}}]}`,
		},
		{
			name: "envelope key spelled with different casing",
			body: `{"Choices":[{"Message":{"Content":"hi"}}]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := EmptyUpstreamError([]byte(tt.body)); err != nil {
				t.Fatalf("EmptyUpstreamError() = %v, want nil: the body carries an answer", err)
			}
		})
	}
}

func TestLooksLikeSSE(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{name: "data frame", body: "data: {}\n\n", want: true},
		{name: "event frame", body: "event: error\ndata: {}\n\n", want: true},
		{name: "leading blank lines", body: "\n\n  data: {}\n\n", want: true},
		{name: "json document", body: `{"choices":[]}`, want: false},
		{name: "html page", body: "<html></html>", want: false},
		{name: "empty", body: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := LooksLikeSSE([]byte(tt.body)); got != tt.want {
				t.Errorf("LooksLikeSSE(%q) = %v, want %v", tt.body, got, tt.want)
			}
		})
	}
}
