package analyticsrange

import (
	"testing"
	"time"
)

func TestResolve(t *testing.T) {
	now := time.Date(2026, 10, 10, 15, 30, 0, 0, time.UTC)

	tests := []struct {
		name       string
		raw        string
		wantCutoff time.Time
		wantHours  int
		unbounded  bool
	}{
		{
			name:       "empty falls back to 24h",
			raw:        "",
			wantCutoff: now.Add(-24 * time.Hour),
			wantHours:  25,
		},
		{
			name:       "24h",
			raw:        "24h",
			wantCutoff: now.Add(-24 * time.Hour),
			wantHours:  25,
		},
		{
			name:       "7d",
			raw:        "7d",
			wantCutoff: now.Add(-7 * 24 * time.Hour),
			wantHours:  7*24 + 1,
		},
		{
			name:       "30d",
			raw:        "30d",
			wantCutoff: now.Add(-30 * 24 * time.Hour),
			wantHours:  30*24 + 1,
		},
		{
			name:       "60d",
			raw:        "60d",
			wantCutoff: now.Add(-60 * 24 * time.Hour),
			wantHours:  60*24 + 1,
		},
		{
			name:      "all is unbounded",
			raw:       "all",
			unbounded: true,
		},
		{
			name:       "custom hours",
			raw:        "12h",
			wantCutoff: now.Add(-12 * time.Hour),
			wantHours:  13,
		},
		{
			name:       "custom days",
			raw:        "14d",
			wantCutoff: now.Add(-14 * 24 * time.Hour),
			wantHours:  14*24 + 1,
		},
		{
			name:       "unknown falls back to 24h",
			raw:        "last-tuesday",
			wantCutoff: now.Add(-24 * time.Hour),
			wantHours:  25,
		},
		{
			// A zero or negative count is not a window; answering it with the
			// default beats answering it with the whole ledger.
			name:       "zero days is not a window",
			raw:        "0d",
			wantCutoff: now.Add(-24 * time.Hour),
			wantHours:  25,
		},
		{
			name:       "negative days is not a window",
			raw:        "-3d",
			wantCutoff: now.Add(-24 * time.Hour),
			wantHours:  25,
		},
		{
			name:       "fractional days is not a window",
			raw:        "1.5d",
			wantCutoff: now.Add(-24 * time.Hour),
			wantHours:  25,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Resolve(tt.raw, now)
			if got.Unbounded() != tt.unbounded {
				t.Fatalf("Unbounded() = %v, want %v", got.Unbounded(), tt.unbounded)
			}
			if tt.unbounded {
				if !got.Cutoff().IsZero() {
					t.Errorf("Cutoff() = %v, want the zero time", got.Cutoff())
				}
				if got.Hours() != 0 {
					t.Errorf("Hours() = %d, want 0", got.Hours())
				}
				if label := got.Label(tt.raw); label != "all" {
					t.Errorf("Label() = %q, want \"all\"", label)
				}
				return
			}
			if !got.Cutoff().Equal(tt.wantCutoff) {
				t.Errorf("Cutoff() = %v, want %v", got.Cutoff(), tt.wantCutoff)
			}
			if got.Hours() != tt.wantHours {
				t.Errorf("Hours() = %d, want %d", got.Hours(), tt.wantHours)
			}
		})
	}
}

// "Today" and "Last 24 hours" are different windows and the picker labels them
// differently, so one must not silently resolve to the other.
func TestResolve_TodayIsAnchoredNotSliding(t *testing.T) {
	now := time.Date(2026, 10, 10, 15, 30, 0, 0, time.UTC)

	today := Resolve("today", now)
	want := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	if !today.Cutoff().Equal(want) {
		t.Errorf("today cutoff = %v, want %v", today.Cutoff(), want)
	}

	sliding := Resolve("24h", now)
	if today.Cutoff().Equal(sliding.Cutoff()) {
		t.Error("today resolved to the same instant as 24h; the two are different windows")
	}
}

// The cutoff is compared as a string against RFC3339 rows, so a sub-second
// component would exclude a row written in the same second the window opened.
func TestResolve_TruncatesToTheStoredPrecision(t *testing.T) {
	now := time.Date(2026, 10, 10, 15, 30, 0, 987654321, time.UTC)

	win := Resolve("24h", now)
	if got := win.Cutoff().Nanosecond(); got != 0 {
		t.Errorf("cutoff carries %d ns; the ledger stores whole seconds", got)
	}
}

// A typo'd window must not ask for a scan that cannot finish.
func TestResolve_ClampsAbsurdWindows(t *testing.T) {
	now := time.Date(2026, 10, 10, 15, 30, 0, 0, time.UTC)

	win := Resolve("99999h", now)
	if win.Unbounded() {
		t.Fatal("a finite request must not resolve to the unbounded window")
	}
	if got := win.Hours(); got != maxWindowHours {
		t.Errorf("Hours() = %d, want the clamp at %d", got, maxWindowHours)
	}
}
