package schedule

import (
	"context"
	"sync"
	"time"
)

// Tick interval matches upstream QUOTA_AUTOPING_CONFIG.tickIntervalMs.
const DefaultTickInterval = time.Minute

// PingFunc fires one ping for a connection. The scheduler owns *when*; the
// caller owns *how*, because sending a provider-specific request is a much
// larger surface than deciding it is due and belongs next to the executors.
type PingFunc func(ctx context.Context, provider, connectionID string) error

// ConfigSource returns the current auto-ping configuration for one provider.
// It is read every tick rather than cached, so a schedule saved in the
// dashboard takes effect on the next tick without a restart.
type ConfigSource func(provider string) Config

// Scheduler fires connection pings at their configured wall-clock slots.
//
// One slot fires once. The dedupe key is the slot's own "<date>#<index>", not
// the wall clock, so a tick that lands twice inside the grace window cannot
// ping twice — and a restart inside the window still fires the slot that came
// due while the process was down, exactly once.
type Scheduler struct {
	Interval time.Duration
	Source   ConfigSource
	Ping     PingFunc
	Now      func() time.Time

	mu      sync.Mutex
	fired   map[string]time.Time
	running bool
}

// firedTTL is how long a slot key is remembered. A slot is only reachable
// from the three-day window DueSlot walks, so two days of history is more than
// enough; keeping it bounded stops the map from growing for the life of the
// process on a long-running gateway.
const firedTTL = 48 * time.Hour

// NewScheduler builds a scheduler with the production tick interval.
func NewScheduler(source ConfigSource, ping PingFunc) *Scheduler {
	return &Scheduler{
		Interval: DefaultTickInterval,
		Source:   source,
		Ping:     ping,
		Now:      time.Now,
		fired:    map[string]time.Time{},
	}
}

// Tick runs one pass: for every configured connection whose next-due slot has
// not fired yet, call Ping once and mark that slot.
//
// A ping error is reported and the slot stays marked: retrying a failed ping
// on every tick for the whole grace window would hammer a provider that just
// refused, and the operator sees the log line.
func (s *Scheduler) Tick(ctx context.Context) []error {
	if s == nil || s.Source == nil {
		return nil
	}
	now := s.now()

	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return nil
	}
	s.running = true
	s.purgeFiredLocked(now)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()

	var errs []error
	for _, provider := range Providers {
		cfg := s.Source(provider)
		for connID, sched := range cfg.Schedules {
			if sched == nil {
				continue
			}
			slot := sched.DueSlot(now)
			if slot == nil || s.Ping == nil {
				continue
			}
			key := provider + ":" + connID + ":" + slot.Key
			if s.claim(key, now) {
				continue
			}
			if err := s.Ping(ctx, provider, connID); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errs
}

// claim reports whether the key already fired, marking it when it had not.
func (s *Scheduler) claim(key string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if at, ok := s.fired[key]; ok && now.Sub(at) <= firedTTL {
		return true
	}
	s.fired[key] = now
	return false
}

// purgeFiredLocked drops expired entries. It runs once at the top of Tick
// rather than inside claim: claim is called per connection, so purging there
// made one tick's bookkeeping quadratic in the number of connections.
func (s *Scheduler) purgeFiredLocked(now time.Time) {
	for k, at := range s.fired {
		if now.Sub(at) > firedTTL {
			delete(s.fired, k)
		}
	}
}

// Run ticks until ctx is cancelled. The first tick runs immediately so a
// process that starts inside a grace window fires the slot it missed.
func (s *Scheduler) Run(ctx context.Context) {
	interval := s.Interval
	if interval <= 0 {
		interval = DefaultTickInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	s.Tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Tick(ctx)
		}
	}
}

func (s *Scheduler) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
