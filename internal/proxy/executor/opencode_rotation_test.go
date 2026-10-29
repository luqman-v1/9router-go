package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"9router/proxy/internal/proxy"
)

func usageLimitErr() *proxy.UpstreamError {
	return &proxy.UpstreamError{
		StatusCode: http.StatusTooManyRequests,
		Body:       []byte(`{"type":"error","error":{"type":"FreeUsageLimitError","message":"Free usage limit reached. Subscribe to OpenCode Go to keep going: https://opencode.ai/go"}}`),
	}
}

func okResponse() (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok"))}, nil
}

// The spent-session recovery: a 429 FreeUsageLimitError on a router-minted
// stable session rotates the session and replays once, mirroring the official
// CLI opening a fresh conversation. A session the client supplied is never
// swapped, and any other failure keeps the error for the caller's fallback.
func TestForwardOpenCodeRotating(t *testing.T) {
	t.Run("usage limit rotates and replays", func(t *testing.T) {
		attempts := 0
		rotations := 0
		resp, err := forwardOpenCodeRotating(context.Background(),
			func() (*http.Response, error) {
				attempts++
				if attempts == 1 {
					return nil, usageLimitErr()
				}
				return okResponse()
			},
			func() (*http.Response, error) { return nil, context.Canceled },
			func() { rotations++ })
		if err != nil {
			t.Fatalf("expected the replay to succeed, got %v", err)
		}
		resp.Body.Close()
		if attempts != 2 || rotations != 1 {
			t.Errorf("expected one rotation and one replay, got attempts=%d rotations=%d", attempts, rotations)
		}
	})

	t.Run("client-supplied session is never rotated", func(t *testing.T) {
		attempts := 0
		_, err := forwardOpenCodeRotating(context.Background(),
			func() (*http.Response, error) { attempts++; return nil, usageLimitErr() },
			func() (*http.Response, error) { return nil, context.Canceled },
			nil)
		var ue *proxy.UpstreamError
		if !errors.As(err, &ue) || ue.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("expected the usage-limit error untouched, got %v", err)
		}
		if attempts != 1 {
			t.Errorf("expected a single attempt, got %d", attempts)
		}
	})

	t.Run("one rotation only", func(t *testing.T) {
		attempts := 0
		rotations := 0
		_, err := forwardOpenCodeRotating(context.Background(),
			func() (*http.Response, error) { attempts++; return nil, usageLimitErr() },
			func() (*http.Response, error) { return nil, context.Canceled },
			func() { rotations++ })
		var ue *proxy.UpstreamError
		if !errors.As(err, &ue) || ue.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("expected the usage-limit error after the replay, got %v", err)
		}
		if attempts != 2 || rotations != 1 {
			t.Errorf("expected exactly one rotation, got attempts=%d rotations=%d", attempts, rotations)
		}
	})

	t.Run("a genuine 429 rate limit is not a rotation signal", func(t *testing.T) {
		rotations := 0
		_, err := forwardOpenCodeRotating(context.Background(),
			func() (*http.Response, error) {
				return nil, &proxy.UpstreamError{
					StatusCode: http.StatusTooManyRequests,
					Body:       []byte(`{"error":{"type":"RateLimitError","message":"too many requests"}}`),
				}
			},
			func() (*http.Response, error) { return nil, context.Canceled },
			func() { rotations++ })
		var ue *proxy.UpstreamError
		if !errors.As(err, &ue) || ue.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("expected the rate-limit error untouched, got %v", err)
		}
		if rotations != 0 {
			t.Error("expected no rotation for a non-usage-limit 429")
		}
	})
}

