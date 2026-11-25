package handlers

import (
	"testing"
	"time"
)

// A fixed instant in the middle of a month, so a month-to-date window and a
// rolling 30-day window differ by a known amount.
var monthTestNow = time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

func TestResolveUsagePeriodMonth(t *testing.T) {
	got := resolveUsagePeriod("month", monthTestNow)

	want := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	if !got.since.Equal(want) {
		t.Errorf("since = %v, want the 1st at local midnight (%v)", got.since, want)
	}
	if got.daily {
		t.Error("a month-to-date window must read raw history: the daily rollup cannot express the part of today inside it")
	}
	// The window is anchored, not sliding: every poll in the month must share
	// one cache key or the aggregate cache would miss on every request.
	if key := got.shape.key(); key != "anchored:"+want.Format(time.RFC3339) {
		t.Errorf("shape key = %q, want the anchored month start", key)
	}
}

func TestMonthDiffersFromThirtyDays(t *testing.T) {
	month := resolveUsagePeriod("month", monthTestNow)
	thirty := resolveUsagePeriod("30d", monthTestNow)

	if month.since.Equal(thirty.since) {
		t.Fatal("month and 30d must resolve to different windows, or the preset means nothing")
	}
	// On the 10th the month opened 9 days ago, so the month window is narrower
	// than a rolling 30 days — the opposite of what a naive "month = 30d"
	// mapping would produce.
	if !month.since.After(thirty.since) {
		t.Errorf("month start %v must be later than the 30d start %v on the 10th", month.since, thirty.since)
	}
}

// The 1st is the boundary that a naive "truncate to the day" implementation
// gets wrong: at 00:30 the month opened 30 minutes ago, not 24 hours ago.
func TestMonthOnTheFirstStartsAtMidnight(t *testing.T) {
	now := time.Date(2026, 4, 1, 0, 30, 0, 0, time.UTC)
	got := resolveUsagePeriod("month", now)

	want := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	if !got.since.Equal(want) {
		t.Errorf("since = %v, want %v", got.since, want)
	}
}

func TestDetailsSinceMonth(t *testing.T) {
	got := detailsSince("month", monthTestNow)
	want := "2026-03-01T00:00:00Z"
	if got != want {
		t.Errorf("detailsSince(month) = %q, want %q", got, want)
	}
}

func TestParseChartPeriodMonth(t *testing.T) {
	kind, count := parseChartPeriod("month")
	if kind != "month" {
		t.Errorf("kind = %q, want its own kind — reusing the day count 0 would mean the whole ledger", kind)
	}
	if count != 0 {
		t.Errorf("count = %d, want 0 (the caller derives the day count from the month anchor)", count)
	}
}

func TestAnalyticsRangeMonth(t *testing.T) {
	// The analytics sections resolve through their own helper; it must agree, or
	// the cards on one page would describe different periods.
	since := resolveUsagePeriod("month", monthTestNow)
	if since.since.Day() != 1 || since.since.Hour() != 0 {
		t.Errorf("month start = %v, want the 1st at midnight", since.since)
	}
}
