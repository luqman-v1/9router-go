package executor

import (
	"context"
	"net/http"
	"testing"
	"time"

	"9router/proxy/internal/proxy"
)

// A 403 or a non-overload 429 means upstream spent or flagged the session the
// request traveled on (#5559). The rotating forward must drop that id so the
// next request mints a fresh one instead of reusing it until restart.
func TestForwardOpenCodeRotatingDropsSpentSession(t *testing.T) {
	cases := []struct {
		name string
		ue   *proxy.UpstreamError
	}{
		{"quota 429", usageLimitErr()},
		{"403", &proxy.UpstreamError{StatusCode: http.StatusForbidden, Body: []byte(`{"error":{"type":"FreeTierError","message":"free tier"}}`)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dropped := 0
			_, err := forwardOpenCodeRotating(context.Background(),
				func() (*http.Response, error) { return nil, tc.ue },
				func() (*http.Response, error) { return nil, context.Canceled },
				nil,
				func() { dropped++ })
			if err == nil {
				t.Fatal("the failure must be reported when no rotation is available")
			}
			if dropped != 1 {
				t.Errorf("expected the spent session to be dropped once, got %d", dropped)
			}
		})
	}
}

// An overload 429 is load shedding, not a verdict on the session: the id must
// survive so the retry can reuse it.
func TestForwardOpenCodeRotatingKeepsSessionOnOverload(t *testing.T) {
	origBase, origMax := openCodeOverloadBaseDelay, openCodeOverloadMaxDelay
	openCodeOverloadBaseDelay, openCodeOverloadMaxDelay = time.Millisecond, time.Millisecond
	t.Cleanup(func() { openCodeOverloadBaseDelay, openCodeOverloadMaxDelay = origBase, origMax })

	dropped := 0
	_, _ = forwardOpenCodeRotating(context.Background(),
		func() (*http.Response, error) { return nil, overloadErr() },
		func() (*http.Response, error) { return nil, context.Canceled },
		nil,
		func() { dropped++ })
	if dropped != 0 {
		t.Errorf("an overload 429 must not drop the session, got %d", dropped)
	}
}

// The forget hook is only built for a router-minted stable session: a
// client-supplied session and a keyed lane are never dropped.
func TestForgetOpencodeStableSession(t *testing.T) {
	if forgetOpencodeStableSession(&Request{ConnectionID: "conn-1", SessionID: "ses_client"}, true, "ses_client") != nil {
		t.Error("a client-supplied session must yield no drop hook")
	}
	if forgetOpencodeStableSession(&Request{ConnectionID: "conn-1"}, false, "ses_x") != nil {
		t.Error("a keyed lane must yield no drop hook")
	}

	drop := forgetOpencodeStableSession(&Request{ConnectionID: "conn-drop"}, true, "ses_drop")
	if drop == nil {
		t.Fatal("expected a drop hook for a stable-session request")
	}
	id := proxy.StableOpenCodeSessionID("opencode:conn:conn-drop")
	if !proxy.StableOpenCodeSessionEstablished("opencode:conn:conn-drop", id) && id == "" {
		t.Fatal("setup: stable session must exist before the drop")
	}
	drop()
	if proxy.StableOpenCodeSessionEstablished("opencode:conn:conn-drop", id) {
		t.Error("the drop hook must remove the session from the cache")
	}
}
