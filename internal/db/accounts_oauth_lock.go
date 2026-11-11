package db

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"strings"
	"time"

	"9router/proxy/internal/providers"
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

// ConnectionRequiresReauth reports whether a connection is stamped
// reauth_required — the marker RecordConnectionOAuthFailure writes for a
// terminal grant rejection (invalid_grant, refresh_token_reused, revoked).
// Such an account is not "cooling down until reset": no wait repairs it, so
// force-fallback and other soonest-reset pickers must never resurrect it.
// Missing or malformed values fail open (false) so a corrupt blob never
// takes routing down.
func ConnectionRequiresReauth(rawData string) bool {
	raw, ok := parseConnData(rawData)
	if !ok {
		return false
	}
	status, _ := raw["testStatus"].(string)
	return status == providers.StatusReauthRequired
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
	if until, ok := readTimestampField(raw, "accountLockedUntil"); ok && (!found || until.Before(earliest)) {
		return until, true
	}
	return earliest, found
}

// ConnectionAccountLockUntil reads the account-ban lockout timestamp a
// connection carries in its data blob, failing open on a missing or malformed
// value.
func ConnectionAccountLockUntil(rawData string) (time.Time, bool) {
	raw, ok := parseConnData(rawData)
	if !ok {
		return time.Time{}, false
	}
	return readTimestampField(raw, "accountLockedUntil")
}

