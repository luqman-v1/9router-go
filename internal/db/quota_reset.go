package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/models"
)

// Resetting a key's token budget (#365).
//
// tokenQuota is a lifetime ceiling and the spend it is measured against is
// derived from usageHistory rather than kept as a counter. That derivation is
// the property worth keeping — there is no counter to drift, replay or
// double-count under concurrency — but it also meant an operator had no way to
// lift an exhausted key without deleting its usage rows.
//
// quotaResetId is that missing knob: a high-water mark on the key row, holding
// the usageHistory row id the key's budget was last reset at. Spend is summed
// from that mark forward, so a reset is one small write and the history stays
// intact for analytics.
//
// It is a row id and NOT a timestamp on purpose. usageHistory.timestamp is
// RFC3339 at second precision, so a reset issued in the same second as the
// spend that exhausted the key would compare equal and keep charging the key —
// exactly the case an operator hits when they reset a key they just watched
// fail. The autoincrement id is monotonic per insert, so the boundary is exact
// at any rate, and `id > ?` needs no clock agreement between the two writes.
const quotaResetColumn = "quotaResetId"

// ResetApiKeyQuota marks a key as fresh from the current end of its history and
// returns the mark. Requests from now on spend against a new budget while
// usageHistory keeps recording every row.
//
// The read and the write share one transaction so a request landing between
// them cannot slip its usage row on the wrong side of the boundary: the mark
// always covers everything already recorded, and never anything after it.
//
// A key that does not exist is reported as such rather than silently accepted:
// the operator asked for a specific key, and a 200 for a key that is not there
// would hide a stale dashboard row.
func (r *Repo) ResetApiKeyQuota(ctx context.Context, keyID string) (int, error) {
	if r == nil || r.db == nil {
		return 0, fmt.Errorf("reset api key quota: nil repo")
	}
	if keyID == "" {
		return 0, fmt.Errorf("reset api key quota: empty apiKey id")
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("reset api key quota %s: begin: %w", keyID, err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit

	var watermark int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM usageHistory`).Scan(&watermark); err != nil {
		return 0, fmt.Errorf("reset api key quota %s: read history mark: %w", keyID, err)
	}

	res, err := tx.ExecContext(ctx, `UPDATE apiKeys SET `+quotaResetColumn+` = ? WHERE id = ?`, watermark, keyID)
	if err != nil {
		return 0, fmt.Errorf("reset api key quota %s: %w", keyID, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return 0, errNoSuchApiKey
	}

	// The carryover holds spend that lived in rows the ledger prune deleted. A
	// reset says "measure from here", and "here" is after everything already
	// recorded — including that carried spend. Leaving it would charge the key
	// for tokens it had just been forgiven, so it is cleared in the same
	// transaction as the mark: the reader can never observe a new mark with the
	// old carryover still counted.
	if _, err := tx.ExecContext(ctx, `DELETE FROM usageLedgerCarryover WHERE keyId = ?`, keyID); err != nil {
		return 0, fmt.Errorf("reset api key quota %s: clear carried usage: %w", keyID, err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("reset api key quota %s: commit: %w", keyID, err)
	}
	return int(watermark), nil
}

// QuotaResetID returns the usageHistory row id a key's budget is measured from,
// or 0 when it has never been reset. An unknown key, and a database created
// before this column existed, both answer 0 — which is the pre-#365 lifetime
// sum, not an error.
func (r *Repo) QuotaResetID(ctx context.Context, keyID string) (int, error) {
	if r == nil || r.db == nil || keyID == "" {
		return 0, nil
	}
	var mark sql.NullInt64
	err := r.db.QueryRowContext(ctx,
		`SELECT `+quotaResetColumn+` FROM apiKeys WHERE id = ? LIMIT 1`, keyID,
	).Scan(&mark)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, nil
	}
	if mark.Valid && mark.Int64 > 0 {
		return int(mark.Int64), nil
	}
	return 0, nil
}

// quotaResetIDForKey resolves the mark by the key's own secret column, which is
// what the limiter holds. An unknown value has no mark and keeps the lifetime
// sum.
func (r *Repo) quotaResetIDForKey(ctx context.Context, rawKey string) (int, error) {
	if r == nil || r.db == nil || rawKey == "" {
		return 0, nil
	}
	var mark sql.NullInt64
	err := r.db.QueryRowContext(ctx,
		`SELECT `+quotaResetColumn+` FROM apiKeys WHERE key = ? LIMIT 1`, rawKey,
	).Scan(&mark)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, nil
	}
	if mark.Valid && mark.Int64 > 0 {
		return int(mark.Int64), nil
	}
	return 0, nil
}

// apiKeyTokenUsageBatchQuery renders the one-statement page read for n keys.
// Extracted so the query plan can be asserted on the exact text the reader
// runs: the OR must stay answerable through idx_uh_clientkey and
// idx_uh_apikey, or the statement degrades to a ledger scan per key again.
func apiKeyTokenUsageBatchQuery(n int) string {
	rows := make([]string, n)
	for i := range rows {
		rows[i] = "(?,?,?,?)"
	}
	return `WITH k(keyId, rawKey, maskedKey, mark) AS (VALUES ` + strings.Join(rows, ",") + `)
SELECT k.keyId,
  (SELECT COALESCE(SUM(COALESCE(u.promptTokens,0) + COALESCE(u.completionTokens,0)), 0)
     FROM usageHistory u
     WHERE (u.clientKey = k.maskedKey OR u.apiKey = k.rawKey OR u.apiKey = k.maskedKey)
       AND u.id > k.mark)
  + COALESCE((SELECT tokens FROM usageLedgerCarryover WHERE keyId = k.keyId), 0)
  AS used
FROM k`
}

// GetApiKeyTokenUsageForKeys returns every key's quota spend in one statement,
// keyed by apiKey id. The listing handler pays this once per page; the per-key
// read it replaces paid three queries and one ledger index scan per row.
//
// Each row's spend is computed by the same terms GetApiKeyTokenUsageByID uses
// for a single key: the history sum over the key's three recorded spellings
// (masked in clientKey, raw and masked in apiKey), bounded below by the key's
// reset mark, plus its carryover. The marks are read once per page and travel
// through the ledger statement as a CTE column, so a key reset between two
// listings starts at zero here exactly as it does for the limiter.
//
// A key with no recorded spend is present in the map with 0, keeping the
// caller's "absent == unspent" rule uniform. A schema without quotaResetId —
// a pre-#365 database — is not an error here: the batched statement is
// skipped and the per-key reader, which already degrades that column to a
// lifetime sum, answers instead. So the batched read is a fast path, not a
// contract; its failure never changes the numbers the dashboard shows.
func (r *Repo) GetApiKeyTokenUsageForKeys(ctx context.Context, keys []*models.APIKey) (map[string]int, error) {
	out := make(map[string]int, len(keys))
	if r == nil || r.db == nil || len(keys) == 0 {
		return out, nil
	}

	placeholders := make([]string, 0, len(keys))
	ids := make([]any, 0, len(keys))
	seen := make(map[string]bool, len(keys))
	pending := make([]*models.APIKey, 0, len(keys))
	for _, k := range keys {
		if k == nil || k.ID == "" || k.Key == "" || seen[k.ID] {
			continue
		}
		seen[k.ID] = true
		out[k.ID] = 0
		pending = append(pending, k)
		placeholders = append(placeholders, "?")
		ids = append(ids, k.ID)
	}
	if len(placeholders) == 0 {
		return out, nil
	}

	// The reset marks are read into the page before the ledger statement, and
	// travel through it as a CTE column: inside the history sum's WHERE a
	// mark fetched per candidate row cost 2.5x the per-key readers on a
	// 108k-row page, while a mark read once per page is five primary-key hits.
	// This read is also where a pre-#365 schema — no quotaResetId column —
	// declares itself: that failure falls back to the per-key reader, which
	// already degrades the missing column to a lifetime sum.
	marks := make(map[string]int64, len(ids))
	markRows, err := r.db.QueryContext(ctx,
		`SELECT id, `+quotaResetColumn+` FROM apiKeys WHERE id IN (`+strings.Join(placeholders, ",")+`)`,
		ids...)
	if err != nil {
		return r.apiKeyTokenUsageByRow(ctx, pending, out)
	}
	defer func() { _ = markRows.Close() }()

	for markRows.Next() {
		var keyID string
		var mark sql.NullInt64
		if err := markRows.Scan(&keyID, &mark); err != nil {
			return nil, fmt.Errorf("get api key token usage for keys: read mark: %w", err)
		}
		if mark.Valid {
			marks[keyID] = mark.Int64
		}
	}
	if err := markRows.Err(); err != nil {
		return nil, fmt.Errorf("get api key token usage for keys: read mark: %w", err)
	}

	args := make([]any, 0, 4*len(pending))
	cteRows := make([]string, 0, len(pending))
	for _, k := range pending {
		cteRows = append(cteRows, "(?,?,?,?)")
		args = append(args, k.ID, k.Key, handlerutil.MaskAPIKey(k.Key), marks[k.ID])
	}

	rows, err := r.db.QueryContext(ctx, apiKeyTokenUsageBatchQuery(len(cteRows)), args...)
	if err != nil {
		return r.apiKeyTokenUsageByRow(ctx, pending, out)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var keyID string
		var used sql.NullInt64
		if err := rows.Scan(&keyID, &used); err != nil {
			return nil, fmt.Errorf("get api key token usage for keys: scan: %w", err)
		}
		out[keyID] = int(used.Int64)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get api key token usage for keys: %w", err)
	}
	return out, nil
}

// apiKeyTokenUsageByRow resolves a page's spend through the per-key reader,
// preserving that reader's own degradation rules for callers on a schema the
// batched statement cannot compile against.
func (r *Repo) apiKeyTokenUsageByRow(ctx context.Context, keys []*models.APIKey, out map[string]int) (map[string]int, error) {
	for _, k := range keys {
		used, err := r.GetApiKeyTokenUsageByID(ctx, k.ID)
		if err != nil {
			// The per-key read already answers 0 on the errors a listing
			// tolerates; anything else fails the page the same way a single
			// read would have.
			return nil, err
		}
		out[k.ID] = used
	}
	return out, nil
}

// GetApiKeyTokenUsageByID is the dashboard's reader: it resolves the key's
// history spellings and reset mark from the row the id names, so the
// operator's progress bar and the limiter's own refusal are computed from the
// same numbers.
func (r *Repo) GetApiKeyTokenUsageByID(ctx context.Context, keyID string) (int, error) {
	if r == nil || r.db == nil || keyID == "" {
		return 0, nil
	}
	row, err := r.GetApiKeyByID(keyID)
	if err != nil {
		return 0, err
	}
	if row == nil {
		return 0, nil
	}
	mark, err := r.QuotaResetID(ctx, keyID)
	if err != nil {
		return 0, err
	}
	return r.tokenUsageSince(ctx, row.Key, mark)
}

// tokenUsageSince sums a key's prompt+completion tokens recorded after a reset
// mark. A mark of 0 means "the key's whole life", which is the pre-#365 answer.
//
// Matching is by the raw key's masked form as well, because that is what this
// gateway writes into usageHistory (maskAPIKey) and rows the upstream Next.js
// dashboard wrote keep the full key: both spellings are summed.
//
// clientKey is the column this gateway writes the caller's own key into (#365).
// The apiKey terms keep rows written by the upstream Next.js dashboard counting:
// that ledger stored the client key in apiKey, where this one stores the upstream
// credential, so both spellings are summed.
//
// The carryover term is what keeps the sum exact once an operator prunes the
// ledger (usage_retention_ledger.go): spend that lived in deleted rows is kept in
// usageLedgerCarryover and added back here. It is resolved by the same key
// secret this query already holds rather than by an id, so no caller has to know
// one. A key whose row is absent resolves to no carryover, which is the same
// answer as a key that never had one — and the ledger prune drops orphaned
// carryover rows, so that only happens for a key created after a prune.
//
// The history sum and the carryover are read in one statement so a prune landing
// between two reads cannot report a spend that is neither the before nor the
// after value.
//
// No index is added for the history predicate: it is an OR across two columns,
// which no single-column index serves, and the alternative — a join on the
// apiKeys table — costs more than the scan it replaces. The carryover lookup is a
// primary-key hit on a table with one row per client key.
func (r *Repo) tokenUsageSince(ctx context.Context, rawKey string, mark int) (int, error) {
	if rawKey == "" {
		return 0, nil
	}
	history := `COALESCE(SUM(COALESCE(promptTokens,0) + COALESCE(completionTokens,0)), 0)
	   FROM usageHistory WHERE (clientKey = ? OR apiKey = ? OR apiKey = ?)`
	args := []any{handlerutil.MaskAPIKey(rawKey), rawKey, handlerutil.MaskAPIKey(rawKey)}
	if mark > 0 {
		history += ` AND id > ?`
		args = append(args, mark)
	}
	query := `SELECT (SELECT ` + history + `)
	   + COALESCE((SELECT tokens FROM usageLedgerCarryover
	                WHERE keyId = (SELECT id FROM apiKeys WHERE key = ? LIMIT 1)), 0)`
	args = append(args, rawKey)

	var used int64
	if err := r.db.QueryRowContext(ctx, query, args...).Scan(&used); err != nil {
		return 0, fmt.Errorf("get api key token usage: %w", err)
	}
	return int(used), nil
}
