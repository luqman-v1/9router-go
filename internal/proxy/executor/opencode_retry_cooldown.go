package executor

import (
	"sync"
	"time"
)

// openCodeRetryCooldown is how long a refusal suppresses the replay after a
// full-shape retry was itself refused.
//
// Upstream's penalty is per egress IP, so replaying on the next request cannot
// succeed and only deepens the penalty. A short window keeps a genuinely
// transient refusal recoverable (its retry still runs on the first request)
// while stopping every subsequent request from re-burning a useless call.
//
// A var so tests can shrink it.
var openCodeRetryCooldown = 30 * time.Second

var (
	openCodeRetryCooldownMu   sync.Mutex
	openCodeRetryRefusedUntil time.Time
)

// openCodeRetryCoolingDown reports whether a refused replay recently failed, so
// the replay must be skipped. Expiry is lazy (checked on read), so no
// background goroutine runs for this — the same pattern as the stable-session
// cache in internal/proxy/opencode.go.
func openCodeRetryCoolingDown(now time.Time) bool {
	openCodeRetryCooldownMu.Lock()
	defer openCodeRetryCooldownMu.Unlock()
	return now.Before(openCodeRetryRefusedUntil)
}

// noteOpenCodeRetryRefused records that a replay was refused, starting the
// cooldown window. It extends an existing window rather than shortening it, so
// concurrent refusals cannot race the deadline backwards.
func noteOpenCodeRetryRefused(now time.Time) {
	openCodeRetryCooldownMu.Lock()
	defer openCodeRetryCooldownMu.Unlock()
	if until := now.Add(openCodeRetryCooldown); until.After(openCodeRetryRefusedUntil) {
		openCodeRetryRefusedUntil = until
	}
}

// resetOpenCodeRetryCooldownForTest clears the cooldown. Test-only seam: the
// state is process-global, so without it a test that ends on a refused replay
// leaks the window into the next one, which then fails on the cooldown rather
// than on the behaviour it asserts. Mirrors resetFreePiCooldownsForTest.
func resetOpenCodeRetryCooldownForTest() {
	openCodeRetryCooldownMu.Lock()
	defer openCodeRetryCooldownMu.Unlock()
	openCodeRetryRefusedUntil = time.Time{}
}

// ResetOpenCodeRetryCooldownForTesting clears the refusal-retry cooldown.
//
// Exported for the integration suite, which boots the real router in another
// package and therefore cannot reach the unexported seam. The cooldown is
// process-global, so a test that runs after one which ended on a refused replay
// would otherwise start inside a live window and assert the wrong attempt count.
// Production code must never call this.
func ResetOpenCodeRetryCooldownForTesting() {
	resetOpenCodeRetryCooldownForTest()
}
