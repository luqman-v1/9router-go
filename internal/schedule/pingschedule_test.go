package schedule

import (
	"testing"
	"time"
)

func enabled(v bool) *bool { return &v }

// TestNormalizePingSchedule covers the eight cases the acceptance row names:
// a valid anchor, an invalid anchor, windowHours clamped at both ends,
// slotsPerDay above floor(24/windowHours), a bad timezone, plus the disabled
// and string-typed forms upstream also accepts.
func TestNormalizePingSchedule(t *testing.T) {
	tests := []struct {
		name         string
		in           *Input
		wantNil      bool
		wantAnchor   string
		wantWindow   float64
		wantSlots    int
		wantTimezone string
	}{
		{
			name:       "valid anchor and window are kept as given",
			in:         &Input{Enabled: enabled(true), Anchor: "09:30", Timezone: "UTC", WindowHours: 5.0, SlotsPerDay: 3},
			wantAnchor: "09:30", wantWindow: 5, wantSlots: 3, wantTimezone: "UTC",
		},
		{
			name:       "invalid anchor falls back to the default",
			in:         &Input{Enabled: enabled(true), Anchor: "25:99", Timezone: "UTC", WindowHours: 5.0, SlotsPerDay: 3},
			wantAnchor: DefaultAnchor, wantWindow: 5, wantSlots: 3, wantTimezone: "UTC",
		},
		{
			name:       "windowHours below the floor clamps to 0.5",
			in:         &Input{Enabled: enabled(true), Anchor: "09:30", Timezone: "UTC", WindowHours: 0.1, SlotsPerDay: 3},
			wantAnchor: "09:30", wantWindow: 0.5, wantSlots: 3, wantTimezone: "UTC",
		},
		{
			name:       "windowHours above the ceiling clamps to 24",
			in:         &Input{Enabled: enabled(true), Anchor: "09:30", Timezone: "UTC", WindowHours: 48.0, SlotsPerDay: 1},
			wantAnchor: "09:30", wantWindow: 24, wantSlots: 1, wantTimezone: "UTC",
		},
		{
			name: "slotsPerDay above floor(24/windowHours) clamps to that floor",
			// floor(24/5) = 4, so 99 must become 4.
			in:         &Input{Enabled: enabled(true), Anchor: "09:30", Timezone: "UTC", WindowHours: 5.0, SlotsPerDay: 99},
			wantAnchor: "09:30", wantWindow: 5, wantSlots: 4, wantTimezone: "UTC",
		},
		{
			name:       "slotsPerDay below one clamps to one",
			in:         &Input{Enabled: enabled(true), Anchor: "09:30", Timezone: "UTC", WindowHours: 5.0, SlotsPerDay: 0},
			wantAnchor: "09:30", wantWindow: 5, wantSlots: 1, wantTimezone: "UTC",
		},
		{
			name:       "an unusable timezone falls back rather than dropping the schedule",
			in:         &Input{Enabled: enabled(true), Anchor: "09:30", Timezone: "Mars/Olympus_Mons", WindowHours: 5.0, SlotsPerDay: 3},
			wantAnchor: "09:30", wantWindow: 5, wantSlots: 3, wantTimezone: LocalTimezone(),
		},
		{
			name:       "numeric strings are accepted like numbers",
			in:         &Input{Enabled: enabled(true), Anchor: "9:05", Timezone: "UTC", WindowHours: "6", SlotsPerDay: "2"},
			wantAnchor: "09:05", wantWindow: 6, wantSlots: 2, wantTimezone: "UTC",
		},
		{
			name:    "disabled schedule normalizes to nil",
			in:      &Input{Enabled: enabled(false), Anchor: "09:30", Timezone: "UTC"},
			wantNil: true,
		},
		{
			name:    "absent schedule normalizes to nil",
			in:      &Input{Anchor: "09:30", Timezone: "UTC"},
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Normalize(tt.in)
			if tt.wantNil {
				if got != nil {
					t.Fatalf("Normalize() = %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("Normalize() = nil, want a schedule")
			}
			if got.Anchor != tt.wantAnchor {
				t.Errorf("anchor = %q, want %q", got.Anchor, tt.wantAnchor)
			}
			if got.WindowHours != tt.wantWindow {
				t.Errorf("windowHours = %v, want %v", got.WindowHours, tt.wantWindow)
			}
			if got.SlotsPerDay != tt.wantSlots {
				t.Errorf("slotsPerDay = %d, want %d", got.SlotsPerDay, tt.wantSlots)
			}
			if got.Timezone != tt.wantTimezone {
				t.Errorf("timezone = %q, want %q", got.Timezone, tt.wantTimezone)
			}
			if !got.Enabled {
				t.Error("enabled = false on a normalized schedule")
			}
		})
	}
}

func TestParseAnchorRejectsMalformedForms(t *testing.T) {
	for _, anchor := range []string{"", "9", "0930", "9:3", "24:00", "09:60", "-1:30", "aa:bb", "9:30x", "+9:30"} {
		if _, _, ok := ParseAnchor(anchor); ok {
			t.Errorf("ParseAnchor(%q) accepted a malformed anchor", anchor)
		}
	}
	for anchor, want := range map[string]struct{ h, m int }{
		"09:30": {9, 30},
		"9:30":  {9, 30},
		"00:00": {0, 0},
		"23:59": {23, 59},
	} {
		h, m, ok := ParseAnchor(anchor)
		if !ok || h != want.h || m != want.m {
			t.Errorf("ParseAnchor(%q) = (%d,%d,%v), want (%d,%d,true)", anchor, h, m, ok, want.h, want.m)
		}
	}
}

// TestSlotsFollowTheAnchor pins the schedule the spec describes: 09:30 with a
// 5h window and 3 slots/day is 09:30 / 14:30 / 19:30, and the next day starts
// back at 09:30 rather than drifting 15h forward.
func TestSlotsFollowTheAnchor(t *testing.T) {
	sched := &Schedule{Enabled: true, Anchor: "09:30", Timezone: "UTC", WindowHours: 5, SlotsPerDay: 3}

	labels := sched.SlotLabels()
	want := []string{"09:30", "14:30", "19:30"}
	if len(labels) != len(want) {
		t.Fatalf("labels = %v, want %v", labels, want)
	}
	for i := range want {
		if labels[i] != want[i] {
			t.Errorf("labels[%d] = %q, want %q", i, labels[i], want[i])
		}
	}

	now := time.Date(2026, 10, 21, 9, 31, 0, 0, time.UTC)
	next := sched.NextSlot(now)
	if next == nil {
		t.Fatal("NextSlot returned nil immediately after a slot")
	}
	if wantAt := time.Date(2026, 10, 21, 14, 30, 0, 0, time.UTC); !next.At.Equal(wantAt) {
		t.Errorf("next slot = %s, want %s", next.At, wantAt)
	}

	// The day boundary re-aligns: at 20:00 the next slot is tomorrow's 09:30,
	// not 00:30 as a pure "add 5h" loop would say.
	late := time.Date(2026, 10, 21, 20, 0, 0, 0, time.UTC)
	tomorrow := sched.NextSlot(late)
	if tomorrow == nil {
		t.Fatal("NextSlot returned nil at 20:00")
	}
	if wantAt := time.Date(2026, 10, 22, 9, 30, 0, 0, time.UTC); !tomorrow.At.Equal(wantAt) {
		t.Errorf("next slot at 20:00 = %s, want the next day's anchor %s", tomorrow.At, wantAt)
	}
}

// TestDueSlotRespectsGrace pins the restart contract: a slot that came due
// while the process was down fires once inside the grace window and never after.
func TestDueSlotRespectsGrace(t *testing.T) {
	sched := &Schedule{Enabled: true, Anchor: "09:30", Timezone: "UTC", WindowHours: 5, SlotsPerDay: 3}
	anchor := time.Date(2026, 10, 21, 9, 30, 0, 0, time.UTC)

	if due := sched.DueSlot(anchor.Add(time.Minute)); due == nil {
		t.Fatal("DueSlot returned nil one minute after a slot")
	} else if !due.At.Equal(anchor) {
		t.Errorf("due slot = %s, want %s", due.At, anchor)
	}

	if due := sched.DueSlot(anchor.Add(SlotGrace + time.Minute)); due != nil {
		t.Errorf("DueSlot returned %s past the grace window, want nil", due.At)
	}

	if due := sched.DueSlot(anchor.Add(-time.Minute)); due != nil {
		t.Errorf("DueSlot returned %s before the slot was due, want nil", due.At)
	}
}

// TestSlotsUseTheScheduleTimezone pins that a non-UTC zone shifts the instant,
// which is the whole point of storing one.
func TestSlotsUseTheScheduleTimezone(t *testing.T) {
	sched := &Schedule{Enabled: true, Anchor: "09:30", Timezone: "Asia/Tokyo", WindowHours: 5, SlotsPerDay: 1}
	if _, ok := LoadLocation("Asia/Tokyo"); !ok {
		t.Skip("Asia/Tokyo is not resolvable in this environment")
	}

	now := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	next := sched.NextSlot(now)
	if next == nil {
		t.Fatal("NextSlot returned nil")
	}
	// 09:30 JST is 00:30 UTC.
	if want := time.Date(2026, 10, 21, 0, 30, 0, 0, time.UTC); !next.At.Equal(want) {
		t.Errorf("next slot = %s, want %s (09:30 Asia/Tokyo)", next.At, want)
	}
}

func TestNilScheduleIsSafe(t *testing.T) {
	var sched *Schedule
	if got := sched.Slots(time.Now()); got != nil {
		t.Errorf("nil schedule Slots() = %v, want nil", got)
	}
	if got := sched.DueSlot(time.Now()); got != nil {
		t.Errorf("nil schedule DueSlot() = %v, want nil", got)
	}
	if got := sched.NextSlot(time.Now()); got != nil {
		t.Errorf("nil schedule NextSlot() = %v, want nil", got)
	}
	if got := sched.SlotLabels(); got != nil {
		t.Errorf("nil schedule SlotLabels() = %v, want nil", got)
	}
}
