package semanticcache

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"9router/proxy/internal/log"
)

// cacheTablePresent reports whether the semantic cache table exists, so a
// failed hydration can tell "nothing to load yet" from "the table is there and
// unreadable". It asks the catalog instead of matching the driver's message
// (§4.E): an error string is not a contract, and a reworded one would silently
// suppress a real failure.
//
// Both engines are covered without importing the schema layer, which would
// make this package depend on its own consumer: the SQLite catalog answers on
// SQLite, and the PostgreSQL catalog answers on PostgreSQL. An engine that
// answers neither counts as present, because the safe default is to report the
// hydration error rather than swallow it.
func cacheTablePresent(ctx context.Context, db *sql.DB) bool {
	const table = "semanticCacheEntries"
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err == nil {
		return count > 0
	}
	var name string
	switch err := db.QueryRowContext(ctx, `SELECT table_name FROM information_schema.tables WHERE table_name = $1 LIMIT 1`, table).Scan(&name); {
	case err == nil:
		return true
	case errors.Is(err, sql.ErrNoRows):
		return false
	default:
		return true
	}
}

// evaluated in SQL (`WHERE storedAt < ?` / `WHERE storedAt >= ?`), so the
// comparison is a string compare on the stored text: plain time.RFC3339
// truncates to whole seconds, which makes every row written in the same second
// as the cutoff compare equal, so an expired row never sorts before the cutoff
// and neither the janitor nor Hydrate ever removes it. This mirrors
// db.rotationTimestampFormat, which carries the same reasoning for lastUsedAt.
// Values written by older builds are second-precision RFC3339 and still parse,
// but they expire a whole second late — acceptable for a cache.
const storedAtFormat = "2006-01-02T15:04:05.000000000Z07:00"

// formatStoredAt renders a timestamp in the fixed-width form written to the
// storedAt column.
func formatStoredAt(t time.Time) string {
	return t.UTC().Format(storedAtFormat)
}

// parseStoredAt reads back a storedAt value written by any version of this
// package: the fixed-width format first, then the plain RFC3339 older builds
// wrote.
func parseStoredAt(value string) (time.Time, bool) {
	for _, layout := range []string{storedAtFormat, time.RFC3339} {
		if t, err := time.Parse(layout, value); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// PersistentLRUStore wraps an in-memory LRUStore with SQLite disk persistence.
type PersistentLRUStore struct {
	lru         *LRUStore
	db          *sql.DB
	ttl         time.Duration
	stopJanitor chan struct{}
	closeOnce   sync.Once
}

// NewPersistentStore creates an LRUStore backed by SQLite disk persistence.
func NewPersistentStore(db *sql.DB, maxEntries int, ttl time.Duration) *PersistentLRUStore {
	store := &PersistentLRUStore{
		lru:         NewLRUStore(maxEntries, ttl),
		db:          db,
		ttl:         ttl,
		stopJanitor: make(chan struct{}),
	}
	// A failed hydration used to be discarded here with `_ =`, so an unreadable
	// cache table booted a gateway whose semantic cache silently served nothing
	// while reporting itself healthy. The cache still starts — a warm cache is an
	// optimization, not a precondition for routing — but the operator can now
	// see why every request is a miss.
	//
	// An absent table is not that failure: this store is built against a
	// repository before its schema is ensured in some paths, and a fresh install
	// has no table yet, so "starting cold" is the normal state there — the same
	// condition Put and Delete already tolerate, and reporting it would blame a
	// healthy gateway on every boot. Existence is asked of the catalog rather
	// than inferred from the message text (§4.E).
	if err := store.Hydrate(context.Background()); err != nil && cacheTablePresent(context.Background(), db) {
		log.Warn("semantic-cache", "hydrate from disk failed; starting cold", "error", err)
	}
	if ttl > 0 {
		store.startJanitor(1 * time.Hour)
	}
	return store
}

func (p *PersistentLRUStore) startJanitor(interval time.Duration) {
	if interval <= 0 {
		interval = 1 * time.Hour
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-p.stopJanitor:
				return
			case <-ticker.C:
				if p.ttl > 0 {
					_ = p.InvalidateOlderThan(context.Background(), p.ttl)
				}
			}
		}
	}()
}

// Close stops the background janitor cleanly.
func (p *PersistentLRUStore) Close() {
	p.closeOnce.Do(func() {
		if p.stopJanitor != nil {
			close(p.stopJanitor)
		}
	})
}

// Hydrate preloads unexpired entries from SQLite into the in-memory LRU cache.
func (p *PersistentLRUStore) Hydrate(ctx context.Context) error {
	if p.db == nil {
		return nil
	}

	cutoff := ""
	if p.ttl > 0 {
		cutoff = formatStoredAt(time.Now().Add(-p.ttl))
	}

	query := `
SELECT key, model, responseBody, contentType, storedAt, hitCount, tokensSaved
FROM semanticCacheEntries`
	args := []any{}
	if cutoff != "" {
		query += " WHERE storedAt >= ?"
		args = append(args, cutoff)
	}

	rows, err := p.db.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("PersistentLRUStore.Hydrate: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			key, model, contentType, storedAtStr string
			body                                 []byte
			hitCount, tokensSaved                int64
		)
		if err := rows.Scan(&key, &model, &body, &contentType, &storedAtStr, &hitCount, &tokensSaved); err != nil {
			return fmt.Errorf("PersistentLRUStore.Hydrate scan: %w", err)
		}
		storedAt, ok := parseStoredAt(storedAtStr)
		if !ok {
			storedAt = time.Now()
		}
		_ = p.lru.Put(ctx, key, Entry{
			Key:          key,
			Model:        model,
			ResponseBody: body,
			ContentType:  contentType,
			StoredAt:     storedAt,
			HitCount:     hitCount,
			TokensSaved:  tokensSaved,
		})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("PersistentLRUStore.Hydrate rows: %w", err)
	}
	return nil
}

