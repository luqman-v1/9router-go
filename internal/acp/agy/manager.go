package agy

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Manager bounds concurrent agy-acp workers. Every knob is an explicit cap:
// the gateway targets a 1 vCPU box, so an unbounded queue or worker count is a
// memory leak with a schedule.
type Manager struct {
	mu       sync.Mutex
	sem      chan struct{}
	waiters  int
	maxWait  int
	waitTime time.Duration
}

// ManagerOptions configures the worker pool. Zero values pick safe defaults.
type ManagerOptions struct {
	MaxWorkers  int
	MaxWaiters  int
	WaitTimeout time.Duration
}

func NewManager(opts ManagerOptions) *Manager {
	max := opts.MaxWorkers
	if max <= 0 {
		max = 4
	}
	waiters := opts.MaxWaiters
	if waiters <= 0 {
		waiters = 32
	}
	wait := opts.WaitTimeout
	if wait <= 0 {
		wait = 30 * time.Second
	}
	return &Manager{sem: make(chan struct{}, max), maxWait: waiters, waitTime: wait}
}

// Acquire reserves a worker slot or fails fast when the bounded queue is full.
// The returned function must be called exactly once to release the slot.
func (m *Manager) Acquire(ctx context.Context) (func(), error) {
	m.mu.Lock()
	if m.waiters >= m.maxWait {
		m.mu.Unlock()
		return nil, fmt.Errorf("agy-acp: worker queue full")
	}
	m.waiters++
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.waiters--
		m.mu.Unlock()
	}()

	timer := time.NewTimer(m.waitTime)
	defer timer.Stop()
	select {
	case m.sem <- struct{}{}:
		return func() { <-m.sem }, nil
	case <-timer.C:
		return nil, fmt.Errorf("agy-acp: timed out waiting for a worker slot")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

var defaultManager = NewManager(ManagerOptions{})

// DefaultManager returns the process-wide bounded worker pool.
func DefaultManager() *Manager { return defaultManager }
