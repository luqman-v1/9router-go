package analyticsrange

import (
	"testing"
	"time"
)

func TestResolveMonthIsMonthToDate(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	got := Resolve("month", now)
	want := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	if !got.Cutoff().Equal(want) {
		t.Errorf("cutoff = %v, want the 1st at local midnight (%v)", got.Cutoff(), want)
	}
	if got.Unbounded() {
		t.Error("month must be bounded")
	}
	// The 1st at midnight to the 10th at noon is nine and a half days, rounded
	// up to the hour by the resolver.
	if got.Hours() != 9*24+12+1 {
		t.Errorf("hours = %d, want %d", got.Hours(), 9*24+12+1)
	}
}

func TestResolveMonthOnTheFirst(t *testing.T) {
	now := time.Date(2026, 4, 1, 0, 30, 0, 0, time.UTC)

	got := Resolve("month", now)
	want := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	if !got.Cutoff().Equal(want) {
		t.Errorf("cutoff = %v, want %v (30 minutes, not a day)", got.Cutoff(), want)
	}
}

func TestResolveMonthLabelPreserved(t *testing.T) {
	// The label is what lets a caller notice the server answered a different
	// window than the one asked for, so a recognised period keeps its name.
	got := Resolve("month", time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC))
	if label := got.Label("month"); label != "month" {
		t.Errorf("label = %q, want month", label)
	}
}