// Get queries the in-memory LRU cache.
func (p *PersistentLRUStore) Get(ctx context.Context, key string) (Entry, bool) {
	return p.lru.Get(ctx, key)
}

// Put updates in-memory LRU and writes through to SQLite.
func (p *PersistentLRUStore) Put(ctx context.Context, key string, e Entry) error {
	if err := p.lru.Put(ctx, key, e); err != nil {
		return err
	}
	if p.db != nil {
		storedAtStr := formatStoredAt(e.StoredAt)
		_, _ = p.db.ExecContext(ctx, `
INSERT INTO semanticCacheEntries (
	key, model, responseBody, contentType, storedAt, hitCount, tokensSaved
) VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(key) DO UPDATE SET
	model = excluded.model, responseBody = excluded.responseBody,
	contentType = excluded.contentType, storedAt = excluded.storedAt,
	hitCount = excluded.hitCount, tokensSaved = excluded.tokensSaved`,
			key, e.Model, e.ResponseBody, e.ContentType, storedAtStr, e.HitCount, e.TokensSaved,
		)
	}
	return nil
}

// Delete removes from in-memory LRU and SQLite.
func (p *PersistentLRUStore) Delete(ctx context.Context, key string) bool {
	ok := p.lru.Delete(ctx, key)
	if p.db != nil {
		_, _ = p.db.ExecContext(ctx, `DELETE FROM semanticCacheEntries WHERE key = ?`, key)
	}
	return ok
}

// Len returns the in-memory entry count.
func (p *PersistentLRUStore) Len() int {
	return p.lru.Len()
}

// DBLen returns the persistent SQLite entry count.
func (p *PersistentLRUStore) DBLen() int {
	if p.db == nil {
		return 0
	}
	var count int
	_ = p.db.QueryRow(`SELECT COUNT(*) FROM semanticCacheEntries`).Scan(&count)
	return count
}

// Clear removes all entries from both memory and SQLite.
func (p *PersistentLRUStore) Clear() {
	p.lru.Clear()
	if p.db != nil {
		_, _ = p.db.Exec(`DELETE FROM semanticCacheEntries`)
	}
}

// Entries returns active entries from the in-memory LRU.
func (p *PersistentLRUStore) Entries() []Entry {
	return p.lru.Entries()
}

// InvalidateByModel deletes matching entries in both memory and SQLite.
func (p *PersistentLRUStore) InvalidateByModel(ctx context.Context, model string) int {
	count := p.lru.InvalidateByModel(ctx, model)
	if p.db != nil {
		_, _ = p.db.ExecContext(ctx, `DELETE FROM semanticCacheEntries WHERE model = ?`, model)
	}
	return count
}

// InvalidateByModelAndPrefix deletes matching entries in both memory and SQLite.
// The in-memory LRU filter matches the model case-insensitively; the SQLite
// delete matches it exactly (case-sensitive), so on a case-mismatched request
// the in-memory entry and its disk row are removed by different predicates. The
// prefix matches any key whose stored key starts with keyPrefix (keys are hex
// digests, so no LIKE escaping is needed). An empty model with a non-empty
// prefix matches all models with that key prefix; an empty prefix drops the
// key predicate.
func (p *PersistentLRUStore) InvalidateByModelAndPrefix(ctx context.Context, model, keyPrefix string) int {
	count := p.lru.InvalidateByModelAndPrefix(ctx, model, keyPrefix)
	if p.db != nil {
		query := `DELETE FROM semanticCacheEntries`
		args := []any{}
		if model != "" && keyPrefix != "" {
			query = `DELETE FROM semanticCacheEntries WHERE model = ? AND key LIKE ?`
			args = []any{model, keyPrefix + "%"}
		} else if model != "" {
			query = `DELETE FROM semanticCacheEntries WHERE model = ?`
			args = []any{model}
		} else if keyPrefix != "" {
			query = `DELETE FROM semanticCacheEntries WHERE key LIKE ?`
			args = []any{keyPrefix + "%"}
		}
		_, _ = p.db.ExecContext(ctx, query, args...)
	}
	return count
}

// InvalidateOlderThan deletes expired entries in both memory and SQLite.
func (p *PersistentLRUStore) InvalidateOlderThan(ctx context.Context, d time.Duration) int {
	count := p.lru.InvalidateOlderThan(ctx, d)
	if p.db != nil {
		cutoff := formatStoredAt(time.Now().Add(-d))
		_, _ = p.db.ExecContext(ctx, `DELETE FROM semanticCacheEntries WHERE storedAt < ?`, cutoff)
	}
	return count
}

// RecordHit increments hit counters in memory and SQLite.
func (p *PersistentLRUStore) RecordHit(ctx context.Context, key string, tokensSaved int64) {
	p.lru.RecordHit(ctx, key, tokensSaved)
	if p.db != nil {
		_, _ = p.db.ExecContext(ctx, `
UPDATE semanticCacheEntries
SET hitCount = hitCount + 1, tokensSaved = tokensSaved + ?
WHERE key = ?`, tokensSaved, key)
	}
}
