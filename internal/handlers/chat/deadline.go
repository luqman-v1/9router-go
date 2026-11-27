package chat

import (
	"context"
	"time"

	"9router/proxy/internal/db"
)

// totalRequestDeadlineKey marks a context whose deadline the gateway set for
// itself, so an expiry can be reported as a gateway timeout rather than as a
// client hangup or a lost upstream socket.
type totalRequestDeadlineKey struct{}

// WithTotalRequestDeadline bounds how long one chat request may run, headers
// plus body, and records that the bound is the gateway's own.
//
// This is the last line of defence against a hung upstream, not a limit on a
// slow one: reasoning models legitimately stream for many minutes, which is why
// the setting is off by default and generous when set. It is separate from the
// stall watchdog (which resets on every upstream byte) and from the connect
// timeout (headers only).
//
// A request that already carries an earlier deadline keeps it: the tighter
// bound wins, and there is no point extending a client's own deadline.
func WithTotalRequestDeadline(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return ctx, func() {}
	}
	if existing, ok := ctx.Deadline(); ok && time.Until(existing) <= d {
		return ctx, func() {}
	}
	ctx, cancel := context.WithTimeout(ctx, d)
	return context.WithValue(ctx, totalRequestDeadlineKey{}, true), cancel
}

// isRequestDeadline reports whether ctx carries the gateway's own deadline.
func isRequestDeadline(ctx context.Context) bool {
	marked, _ := ctx.Value(totalRequestDeadlineKey{}).(bool)
	return marked
}

// TotalRequestTimeoutSetting is the settings key, named in the db package where
// the parser reads it.
const TotalRequestTimeoutSetting = db.TotalRequestTimeoutSetting

// totalRequestTimeout resolves the configured bound. Zero (or a failed settings
// read) means off: an unbounded request is the behaviour that existed before
// this setting, and turning a new bound on by default would cut generations
// that are merely long.
// totalRequestTimeout reads the operator's end-to-end bound from the request's
// settings row. It returns 0 when there is none.
func (h *ChatHandler) totalRequestTimeout(ctx context.Context) time.Duration {
	if h == nil || h.Repo == nil {
		return 0
	}
	settings := h.settingsForRequest(ctx)
	if settings == nil {
		// The row could not be read. An unbounded request is the "off" state,
		// and a database that is already unhappy must not become a panic.
		return 0
	}
	ms := settings.TotalRequestTimeoutMs
	if ms <= 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}
