package schedule

import (
	"context"
	"testing"
	"time"
)

// TestSchedulerPurgesExpiredSlotsOnEveryTick pins the behaviour behind moving
// the expiry sweep out of claim.
//
// claim runs once per configured connection, so purging inside it walked the
// whole fired map once per connection — quadratic in connection count. The
// sweep now runs once at the top of Tick; the map must still shrink, and a
// key claimed in the same tick must survive it.
func TestSchedulerPurgesExpiredSlotsOnEveryTick(t *testing.T) {
	sched := &Schedule{Enabled: true, Anchor: "09:30", Timezone: "UTC", WindowHours: 5, SlotsPerDay: 3}
	now := time.Date(2026, 10, 21, 9, 31, 0, 0, time.UTC)

	s := NewScheduler(fixedConfig("claude", map[string]*Schedule{"conn-1": sched}),
		func(context.Context, string, string) error { return nil })
	s.Now = func() time.Time { return now }

	// A long-running gateway holds keys well past firedTTL.
	for i := 0; i < 50; i++ {
		s.fired[string(rune('a'+i%26))+string(rune('0'+i/26))] = now.Add(-3 * firedTTL)
	}
	if got := len(s.fired); got != 50 {
		t.Fatalf("seeded %d keys, want 50", got)
	}

	s.Tick(context.Background())

	if got := len(s.fired); got != 1 {
		t.Errorf("fired map holds %d keys after a tick, want 1 (only the slot claimed this tick)", got)
	}
}
