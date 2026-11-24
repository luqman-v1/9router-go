package executor

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
)

// Free-pi session machinery.
//
// free-pi's upstream ties every completion to an `x-session-id` and bounds how
// many sessions one account may hold at once (4 — hungkevin/free-pi-cli-proxy
// server.mjs:958). Exceeding it yields a 409 with code `concurrent_session`,
// which the reference proxy clears with POST /session/reset and a single retry.
//
// This file builds deliberately for the tightest reading of that limit: one
// session per account, no matter how many connections or processes exist. The
// other three are unspent headroom. That costs nothing and survives the day
// upstream tightens the cap to 1.
//
// Two mechanisms carry it:
//
//   - Session identity is DERIVED, not minted. uuid.NewSHA1 over the account
//     identity means two dashboard connections for one account, a restarted
//     process, and a second gateway instance all compute the SAME id. Nothing
//     has to be remembered for that to hold.
//   - A lease marks which process is currently driving the account, so a 409
//     from a sibling's in-flight request is never "fixed" by resetting the
//     session out from under it.
//
// The lease holds a per-process epoch rather than the session id on purpose:
// every holder of an account computes the same session id, so a session-id
// value would make the compare-and-delete in LeaseStore.ReleaseLease match for
// everybody and free a sibling's live row.

const (
	// freePiLeaseScope namespaces free-pi session leases in upstream_leases.
	// Aliased from db so the two cannot drift.
	freePiLeaseScope = db.LeaseScopeFreePiSession

	// freePiLeaseTTL bounds how long a process claims its account. It is a
	// reset GUARD, not a request lock: an expired row only means a sibling may
	// now take over reset rights, and since every process derives the same
	// session id, taking over re-establishes the same session. Short enough
	// that a crashed process stops blocking within a minute (AcquireLease only
	// takes over *expired* rows, so a long TTL turns one crash into a long
	// block), long enough to cover a normal request.
	freePiLeaseTTL = 60 * time.Second

	// freePiResetCooldown is the minimum gap between two automatic
	// POST /session/reset calls for one account. It is the last line of
	// defence against a reset loop.
	freePiResetCooldown = 3 * time.Minute

	// freePiCooldownEntries caps the cooldown bookkeeping. Entries expire after
	// freePiResetCooldown, so the only way to grow past this is a burst of
	// distinct accounts larger than the cap — at which point forgetting the
	// oldest is right, not fatal (AGENTS.md section 4.G).
	freePiCooldownEntries = 256
)

// freePiSessionNamespace is the UUIDv5 namespace for derived session ids. A
// fixed constant: changing it would orphan every existing upstream session.
var freePiSessionNamespace = uuid.MustParse("6f70656e-7069-2d66-7265-6570692d7365")

// freePiProcessEpoch identifies THIS process for the lifetime of the binary. It
// is the lease value, so a lease row can be attributed to its holder.
var freePiProcessEpoch = uuid.New().String()

// freePiStripeCount is the number of mutexes guarding accounts. Striping keeps
// memory O(1) with no map to bound, while still serializing the only critical
// section that matters: the read-lease / reset / record-lease decision
// (AGENTS.md section 4.G — an unbounded per-connection lock map is a leak).
const freePiStripeCount = 64

var freePiStripes [freePiStripeCount]sync.Mutex

// freePiStripe returns the mutex guarding one account's reset decision.
func freePiStripe(accountKey string) *sync.Mutex {
	sum := sha256.Sum256([]byte(accountKey))
	return &freePiStripes[int(sum[0])%freePiStripeCount]
}

// freePiAccountKey is the stable, non-secret identity of a free-pi account.
//
// The JWT `sub` claim is the account id (see testdata/freepi/auth-token-granted.json).
// When a token cannot be decoded — a malformed paste, or a future opaque
// credential — the token itself is hashed instead, so callers still get a
// stable key and the raw secret never reaches the leases table or a log.
func freePiAccountKey(apiKey string) string {
	if sub := freePiJWSubject(apiKey); sub != "" {
		return "sub:" + sub
	}
	sum := sha256.Sum256([]byte(apiKey))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// freePiJWSubject extracts the `sub` claim from a JWT without verifying the
// signature. Verification is upstream's job: we only need a stable label, never
// a security decision, so a forged token here is already worthless upstream.
func freePiJWSubject(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		// Some issuers emit padded base64url; accept that too rather than
		// treating a valid token as opaque.
		payload, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return ""
		}
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	return claims.Sub
}

// freePiAccountSessionID is the stable per-ACCOUNT session id, used as the
// derivation input for every per-rotation variant.
//
// It is kept separate from freePiSessionIDFor because the two answer different
// questions: this one is "which account is this?", the other is "which
// conversation is this?". Only the account answer may be persisted.
func freePiAccountSessionID(apiKey string) string {
	return uuid.NewSHA1(freePiSessionNamespace, []byte(freePiAccountKey(apiKey))).String()
}

