package executor

import (
	"fmt"

	"9router/proxy/internal/db"
)

// --- lease ----------------------------------------------------------------

// freePiAcquireLease claims the account for this process. A false result is not
// an error and must not stop the request: it only means a sibling currently
// owns the right to reset, so this request must not reset on a 409.
func freePiAcquireLease(store LeaseStore, accountKey string) bool {
	if store == nil {
		// Single-process mode: memory only, and there is no sibling to
		// coordinate with, so we are trivially the holder.
		return true
	}
	ok, err := store.AcquireLease(freePiLeaseScope, db.LeaseKey(accountKey), freePiProcessEpoch, freePiLeaseTTL)
	if err != nil {
		// A lease backend failure must never break a completion; fall back to
		// assuming we hold it, which at worst re-enables the pre-lease
		// behaviour of resetting on 409.
		return true
	}
	return ok
}

// freePiLeaseHolder returns the epoch currently holding the account, or "" when
// no live holder exists.
func freePiLeaseHolder(store LeaseStore, accountKey string) string {
	if store == nil {
		return ""
	}
	lease, err := store.ReadLease(freePiLeaseScope, db.LeaseKey(accountKey))
	if err != nil || lease == nil {
		return ""
	}
	return lease.Value
}

// freePiReleaseLease drops our claim, but only while it is still ours
// (compare-and-delete): a sibling that took over after our TTL expired must not
// lose its row to our delayed release.
func freePiReleaseLease(store LeaseStore, accountKey string) {
	if store == nil {
		return
	}
	_ = store.ReleaseLease(freePiLeaseScope, db.LeaseKey(accountKey), freePiProcessEpoch)
}

// freePiMayReset is the whole safety property in one place.
//
// A 409 means the account already holds the maximum number of sessions. Three
// parties can be responsible: a sibling gateway process (visible as a live
// lease under a different epoch), this process's own stale attempt, or a human
// running free-pi-cli (invisible to us entirely — no lease row at all).
//
// Because a human is indistinguishable from our own crash, the rule cannot be
// "reset whenever nothing else is claimed". It is the narrower, provable one:
// reset only when the live lease is absent or already ours, i.e. when no
// sibling is actively driving the account. A human's session is then reset only
// in the case where it is genuinely the thing occupying a slot, which is the
// same outcome running /close-other-session produces — and the cooldown plus
// the per-connection opt-out bound how often that can happen.
func freePiMayReset(store LeaseStore, accountKey string) bool {
	holder := freePiLeaseHolder(store, accountKey)
	if holder == "" {
		return true
	}
	return holder == freePiProcessEpoch
}

// freePiSessionState bundles the per-request identity the executor and the
// reset path share, so a retry re-sends exactly the same headers.
type freePiSessionState struct {
	AccountKey    string
	SessionID     string
	ClientVersion string
	AutoReset     bool
	HeldLease     bool
}

// newFreePiSessionState derives everything one request needs from its
// credential and stored connection data.
func newFreePiSessionState(apiKey string, connData map[string]any) freePiSessionState {
	accountKey := freePiAccountKey(apiKey)
	return freePiSessionState{
		AccountKey:    accountKey,
		SessionID:     freePiSessionIDFor(apiKey, connData),
		ClientVersion: freePiClientVersionFor(connData),
		AutoReset:     freePiAutoResetEnabled(connData),
	}
}

// String renders the state for logs without leaking the credential: the account
// key is already a subject id or a hash, never the token.
func (s freePiSessionState) String() string {
	return fmt.Sprintf("account=%s session=%s version=%s", s.AccountKey, s.SessionID, s.ClientVersion)
}
