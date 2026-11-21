package chat

import (
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/models"
)

// Operator-controlled bypass of the account-scoped rate-limit cooldown.
//
// The dashboard toggle writes data.ignoreRateLimit on a connection row. With
// it on, the selector hands the account out even while rateLimitedUntil is in
// the future, and the failure loops never write that cooldown for it, so a
// custom endpoint stuck behind a long upstream window (an hourly quota, an
// IP-level throttle that clears sooner than announced) keeps serving instead
// of parking for the full announced wait.
//
// The bypass is deliberately narrow. It covers exactly the two behaviours the
// operator wants gone for a custom endpoint: the account park the selector
// reads through ConnectionBlockedUntil, and the park the failure loops write
// through LockConnectionRateLimit. It does NOT touch:
//
//   - per-model locks (modelLock_<model>) — a model that 404s or is declared
//     dead fails on every connection; retrying it can only deepen the failure;
//   - the OAuth reauth park (oauthLockedUntil) — a dead grant answers 401
//     until re-logged, so forcing it wastes the account and a token refresh;
//   - provider node limits (RPM/TPM/concurrency) — a contract the operator
//     set themselves on the node editor;
//   - combo Retry-After pass waits — request-scoped pacing, not an account
//     park.
//
// Last write wins everywhere: when every account is cooling and nothing is
// selectable, the connection still answers, and that is the point of the
// toggle.
func ignoresRateLimit(c *models.ProviderConnection) bool {
	return c != nil && db.ConnectionIgnoresRateLimit(c.Data)
}

// ctxConnIgnoresRateLimit reports whether the connection a chat loop already
// resolved carries the bypass. The combo and account-fallback loops hold
// connData as their in-memory blob; reading the flag from it avoids a re-fetch
// and keeps the write side and the selector on one source of truth.
func ctxConnIgnoresRateLimit(connData *ConnectionData) bool {
	return connData != nil && connData.IgnoreRateLimit
}

// accountParkBlocked is the selector's account-cooldown question with the
// bypass applied: a flagged connection is never parked by rateLimitedUntil.
// OAuth reauth parks are checked by the caller before this helper and stay
// binding.
func accountParkBlocked(c *models.ProviderConnection, now time.Time) (time.Time, bool) {
	if ignoresRateLimit(c) {
		return time.Time{}, false
	}
	return db.ConnectionBlockedUntil(c.Data)
}

// ignoredAccountPark returns the park a bypassed connection carries, for the
// caller that wants to log the window it chose to ride out. Zero when the
// connection is not flagged or carries no cooldown.
func ignoredAccountPark(c *models.ProviderConnection, now time.Time) time.Time {
	if !ignoresRateLimit(c) {
		return time.Time{}
	}
	until, ok := db.ConnectionAccountCooldownUntil(c.Data)
	if !ok || !until.After(now) {
		return time.Time{}
	}
	return until
}

// connectionIgnoresRateLimitByID answers the bypass question from a bare
// connection id, for the failure loops that hold only the id at the point of
// the lock write. One indexed row read per failure — failures are the rare
// path, not the hot one.
func (h *ChatHandler) connectionIgnoresRateLimitByID(connID string) bool {
	if h == nil || h.Repo == nil || connID == "" || connID == "noauth" {
		return false
	}
	conn, err := h.Repo.GetProviderConnectionByID(connID)
	if err != nil || conn == nil {
		return false
	}
	return ignoresRateLimit(conn)
}
