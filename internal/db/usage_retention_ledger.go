package db

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"fmt"
	"log"
	"slices"
	"time"

	"9router/proxy/internal/handlerutil"
)

// Ledger retention.
//
// usageHistory is the billing ledger: the `all` usage period reads it, the
// analytics sections scan it, and a per-key tokenQuota is measured from it. That
// last reader is what makes trimming the ledger non-trivial. Spend is *derived*
// from rows rather than kept as a counter (the property that makes it
// impossible to drift or double-count — see quota_reset.go), so deleting an old
// row silently refunds whatever that row cost: an exhausted key quietly becomes
// unexhausted.
//
// The carryover closes that hole without giving up the derivation. Before a
// range of rows is deleted, the tokens those rows contributed to each key's
// budget are summed into usageLedgerCarryover, and tokenUsageSince adds that
// term back. One number per key, written only when rows it accounts for
// disappear — and exactly one reader learns about it, instead of every ledger
// consumer having to filter synthetic rows out of the ledger itself.
//
// Only tokens are carried, not cost. Nothing bills from `cost` today (it is
// telemetry; see the budget notes in docs/keirouter-port-plan.md), so there is
// nothing to keep exact yet. The day a per-period USD budget is measured from
// this table, the carryover has to grow a cost column or that budget moves when
// an operator prunes.

// LedgerPruneResult reports what one ledger prune removed.
type LedgerPruneResult struct {
	// Rows is how many usageHistory rows were deleted.
	Rows int `json:"rows"`
	// CarriedTokens is how many prompt+completion tokens were moved into the
	// carryover so per-key budgets stayed exact.
	CarriedTokens int `json:"carriedTokens"`
	// Cutoff is the exclusive upper bound the prune used, as stored
	// (RFC3339, UTC, second precision).
	Cutoff string `json:"cutoff"`
}

// ledgerRow is one doomed usageHistory row, reduced to what the carryover needs.
type ledgerRow struct {
	rowid     int64
	clientKey string
	apiKey    string
	provider  string
	tokens    int64
}

// ledgerWatchedKey is one apiKeys row a carryover can be attributed to.
type ledgerWatchedKey struct {
	id   string
	mark int64
}

// ledgerWatchIndex indexes the watched keys by the two spellings a ledger row
// can carry, so folding a chunk is a map lookup per row rather than a scan of
// the key list. providers maps each watched providerNodes id to its own reset
// mark: usageHistory.provider holds the node id verbatim for a custom provider,
// so that dimension needs one exact match and no spelled-form index. A nil map
// means the dimension was never loaded, which is how a partial fixture without a
// providerNodes table still prunes.
type ledgerWatchIndex struct {
	keys      []ledgerWatchedKey
	byMask    map[string][]int
	byRaw     map[string][]int
	providers map[string]int64
}

// PruneUsageHistory deletes usageHistory rows older than retention, carrying the
// per-key token spend of the deleted rows into usageLedgerCarryover first.
//
// Each chunk is folded and deleted in one transaction, so a crash between two
// chunks can never leave a deleted row whose spend was not carried (which would
// refund a key) or a carried row that still exists (which would charge it
// twice). The transaction is bounded by retentionChunkSize, so the write lock is
// never held for the length of a full-table delete.
func (r *Repo) PruneUsageHistory(ctx context.Context, retention time.Duration) (LedgerPruneResult, error) {
	var result LedgerPruneResult
	if r == nil || r.db == nil {
		return result, fmt.Errorf("prune usageHistory: nil repo")
	}
	if ctx == nil {
		return result, fmt.Errorf("prune usageHistory: nil context")
	}
	if retention <= 0 {
		return result, fmt.Errorf("prune usageHistory: retention must be positive, got %v", retention)
	}

	cutoff := ledgerCutoff(retention)
	result.Cutoff = cutoff

	watched, err := r.ledgerWatchedKeys(ctx)
	if err != nil {
		return result, err
	}

	cursor := int64(0)
	for {
		if err := ctx.Err(); err != nil {
			return result, fmt.Errorf("prune usageHistory by age: %w", err)
		}
		chunk, err := r.readLedgerChunk(ctx, cursor, cutoff)
		if err != nil {
			return result, err
		}
		if len(chunk) == 0 {
			break
		}
		carried, err := r.deleteLedgerChunk(ctx, chunk, watched)
		if err != nil {
			return result, err
		}
		cursor = chunk[len(chunk)-1].rowid
		result.Rows += len(chunk)
		result.CarriedTokens += int(carried.total())
	}

	if result.Rows > 0 {
		// The deleted rows left the planner's statistics describing a table that
		// is no longer there, and the statements that read the ledger are the
		// ones the dashboard polls. Fail-open: the rows are already gone, so a
		// statistics refresh that cannot run costs query plans, not correctness.
		if _, err := r.db.ExecContext(ctx, `PRAGMA optimize`); err != nil && ctx.Err() == nil {
			log.Printf("[RETENTION] ledger PRAGMA optimize failed: %v", err)
		}
	}
	return result, nil
}

