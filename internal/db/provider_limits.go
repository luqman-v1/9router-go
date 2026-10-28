package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Per-custom-provider capacity limits (docs/provider-limits-plan.md).
//
// A custom provider is one providerNodes row, and every connection under it talks
// to the same upstream endpoint. These limits therefore hang off the node:
// rpmLimit, tpmLimit and concurrencyLimit bound what the endpoint itself
// tolerates, queueTimeoutMs is how long a request may wait in the provider's FIFO
// for one of those slots, tokenQuota is a lifetime ceiling on the tokens routed
// through the node, and contractExpiresAt is the day the subscription behind it
// stops being usable.
//
// This is a different axis from the per-key limits (F-1/F-3): a key's limit is one
// caller's allowance, a node's limit is capacity. Both apply, and the key is
// checked first so a caller that has spent its own budget cannot consume a node's
// capacity on the way to being refused.
//
// 0 means unlimited and an empty contractExpiresAt means no expiry, the same
// convention apiKeys.rateLimitRPM and apiKeys.expiresAt already use.

// ErrNoSuchProviderNode is returned by the writers when the node id names no row.
// A reader answers the zero value instead (see GetProviderLimits), because an
// unknown node and an unlimited one are the same policy as far as a caller is
// concerned; a caller that must tell them apart reads the node row itself.
var ErrNoSuchProviderNode = errors.New("no such provider node")

// providerQuotaResetColumn is the providerNodes twin of quotaResetColumn: the
// usageHistory row id a node's tokenQuota is measured from. It carries the same
// name on purpose — it means the same thing in both tables.
const providerQuotaResetColumn = "quotaResetId"

// ProviderLimits is the capacity policy of one custom provider node.
type ProviderLimits struct {
	// RPM is requests per minute and TPM is prompt tokens per minute, both
	// measured over the limiter's sliding window. 0 = unlimited.
	RPM int
	TPM int
	// Concurrency is how many requests may be in flight against the node at
	// once. 0 = unlimited.
	Concurrency int
	// QueueTimeoutMs is how long a request may wait for an RPM or concurrency
	// slot before it gives up and is refused. 0 = do not wait at all.
	QueueTimeoutMs int
	// TokenQuota is a lifetime ceiling on prompt+completion tokens routed
	// through the node. 0 = no ceiling.
	TokenQuota int
	// QuotaResetID is the usageHistory row id TokenQuota is measured from. It is
	// written by ResetProviderQuota, never by SetProviderLimits.
	QuotaResetID int
	// ContractExpiresAt is an RFC3339 UTC timestamp, or "" for no expiry.
	ContractExpiresAt string
}

// providerLimitColumns is the shared column list for every provider limit read.
const providerLimitColumns = `rpmLimit, tpmLimit, concurrencyLimit, tokenQuota, queueTimeoutMs, quotaResetId, COALESCE(contractExpiresAt, '')`

// scanProviderLimits fills limits from a row shaped like providerLimitColumns.
func scanProviderLimits(scan func(dest ...any) error, limits *ProviderLimits) error {
	return scan(
		&limits.RPM, &limits.TPM, &limits.Concurrency, &limits.TokenQuota,
		&limits.QueueTimeoutMs, &limits.QuotaResetID, &limits.ContractExpiresAt,
	)
}

// Expired reports whether the node's contract had run out by now. An empty
// timestamp is no expiry at all.
//
// An unparseable timestamp reads as "not expired" rather than as expired. The
// writers normalise what they store (SetProviderLimits), so the only way to get
// one is a hand-edited database, and taking a working node offline over a typo
// would be the more expensive mistake.
func (l ProviderLimits) Expired(now time.Time) bool {
	if l.ContractExpiresAt == "" {
		return false
	}
	until, err := time.Parse(time.RFC3339, l.ContractExpiresAt)
	if err != nil {
		return false
	}
	return now.After(until)
}

