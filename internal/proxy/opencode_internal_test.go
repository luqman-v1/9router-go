package proxy

import (
	"fmt"
	"testing"
	"time"
)

func TestStableOpenCodeSessionIDRecyclesAfterTTL(t *testing.T) {
	previous := opencodeStableSessionTTL
	opencodeStableSessionTTL = time.Millisecond
	t.Cleanup(func() { opencodeStableSessionTTL = previous })

	first := StableOpenCodeSessionID("ttl-key")
	time.Sleep(5 * time.Millisecond)
	second := StableOpenCodeSessionID("ttl-key")
	if first == second {
		t.Errorf("stale session %q was reused after the TTL", first)
	}
	if !IsCanonicalOpenCodeSession(second) {
		t.Errorf("recycled session %q is not canonical", second)
	}
}

func TestStableOpenCodeSessionIDBounded(t *testing.T) {
	for i := range opencodeStableSessionLimit + 1 {
		StableOpenCodeSessionID(fmt.Sprintf("bound-%d", i))
	}
	opencodeStableMu.Lock()
	size := len(opencodeStableSessions)
	opencodeStableMu.Unlock()
	if size > opencodeStableSessionLimit {
		t.Errorf("stable session map grew to %d entries, limit %d", size, opencodeStableSessionLimit)
	}
}