// CountExpiredUsageHistory reports how many rows a ledger prune would remove,
// without removing anything. It backs the dashboard's dry run, which is how an
// operator sees the size of the deletion before agreeing to it.
func (r *Repo) CountExpiredUsageHistory(ctx context.Context, retention time.Duration) (int, string, error) {
	if r == nil || r.db == nil {
		return 0, "", fmt.Errorf("count expired usageHistory: nil repo")
	}
	if retention <= 0 {
		return 0, "", fmt.Errorf("count expired usageHistory: retention must be positive, got %v", retention)
	}
	cutoff := ledgerCutoff(retention)
	var rows int
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM usageHistory WHERE timestamp < ?`, cutoff,
	).Scan(&rows); err != nil {
		return 0, cutoff, fmt.Errorf("count expired usageHistory: %w", err)
	}
	return rows, cutoff, nil
}

// ledgerCutoff is the exclusive upper bound of the retained window.
//
// Truncated to the second because that is the precision this build writes
// (RFC3339), so the bound compares against a stored timestamp without inventing
// sub-second ordering the column does not have. A row stamped inside the cutoff
// second may land on either side of it; a window measured in days cannot notice.
func ledgerCutoff(retention time.Duration) string {
	return time.Now().UTC().Add(-retention).Truncate(time.Second).Format(time.RFC3339)
}

// ledgerWatchedKeys loads the keys and the providers a carryover can be
// attributed to, indexed by the spellings tokenUsageSince matches on.
//
// Every key is watched, not only the ones carrying a tokenQuota today: a
// carryover for a key with no budget is never read, but it is also never wrong,
// and it means an operator who sets a quota tomorrow is measured against spend
// that already happened instead of against a ledger with a hole in it. The same
// reasoning watches every provider node, whose tokenQuota needs provider
// carryover for exactly the same reason.
func (r *Repo) ledgerWatchedKeys(ctx context.Context) (*ledgerWatchIndex, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, COALESCE(key, ''), COALESCE(`+quotaResetColumn+`, 0) FROM apiKeys`)
	if err != nil {
		return nil, fmt.Errorf("read api keys for carryover: %w", err)
	}
	defer func() { _ = rows.Close() }()

	index := &ledgerWatchIndex{byMask: map[string][]int{}, byRaw: map[string][]int{}}
	for rows.Next() {
		var id, rawKey string
		var mark int64
		if err := rows.Scan(&id, &rawKey, &mark); err != nil {
			return nil, fmt.Errorf("scan api key for carryover: %w", err)
		}
		if id == "" || rawKey == "" {
			// A key with no secret can never match a ledger row, and the
			// limiter short-circuits it before reading any spend, so there is
			// nothing a carryover for it could hold.
			continue
		}
		index.add(id, rawKey, mark)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate api keys for carryover: %w", err)
	}

	// Carryover rows orphaned by a key deletion are unreachable by the reader
	// (which joins apiKeys) and would otherwise sit in the table forever.
	if _, err := r.db.ExecContext(ctx,
		`DELETE FROM usageLedgerCarryover WHERE keyId NOT IN (SELECT id FROM apiKeys)`,
	); err != nil {
		return nil, fmt.Errorf("drop orphaned usage carryover: %w", err)
	}
	if err := r.loadProviderMarks(ctx, index); err != nil {
		return nil, err
	}
	return index, nil
}

