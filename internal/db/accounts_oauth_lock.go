package db

import (
	json "encoding/json/v2"
	"fmt"
	"strings"
	"time"
)

// OAuth rejection cooldown (OmniRoute #14917).
//
// An account whose grant the provider has revoked answers every refresh with
// 401, and the router used to retry that refresh on every request until the
// token endpoint rate limited the whole egress IP. The account is parked here
// rather than on the quota cooldown: the dashboard renders rateLimitedUntil as
// "0% quota left, resets at", which is a lie for a dead credential — the quota
// is untouched, and waiting out the window cannot fix it. Only a re-login can,
// so this park has its own fields and its own counter.
const (
	// oauthLockBaseDelay is the cooldown after the first rejection. Each
	// further consecutive rejection doubles it, capped at oauthLockMaxDelay.
	oauthLockBaseDelay = 5 * time.Minute
	oauthLockMaxDelay  = 24 * time.Hour
	// oauthLockMaxShift bounds the exponent so a corrupted counter cannot
	// overflow the shift in OAuthLockDelay.
	oauthLockMaxShift = 16
)

// OAuthLockDelay returns the cooldown for the nth consecutive OAuth rejection
// (1-based): 5m, 10m, 20m, 40m, ... capped at 24h.
func OAuthLockDelay(failures int) time.Duration {
	if failures < 1 {
		failures = 1
	}
	if failures > oauthLockMaxShift {
		failures = oauthLockMaxShift
	}
	delay := oauthLockBaseDelay << (failures - 1)
	if delay > oauthLockMaxDelay {
		return oauthLockMaxDelay
	}
	return delay
}

// parseConnData decodes a connection data blob, reporting false when it cannot
// be read at all.
func parseConnData(rawData string) (map[string]any, bool) {
	if rawData == "" {
		return nil, false
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(rawData), &raw); err != nil {
		return nil, false
	}
	return raw, true
}

// readTimestampField reads an RFC3339 timestamp field, failing open on a
// missing, empty or unparseable value: "cannot tell" must never mean "skip".
func readTimestampField(raw map[string]any, key string) (time.Time, bool) {
	value, ok := raw[key].(string)
	if !ok || strings.TrimSpace(value) == "" {
		return time.Time{}, false
	}
	stamp, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, false
	}
	return stamp, true
}

// ConnectionOAuthLockUntil reads the OAuth rejection cooldown a connection
// carries in its data blob, failing open on a missing or malformed value.
func ConnectionOAuthLockUntil(rawData string) (time.Time, bool) {
	raw, ok := parseConnData(rawData)
	if !ok {
		return time.Time{}, false
	}
	return readTimestampField(raw, "oauthLockedUntil")
}

// ConnectionBlockedUntil returns the earliest instant the account becomes
// selectable again across the account-scoped cooldowns, so the selector can
// ask one question instead of one per park.
func ConnectionBlockedUntil(rawData string) (time.Time, bool) {
	raw, ok := parseConnData(rawData)
	if !ok {
		return time.Time{}, false
	}
	earliest, found := readTimestampField(raw, "rateLimitedUntil")
	if until, ok := readTimestampField(raw, "oauthLockedUntil"); ok && (!found || until.Before(earliest)) {
		return until, true
	}
	return earliest, found
}

// connectionOAuthState reads the park and the consecutive-rejection count in
// one pass, reporting the zero time and 0 for a blob it cannot read.
func (r *Repo) connectionOAuthState(connID string) (time.Time, int) {
	if connID == "" {
		return time.Time{}, 0
	}
	var rawData string
	if err := r.db.QueryRow("SELECT data FROM providerConnections WHERE id = ?", connID).Scan(&rawData); err != nil {
		return time.Time{}, 0
	}
	raw, ok := parseConnData(rawData)
	if !ok {
		return time.Time{}, 0
	}
	until, _ := readTimestampField(raw, "oauthLockedUntil")
	failures, ok := raw["oauthFailureCount"].(float64)
	if !ok || failures < 0 {
		return until, 0
	}
	return until, int(failures)
}

// GetConnectionOAuthFailures reads the consecutive OAuth rejection count,
// reporting 0 when it is missing or malformed so a corrupt counter costs the
// account its longer backoff, never its routing.
func (r *Repo) GetConnectionOAuthFailures(connID string) int {
	_, failures := r.connectionOAuthState(connID)
	return failures
}

// RecordConnectionOAuthFailure parks a connection whose OAuth grant the
// provider rejected and reports the window it parked for. The cooldown grows
// with the number of consecutive rejections nobody repaired, and a successful
// refresh clears the counter.
//
// A rejection that lands while the account is already parked is the same
// strike seen again — one request can discover a dead grant more than once
// (the forward path refreshes, then the project probe force-refreshes) — so
// the counter only moves once the previous window has run out. Counting the
// repeats would turn a single bad request into a 20 minute cooldown.
func (r *Repo) RecordConnectionOAuthFailure(connID string, status int, errText string) (time.Time, int, error) {
	if connID == "" {
		return time.Time{}, 0, nil
	}
	now := time.Now().UTC()
	lockedUntil, failures := r.connectionOAuthState(connID)
	if !lockedUntil.After(now) {
		failures++
	}
	until := now.Add(OAuthLockDelay(failures))
	stamp := now.Format(time.RFC3339)
	lastError, err := json.Marshal(map[string]any{
		"status":    status,
		"message":   errText,
		"timestamp": stamp,
	})
	if err != nil {
		return time.Time{}, 0, fmt.Errorf("marshal last error for %s: %w", connID, err)
	}
	_, err = r.db.Exec(
		`UPDATE providerConnections
		 SET data = json_set(data,
		   '$.oauthLockedUntil', ?,
		   '$.oauthFailureCount', ?,
		   '$.lastError', json(?)),
		     updatedAt = ?
		 WHERE id = ?`,
		until.UTC().Format(time.RFC3339), failures, string(lastError), stamp, connID,
	)
	if err != nil {
		return time.Time{}, 0, fmt.Errorf("lock connection oauth %s: %w", connID, err)
	}
	return until, failures, nil
}

// ClearConnectionOAuthLock releases the park once the account refreshed again,
// so a credential that was repaired does not stay out of rotation for the rest
// of the window, and the next rejection starts from the base cooldown again.
// The quota cooldown is deliberately left alone: a working token says nothing
// about a spent quota.
func (r *Repo) ClearConnectionOAuthLock(connID string) error {
	if connID == "" {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := r.db.Exec(
		`UPDATE providerConnections
		 SET data = json_set(data,
		   '$.oauthLockedUntil', NULL,
		   '$.oauthFailureCount', 0),
		     updatedAt = ?
		 WHERE id = ?`,
		now, connID,
	)
	if err != nil {
		return fmt.Errorf("clear connection oauth lock %s: %w", connID, err)
	}
	return nil
}
