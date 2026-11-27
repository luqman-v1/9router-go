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

// The forget hook is built for every router-owned stable session: hint-less
// requests and translated non-native client ids. A native `ses_` session and a
// keyed lane are never dropped.
func TestForgetOpencodeStableSession(t *testing.T) {
	native := "ses_000000000fcfabcdefghijklmn"
	if forgetOpencodeStableSession(&Request{ConnectionID: "conn-1", SessionID: native}, true, native) != nil {
		t.Error("a native client session must yield no drop hook")
	}
	if forgetOpencodeStableSession(&Request{ConnectionID: "conn-1"}, false, "ses_x") != nil {
		t.Error("a keyed lane must yield no drop hook")
	}

	drop := forgetOpencodeStableSession(&Request{ConnectionID: "conn-drop"}, true, "ses_drop")
	if drop == nil {
		t.Fatal("expected a drop hook for a stable-session request")
	}
	drop()

	uuid := "01a21c78-3cb9-771c-be11-1e5b4a46b8a1"
	clientDrop := forgetOpencodeStableSession(&Request{ConnectionID: "conn-1", SessionID: uuid}, true, "ses_client")
	if clientDrop == nil {
		t.Fatal("expected a drop hook for a non-native client session")
	}
	clientDrop()
}

func TestOpencodeSessionHint_NonNativeClientSessionStableAndRotatable(t *testing.T) {
	req := &Request{ConnectionID: "conn-client-hint", SessionID: "01a21c78-3cb9-771c-be11-1e5b4a46b8a1"}
	first := opencodeSessionHint(req, true)
	if !proxy.IsCanonicalOpenCodeSession(first) {
		t.Fatalf("translated session %q is not canonical", first)
	}
	key := opencodeStableSessionKey(req, true)
	if key == "" {
		t.Fatal("a non-native client session must get a router-owned key")
	}
	if second := opencodeSessionHint(req, true); second != first {
		t.Fatalf("client session hint changed across turns: %q -> %q", first, second)
	}
	if !proxy.StableOpenCodeSessionEstablished(key, first) {
		t.Fatal("setup: the session must have prior turns before a rotation is allowed")
	}
	rotated := 0
	rotate := opencodeSessionRotation(req, true, first, func(session string) {
		rotated++
		if session == first {
			t.Errorf("rotation installed the same spent session: %q", session)
		}
	})
	if rotate == nil {
		t.Fatal("a non-native client session must get a rotation hook")
	}
	rotate()
	if rotated != 1 {
		t.Fatalf("expected one rotation, got %d", rotated)
	}
	if fresh := proxy.StableOpenCodeSessionID(key); fresh == first {
		t.Error("rotation must replace the spent session in the cache")
	}
}
