package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// rotationTimestampFormat is a fixed-width RFC3339 with nanoseconds. The
// round-robin selector compares lastUsedAt as a string to stay allocation-free
// on the hot path, and that only works if the fractional part is zero-padded to
// a constant width: time.RFC3339Nano trims trailing zeros, which would make
// "…:00.5Z" sort after "…:00.500000001Z".
//
// Nanosecond width alone does not buy strict ordering, though. time.Now is a
// wall-clock read whose resolution is the platform's, not Go's promise: on the
// Windows host this was diagnosed on, back-to-back calls advanced the clock only
// every ~815µs, so a burst of rotation picks inside one millisecond all
// formatted to the same string. Rows with equal stamps are indistinguishable to
// the least-recently-used tie-break, which then keeps handing back the same
// account — the exact "round robin never reaches the next account" symptom.
// nextRotationStamp therefore pushes the value past whatever is already
// stored whenever the clock would not beat it, making ordering a property of
// the write path instead of a property of the clock.
const rotationTimestampFormat = "2006-01-02T15:04:05.000000000Z07:00"

// rotationClock is the wall clock stamps are derived from. It is a variable only
// so tests can freeze it and pin the ordering guarantee against a clock that
// never advances; production always reads time.Now.
var rotationClock = time.Now

// TouchConnectionRotation stamps a connection as the one just selected by the
// persistent round-robin and sets its consecutive-use counter to exactly
// `consecutive` (1 for a fresh pick, previous+1 while a sticky window holds).
// It deliberately sets the counter rather than incrementing it: the selector
// decides the value from the row it read, and a lost race should converge on a
// fresh window rather than drift upward forever.
//
// The returned string is the value actually persisted, so the caller mirrors it
// onto its in-memory row instead of reading the clock a second time and getting
// a different answer.
func (r *Repo) TouchConnectionRotation(connectionID string, consecutive int) (string, error) {
	return r.stampConnection(connectionID, consecutive, false)
}

// UpdateConnectionLastUsed records a successful request against a connection and
// increments its consecutive-use counter. It shares the rotation stamp source so
// a media request cannot write a stamp that sorts ahead of the chat path's.
func (r *Repo) UpdateConnectionLastUsed(connectionID string) error {
	_, err := r.stampConnection(connectionID, 0, true)
	return err
}

// stampConnection writes a rotation stamp strictly greater than every stamp
// already stored, reading the current maximum and writing the new value under a
// single SQLite write lock.
//
// The lock is taken up front by BEGIN IMMEDIATE. A deferred transaction would
// only acquire it at the UPDATE — after MAX had been read — so two processes
// sharing one database could both read the same maximum and mint the same stamp
// for two different rows, reintroducing the tie the selector cannot break. Under
// this the guarantee also survives a restart, because the stamps live in the rows
// and not in process memory.
func (r *Repo) stampConnection(connectionID string, consecutive int, increment bool) (string, error) {
	ctx := context.Background()

	conn, err := r.db.Conn(ctx)
	if err != nil {
		return "", fmt.Errorf("stamp connection %s: acquire connection: %w", connectionID, err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return "", fmt.Errorf("stamp connection %s: begin: %w", connectionID, err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	var previous sql.NullString
	if err := conn.QueryRowContext(ctx, `SELECT MAX(lastUsedAt) FROM providerConnections`).Scan(&previous); err != nil {
		return "", fmt.Errorf("stamp connection %s: read stored stamp: %w", connectionID, err)
	}
	stamp := nextRotationStamp(previous.String)

	// MAX is taken over the whole table rather than one provider pool: the
	// selector only ever compares rows of a single pool, so a table-wide
	// maximum is a stricter bound than it needs and needs no extra argument
	// threaded through to keep it that way.
	query := `UPDATE providerConnections SET lastUsedAt = ?, consecutiveUseCount = ? WHERE id = ?`
	args := []any{stamp, consecutive, connectionID}
	if increment {
		query = `UPDATE providerConnections SET lastUsedAt = ?, consecutiveUseCount = COALESCE(consecutiveUseCount, 0) + 1 WHERE id = ?`
		args = []any{stamp, connectionID}
	}
	if _, err := conn.ExecContext(ctx, query, args...); err != nil {
		return "", fmt.Errorf("stamp connection %s: write stamp: %w", connectionID, err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return "", fmt.Errorf("stamp connection %s: commit: %w", connectionID, err)
	}
	committed = true

	return stamp, nil
}

// nextRotationStamp mints the stamp for one pick. The selector compares stamps
// as plain strings, so the only rule that matters is that the returned string
// sorts strictly after `previous` — which the wall clock alone does not
// guarantee, since its resolution is the platform's.
func nextRotationStamp(previous string) string {
	now := rotationClock().UTC().Format(rotationTimestampFormat)
	if previous == "" || now > previous {
		return now
	}
	prev, ok := parseRotationStamp(previous)
	if !ok {
		return now
	}
	// One nanosecond on is enough for fixed-width stamps, but not across the
	// legacy format: "…:00Z" sorts *after* "…:00.000000001Z" because '.'
	// (0x2E) beats 'Z' (0x5A), so a nanosecond bump in the same second would
	// still look older than the row it was minted to overtake. Advancing a
	// whole second puts the padded value first, which is the correct order.
	if successor := prev.Add(time.Nanosecond).Format(rotationTimestampFormat); successor > previous {
		return successor
	}
	return prev.Truncate(time.Second).Add(time.Second).Format(rotationTimestampFormat)
}

// parseRotationStamp reads back a stamp written by any version of this package:
// the fixed-width rotation format, or the plain second-precision RFC3339 that
// older rows still carry. Rows left behind by an older build must keep ordering
// correctly, otherwise one stale stamp would pin the rotation to a single row.
func parseRotationStamp(value string) (time.Time, bool) {
	if value == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{rotationTimestampFormat, time.RFC3339} {
		if t, err := time.Parse(layout, value); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// NextRotationStamp mints a stamp without touching a database, for a caller that
// only wants the formatting and tie-breaking guarantee.
func NextRotationStamp(previous string) string {
	return nextRotationStamp(previous)
}