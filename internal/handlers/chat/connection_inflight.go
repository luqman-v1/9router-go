package chat

import (
	"os"
	"strconv"
	"strings"
	"sync"
)

// Per-account in-flight ceiling for the providers whose gateway turns a burst
// into a risk verdict.
//
// CodeBuddy/WorkBuddy answer a per-model throttle with `6004` and an account
// restriction with `11140`. The step between them is concurrency: several
// simultaneous requests on one credential look like automation, and every
// project that has run this channel long enough caps it per account rather than
// per provider — workbuddy2api at 2, Workbuddy-WB2api at 3 (their
// ADMIN_POOL_MAX_IN_FLIGHT default). A provider-wide cap is not the same thing:
// with five accounts it still allows five concurrent attempts on one of them.
//
// The gate refuses instead of queueing, so a request that would exceed the
// ceiling is routed to the next account without any upstream call being spent.
// That mirrors the pool behaviour those projects document ("Acquire 拒绝的请求
// 不产生上游调用").
const (
	defaultAccountInFlight = 3
	accountInFlightEnv     = "CODEBUDDY_MAX_IN_FLIGHT"
)

// accountGatedProviders are the lanes the ceiling applies to. Other providers
// keep their current behaviour: this is a risk-control measure for one channel,
// not a global transport limit.
func accountGatedProvider(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "codebuddy-intl", "codebuddy-cn", "cbai", "cbcn":
		return true
	}
	return false
}

// accountInFlightLimit reads the ceiling once per call so an operator can change
// it without a rebuild. Zero or a non-numeric value disables the gate.
func accountInFlightLimit(provider string) int {
	if !accountGatedProvider(provider) {
		return 0
	}
	raw := strings.TrimSpace(os.Getenv(accountInFlightEnv))
	if raw == "" {
		return defaultAccountInFlight
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return defaultAccountInFlight
	}
	return n
}

// connectionGate counts in-flight attempts per (provider, connection).
type connectionGate struct {
	mu   sync.Mutex
	used map[string]int
}

var chatConnectionGate = &connectionGate{used: map[string]int{}}

// acquireConnectionSlot reserves one attempt against the account's ceiling.
// A busy account returns nil, true: the caller skips it and spends nothing
// upstream. The returned release is idempotent so a double defer cannot free a
// slot twice.
func (g *connectionGate) acquire(key string, limit int) (func(), bool) {
	if limit <= 0 {
		return func() {}, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.used[key] >= limit {
		return nil, true
	}
	g.used[key]++
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			if g.used[key] > 0 {
				g.used[key]--
				if g.used[key] == 0 {
					delete(g.used, key)
				}
			}
		})
	}, false
}

// inFlight reports the observed count, for tests and diagnostics.
func (g *connectionGate) inFlight(key string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.used[key]
}

// acquireConnectionSlot is the handler-facing entry point.
func acquireConnectionSlot(provider, connID string) (func(), bool) {
	if connID == "" {
		return func() {}, false
	}
	return chatConnectionGate.acquire(provider+"|"+connID, accountInFlightLimit(provider))
}