// connectionOAuthState reads the park and the consecutive-rejection count in
// one pass, reporting the zero time and 0 for a blob it cannot read.
func (r *Repo) connectionOAuthState(connID string) (time.Time, int) {
	if connID == "" {
		return time.Time{}, 0
	}
	var rawData string
	if err := r.db.QueryRowContext(context.Background(), "SELECT data FROM providerConnections WHERE id = ?", connID).Scan(&rawData); err != nil {
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

// connectionAccountBanState reads the account-ban park and the consecutive
// ban-failure count in one pass, reporting the zero time and 0 for a blob it
// cannot read. Separate from oauthFailureCount: an OAuth rejection and an
// account ban are different verdicts, each with its own ladder.
func (r *Repo) connectionAccountBanState(connID string) (time.Time, int) {
	if connID == "" {
		return time.Time{}, 0
	}
	var rawData string
	if err := r.db.QueryRowContext(context.Background(), "SELECT data FROM providerConnections WHERE id = ?", connID).Scan(&rawData); err != nil {
		return time.Time{}, 0
	}
	raw, ok := parseConnData(rawData)
	if !ok {
		return time.Time{}, 0
	}
	until, _ := readTimestampField(raw, "accountLockedUntil")
	failures, ok := raw["accountBanFailureCount"].(float64)
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
//
// When the rejection is terminal (a revoked grant, invalid_grant, an expired
// refresh token) only a re-login can repair it, so the row is also stamped
// with testStatus = reauth_required and the account is quarantined from
// routing for the cooldown window instead of being retried in a loop.
func (r *Repo) RecordConnectionOAuthFailure(connID string, status int, err error) (time.Time, int, error) {
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
	deadGrant := providers.IsRefreshGrantDead(err)
	lastError, marshalErr := json.Marshal(map[string]any{
		"status":    status,
		"message":   err.Error(),
		"timestamp": stamp,
	})
	if marshalErr != nil {
		return time.Time{}, 0, fmt.Errorf("marshal last error for %s: %w", connID, marshalErr)
	}

	query := `UPDATE providerConnections
		SET data = json_set(data,
			'$.oauthLockedUntil', ?,
			'$.oauthFailureCount', ?,
			'$.lastError', json(?)),
			updatedAt = ?
		WHERE id = ?`
	args := []any{until.UTC().Format(time.RFC3339), failures, string(lastError), stamp, connID}
	if deadGrant {
		query = `UPDATE providerConnections
			SET data = json_set(data,
				'$.oauthLockedUntil', ?,
				'$.oauthFailureCount', ?,
				'$.testStatus', ?,
				'$.lastError', json(?)),
				updatedAt = ?
			WHERE id = ?`
		args = []any{until.UTC().Format(time.RFC3339), failures, providers.StatusReauthRequired, string(lastError), stamp, connID}
	}
	_, execErr := r.db.ExecContext(context.Background(), query, args...)
	if execErr != nil {
		return time.Time{}, 0, fmt.Errorf("lock connection oauth %s: %w", connID, execErr)
	}
	return until, failures, nil
}

// ClearConnectionOAuthLock releases the park once the account refreshed again,
// so a credential that was repaired does not stay out of rotation for the rest
// of the window, and the next rejection starts from the base cooldown again.
// The quota cooldown is deliberately left alone: a working token says nothing
// about a spent quota.
// A re-authenticated account also sheds its
// reauth_required testStatus so it reports healthy again.
func (r *Repo) ClearConnectionOAuthLock(connID string) error {
	if connID == "" {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := r.db.ExecContext(context.Background(),
		`UPDATE providerConnections
		SET data = json_set(data,
			'$.oauthLockedUntil', NULL,
			'$.oauthFailureCount', 0,
			'$.testStatus', NULL),
			updatedAt = ?
		WHERE id = ?`,
		now, connID,
	)
	if err != nil {
		return fmt.Errorf("clear connection oauth lock %s: %w", connID, err)
	}
	return nil
}

// GetConnectionAccountBanFailures reads the consecutive account-ban failure
// count, reporting 0 when it is missing or malformed so a corrupt counter costs
// the account its longer backoff, never its routing.
func (r *Repo) GetConnectionAccountBanFailures(connID string) int {
	_, failures := r.connectionAccountBanState(connID)
	return failures
}

// RecordConnectionAccountBanFailure parks a connection whose account suffered a
// permanent/severe account-level failure (e.g. an Antigravity TOS ban) and
// reports the window it parked for. The cooldown climbs the escalating lockout
// ladder with each consecutive ban nobody repaired, and a successful request or
// successful test connection clears the streak.
//
// Every recorded ban is a distinct failure event, so the counter always moves:
// unlike OAuth rejections (where one dead grant can be re-discovered several
// times in one request), an account ban is a fresh verdict each time it is
// observed.
func (r *Repo) RecordConnectionAccountBanFailure(connID string) (time.Time, int, error) {
	if connID == "" {
		return time.Time{}, 0, nil
	}
	now := time.Now().UTC()
	_, failures := r.connectionAccountBanState(connID)
	failures++
	until := now.Add(providers.GetEscalatingLockoutCooldown(failures))
	stamp := now.Format(time.RFC3339)
	_, err := r.db.ExecContext(context.Background(),
		`UPDATE providerConnections
		SET data = json_set(data,
			'$.accountLockedUntil', ?,
			'$.accountBanFailureCount', ?),
			updatedAt = ?
		WHERE id = ?`,
		until.UTC().Format(time.RFC3339), failures, stamp, connID,
	)
	if err != nil {
		return time.Time{}, 0, fmt.Errorf("lock connection account ban %s: %w", connID, err)
	}
	return until, failures, nil
}

// ClearConnectionAccountBanLock releases the account-ban park and resets the
// failure streak, so a repaired account starts from the base cooldown again.
// Called once the account has been proven usable (a successful request or a
// successful manual test connection).
func (r *Repo) ClearConnectionAccountBanLock(connID string) error {
	if connID == "" {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := r.db.ExecContext(context.Background(),
		`UPDATE providerConnections
		SET data = json_set(data,
			'$.accountLockedUntil', NULL,
			'$.accountBanFailureCount', 0),
			updatedAt = ?
		WHERE id = ?`,
		now, connID,
	)
	if err != nil {
		return fmt.Errorf("clear connection account ban lock %s: %w", connID, err)
	}
	return nil
}
