package handlers

import (
	"testing"
	"time"
)

// TestDetailsSince pins the ?period mapping the Details tab relies on. The
// resolver is shared with /api/usage/stats on purpose: if the two disagreed,
// "Last 7 days" would mean one window in the KPI row and another in the table
// below it, with nothing on screen saying so.
func TestDetailsSince(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		period string
		want   string
	}{
		{name: "absent parameter is unbounded", period: "", want: ""},
		{name: "all is unbounded", period: "all", want: ""},
		{name: "today starts at midnight", period: "today", want: "2026-03-10T00:00:00Z"},
		{name: "24h counts back from now", period: "24h", want: "2026-03-09T12:00:00Z"},
		{name: "a custom hour window", period: "6h", want: "2026-03-10T06:00:00Z"},
		{name: "a custom day window", period: "7d", want: "2026-03-03T12:00:00Z"},
		{name: "60d matches the preset", period: "60d", want: "2026-01-09T12:00:00Z"},
		{name: "an unknown value follows the stats fallback", period: "nonsense", want: "2026-03-03T12:00:00Z"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detailsSince(tt.period, now); got != tt.want {
				t.Errorf("detailsSince(%q) = %q, want %q", tt.period, got, tt.want)
			}
		})
	}
}
