package chat

import (
	"context"
	"errors"
	"net/http"
	"time"

	"9router/proxy/internal/connlimit"
	"9router/proxy/internal/proxy"
)

// admitAntigravity bounds per-connection concurrency and enforces backoff and
// the circuit breaker before an Antigravity upstream call.
//
// Steps, in order:
//  1. connlimit.Allow: breaker open → fail fast (zero upstream calls).
//     Backoff window active → sleep for the computed wait, honouring ctx.
//  2. connlimit.Acquire: reserve one in-flight slot; at the limit fail fast
//     with a labelled error (no unbounded queueing).
//
// The caller must invoke the returned release exactly once, on every path,
// including after ForwardGemini returns.
func admitAntigravity(ctx context.Context, connectionID string) (release func(), err error) {
	if connectionID == "" {
		return func() {}, nil
	}

	wait, allowed := connlimit.Allow(connectionID)
	if !allowed {
		return nil, &proxy.UpstreamError{
			StatusCode: http.StatusServiceUnavailable,
			Body:       []byte(`{"error":{"message":"antigravity circuit breaker open for connection","type":"circuit_breaker_open","code":503}}`),
		}
	}
	if wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	release, err = connlimit.Acquire(ctx, connectionID)
	if err != nil {
		if errors.Is(err, connlimit.ErrConnLimit) {
			return nil, &proxy.UpstreamError{
				StatusCode: http.StatusTooManyRequests,
				Body:       []byte(err.Error()),
			}
		}
		return nil, err
	}
	return release, nil
}

// recordAntigravityOutcome charges the attempt's result to the per-connection
// backoff and breaker. A 2xx resets everything (only a real successful response
// releases the breaker — never a token refresh). 403/429 widen the backoff and
// may open the breaker; a 403 TOS_VIOLATION bypasses both entirely (#996).
func recordAntigravityOutcome(connectionID string, status int, body []byte) {
	if connectionID == "" {
		return
	}
	if status >= 200 && status < 300 {
		connlimit.RecordSuccess(connectionID)
		return
	}
	if status == http.StatusForbidden || status == http.StatusTooManyRequests {
		connlimit.RecordFailure(connectionID, status, body)
	}
}

// closeAntigravityState drops a connection's breaker/backoff state. Called on
// connection delete and re-authentication so nothing leaks.
func closeAntigravityState(connectionID string) {
	if connectionID == "" {
		return
	}
	connlimit.Close(connectionID)
}
