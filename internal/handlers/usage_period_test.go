package handlers

import (
	"testing"
	"time"
)

func TestResolveUsagePeriod(t *testing.T) {
	now := time.Date(2026, 10, 2, 15, 30, 0, 0, time.UTC)
	startOfDay := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		raw       string
		wantDaily bool
		wantDays  int
		wantSince time.Time
	}{
		{
			name:      "empty defaults to today",
			raw:       "",
			wantSince: startOfDay,
		},
		{
			name:      "today cuts at midnight",
			raw:       "today",
			wantSince: startOfDay,
		},
		{
			name:      "24h is a rolling window",
			raw:       "24h",
			wantSince: now.Add(-24 * time.Hour),
		},
		{
			name:      "a sub-day custom window stays on raw history",
			raw:       "12h",
			wantSince: now.Add(-12 * time.Hour),
		},
		{
			name:      "7d is a daily rollup",
			raw:       "7d",
			wantDaily: true,
			wantDays:  7,
		},
		{
			name:      "30d is a daily rollup",
			raw:       "30d",
			wantDaily: true,
			wantDays:  30,
		},
		{
			name:      "60d is a daily rollup",
			raw:       "60d",
			wantDaily: true,
			wantDays:  60,
		},
		{
			name:      "all is unbounded",
			raw:       "all",
			wantDaily: true,
			wantDays:  allUsageDays,
		},
		{
			name:      "an arbitrary day count is honoured",
			raw:       "14d",
			wantDaily: true,
			wantDays:  14,
		},
		{
			name:      "an arbitrary hour count is honoured",
			raw:       "90d",
			wantDaily: true,
			wantDays:  90,
		},
		{
			name:      "unknown input falls back to 7d rather than erroring",
			raw:       "last-tuesday",
			wantDaily: true,
			wantDays:  7,
		},
		{
			name:      "a zero day count is not a window",
			raw:       "0d",
			wantDaily: true,
			wantDays:  7,
		},
		{
			name:      "a negative day count is not a window",
			raw:       "-7d",
			wantDaily: true,
			wantDays:  7,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveUsagePeriod(tt.raw, now)
			if got.daily != tt.wantDaily {
				t.Fatalf("resolveUsagePeriod(%q).daily = %v, want %v", tt.raw, got.daily, tt.wantDaily)
			}
			if tt.wantDaily && got.days != tt.wantDays {
				t.Errorf("resolveUsagePeriod(%q).days = %d, want %d", tt.raw, got.days, tt.wantDays)
			}
			if !tt.wantDaily && !got.since.Equal(tt.wantSince) {
				t.Errorf("resolveUsagePeriod(%q).since = %s, want %s", tt.raw, got.since, tt.wantSince)
			}
		})
	}
}

// The custom <n>h form is what makes the selector customizable; before it only
// the named presets resolved, so any other value silently became 7 days.
func TestResolveUsagePeriod_CustomHourWindowIsNotSevenDays(t *testing.T) {
	now := time.Date(2026, 10, 2, 15, 30, 0, 0, time.UTC)
	got := resolveUsagePeriod("6h", now)

	if got.daily {
		t.Fatal("a 6h window must read raw history, not a 7-day rollup")
	}
	if want := now.Add(-6 * time.Hour); !got.since.Equal(want) {
		t.Errorf("since = %s, want %s", got.since, want)
	}
}