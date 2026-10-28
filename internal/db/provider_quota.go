package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Resetting a custom provider's token budget.
//
// Mirrors the per-key budget reset (#365, quota_reset.go) because it is the same
// problem: tokenQuota is a lifetime ceiling *derived* from usageHistory rather
// than kept as a counter, which is the property that keeps it impossible to drift
// or double-count, and it also means lifting an exhausted ceiling needs a
// high-water mark rather than a counter write. The mark is the usageHistory row id
// the node's budget was last reset at, and spend is summed from that mark forward,
// so one small write restores a node while the history analytics reads stays
// intact.
//
// The mark is a row id and not a timestamp for the reason spelled out in
// quota_reset.go: usageHistory.timestamp is second-precision, so a reset issued in
// the same second as the spend that exhausted the node would compare equal and
// keep charging it.
//
// providerLedgerCarryover exists for the same reason usageLedgerCarryover does: a
// retention prune deletes ledger rows, and without a provider-keyed carryover
// those deletions would silently refund the node's budget (see the fold in
// usage_retention_ledger.go).

// ResetProviderQuota marks a node as fresh from the current end of the ledger and
// returns the mark.
//
// The read and the write share one transaction so a request landing between them
// cannot slip its usage row onto the wrong side of the boundary: the mark always
// covers everything already recorded and never anything after it.
//
// The node's carryover is cleared in that same transaction. The mark says "measure
// from here", and "here" is after everything already recorded — including spend
// that lived in rows a prune deleted — so leaving it would charge the node for
// tokens it had just been forgiven.
//
// A node that does not exist is reported rather than silently accepted, the same
// way ResetApiKeyQuota refuses an unknown key.
func (r *Repo) ResetProviderQuota(ctx context.Context, nodeID string) (int, error) {
	if r == nil || r.db == nil {
		return 0, fmt.Errorf("reset provider quota: nil repo")
	}
	if nodeID == "" {
		return 0, fmt.Errorf("reset provider quota: empty node id")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("reset provider quota %s: begin: %w", nodeID, err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit

	var watermark int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(id), 0) FROM usageHistory`).Scan(&watermark); err != nil {
		return 0, fmt.Errorf("reset provider quota %s: read history mark: %w", nodeID, err)
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE providerNodes SET `+providerQuotaResetColumn+` = ? WHERE id = ?`, watermark, nodeID)
	if err != nil {
		return 0, fmt.Errorf("reset provider quota %s: %w", nodeID, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return 0, ErrNoSuchProviderNode
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM providerLedgerCarryover WHERE providerId = ?`, nodeID); err != nil {
		return 0, fmt.Errorf("reset provider quota %s: clear carried usage: %w", nodeID, err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("reset provider quota %s: commit: %w", nodeID, err)
	}
	return int(watermark), nil
}

// ProviderQuotaResetID returns the usageHistory row id a node's budget is measured
// from, or 0 when it has never been reset. An unknown node, and a database created
// before this column existed, both answer 0 — which is the lifetime sum, not an
// error. It mirrors QuotaResetID, including that fail-open shape: this reader sits
// in front of every dispatch, and a transient read failure must not take a node
// offline by pretending its budget is spent.
func (r *Repo) ProviderQuotaResetID(ctx context.Context, nodeID string) (int, error) {
	if r == nil || r.db == nil || nodeID == "" {
		return 0, nil
	}
	var mark sql.NullInt64
	err := r.db.QueryRowContext(ctx,
		`SELECT `+providerQuotaResetColumn+` FROM providerNodes WHERE id = ? LIMIT 1`, nodeID,
	).Scan(&mark)
	if errors.Is(err, sql.ErrNoRows) {
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

// ProviderTokenUsage sums the prompt+completion tokens routed through a node since
// its reset mark, plus whatever a prune carried over.
//
// Matching is one exact column comparison because usageHistory.provider holds the
// providerNodes id the request resolved to (resolution.go returns node.ID for a
// custom provider), which idx_uh_prov_tok serves directly — and serves as a
// covering read, so the sum does not go back to the table once per matching
// row. There is no OR across spellings here, unlike the key reader: a node
// has exactly one name on the wire side of this gateway.
//
// The history sum and the carryover are read in one statement so a prune landing
// between two reads cannot report a spend that is neither the before nor the after
// value.
func (r *Repo) ProviderTokenUsage(ctx context.Context, nodeID string) (int, error) {
	if r == nil || r.db == nil || nodeID == "" {
		return 0, nil
	}
	mark, err := r.ProviderQuotaResetID(ctx, nodeID)
	if err != nil {
		return 0, err
	}
	history := `COALESCE(SUM(COALESCE(promptTokens, 0) + COALESCE(completionTokens, 0)), 0)
	   FROM usageHistory WHERE provider = ?`
	args := []any{nodeID}
	if mark > 0 {
		history += ` AND id > ?`
		args = append(args, mark)
	}
	query := `SELECT (SELECT ` + history + `)
	   + COALESCE((SELECT tokens FROM providerLedgerCarryover WHERE providerId = ?), 0)`
	args = append(args, nodeID)

	var used int64
	if err := r.db.QueryRowContext(ctx, query, args...).Scan(&used); err != nil {
		return 0, fmt.Errorf("get provider token usage: %w", err)
	}
	return int(used), nil
}

// ProviderTokenUsageByNode sums the same spend as ProviderTokenUsage for every
// node, keyed by node id. The dashboard lists every node with its policy and
// usage, so a read per node there would be N+1 on a page load.
//
// The map holds one entry per node and nothing else. The statement it
// replaced aggregated the whole ledger by provider, so it also returned an
// entry for every registry provider that had spent anything — entries no
// caller of this function ever read, because the caller looks a node's spend
// up by the node's own id.
//
// The statement walks providerNodes and reads each node's spend through a
// correlated seek rather than aggregating the whole ledger. The aggregate
// version grouped every ledger row by provider, which on a 400k-row ledger
// took 1.5 s for a list of 200 nodes — and the cost grew with the ledger, not
// with the number of nodes, because the scan visited rows belonging to
// providers that are not custom nodes at all. The seek reads only the rows of
// the node being asked about, so the list costs one indexed lookup per node
// and stays flat as the ledger grows.
//
// Each node keeps its own reset mark, so the mark is read from the node's own
// row. The carryover is added rather than joined, so a node whose history a
// prune already deleted still reports the tokens that were carried over (a
// join would drop exactly those rows). A node with neither history nor
// carryover reports zero, which is what the caller reads.
func (r *Repo) ProviderTokenUsageByNode(ctx context.Context) (map[string]int, error) {
	usage := map[string]int{}
	if r == nil || r.db == nil {
		return usage, nil
	}
	rows, err := r.db.QueryContext(ctx, providerTokenUsageByNodeQuery)
	if err != nil {
		return nil, fmt.Errorf("get provider token usage by node: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var nodeID string
		var used int64
		if err := rows.Scan(&nodeID, &used); err != nil {
			return nil, fmt.Errorf("get provider token usage by node: %w", err)
		}
		usage[nodeID] = int(used)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get provider token usage by node: %w", err)
	}
	return usage, nil
}

// providerTokenUsageByNodeQuery is the statement ProviderTokenUsageByNode runs.
// It is a named constant because the query-plan test pins the plan: the spend
// of every node must be read by an index seek, and a plan that falls back to
// scanning the ledger is the regression this statement exists to prevent.
const providerTokenUsageByNodeQuery = `
	SELECT n.id,
	       COALESCE((SELECT SUM(COALESCE(h.promptTokens, 0) + COALESCE(h.completionTokens, 0))
	                   FROM usageHistory h
	                  WHERE h.provider = n.id AND h.id > COALESCE(n.quotaResetId, 0)), 0)
	     + COALESCE((SELECT c.tokens FROM providerLedgerCarryover c WHERE c.providerId = n.id), 0)
	  FROM providerNodes n`
