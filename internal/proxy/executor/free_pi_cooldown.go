package executor

import (
	"sync"
	"time"
)

// --- reset cooldown -------------------------------------------------------

type freePiCooldownEntry struct {
	at time.Time
}

var (
	freePiCooldownMu sync.Mutex
	freePiCooldowns  = make(map[string]freePiCooldownEntry, freePiCooldownEntries)
)

// freePiResetAllowed reports whether an automatic reset may run now, and
// records the attempt when it may. Recording at decision time (not after the
// call) means a failing reset still counts against the cooldown, so a broken
// upstream cannot be hammered.
func freePiResetAllowed(accountKey string, now time.Time) bool {
	freePiCooldownMu.Lock()
	defer freePiCooldownMu.Unlock()

	freePiPruneCooldownsLocked(now)

	if e, ok := freePiCooldowns[accountKey]; ok && now.Sub(e.at) < freePiResetCooldown {
		return false
	}
	freePiCooldowns[accountKey] = freePiCooldownEntry{at: now}
	return true
}

// freePiPruneCooldownsLocked drops expired entries so the map cannot grow
// without bound. Callers must hold freePiCooldownMu.
func freePiPruneCooldownsLocked(now time.Time) {
	if len(freePiCooldowns) < freePiCooldownEntries {
		for k, e := range freePiCooldowns {
			if now.Sub(e.at) >= freePiResetCooldown {
				delete(freePiCooldowns, k)
			}
		}
		return
	}
	// At the cap: expire everything stale, and if that was not enough, drop the
	// oldest entry. Forgetting one cooldown can at worst allow one extra reset
	// for an account that will not be seen again soon.
	for k, e := range freePiCooldowns {
		if now.Sub(e.at) >= freePiResetCooldown {
			delete(freePiCooldowns, k)
		}
	}
	if len(freePiCooldowns) >= freePiCooldownEntries {
		var oldestKey string
		var oldest time.Time
		for k, e := range freePiCooldowns {
			if oldestKey == "" || e.at.Before(oldest) {
				oldestKey, oldest = k, e.at
			}
		}
		delete(freePiCooldowns, oldestKey)
	}
}

// resetFreePiCooldownsForTest clears the cooldown bookkeeping. Test-only seam:
// the map is process-global, so tests would otherwise leak state into each
// other and start failing on the cooldown rather than the behaviour.
func resetFreePiCooldownsForTest() {
	freePiCooldownMu.Lock()
	defer freePiCooldownMu.Unlock()
	freePiCooldowns = make(map[string]freePiCooldownEntry, freePiCooldownEntries)
}
