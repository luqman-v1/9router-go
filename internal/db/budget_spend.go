package db

import (
	"context"
	"fmt"

	"9router/proxy/internal/budget"
	"9router/proxy/internal/handlerutil"
)

// Per-period budget spend (F-2).
//
// The lifetime token quota measures from a row-id high-water mark on the key
// row, because a lifetime sum never needs a window. A per-period budget does:
// it is the same ledger read bounded by a timestamp floor instead, which is
// what ResolveWindow produces for daily/weekly/monthly. "total" is the one
// period with no floor, and it reads the whole ledger exactly as the lifetime
// quota does.
//
// The attribution terms are the three the lifetime reader already matches —
// the client key's masked form in clientKey, plus the raw and masked
// spellings in apiKey — so a row written by the upstream Next.js dashboard
// counts here too rather than silently dropping out of the budget.
//
// Cost is converted to integer micros exactly once, here. usageHistory.cost
// is a float in USD; the engine that refuses a request compares integers, so
// no float reaches the decision.

// budgetSpendQuery sums cost and tokens for one key inside a window. The
// predicate is the same OR across clientKey and apiKey the token-quota reader
// uses, which idx_uh_clientkey_tok and idx_uh_apikey_tok answer; the timestamp
// floor is served by idx_uh_ts, so the statement is answered from indexes
// rather than a ledger scan.
const budgetSpendQuery = `SELECT
  COALESCE(SUM(CAST(ROUND(COALESCE(cost, 0.0) * 1000000) AS INTEGER)), 0),
  COALESCE(SUM(COALESCE(promptTokens,0) + COALESCE(completionTokens,0)), 0)
FROM usageHistory
WHERE (clientKey = ? OR apiKey = ? OR apiKey = ?)`

// SpendForKeySince returns the key's cost and token spend since the given
// timestamp floor. An empty since reads the whole ledger.
//
// A key with no history is zero spend, not an error — that is a fresh key.
func (r *Repo) SpendForKeySince(ctx context.Context, rawKey, since string) (budget.Spend, error) {
	if r == nil || r.db == nil || rawKey == "" {
		return budget.Spend{}, nil
	}
	masked := handlerutil.MaskAPIKey(rawKey)
	query := budgetSpendQuery
	args := []any{masked, rawKey, masked}
	if since != "" {
		query += ` AND timestamp >= ?`
		args = append(args, since)
	}
	var s budget.Spend
	if err := r.db.QueryRowContext(ctx, query, args...).Scan(&s.Micros, &s.Tokens); err != nil {
		return budget.Spend{}, fmt.Errorf("budget spend for key: %w", err)
	}
	return s, nil
}
