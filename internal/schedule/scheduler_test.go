package schedule

import (
	"context"
	"errors"
	"testing"
	"time"
)

func fixedConfig(provider string, schedules map[string]*Schedule) ConfigSource {
	return func(p string) Config {
		if p != provider {
			return Config{Connections: map[string]bool{}, Schedules: map[string]*Schedule{}}
		}
		return Config{Connections: map[string]bool{}, Schedules: schedules}
	}
}

// TestSchedulerFiresOncePerSlot pins the dedupe contract: two ticks inside the
// same grace window produce one ping, not two.
func TestSchedulerFiresOncePerSlot(t *testing.T) {
	sched := &Schedule{Enabled: true, Anchor: "09:30", Timezone: "UTC", WindowHours: 5, SlotsPerDay: 3}
	now := time.Date(2026, 10, 21, 9, 31, 0, 0, time.UTC)

	var pings []string
	s := NewScheduler(fixedConfig("claude", map[string]*Schedule{"conn-1": sched}), func(_ context.Context, provider, connID string) error {
		pings = append(pings, provider+":"+connID)
		return nil
	})
	s.Now = func() time.Time { return now }

	s.Tick(context.Background())
	s.Tick(context.Background())

	if len(pings) != 1 || pings[0] != "claude:conn-1" {
		t.Errorf("pings = %v, want exactly one for claude:conn-1", pings)
	}
}

// TestSchedulerSkipsOutsideGrace pins that a tick that lands well after the
// slot does not fire it: a process starting at noon must not ping for 09:30.
func TestSchedulerSkipsOutsideGrace(t *testing.T) {
	sched := &Schedule{Enabled: true, Anchor: "09:30", Timezone: "UTC", WindowHours: 5, SlotsPerDay: 3}

	var pings int
	s := NewScheduler(fixedConfig("claude", map[string]*Schedule{"conn-1": sched}), func(context.Context, string, string) error {
		pings++
		return nil
	})
	s.Now = func() time.Time { return time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC) }

	s.Tick(context.Background())
	if pings != 0 {
		t.Errorf("pings = %d at noon, want 0 (09:30 is past the grace window)", pings)
	}
}

// TestSchedulerReportsPingErrors pins that a failed ping is returned rather
// than swallowed, and that it is not retried on the next tick.
func TestSchedulerReportsPingErrors(t *testing.T) {
	sched := &Schedule{Enabled: true, Anchor: "09:30", Timezone: "UTC", WindowHours: 5, SlotsPerDay: 3}
	now := time.Date(2026, 10, 21, 9, 31, 0, 0, time.UTC)

	calls := 0
	s := NewScheduler(fixedConfig("claude", map[string]*Schedule{"conn-1": sched}), func(context.Context, string, string) error {
		calls++
		return errors.New("upstream refused")
	})
	s.Now = func() time.Time { return now }

	if errs := s.Tick(context.Background()); len(errs) != 1 {
		t.Fatalf("Tick returned %d errors, want 1", len(errs))
	}
	if errs := s.Tick(context.Background()); len(errs) != 0 {
		t.Errorf("second Tick returned %d errors, want 0 (the slot already fired)", len(errs))
	}
	if calls != 1 {
		t.Errorf("ping called %d times, want 1", calls)
	}
}

// TestSchedulerReadsSchedulePerTick pins that the configuration is re-read on
// every tick: a schedule saved in the dashboard must take effect without a
// restart.
func TestSchedulerReadsSchedulePerTick(t *testing.T) {
	now := time.Date(2026, 10, 21, 9, 31, 0, 0, time.UTC)
	current := map[string]*Schedule{}

	var pings int
	s := NewScheduler(func(provider string) Config {
		if provider != "claude" {
			return Config{Connections: map[string]bool{}, Schedules: map[string]*Schedule{}}
		}
		return Config{Connections: map[string]bool{}, Schedules: current}
	}, func(context.Context, string, string) error {
		pings++
		return nil
	})
	s.Now = func() time.Time { return now }

	s.Tick(context.Background())
	if pings != 0 {
		t.Fatalf("pings = %d with no schedule configured, want 0", pings)
	}

	current = map[string]*Schedule{
		"conn-1": {Enabled: true, Anchor: "09:30", Timezone: "UTC", WindowHours: 5, SlotsPerDay: 3},
	}
	s.Tick(context.Background())
	if pings != 1 {
		t.Errorf("pings = %d after adding a schedule, want 1", pings)
	}
}

// TestSchedulerNilSafety keeps a scheduler with no ping or no source from
// panicking: the loop starts even when the feature is unused.
func TestSchedulerNilSafety(t *testing.T) {
	var s *Scheduler
	if errs := s.Tick(context.Background()); errs != nil {
		t.Errorf("nil scheduler Tick = %v, want nil", errs)
	}
	empty := NewScheduler(nil, nil)
	if errs := empty.Tick(context.Background()); errs != nil {
		t.Errorf("scheduler with no source Tick = %v, want nil", errs)
	}
}
