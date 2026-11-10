package dashboard

// The served-creative table. Kept apart from the ad HTTP client because it is
// the mechanism that makes an impression verifiable, not part of talking to the
// ad network.

import (
	"sync"
	"time"
)

// freePiPendingAd is a creative handed to a client and not yet reported on.
type freePiPendingAd struct {
	ad        freePiAd
	expiresAt time.Time
}

var (
	freePiAdMu    sync.Mutex
	freePiPending = make(map[string]freePiPendingAd, freePiAdEntries)
)

// freePiStorePendingAd records a served creative, evicting expired entries and
// the oldest if still at capacity.
func freePiStorePendingAd(connID string, ad freePiAd) {
	freePiAdMu.Lock()
	defer freePiAdMu.Unlock()

	now := time.Now()
	for k, v := range freePiPending {
		if now.After(v.expiresAt) {
			delete(freePiPending, k)
		}
	}
	if _, exists := freePiPending[connID]; !exists && len(freePiPending) >= freePiAdEntries {
		var oldestKey string
		var oldest time.Time
		for k, v := range freePiPending {
			if oldestKey == "" || v.expiresAt.Before(oldest) {
				oldestKey, oldest = k, v.expiresAt
			}
		}
		delete(freePiPending, oldestKey)
	}
	freePiPending[connID] = freePiPendingAd{ad: ad, expiresAt: now.Add(freePiAdTTL)}
}

// freePiTakePendingAd consumes a pending creative, returning it only when it
// matches adID and has not expired. Consuming on read is what makes a second
// report for the same ad impossible: one serving, one impression.
func freePiTakePendingAd(connID, adID string) (freePiAd, bool) {
	freePiAdMu.Lock()
	defer freePiAdMu.Unlock()

	pending, ok := freePiPending[connID]
	if !ok {
		return freePiAd{}, false
	}
	if time.Now().After(pending.expiresAt) {
		delete(freePiPending, connID)
		return freePiAd{}, false
	}
	if pending.ad.AdID != adID {
		// A stale id from a previous ad: leave the live one in place.
		return freePiAd{}, false
	}
	delete(freePiPending, connID)
	return pending.ad, true
}

// freePiResetAdsForTest clears the served-creative table.
func freePiResetAdsForTest() {
	freePiAdMu.Lock()
	defer freePiAdMu.Unlock()
	freePiPending = make(map[string]freePiPendingAd, freePiAdEntries)
}
