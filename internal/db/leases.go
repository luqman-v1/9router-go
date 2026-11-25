package db

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Upstream lease scopes. A scope namespaces one coordination use-case so
// unrelated features never collide on the same key space. Add a new constant
// per use-case — never reuse a scope for a different purpose.
const (
	// LeaseScopeFreebuffSession coordinates Freebuff session admission
	// across processes sharing one DB file: key = sha256(token)+"::"+model,
	// value = instanceId. Exactly one process admits; the rest follow the
	// stored instanceId instead of re-claiming (which upstream reads as
	// session hijacking: 409 session_superseded).
	LeaseScopeFreebuffSession = "freebuff-session"

	// LeaseScopeFreePiSession coordinates free-pi session ownership across
	// processes sharing one DB file: key = sha256 of the account identity,
	// value = the holder's per-process epoch.
	//
	// The value is deliberately NOT the session id. Every process derives the
	// same free-pi session id from the account (uuid.NewSHA1), so a session-id
	// value would match for all holders and make ReleaseLease's
	// compare-and-delete free a sibling's live row. The epoch answers "is the
	// holder me?"; the session id is recomputable and needs no storage.
	LeaseScopeFreePiSession = "free-pi-session"
)

// EnsureUpstreamLeases creates the upstream_leases table if absent. Safe to
// call on every startup (idempotent) and invisible to the Next.js dashboard,
// which simply ignores tables it does not know. Never store raw tokens,
// API keys, or other secrets here — value holds opaque lease payloads
// (instance ids, versions, etags), keyed by hashes, never by secret text.
func EnsureUpstreamLeases(db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("upstream_leases: nil db")
	}
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS upstream_leases (
		scope      TEXT NOT NULL,
		key        TEXT NOT NULL,
		value      TEXT NOT NULL,
		expires_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		PRIMARY KEY (scope, key)
	)`)
	if err != nil {
		return fmt.Errorf("upstream_leases: create table: %w", err)
	}
	return nil
}

// Lease is one coordination record. ExpiresAt is the lease deadline: a
// crashed holder stops blocking others once it passes (no manual cleanup).
type Lease struct {
	Scope     string
	Key       string
	Value     string
	ExpiresAt time.Time
}

// ReadLease returns the lease when present and unexpired, else (nil, nil).
// Expired rows are treated as absent (a later Acquire overwrites them).
func (r *Repo) ReadLease(scope, key string) (*Lease, error) {
	var value, expiresAt, updatedAt string
	err := r.db.QueryRow(
		`SELECT value, expires_at, updated_at FROM upstream_leases
		 WHERE scope = ? AND key = ? LIMIT 1`, scope, key,
	).Scan(&value, &expiresAt, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read lease %s/%s: %w", scope, key, err)
	}
	exp, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return nil, fmt.Errorf("read lease %s/%s: bad expires_at: %w", scope, key, err)
	}
	if !exp.After(time.Now().UTC()) {
		return nil, nil
	}
	return &Lease{Scope: scope, Key: key, Value: value, ExpiresAt: exp}, nil
}

// AcquireLease claims (scope, key) for ttl when absent or expired. Returns
// true when this caller now holds the lease, false when another live holder
// owns it. The single INSERT … WHERE guard makes the check-and-set atomic
// across processes sharing the DB file — concurrent acquirers cannot both win.
func (r *Repo) AcquireLease(scope, key, value string, ttl time.Duration) (bool, error) {
	now := time.Now().UTC()
	exp := now.Add(ttl)
	nowStr := now.Format(time.RFC3339)
	expStr := exp.Format(time.RFC3339)
	// Take over an expired row first (a dead holder's row must never block).
	// The expires_at guard keeps this atomic: two racers cannot both match.
	res, err := r.db.Exec(
		`UPDATE upstream_leases SET value = ?, expires_at = ?, updated_at = ?
		 WHERE scope = ? AND key = ? AND expires_at <= ?`,
		value, expStr, nowStr, scope, key, nowStr,
	)
	if err != nil {
		return false, fmt.Errorf("acquire lease %s/%s takeover: %w", scope, key, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return false, fmt.Errorf("acquire lease %s/%s takeover rows: %w", scope, key, err)
	} else if n > 0 {
		return true, nil
	}
	// No row at all: insert, but only when no live holder appeared meanwhile.
	// A concurrent winner's row makes the SELECT guard miss, so we lose.
	res, err = r.db.Exec(
		`INSERT INTO upstream_leases (scope, key, value, expires_at, updated_at)
		 SELECT ?, ?, ?, ?, ?
		 WHERE NOT EXISTS (
			SELECT 1 FROM upstream_leases
			WHERE scope = ? AND key = ?
		 )`,
		scope, key, value, expStr, nowStr, scope, key,
	)
	if err != nil {
		// Lost a same-instant insert race (PK conflict): the other holder won.
		if isLeaseConflict(err) {
			return false, nil
		}
		return false, fmt.Errorf("acquire lease %s/%s: %w", scope, key, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("acquire lease %s/%s rows: %w", scope, key, err)
	}
	return n > 0, nil
}

// isLeaseConflict reports a primary-key race lost to a concurrent inserter.
func isLeaseConflict(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "PRIMARY KEY") ||
		strings.Contains(msg, "constraint failed")
}

// RefreshLease extends a held lease when value still matches (same holder).
// Returns false when the lease moved on (another holder or released).
func (r *Repo) RefreshLease(scope, key, value string, ttl time.Duration) (bool, error) {
	now := time.Now().UTC()
	exp := now.Add(ttl)
	res, err := r.db.Exec(
		`UPDATE upstream_leases SET expires_at = ?, updated_at = ?
		 WHERE scope = ? AND key = ? AND value = ? AND expires_at > ?`,
		exp.Format(time.RFC3339), now.Format(time.RFC3339),
		scope, key, value, now.Format(time.RFC3339),
	)
	if err != nil {
		return false, fmt.Errorf("refresh lease %s/%s: %w", scope, key, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("refresh lease %s/%s rows: %w", scope, key, err)
	}
	return n > 0, nil
}

// ReleaseLease drops the lease only when value matches (compare-and-delete).
// A holder releasing a stale instanceId must never delete a fresh row stored
// by another process that re-claimed meanwhile.
func (r *Repo) ReleaseLease(scope, key, value string) error {
	_, err := r.db.Exec(
		`DELETE FROM upstream_leases WHERE scope = ? AND key = ? AND value = ?`,
		scope, key, value,
	)
	if err != nil {
		return fmt.Errorf("release lease %s/%s: %w", scope, key, err)
	}
	return nil
}

// LeaseKey hashes secret-derived identity into a non-sensitive lookup key.
// The raw token never touches the table: key is hex(sha256("a::b::c")).
func LeaseKey(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "::")))
	return fmt.Sprintf("%x", sum)
}
