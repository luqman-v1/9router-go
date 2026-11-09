package schedule

import (
	"context"
	"testing"
	"time"
)

// A non-finite number must never reach the schedule's arithmetic.
//
// The value arrives untrusted: a dashboard write carries it in the payload,
// and a restored backup carries it in the settings blob. Go's
// strconv.ParseFloat accepts "NaN" and "Inf" where JavaScript's Number()
// does not, so the finiteness check upstream gets from Number.isFinite has
// to be made explicit here.
//
// Without it, slotsPerDay "NaN" became int(NaN) = MinInt64, and the slot
// list was built with a negative capacity — a panic inside the scheduler's
// tick, which is the goroutine the whole gateway's auto-ping depends on.
func TestNormalizeRejectsNonFiniteNumbers(t *testing.T) {
	cases := []struct {
		name        string
		windowHours any
		slotsPerDay any
		wantWindow  float64
		wantSlots   int
	}{
		{"NaN window", "NaN", 3, DefaultWindowHours, 3},
		{"NaN slots", 5.0, "NaN", 5, 3},
		{"Inf window", "Inf", 3, DefaultWindowHours, 3},
		{"Inf slots", 5.0, "Inf", 5, 3},
		{"-Inf slots", 5.0, "-Inf", 5, 3},
		{"numeric NaN window", math_NaN(), 3, DefaultWindowHours, 3},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sched := Normalize(&Input{
				Enabled:     enabled(true),
				Anchor:      "09:30",
				Timezone:    "UTC",
				WindowHours: tc.windowHours,
				SlotsPerDay: tc.slotsPerDay,
			})
			if sched == nil {
				t.Fatal("Normalize returned nil")
			}
			if sched.WindowHours != tc.wantWindow {
				t.Errorf("WindowHours = %v, want %v", sched.WindowHours, tc.wantWindow)
			}
			if sched.SlotsPerDay != tc.wantSlots {
				t.Errorf("SlotsPerDay = %d, want %d", sched.SlotsPerDay, tc.wantSlots)
			}
			// The counts must survive the calls that size a slice from them.
			if got := len(sched.SlotLabels()); got != tc.wantSlots {
				t.Errorf("len(SlotLabels()) = %d, want %d", got, tc.wantSlots)
			}
			if got := len(sched.Slots(time.Now())); got != 3*tc.wantSlots {
				t.Errorf("len(Slots()) = %d, want %d", got, 3*tc.wantSlots)
			}
		})
	}
}

// math_NaN is the float64 NaN literal, spelled through a helper so the case
// table above reads as data rather than as an expression.
func math_NaN() float64 {
	zero := 0.0
	return zero / zero
}

// A hand-built Schedule with a negative slot count must not panic the slot
// builders. Normalize refuses to produce one, but a Schedule can also be
// built by hand — a test, a future caller — and the panic it used to cause
// was reachable from the scheduler's tick.
func TestSlotBuildersSurviveNegativeCount(t *testing.T) {
	sched := &Schedule{
		Enabled:     true,
		Anchor:      "09:30",
		Timezone:    "UTC",
		WindowHours: 5,
		SlotsPerDay: -9223372036854775808,
	}
	now := time.Date(2026, 10, 21, 9, 31, 0, 0, time.UTC)

	// A panic here fails the test rather than taking the process down.
	if got := len(sched.SlotLabels()); got != 1 {
		t.Errorf("len(SlotLabels()) = %d, want 1 (clamped to the minimum)", got)
	}
	if got := len(sched.Slots(now)); got != 3 {
		t.Errorf("len(Slots()) = %d, want 3 (one slot per day over three days)", got)
	}
}

// The scheduler's own tick must survive a schedule it cannot use. A tick
// that panics kills the goroutine the gateway's auto-ping runs on, and with
// it every other connection's ping.
func TestSchedulerTickSurvivesUnusableSchedule(t *testing.T) {
	sched := &Schedule{
		Enabled:     true,
		Anchor:      "09:30",
		Timezone:    "UTC",
		WindowHours: 5,
		SlotsPerDay: -1,
	}
	called := 0
	s := NewScheduler(
		func(string) Config {
			return Config{Connections: map[string]bool{}, Schedules: map[string]*Schedule{"conn-1": sched}}
		},
		func(context.Context, string, string) error {
			called++
			return nil
		},
	)
	s.Now = func() time.Time { return time.Date(2026, 10, 21, 9, 31, 0, 0, time.UTC) }

	s.Tick(context.Background())
	t.Logf("ping calls = %d", called)
}
