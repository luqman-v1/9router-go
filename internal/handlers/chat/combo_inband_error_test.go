package chat

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// inbandErrorStream answers HTTP 200 with a provider-injected in-band error
// event and then closes: no usable content, no [DONE]. The combo must treat
// this as an explicit failure, serve the error frame to the client, and must
// not fail over to the next combo member.
func TestHandleChatCompletions_ComboDoesNotFailoverOnInbandError(t *testing.T) {
	sickHits := newCounter()

	sickUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sickHits.inc()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"id\":\"gen-sick\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"sick-model\",\"choices\":[],\"error\":{\"code\":502,\"message\":\"sick downstream unavailable\"}}" + "\n\n"))
	}))
	defer sickUpstream.Close()

	healthyUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"should not be served\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer healthyUpstream.Close()

	repo := seedComboWithTwoUpstreams(t, "combo-inband-nofailover", sickUpstream.URL, healthyUpstream.URL, "inband-nofailover")

	rec := httptest.NewRecorder()
	NewChatHandler(repo).HandleChatCompletions(rec,
		httptest.NewRequest("POST", "/chat/completions",
			strings.NewReader("{\"model\":\"inband-nofailover\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}],\"stream\":true}")))

	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("expected text/event-stream, got %q: %s", ct, rec.Body.String())
	}
	body := rec.Body.String()
	// The raw provider error frame reaches the client (committed by the peeker).
	if !strings.Contains(body, "sick downstream unavailable") {
		t.Fatalf("expected the in-band provider error to reach the client, got body:\n%s", body)
	}
	// The healthy member must not be tried: no silent failover.
	if sickHits.get() != 1 {
		t.Fatalf("sick upstream hit %d times, want exactly 1 (no failover to healthy member)", sickHits.get())
	}
	if strings.Contains(body, "should not be served") {
		t.Fatalf("healthy member content must not be appended after the in-band error: %s", body)
	}
}

type counter struct{ n int }

func newCounter() *counter { return &counter{} }

func (c *counter) inc()     { c.n++ }
func (c *counter) get() int { return c.n }
