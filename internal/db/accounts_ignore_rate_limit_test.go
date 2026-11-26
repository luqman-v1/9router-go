package db

import (
	"testing"
	"time"
)

// The toggle must round-trip through the same blob the cooldown readers use,
// and a flagged connection must stop reporting the account park through
// ConnectionBlockedUntil while the quota cooldown itself stays readable for
// the log line.
func TestConnectionIgnoresRateLimit_RoundTrip(t *testing.T) {
	until := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	flagged := `{"apiKey":"sk-x","rateLimitedUntil":"` + until + `","ignoreRateLimit":true}`
	plain := `{"apiKey":"sk-x","rateLimitedUntil":"` + until + `"}`

	if !ConnectionIgnoresRateLimit(flagged) {
		t.Error("ConnectionIgnoresRateLimit = false, want true for the flagged blob")
	}
	if ConnectionIgnoresRateLimit(plain) {
		t.Error("ConnectionIgnoresRateLimit = true, want false for an unflagged blob")
	}
	if ConnectionIgnoresRateLimit("") || ConnectionIgnoresRateLimit(`{`) {
		t.Error("ConnectionIgnoresRateLimit must fail closed on an unreadable blob")
	}

	// The flag shortens exactly the account park: a flagged connection reads
	// as selectable while the underlying cooldown is still recorded.
	if _, blocked := ConnectionBlockedUntil(flagged); blocked {
		t.Error("ConnectionBlockedUntil = blocked, want not blocked for the flagged blob")
	}
	if _, ok := ConnectionAccountCooldownUntil(flagged); !ok {
		t.Error("ConnectionAccountCooldownUntil missing, want the parked window for the flagged blob")
	}
	if _, blocked := ConnectionBlockedUntil(plain); !blocked {
		t.Error("ConnectionBlockedUntil = not blocked, want blocked for the unflagged blob")
	}
}
