package proxy

import (
	"context"
	"errors"
	"net/http"
	"time"

	"9router/proxy/internal/log"
)

// transientRetryPolicy is the Go form of upstream's DEFAULT_RETRY_CONFIG
// (open-sse/config/runtimeConfig.js:78-83): how many extra attempts a
// temporarily-unavailable upstream gets, and how long to wait between them.
//
//	502: 3 attempts @3s   503: 3 attempts @2s   504: 2 attempts @3s
//
// 429 is deliberately absent. Upstream sets its attempts to 0 there so a
// throttled credential is handed to the next account instead of being retried
// against the same window, and this gateway keeps that contract: a 429 belongs
// to the account-fallback path, not here.
var transientRetryPolicy = map[int]transientRetry{
	http.StatusBadGateway:         {attempts: 3, delay: 3 * time.Second},
	http.StatusServiceUnavailable: {attempts: 3, delay: 2 * time.Second},
	http.StatusGatewayTimeout:     {attempts: 2, delay: 3 * time.Second},
}

type transientRetry struct {
	attempts int
	delay    time.Duration
}

// retryTransientUpstream repeats a failed transient attempt per
// transientRetryPolicy, so an upstream that answers `503 service_overloaded`
// (opencode-zen's Console backend under load) gets another chance inside the same
// client turn instead of surfacing as a failed request.
//
// The retry lives at the transport choke point rather than in each provider,
// because upstream applies it in BaseExecutor.execute — the shared path every
// provider inherits. It only ever runs before any response byte has reached the
// client, so a repeat cannot duplicate a partially delivered answer.
//
// Transport-level errors are not repeated: DoRequest already re-dials directly
// when a configured proxy refuses the tunnel, and a client that has gone away
// must not be kept waiting out a backoff.
func retryTransientUpstream(
	ctx context.Context,
	send func() (*http.Response, error),
) (*http.Response, error) {
	resp, err := send()
	var ue *UpstreamError
	if !errors.As(err, &ue) {
		return resp, err
	}

	policy, retryable := transientRetryPolicy[ue.StatusCode]
	for attempt := 1; retryable && attempt <= policy.attempts; attempt++ {
		if waitErr := sleepCtx(ctx, policy.delay); waitErr != nil {
			// The client is gone or the deadline passed: report the upstream
			// failure already in hand rather than starting a turn nobody reads.
			log.Debug("retry", "retry abandoned, client gone", "status", ue.StatusCode, "attempt", attempt)
			return nil, err
		}
		log.Debug("retry", "transient upstream failure, retrying", "status", ue.StatusCode, "attempt", attempt, "max", policy.attempts, "delay_ms", policy.delay.Milliseconds())

		resp, err = send()
		if !errors.As(err, &ue) {
			return resp, err
		}
		_, retryable = transientRetryPolicy[ue.StatusCode]
	}
	return resp, err
}

// sleepCtx waits for d, or returns early when ctx ends. A plain time.Sleep would
// keep the request alive for the whole backoff after the client disconnected.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
