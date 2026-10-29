package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/proxy"
)

// Freebuff behind an edge relay.
//
// Freebuff makes control-plane calls beside the chat POST: requesting a session,
// starting a run, finishing a run, releasing a session. Each call targets its own
// path on https://www.codebuff.com (/api/v1/freebuff/session, /api/v1/agent-runs).
//
// The relay is a RoundTripper on the client the handler supplies, so each of
// those calls lands on the path it actually built — without any freebuff-specific
// relay code, and without sending the control plane to the chat-completions
// endpoint.

type relayFake struct {
	*httptest.Server
	gotTarget string
	gotPath   string
	calls     int
}

func newRelayFake(t *testing.T, body string) *relayFake {
	t.Helper()
	f := &relayFake{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.gotTarget = r.Header.Get("x-relay-target")
		f.gotPath = r.Header.Get("x-relay-path")
		f.calls++
		if f.gotTarget == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(f.Close)
	return f
}

func newFreebuffRelayedClient(t *testing.T, relayURL string) *http.Client {
	t.Helper()
	return &http.Client{
		Transport: proxy.RelayTransport(relayURL, http.DefaultTransport),
	}
}

func assertLanded(t *testing.T, relay *relayFake, wantPath string) {
	t.Helper()
	if relay.calls == 0 {
		t.Fatal("relay was never called: the control-plane request bypassed it")
	}
	if relay.gotTarget != "https://www.codebuff.com" {
		t.Errorf("x-relay-target = %q, want https://www.codebuff.com", relay.gotTarget)
	}
	if relay.gotPath != wantPath {
		t.Errorf("control-plane call routed to %q, want %q", relay.gotPath, wantPath)
	}
}

func TestFreebuffRelay_SessionRequestLandsOnSessionEndpoint(t *testing.T) {
	relay := newRelayFake(t, `{"status":"active","instanceId":"inst-1","expiresAt":"2099-01-01T00:00:00Z"}`)
	client := newFreebuffRelayedClient(t, relay.URL)

	if _, err := postFreebuffSession(context.Background(), client, "https://www.codebuff.com", "tok", "m-1", nil); err != nil {
		t.Fatalf("postFreebuffSession through a relay: %v", err)
	}
	assertLanded(t, relay, "/api/v1/freebuff/session")
}

func TestFreebuffRelay_RunStartLandsOnRunEndpoint(t *testing.T) {
	relay := newRelayFake(t, `{"runId":"run-1"}`)
	client := newFreebuffRelayedClient(t, relay.URL)

	runID, err := startFreebuffRun(context.Background(), client, "https://www.codebuff.com", "tok", "m-1", nil)
	if err != nil {
		t.Fatalf("startFreebuffRun through a relay: %v", err)
	}
	if runID != "run-1" {
		t.Errorf("runId = %q, want run-1", runID)
	}
	assertLanded(t, relay, "/api/v1/agent-runs")
}

func TestFreebuffRelay_RunFinishLandsOnRunEndpoint(t *testing.T) {
	relay := newRelayFake(t, `{}`)
	client := newFreebuffRelayedClient(t, relay.URL)

	finishFreebuffRun(context.Background(), client, "https://www.codebuff.com", "tok", "run-1", "completed", nil)
	assertLanded(t, relay, "/api/v1/agent-runs")
}

func TestFreebuffRelay_SessionReleaseLandsOnSessionEndpoint(t *testing.T) {
	relay := newRelayFake(t, `{"status":"released","freebucksRefund":3}`)
	client := newFreebuffRelayedClient(t, relay.URL)

	refund, err := releaseFreebuffSession(context.Background(), client, "https://www.codebuff.com", "tok", "inst-1", nil)
	if err != nil {
		t.Fatalf("releaseFreebuffSession through a relay: %v", err)
	}
	if refund != 3 {
		t.Errorf("refund = %d, want 3", refund)
	}
	assertLanded(t, relay, "/api/v1/freebuff/session")
}

// A connection that is not relayed sends no relay headers.
func TestFreebuffRelay_UnrelayedConnectionSendsNoRelayHeaders(t *testing.T) {
	var gotTarget, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTarget = r.Header.Get("x-relay-target")
		gotPath = r.Header.Get("x-relay-path")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"active","instanceId":"inst-1"}`))
	}))
	defer srv.Close()

	if _, err := postFreebuffSession(context.Background(), srv.Client(), srv.URL, "tok", "m-1", nil); err != nil {
		t.Fatalf("postFreebuffSession without a relay: %v", err)
	}
	if gotTarget != "" || gotPath != "" {
		t.Errorf("unrelayed call carried relay headers: target=%q path=%q", gotTarget, gotPath)
	}
}
