// Package budget enforces a per-period spend ceiling for one API key.
//
// It is the enforcement half of the F-2 work: the plans table and the
// apiKeys columns have carried LimitMicros, LimitTokens, Period, AlertPct and
// HardCutoff since F-3 landed, and policy.Resolve has resolved them since the
// same commit, but nothing read them. This package is the reader.
//
// The measurement is a SUM over usageHistory inside the period window, which
// is the same source the Usage dashboard reads. That is a deliberate choice
// over a reservation counter: a counter needs a reset job per period and a
// write on every request, while the ledger already holds every row with its
// cost and its client attribution. The trade-off is that the ceiling is
// enforced at request start rather than continuously, so a key can overshoot
// by the cost of the requests already in flight when the ceiling is crossed.
// For a personal gateway that is the right trade: the alternative spends a
// write per request on the hot path to close a window measured in cents.
//
// Money is integer micros everywhere. usageHistory.cost is a float in USD, so
// the conversion happens exactly once, in SpendFor, and no float reaches the
// comparison that refuses a request.
package budget

import (
	"context"
	"errors"
	"fmt"
	"time"

	"9router/proxy/internal/handlerutil"
)

// Period is one budget window. The four values are the ones the plans table
// already validates (db.IsValidPlanPeriod), so a plan and a key override
// cannot name a window the engine does not know.
//
// They are declared here rather than referenced from db so the dependency
// runs one way: db imports this package to answer a spend read, and a package
// the ledger layer depends on cannot depend back on it.
type Period = string

const (
	PeriodDaily   = "daily"
	PeriodWeekly  = "weekly"
	PeriodMonthly = "monthly"
	PeriodTotal   = "total"
)

// Window is the resolved [start, end) interval a period covers, plus the
// instant it stops moving. Anchored windows (daily, weekly, monthly) restart
// on a calendar boundary, so two requests inside the same day read the same
// floor; a sliding window (total) has no floor at all and reads the whole
// ledger.
type Window struct {
	// Since is the inclusive usageHistory timestamp floor. Empty means
	// unbounded, which is what PeriodTotal uses.
	Since string
	// Until is the exclusive ceiling, used only to detect that a window has
	// rolled over between two reads.
	Until string
	// ResetAt is when the window next rolls. Zero for PeriodTotal.
	ResetAt time.Time
}

// Spend is one key's measured consumption inside its period window.
type Spend struct {
	// Micros is the period spend in USD micros.
	Micros int64
	// Tokens is prompt+completion tokens inside the window.
	Tokens int64
}

// Limits is the budget half of a resolved policy. Zero values mean "no
// budget on this axis", which is how an unconfigured key behaves.
type Limits struct {
	LimitMicros int64
	LimitTokens int64
	Period      Period
	AlertPct    int
	HardCutoff  bool
}

// Armed reports whether any budget axis is configured. A key with no budget
// on either axis costs one short-circuit and no database read at all.
func (l Limits) Armed() bool {
	return l.LimitMicros > 0 || l.LimitTokens > 0
}

// ResolveWindow maps a period onto the interval it covers, measured from now.
//
// An unknown or empty period resolves to monthly rather than to unbounded:
// a typo in a plan must not silently disable the ceiling it was written for.
func ResolveWindow(period Period, now time.Time) Window {
	switch period {
	case PeriodDaily:
		start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		return Window{
			Since:   start.UTC().Format(time.RFC3339),
			Until:   start.AddDate(0, 0, 1).UTC().Format(time.RFC3339),
			ResetAt: start.AddDate(0, 0, 1),
		}
	case PeriodWeekly:
		// Weeks start Monday. The offset is (weekday+6)%7 rather than a
		// subtraction from time.Monday because Sunday is 0 in Go's Weekday:
		// subtracting 1 from it would move a Sunday forward into the next
		// week instead of back into the one that started six days earlier.
		start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		start = start.AddDate(0, 0, -int((start.Weekday()+6)%7))
		return Window{
			Since:   start.UTC().Format(time.RFC3339),
			Until:   start.AddDate(0, 0, 7).UTC().Format(time.RFC3339),
			ResetAt: start.AddDate(0, 0, 7),
		}
	case PeriodMonthly:
		start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		return Window{
			Since:   start.UTC().Format(time.RFC3339),
			Until:   start.AddDate(0, 1, 0).UTC().Format(time.RFC3339),
			ResetAt: start.AddDate(0, 1, 0),
		}
	case PeriodTotal:
		return Window{}
	default:
		return ResolveWindow(PeriodMonthly, now)
	}
}

