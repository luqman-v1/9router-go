package executor

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func envelopeResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}
}

// The plan gateway reports drain conditions inside a 200 envelope instead of
// failing the request. Each one has to reach the shared classifier as the
// status it already understands, otherwise a drained or gated connection stays
// eligible and keeps swallowing requests.
func TestZcodeEnvelopeError_mapsDrainCodesToAccountScopedStatuses(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{"quota exhausted 1005", `{"code":1005,"msg":"exceed quota limit","logid":"x"}`, http.StatusTooManyRequests},
		{"grant spent 1113", `{"code":1113,"msg":"Insufficient balance or no resource package"}`, http.StatusTooManyRequests},
		{"model concurrency 3009", `{"code":3009,"msg":"model concurrency limit exceeded"}`, http.StatusTooManyRequests},
		{"account concurrency 3008", `{"code":3008,"msg":"user concurrency limit exceeded"}`, http.StatusTooManyRequests},
		{"identity gate 3012", `{"code":3012,"msg":"request has been blocked due to unusual activity."}`, http.StatusForbidden},
	} {
		_, status, isErr := zcodeEnvelopeError(envelopeResponse(http.StatusOK, tc.body))
		if !isErr {
			t.Errorf("%s: envelope not detected as an error", tc.name)
			continue
		}
		if status != tc.want {
			t.Errorf("%s: status = %d, want %d", tc.name, status, tc.want)
		}
	}
}

// A real completion and a concurrency code that the retry path consumes must
// both pass through with their body intact.
func TestZcodeEnvelopeError_passesThroughRealResponses(t *testing.T) {
	body := `{"id":"msg_1","type":"message","content":[{"type":"text","text":"PONG"}]}`
	buffered, status, isErr := zcodeEnvelopeError(envelopeResponse(http.StatusOK, body))
	if isErr {
		t.Fatalf("a real completion was reported as an error (status %d)", status)
	}
	if string(buffered) != body {
		t.Errorf("body not preserved: %q", buffered)
	}

	buffered, _, isErr = zcodeEnvelopeError(envelopeResponse(http.StatusTooManyRequests,
		`{"code":3009,"msg":"model concurrency limit exceeded"}`))
	if !isErr {
		t.Error("a 429 must stay an error for the caller")
	}
	if len(buffered) == 0 {
		t.Error("429 body was dropped")
	}
}