// loadProviderMarks fills the index's provider dimension.
//
// providerNodes is probed rather than assumed: a fixture that builds only part
// of the schema still gets a working prune, with the dimension left nil, instead
// of failing on a table it never asked for. Both the refresh and the fold read a
// nil map as "nothing to carry".
func (r *Repo) loadProviderMarks(ctx context.Context, index *ledgerWatchIndex) error {
	exists, err := tableExistsOnDB(r.db, "providerNodes")
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, COALESCE(`+providerQuotaResetColumn+`, 0) FROM providerNodes`)
	if err != nil {
		return fmt.Errorf("read provider nodes for carryover: %w", err)
	}
	defer func() { _ = rows.Close() }()

	marks := map[string]int64{}
	for rows.Next() {
		var id string
		var mark int64
		if err := rows.Scan(&id, &mark); err != nil {
			return fmt.Errorf("scan provider node for carryover: %w", err)
		}
		if id == "" {
			continue
		}
		marks[id] = mark
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate provider nodes for carryover: %w", err)
	}
	index.providers = marks

	// The same orphan rule as the key dimension: the reader resolves provider
	// carryover by node id, so a row whose node is gone can never be read again.
	if _, err := r.db.ExecContext(ctx,
		`DELETE FROM providerLedgerCarryover WHERE providerId NOT IN (SELECT id FROM providerNodes)`,
	); err != nil {
		return fmt.Errorf("drop orphaned provider carryover: %w", err)
	}
	return nil
}

func (w *ledgerWatchIndex) add(id, rawKey string, mark int64) {
	i := len(w.keys)
	w.keys = append(w.keys, ledgerWatchedKey{id: id, mark: mark})
	mask := handlerutil.MaskAPIKey(rawKey)
	w.byMask[mask] = append(w.byMask[mask], i)
	w.byRaw[rawKey] = append(w.byRaw[rawKey], i)
}

// refreshMarks re-reads every watched key's and provider's reset mark through
// the caller's transaction, so the marks a chunk is folded against are the ones
// current at the instant the carryover commits.
//
// It only updates the marks in place; both watched lists are left alone, because
// a key or node created after the prune started cannot own a row this pass is
// deleting (that row predates its first request).
func (w *ledgerWatchIndex) refreshMarks(ctx context.Context, tx *sql.Tx) error {
	if err := w.refreshKeyMarks(ctx, tx); err != nil {
		return err
	}
	return w.refreshProviderMarks(ctx, tx)
}

// refreshKeyMarks refreshes the client-key half of the index.
func (w *ledgerWatchIndex) refreshKeyMarks(ctx context.Context, tx *sql.Tx) error {
	if len(w.keys) == 0 {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, COALESCE(`+quotaResetColumn+`, 0) FROM apiKeys`)
	if err != nil {
		return fmt.Errorf("refresh reset marks for carryover: %w", err)
	}
	defer func() { _ = rows.Close() }()

	marks := make(map[string]int64, len(w.keys))
	for rows.Next() {
		var id string
		var mark int64
		if err := rows.Scan(&id, &mark); err != nil {
			return fmt.Errorf("scan reset mark for carryover: %w", err)
		}
		marks[id] = mark
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate reset marks for carryover: %w", err)
	}
	for i := range w.keys {
		if mark, ok := marks[w.keys[i].id]; ok {
			w.keys[i].mark = mark
		}
	}
	return nil
}