// GetProviderLimits reads the limits of one node. A node that does not exist
// answers the zero value, which reads as unlimited.
func (r *Repo) GetProviderLimits(ctx context.Context, nodeID string) (ProviderLimits, error) {
	if r == nil || r.db == nil || nodeID == "" {
		return ProviderLimits{}, nil
	}
	var limits ProviderLimits
	err := scanProviderLimits(r.db.QueryRowContext(ctx,
		`SELECT `+providerLimitColumns+` FROM providerNodes WHERE id = ? LIMIT 1`, nodeID,
	).Scan, &limits)
	if errors.Is(err, sql.ErrNoRows) {
		return ProviderLimits{}, nil
	}
	if err != nil {
		return ProviderLimits{}, fmt.Errorf("get provider limits %s: %w", nodeID, err)
	}
	return limits, nil
}

// ListProviderLimits reads every node's limits, keyed by node id.
//
// One query rather than a read per node: the dashboard lists every node with its
// policy, and a per-node read there would be N+1 on a page load.
func (r *Repo) ListProviderLimits(ctx context.Context) (map[string]ProviderLimits, error) {
	if r == nil || r.db == nil {
		return nil, nil
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id, `+providerLimitColumns+` FROM providerNodes`)
	if err != nil {
		return nil, fmt.Errorf("list provider limits: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]ProviderLimits{}
	for rows.Next() {
		var id string
		var limits ProviderLimits
		if err := rows.Scan(&id,
			&limits.RPM, &limits.TPM, &limits.Concurrency, &limits.TokenQuota,
			&limits.QueueTimeoutMs, &limits.QuotaResetID, &limits.ContractExpiresAt,
		); err != nil {
			return nil, fmt.Errorf("scan provider limits: %w", err)
		}
		out[id] = limits
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate provider limits: %w", err)
	}
	return out, nil
}

// SetProviderLimits writes the whole policy of one node.
//
// The write is total, not a merge: the dashboard's PUT reads the row, applies the
// operator's edit and hands the result here, so "field omitted" is decided by the
// caller that can see the previous value. Numbers are rejected when negative — the
// column has no CHECK constraint, and a negative count would reach the limiter as
// either "block everything" or "unlimited" depending on which comparison it landed
// in — and an unparseable contract timestamp is refused rather than stored,
// because the column is parsed on every dispatch and a value that never parses
// would silently mean "no expiry".
//
// quotaResetId is deliberately not written here: it is the reset mark
// (ResetProviderQuota), and letting a policy save move it would either forgive
// spend the node actually made or charge it twice.
func (r *Repo) SetProviderLimits(ctx context.Context, nodeID string, limits ProviderLimits) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("set provider limits: nil repo")
	}
	if nodeID == "" {
		return fmt.Errorf("set provider limits: empty node id")
	}
	if limits.RPM < 0 || limits.TPM < 0 || limits.Concurrency < 0 ||
		limits.TokenQuota < 0 || limits.QueueTimeoutMs < 0 {
		return fmt.Errorf("set provider limits %s: limits are counts and cannot be negative", nodeID)
	}
	expiresAt := strings.TrimSpace(limits.ContractExpiresAt)
	if expiresAt != "" {
		parsed, err := time.Parse(time.RFC3339, expiresAt)
		if err != nil {
			return fmt.Errorf("set provider limits %s: contractExpiresAt must be RFC3339: %w", nodeID, err)
		}
		expiresAt = parsed.UTC().Format(time.RFC3339)
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE providerNodes
		   SET rpmLimit = ?, tpmLimit = ?, concurrencyLimit = ?, tokenQuota = ?,
		       queueTimeoutMs = ?, contractExpiresAt = ?
		 WHERE id = ?`,
		limits.RPM, limits.TPM, limits.Concurrency, limits.TokenQuota,
		limits.QueueTimeoutMs, expiresAt, nodeID,
	)
	if err != nil {
		return fmt.Errorf("set provider limits %s: %w", nodeID, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNoSuchProviderNode
	}
	return nil
}