// freePiSessionVariant derives a distinct-but-stable session id for one
// rotation step of one account.
//
// free-pi binds a conversation to the session id that opened it and refuses a
// later request on that conversation from a different account — the rejection
// reads as the conversation belonging to another session. Reusing one session
// id across accounts therefore makes rotation impossible no matter how healthy
// the credentials are. Deriving from (account, step) rather than minting a
// random uuid keeps the mapping one-to-one, so a given step always names the
// same id and a step already handed out is never re-derived differently. The
// step itself comes from a process-local counter (freePiRotationCounter), so it
// is monotonic within a run and restarts with the process — the derivation is
// what is stable, not the counter's value.
func freePiSessionVariant(apiKey string, step uint64) string {
	if step == 0 {
		return freePiAccountSessionID(apiKey)
	}
	name := fmt.Sprintf("%s/rotation/%d", freePiAccountKey(apiKey), step)
	return uuid.NewSHA1(freePiSessionNamespace, []byte(name)).String()
}

// freePiSessionIDFor returns the upstream session id for one connection.
//
// The id is ALWAYS derived from the account identity and never read from stored
// connection data. That is the invariant the whole rotation design rests on: the
// id must be a function of the account, so account A can never present account
// B's id — which is exactly what upstream refuses with "This conversation
// belongs to another free-pi account."
//
// A stored value used to win here, as an operator "pin". Nothing in the product
// ever set one (the dashboard has no field for it) but the login handler wrote
// the derived value into that slot as a cache, and every reader then treated it
// as authoritative — so freePiRotatedSessionIDFor returned it at every step and
// rotation could never change anything. Deriving unconditionally removes the
// failure mode instead of documenting around it.
func freePiSessionIDFor(apiKey string, _ map[string]any) string {
	return freePiAccountSessionID(apiKey)
}

// freePiRotatedSessionIDFor returns the session id for one rotation step of one
// account. Like the base id it is derived, never stored, so a step always names
// an id belonging to THIS account and no other account can be handed it.
func freePiRotatedSessionIDFor(apiKey string, _ map[string]any, step uint64) string {
	return freePiSessionVariant(apiKey, step)
}

// FreePiSessionID is the exported form of the derivation, so the login handler
// can persist exactly the value the executor will send. Two implementations of
// this rule would be two ways to drift into a second upstream session.
func FreePiSessionID(apiKey string) string {
	return freePiSessionIDFor(apiKey, nil)
}

// freePiClientVersionFor returns the x-client-version to send.
//
// The server refuses clients below a minimum it publishes at GET
// /client-version, so the value stored at login (the server's own `latest`) is
// preferred over the compiled-in floor.
func freePiClientVersionFor(connData map[string]any) string {
	if connData != nil {
		if v, ok := connData["freePiClientVersion"].(string); ok && v != "" {
			return v
		}
	}
	return providers.FreePiClientVersion
}

// freePiAutoResetEnabled reports whether the connection permits an automatic
// POST /session/reset. Default is enabled; `freePiAutoReset: false` opts out,
// which is the documented setting for anyone running free-pi-cli interactively
// on the same account.
func freePiAutoResetEnabled(connData map[string]any) bool {
	if connData == nil {
		return true
	}
	if v, ok := connData["freePiAutoReset"].(bool); ok {
		return v
	}
	return true
}

// FreePiTokenExpiredCode is the synthetic code reported when the credential's
// own `exp` claim is in the past.
//
// free-pi issues a long-lived JWT with NO refresh token, so expiry is terminal:
// the only remedy is re-authentication. Catching it locally, before the request
// goes out, buys two things — the client gets "re-authenticate" instead of a
// bare 401 after a wasted round trip, and the connection is classified with a
// long cooldown rather than the generic two-minute 401 rule that would otherwise
// retry a permanently dead credential every couple of minutes, forever.
const FreePiTokenExpiredCode = "free_pi_token_expired"

// freePiTokenExpiry returns the credential's `exp` claim. ok is false when the
// token is opaque or carries no usable expiry, in which case the caller must let
// upstream decide.
func freePiTokenExpiry(token string) (exp time.Time, ok bool) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return time.Time{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		payload, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return time.Time{}, false
		}
	}
	var claims struct {
		Exp json.Number `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp == "" {
		return time.Time{}, false
	}
	secs, err := claims.Exp.Int64()
	if err != nil || secs <= 0 {
		return time.Time{}, false
	}
	return time.Unix(secs, 0), true
}

// freePiTokenExpired reports whether the credential is already dead.
//
// The skew allowance is deliberately zero: `exp` is a hard boundary and a token
// a few seconds from expiry would fail upstream anyway, so treating it as live
// only wastes a request.
func freePiTokenExpired(token string, now time.Time) bool {
	exp, ok := freePiTokenExpiry(token)
	if !ok {
		return false
	}
	return !exp.After(now)
}
