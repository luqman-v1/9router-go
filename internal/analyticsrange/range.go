// Package analyticsrange resolves the time window an analytics read covers.
//
// Cache Analytics and Compression Analytics both report totals, breakdowns and a
// trend, and all three have to answer the same question — "which rows are in
// scope" — or the cards on one page describe different periods. The Overview
// already had a resolver of its own (`resolveUsagePeriod` in
// `internal/handlers`), but it is unexported there and it splits a window into
// two shapes: a `usageDaily` rollup for whole-day periods and a raw
// `usageHistory` scan for sub-day ones. These analytics sections read
// `usageHistory` directly and always bucket by hour, so one cutoff instant is
// the whole contract and the split would only be a second thing to keep true.
//
// The windows the dashboard offers are the presets in
// `web/src/components/analytics/types.ts`, plus the `<n>d` / `<n>h` forms the
// Overview has always accepted. An unrecognised value falls back to 24h rather
// than erroring: the SPA can be newer than the gateway it talks to, and a 400
// on an analytics card reads as a broken section rather than as a default.
package analyticsrange

import (
	"strconv"
	"strings"
	"time"
)

// maxWindowHours bounds every window, including the unbounded one. usageHistory
// holds one row per request and is never pruned, so `all` is genuinely
// open-ended; the ceiling only stops a typo'd period from asking for a scan
// that cannot finish. Ten years is far past any ledger this build has written.
//
// The bound also keeps the arithmetic below inside time.Duration: a ceiling near
// the 292-year limit makes now.Sub(cutoff) overflow rather than clamp, and the
// overflow reads back as a window far shorter than the one that was asked for.
const maxWindowHours = 10 * 365 * 24

// Window is a resolved time window. The zero value is the unbounded window, so
// a caller that never resolves one reads the whole ledger — which is what these
// analytics did before any window was plumbed through.
type Window struct {
	// cutoff is the inclusive lower bound of the window. The zero time means
	// unbounded.
	cutoff time.Time
	// hours is the window's width in hours, rounded up. Zero for unbounded.
	hours int
}

// Cutoff returns the inclusive lower bound, or the zero time for an unbounded
// window. Repo methods take this directly and treat zero as "no WHERE clause",
// so a caller cannot accidentally bound a window with a zero timestamp that
// matches nothing.
func (w Window) Cutoff() time.Time { return w.cutoff }

// Hours returns the window's width, rounded up to the hour. Trend charts
// bucket by hour, so a sub-hour window still yields at most one bucket and
// rounding keeps every caller from having to special-case zero.
func (w Window) Hours() int { return w.hours }

// Unbounded reports whether the window has no lower bound.
func (w Window) Unbounded() bool { return w.cutoff.IsZero() }

// Label names the window the way the dashboard's picker does, so a response can
// say which period produced it. An unrecognised period is reported verbatim
// rather than as its fallback, which is the only way a caller could notice the
// server answered a different window than the one it asked for.
func (w Window) Label(requested string) string {
	if w.Unbounded() {
		return "all"
	}
	return requested
}

// Resolve maps a period parameter onto a window.
//
// Accepted: today, 24h, 7d, 30d, 60d, all, and the <n>d / <n>h forms. `today`
// is anchored at local midnight rather than measured back 24 hours, so "Today"
// and "Last 24 hours" stay distinguishable in the label the operator picked.
//
// now is a parameter rather than read from the clock so a caller resolving
// several ranges for one response gets one instant.
func Resolve(raw string, now time.Time) Window {
	raw = strings.ToLower(strings.TrimSpace(raw))

	switch raw {
	case "", "24h":
		return since(now, 24*time.Hour)
	case "today":
		midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		return bounded(now, midnight)
	case "7d":
		return since(now, 7*24*time.Hour)
	case "30d":
		return since(now, 30*24*time.Hour)
	case "60d":
		return since(now, 60*24*time.Hour)
	case "all":
		return Window{}
	}

	if n, ok := count(raw, 'd'); ok {
		return since(now, time.Duration(n)*24*time.Hour)
	}
	if n, ok := count(raw, 'h'); ok {
		return since(now, time.Duration(n)*time.Hour)
	}

	// Unrecognised input falls back to the 24h default rather than erroring:
	// the SPA can be newer than the gateway it talks to.
	return since(now, 24*time.Hour)
}

// since builds a window ending at now.
func since(now time.Time, width time.Duration) Window {
	return bounded(now, now.Add(-width))
}

// bounded normalises a cutoff and measures the width against now. Rounding the
// cutoff down to the second is not cosmetic: the ledger stores RFC3339 with
// second precision, so a sub-second cutoff compares lexicographically against
// a row written in the same second and excludes it.
func bounded(now, cutoff time.Time) Window {
	cutoff = cutoff.UTC().Truncate(time.Second)
	hours := int(now.Sub(cutoff)/time.Hour) + 1
	if hours > maxWindowHours {
		hours = maxWindowHours
	}
	return Window{cutoff: cutoff, hours: hours}
}

// count parses the "<n><unit>" shorthand, accepting only a positive count with
// no sign and no decimal part. A count above the ceiling is clamped rather than
// rejected: a caller asking for a 99999h window wants "as far back as this
// goes", and an error would read as a broken page.
func count(raw string, unit byte) (int, bool) {
	if len(raw) < 2 || raw[len(raw)-1] != unit {
		return 0, false
	}
	digits := raw[:len(raw)-1]
	n, err := strconv.Atoi(digits)
	if err != nil || n <= 0 {
		return 0, false
	}
	if n > maxWindowHours {
		n = maxWindowHours
	}
	return n, true
}
