package proxy

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strconv"
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

type seatBudgetCtxKey struct{}

// WithSeatBudget attaches a seat attempt budget in milliseconds to ctx.
func WithSeatBudget(ctx context.Context, ms int) context.Context {
	return context.WithValue(ctx, seatBudgetCtxKey{}, ms)
}

func resolveSeatBudget(ctx context.Context) int {
	if ctx != nil {
		if val, ok := ctx.Value(seatBudgetCtxKey{}).(int); ok && val > 0 {
			return val
		}
	}
	for _, envVar := range []string{"SEAT_ATTEMPT_BUDGET_MS", "NINEROUTER_SEAT_ATTEMPT_BUDGET_MS"} {
		if val := os.Getenv(envVar); val != "" {
			if ms, err := strconv.Atoi(val); err == nil && ms > 0 {
				return ms
			}
		}
	}
	return 0
}

// retryTransientUpstream repeats a failed transient attempt per
// transientRetryPolicy, bounded by per-seat attempt budget (upstream #5341).
func retryTransientUpstream(
	ctx context.Context,
	send func(context.Context) (*http.Response, error),
) (*http.Response, error) {
	seatBudgetMs := resolveSeatBudget(ctx)
	startTime := time.Now()

	executeAttempt := func() (*http.Response, error) {
		return send(ctx)
	}

	resp, err := executeAttempt()
	var ue *UpstreamError
	if !errors.As(err, &ue) {
		return resp, err
	}

	policy, retryable := transientRetryPolicy[ue.StatusCode]
	for attempt := 1; retryable && attempt <= policy.attempts; attempt++ {
		elapsed := time.Since(startTime).Milliseconds()
		waitMs := policy.delay.Milliseconds()

		if seatBudgetMs > 0 {
			if elapsed+waitMs > int64(seatBudgetMs) {
				log.Debug("retry", "seat attempt budget exceeded, skipping retry", "status", ue.StatusCode, "elapsed_ms", elapsed, "budget_ms", seatBudgetMs, "wait_ms", waitMs)
				return resp, err
			}
		}

		if waitErr := sleepCtx(ctx, policy.delay); waitErr != nil {
			log.Debug("retry", "retry abandoned, client gone", "status", ue.StatusCode, "attempt", attempt)
			return nil, err
		}
		log.Debug("retry", "transient upstream failure, retrying", "status", ue.StatusCode, "attempt", attempt, "max", policy.attempts, "delay_ms", policy.delay.Milliseconds())

		resp, err = executeAttempt()
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