// refreshProviderMarks refreshes the provider half, for the reset that lands
// mid-prune and would otherwise bank spend a provider quota had just forgiven.
func (w *ledgerWatchIndex) refreshProviderMarks(ctx context.Context, tx *sql.Tx) error {
	if w.providers == nil {
		return nil
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT id, COALESCE(`+providerQuotaResetColumn+`, 0) FROM providerNodes`)
	if err != nil {
		return fmt.Errorf("refresh provider reset marks for carryover: %w", err)
	}
	defer func() { _ = rows.Close() }()

	marks := make(map[string]int64, len(w.providers))
	for rows.Next() {
		var id string
		var mark int64
		if err := rows.Scan(&id, &mark); err != nil {
			return fmt.Errorf("scan provider reset mark for carryover: %w", err)
		}
		marks[id] = mark
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate provider reset marks for carryover: %w", err)
	}
	for id := range w.providers {
		if mark, ok := marks[id]; ok {
			w.providers[id] = mark
		}
	}
	return nil
}

// ledgerCarry is what one folded chunk contributes to the two carryover tables:
// tokens per client key for usageLedgerCarryover, and tokens per custom provider
// node for providerLedgerCarryover.
type ledgerCarry struct {
	keys      map[string]int64
	providers map[string]int64
}

// total is every token this chunk carried, summed over both dimensions: the
// per-key carryover and the per-provider-node carryover. It is a sum of two
// ledgers, not a count of distinct tokens — a row that belongs to a watched key
// *and* a watched node is carried into both tables and counted twice here, which
// is correct for the question the number answers (how much spend this prune
// stopped refunding) and wrong for "how many tokens were deleted". `Rows` is the
// deletion figure.
func (c ledgerCarry) total() int64 {
	var sum int64
	for _, tokens := range c.keys {
		sum += tokens
	}
	for _, tokens := range c.providers {
		sum += tokens
	}
	return sum
}

// fold sums, per key and per provider, what this chunk contributed to those
// budgets.
//
// The three key lookups are exactly the three terms tokenUsageSince sums: the
// row's clientKey against the key's masked form, and the row's apiKey against
// both the raw and the masked form (rows the upstream Next.js dashboard wrote
// put the client key in apiKey; this build puts the upstream credential there).
// Keeping the two in step is what makes the reader's answer identical across a
// prune.
//
// A row is counted once per key even when it matches through more than one term,
// and only when it sits after that key's reset mark: rows at or before the mark
// were already excluded from the budget before they were deleted, so carrying
// them would inflate a key that had been deliberately reset. The provider
// dimension carries the same guard against the node's own mark, and needs no
// dedupe because a row names at most one node.
//
// The two dimensions are folded in separate passes rather than interleaved: the
// chunk is bounded by retentionChunkSize, and one loop per budget reads better
// than one loop serving two unrelated match rules.
func (w *ledgerWatchIndex) fold(chunk []ledgerRow) ledgerCarry {
	var carried ledgerCarry
	carried.keys = w.foldKeys(chunk)
	carried.providers = w.foldProviders(chunk)
	return carried
}

// foldKeys is the client-key half of fold.
func (w *ledgerWatchIndex) foldKeys(chunk []ledgerRow) map[string]int64 {
	if len(w.keys) == 0 {
		return nil
	}
	carried := make(map[string]int64)
	var candidates []int
	for _, row := range chunk {
		if row.tokens == 0 {
			continue
		}
		candidates = append(candidates[:0], w.byMask[row.clientKey]...)
		candidates = append(candidates, w.byRaw[row.apiKey]...)
		candidates = append(candidates, w.byMask[row.apiKey]...)
		for i := range candidates {
			if slices.Contains(candidates[:i], candidates[i]) {
				continue
			}
			key := &w.keys[candidates[i]]
			if row.rowid <= key.mark {
				continue
			}
			carried[key.id] += row.tokens
		}
	}
	if len(carried) == 0 {
		return nil
	}
	return carried
}

// foldProviders is the custom-provider half of fold.
//
// The match is exact: usageHistory.provider carries the providerNodes id the
// request resolved to, so a row belongs to at most one node and a node's spend
// survives a prune only if its own mark says the rows still counted.
func (w *ledgerWatchIndex) foldProviders(chunk []ledgerRow) map[string]int64 {
	if len(w.providers) == 0 {
		return nil
	}
	carried := make(map[string]int64)
	for _, row := range chunk {
		if row.tokens == 0 || row.provider == "" {
			continue
		}
		mark, watched := w.providers[row.provider]
		if !watched || row.rowid <= mark {
			continue
		}
		carried[row.provider] += row.tokens
	}
	if len(carried) == 0 {
		return nil
	}
	return carried
}

// readLedgerChunk reads the next chunk of doomed rows in rowid order.
//
// Ordered by rowid rather than by timestamp so the cursor advances monotonically
// and the whole prune is one sequential pass, however many chunks it takes. The
// token columns are CAST because a row whose INTEGER-affinity column holds
// non-numeric text (an import, or a hand-edited database) would otherwise fail
// the scan and block retention forever on every pass.
func (r *Repo) readLedgerChunk(ctx context.Context, cursor int64, cutoff string) ([]ledgerRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT rowid, COALESCE(clientKey, ''), COALESCE(apiKey, ''), COALESCE(provider, ''),
		       CAST(COALESCE(promptTokens, 0) AS INTEGER) + CAST(COALESCE(completionTokens, 0) AS INTEGER)
		  FROM usageHistory
		 WHERE rowid > ? AND timestamp < ?
		 ORDER BY rowid
		 LIMIT ?`, cursor, cutoff, retentionChunkSize)
	if err != nil {
		return nil, fmt.Errorf("read usageHistory chunk: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var chunk []ledgerRow
	for rows.Next() {
		var row ledgerRow
		if err := rows.Scan(&row.rowid, &row.clientKey, &row.apiKey, &row.provider, &row.tokens); err != nil {
			return nil, fmt.Errorf("scan usageHistory chunk: %w", err)
		}
		chunk = append(chunk, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate usageHistory chunk: %w", err)
	}
	return chunk, nil
}

// deleteLedgerChunk carries the chunk's spend and deletes the chunk, in one
// transaction. The rowids travel as a JSON array so the delete is bounded by the
// rows that were folded: only rows whose spend was carried are removed.
//
// The reset marks are re-read inside this transaction rather than reused from
// PruneUsageHistory's up-front snapshot. A reset that commits mid-prune raises a
// key's mark past the doomed rows and clears its carryover; folding against the
// older mark would re-bank spend that reset had just forgiven, and the key would
// be charged for it again. Reading the marks here — in the same transaction that
// writes the carryover and deletes the rows — makes "which mark decided what to
// carry" and "which rows are gone" the same instant.
func (r *Repo) deleteLedgerChunk(ctx context.Context, chunk []ledgerRow, watched *ledgerWatchIndex) (ledgerCarry, error) {
	rowids := make([]int64, 0, len(chunk))
	for _, row := range chunk {
		rowids = append(rowids, row.rowid)
	}
	encoded, err := json.Marshal(rowids)
	if err != nil {
		return ledgerCarry{}, fmt.Errorf("encode usageHistory chunk rowids: %w", err)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return ledgerCarry{}, fmt.Errorf("prune usageHistory: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit

	if err := watched.refreshMarks(ctx, tx); err != nil {
		return ledgerCarry{}, err
	}
	carried := watched.fold(chunk)

	now := time.Now().UTC().Format(time.RFC3339)
	for _, keyID := range sortedKeyIDs(carried.keys) {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO usageLedgerCarryover (keyId, tokens, updatedAt) VALUES (?, ?, ?)
			ON CONFLICT(keyId) DO UPDATE SET tokens = tokens + excluded.tokens, updatedAt = excluded.updatedAt`,
			keyID, carried.keys[keyID], now,
		); err != nil {
			return ledgerCarry{}, fmt.Errorf("carry usage tokens for key %s: %w", keyID, err)
		}
	}
	for _, nodeID := range sortedKeyIDs(carried.providers) {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO providerLedgerCarryover (providerId, tokens, updatedAt) VALUES (?, ?, ?)
			ON CONFLICT(providerId) DO UPDATE SET tokens = tokens + excluded.tokens, updatedAt = excluded.updatedAt`,
			nodeID, carried.providers[nodeID], now,
		); err != nil {
			return ledgerCarry{}, fmt.Errorf("carry usage tokens for provider %s: %w", nodeID, err)
		}
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM usageHistory WHERE rowid IN (SELECT value FROM json_each(?))`, string(encoded),
	); err != nil {
		return ledgerCarry{}, fmt.Errorf("delete usageHistory chunk: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return ledgerCarry{}, fmt.Errorf("prune usageHistory: commit: %w", err)
	}
	return carried, nil
}

// sortedKeyIDs orders the carryover writes so a prune produces the same
// statement sequence every run. It serves both tables: the key and the provider
// dimension are each a map from row id to carried tokens.
func sortedKeyIDs(carried map[string]int64) []string {
	ids := make([]string, 0, len(carried))
	for id := range carried {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}
