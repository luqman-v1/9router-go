package chat

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestComboNonRetryableErrorTriesEachConnectionOnce pins that a combo entry
// never sends the same request to the same connection twice. A status outside
// RetryableStatusCodes (a 400 model_not_supported, a 500) does not lock the
// connection, and before this fix nothing else excluded it either, so
// getBestConnection handed the same account back for all ten connection
// attempts: ten identical upstream calls before the combo moved on.
func TestComboNonRetryableErrorTriesEachConnectionOnce(t *testing.T) {
	cases := []struct {
		status int
		body   string
	}{
		{http.StatusBadRequest, `{"error":{"message":"The requested model is not supported.","code":"model_not_supported","type":"invalid_request_error"}}`},
		{http.StatusUnprocessableEntity, `{"error":{"message":"unprocessable","type":"invalid_request_error"}}`},
		{http.StatusInternalServerError, `{"error":{"message":"internal error","type":"server_error"}}`},
	}
	for _, tc := range cases {
		for _, messages := range []bool{false, true} {
			name := fmt.Sprintf("%d chat", tc.status)
			if messages {
				name = fmt.Sprintf("%d messages", tc.status)
			}
			t.Run(name, func(t *testing.T) {
				var failingCalls, healthyCalls atomic.Int32
				failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					failingCalls.Add(1)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(tc.body))
				}))
				defer failing.Close()
				healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					healthyCalls.Add(1)
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"healthy response"}}]}`))
				}))
				defer healthy.Close()

				repo := seedComboWithTwoUpstreams(t, "combo-nonretry", failing.URL, healthy.URL, "nonretry-test")
				h := NewChatHandler(repo)
				req := httptest.NewRequest(http.MethodPost, "/chat/completions",
					strings.NewReader(`{"model":"nonretry-test","messages":[{"role":"user","content":"hi"}],"stream":false}`))
				rec := httptest.NewRecorder()
				if messages {
					h.HandleMessages(rec, req)
				} else {
					h.HandleChatCompletions(rec, req)
				}

				if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "healthy response") {
					t.Fatalf("combo did not fall over to the next member: %d %s", rec.Code, rec.Body.String())
				}
				if got := failingCalls.Load(); got != 1 {
					t.Errorf("failing connection received %d requests, want 1", got)
				}
				if got := healthyCalls.Load(); got != 1 {
					t.Errorf("healthy connection received %d requests, want 1", got)
				}
			})
		}
	}
}