// The rotation hook only exists for a router-minted stable session; keyed
// lanes and client-supplied sessions are never rotated.
func TestOpencodeStableSessionKey(t *testing.T) {
	free := &Request{ConnectionID: "conn-1"}
	if got := opencodeStableSessionKey(free, true); got != "opencode:conn:conn-1" {
		t.Errorf("free lane without a client session: got key %q", got)
	}
	withSession := &Request{ConnectionID: "conn-1", SessionID: "ses_client"}
	if got := opencodeStableSessionKey(withSession, true); got != "" {
		t.Errorf("a client session must never be rotated, got key %q", got)
	}
	keyed := &Request{ConnectionID: "conn-1"}
	if got := opencodeStableSessionKey(keyed, false); got != "" {
		t.Errorf("keyed lane quota is per key, got key %q", got)
	}
	if got := opencodeStableSessionKey(&Request{}, true); got != "opencode:default" {
		t.Errorf("missing connection falls back to the default key, got %q", got)
	}
}

// The hook expires the failed id and installs a freshly minted one; a nil key
// yields no hook at all.
func TestRotateOpencodeStableSession(t *testing.T) {
	if rotateOpencodeStableSession("", "ses_x", func(string) {}) != nil {
		t.Error("a client-supplied session must yield no rotation hook")
	}

	id := proxy.StableOpenCodeSessionID("rotate-test")
	var installed []string
	rotate := rotateOpencodeStableSession("rotate-test", id, func(session string) {
		installed = append(installed, session)
	})
	if rotate == nil {
		t.Fatal("expected a rotation hook for a stable-session request")
	}
	rotate()
	if len(installed) != 1 || installed[0] == id {
		t.Fatalf("expected one fresh session install, got %v", installed)
	}
	if !proxy.IsCanonicalOpenCodeSession(installed[0]) {
		t.Errorf("installed session %q is not canonical", installed[0])
	}

	// Rotating with a stale id after the rotation already happened keeps the
	// newer session and its fresh budget: the replay installs the current id,
	// never a third mint.
	rotate2 := rotateOpencodeStableSession("rotate-test", id, func(session string) {
		installed = append(installed, session)
	})
	rotate2()
	current := proxy.StableOpenCodeSessionID("rotate-test")
	if current != installed[0] {
		t.Errorf("a lost rotation race must keep the newer session: map=%q installed=%v", current, installed)
	}
	if len(installed) != 2 || installed[1] != installed[0] {
		t.Errorf("a lost rotation race must reinstall the current session only, got %v", installed)
	}
}

// The stealth forward answers a non-200 as a plain response, not an error: a
// refusal in the response position must still be lifted into the error
// position, or the 403 direct replay and the 429 session rotation never see
// it. Regression for the live run where every 429 sailed past the rotation.
func TestForwardOpenCodeRotating_ResponsePositionUsageLimit(t *testing.T) {
	attempts := 0
	rotations := 0
	resp, err := forwardOpenCodeRotating(context.Background(),
		func() (*http.Response, error) {
			attempts++
			if attempts == 1 {
				return &http.Response{
					StatusCode: http.StatusTooManyRequests,
					Body:       io.NopCloser(strings.NewReader(string(usageLimitErr().Body))),
				}, nil
			}
			return okResponse()
		},
		func() (*http.Response, error) { return nil, context.Canceled },
		func() { rotations++ })
	if err != nil {
		t.Fatalf("expected the replay to succeed, got %v", err)
	}
	resp.Body.Close()
	if attempts != 2 || rotations != 1 {
		t.Errorf("expected the response-position 429 to rotate once, got attempts=%d rotations=%d", attempts, rotations)
	}
}

func TestForwardOpenCode_ResponsePositionRefusalReplaysDirect(t *testing.T) {
	attempts := 0
	resp, err := forwardOpenCode(context.Background(),
		func() (*http.Response, error) {
			attempts++
			return &http.Response{
				StatusCode: http.StatusForbidden,
				Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"FreeTierError","message":"free tier"}}`)),
			}, nil
		},
		func() (*http.Response, error) {
			return okResponse()
		})
	if err != nil {
		t.Fatalf("expected the direct replay to succeed, got %v", err)
	}
	resp.Body.Close()
	if attempts != 1 || resp.StatusCode != http.StatusOK {
		t.Errorf("expected one refusal attempt plus a direct replay, got attempts=%d status=%d", attempts, resp.StatusCode)
	}
}