// Reader is the ledger read the engine needs. *db.Repo satisfies it.
type Reader interface {
	SpendForKeySince(ctx context.Context, rawKey, since string) (Spend, error)
}

// SpendFor measures one key's consumption inside its period window.
//
// The read is attributed the same way the lifetime token quota is: by the
// client key's masked form in clientKey, plus the raw and masked spellings in
// apiKey so rows the upstream dashboard wrote keep counting. A failed read is
// an error, not a zero: reporting zero spend for a key that has spent its
// budget would refuse nothing, and reporting zero for a key that has spent
// nothing would refuse everything. The caller decides which way to fail.
func SpendFor(ctx context.Context, r Reader, rawKey string, w Window) (Spend, error) {
	if r == nil || rawKey == "" {
		return Spend{}, nil
	}
	return r.SpendForKeySince(ctx, rawKey, w.Since)
}

// Decision is what the engine concluded about one request.
type Decision struct {
	// Refuse is true when the request must not be served.
	Refuse bool
	// Alerting is true when the spend has crossed the alert threshold but
	// not the hard cutoff, so the response carries a warning header.
	Alerting bool
	// Axis names which budget refused: "cost" or "tokens".
	Axis string
	// Spend is what was measured, for the refusal body and the header.
	Spend Spend
	// Limits is the budget that refused, for the same.
	Limits Limits
	// ResetAt is when the window rolls, for Retry-After. Zero for a
	// lifetime-style total budget, where waiting does not help.
	ResetAt time.Time
}

// Check measures one key's spend and decides whether the request may proceed.
//
// A key with no budget armed returns an empty decision without touching the
// database. A read failure returns an empty decision too, which lets the
// request through: a storage blip must not lock a key out of a service it has
// always been able to call, and the ceiling is a spend cap rather than an
// access grant. The same fail-open choice the lifetime token quota makes.
func Check(ctx context.Context, r Reader, rawKey string, limits Limits, now time.Time) Decision {
	if !limits.Armed() || rawKey == "" {
		return Decision{}
	}
	w := ResolveWindow(limits.Period, now)
	spend, err := SpendFor(ctx, r, rawKey, w)
	if err != nil {
		return Decision{}
	}
	return decide(spend, limits, w.ResetAt)
}

// decide applies the measured spend to the configured limits. It is separate
// from Check so the thresholds can be tested without a database.
func decide(spend Spend, limits Limits, resetAt time.Time) Decision {
	out := Decision{Spend: spend, Limits: limits, ResetAt: resetAt}
	if limits.LimitMicros > 0 && spend.Micros >= limits.LimitMicros {
		if limits.HardCutoff {
			out.Refuse = true
			out.Axis = "cost"
			return out
		}
		out.Alerting = true
	}
	if limits.LimitTokens > 0 && spend.Tokens >= limits.LimitTokens {
		if limits.HardCutoff {
			out.Refuse = true
			out.Axis = "tokens"
			return out
		}
		out.Alerting = true
	}
	// The alert threshold is a percentage of whichever ceiling is configured.
	// It is evaluated after the cutoff so an exhausted key is refused rather
	// than merely warned about.
	if !out.Alerting && limits.AlertPct > 0 {
		if pctReached(spend.Micros, limits.LimitMicros, limits.AlertPct) ||
			pctReached(spend.Tokens, limits.LimitTokens, limits.AlertPct) {
			out.Alerting = true
		}
	}
	return out
}

// pctReached reports whether spent has reached pct percent of limit. A limit
// of zero means the axis is not configured, so it never alerts.
func pctReached(spent, limit int64, pct int) bool {
	if limit <= 0 || pct <= 0 {
		return false
	}
	return spent*100 >= limit*int64(pct)
}

// ErrNoReader is returned by helpers that need a ledger reader and were given
// none. It exists so a wiring mistake fails at the call site rather than as a
// nil dereference inside a request.
var ErrNoReader = errors.New("budget: no ledger reader configured")

// FormatMicros renders USD micros as a decimal string for a refusal body.
func FormatMicros(micros int64) string {
	return fmt.Sprintf("%.2f", float64(micros)/1_000_000)
}

// Masked is the attribution shape the ledger read matches on. It is re-exported
// so a caller holding a raw key does not have to import the handler utility to
// ask the same question the engine asks.
func Masked(rawKey string) string { return handlerutil.MaskAPIKey(rawKey) }
