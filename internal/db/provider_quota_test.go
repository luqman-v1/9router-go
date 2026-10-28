package db

import (
	"context"
	"errors"
	"testing"
	"time"
)

// seedProviderLedgerRow inserts one usageHistory row attributed to a custom
// provider node, with exact tokens and a timestamp the prune can act on.
func seedProviderLedgerRow(t *testing.T, repo *Repo, id int64, provider string, prompt, compl int, at time.Time) {
	t.Helper()
	ts := at.UTC().Truncate(time.Second).Format(time.RFC3339)
	var err error
	if id > 0 {
		_, err = repo.RawDB().Exec(
			`INSERT INTO usageHistory (id, timestamp, provider, model, connectionId, apiKey, clientKey, endpoint, promptTokens, completionTokens, cost, status, tokens, meta)
			 VALUES (?, ?, ?, 'glm-5.3', 'conn-1', 'upstream-cred', 'unrelated-client', '/v1/chat/completions', ?, ?, 0.01, 'success', '{}', '')`,
			id, ts, provider, prompt, compl)
	} else {
		_, err = repo.RawDB().Exec(
			`INSERT INTO usageHistory (timestamp, provider, model, connectionId, apiKey, clientKey, endpoint, promptTokens, completionTokens, cost, status, tokens, meta)
			 VALUES (?, ?, 'glm-5.3', 'conn-1', 'upstream-cred', 'unrelated-client', '/v1/chat/completions', ?, ?, 0.01, 'success', '{}', '')`,
			ts, provider, prompt, compl)
	}
	if err != nil {
		t.Fatalf("seed usageHistory for %s: %v", provider, err)
	}
}

func providerCarryover(t *testing.T, repo *Repo, nodeID string) int {
	t.Helper()
	var tokens int
	err := repo.RawDB().QueryRow(
		`SELECT COALESCE((SELECT tokens FROM providerLedgerCarryover WHERE providerId = ?), 0)`, nodeID,
	).Scan(&tokens)
	if err != nil {
		t.Fatalf("read provider carryover for %s: %v", nodeID, err)
	}
	return tokens
}

func TestProviderTokenUsage_SumsOnlyThisNode(t *testing.T) {
	repo := openEphemeralSchemaDB(t)
	ctx := context.Background()
	seedProviderNode(t, repo, "node-nara", "Nara AI")
	seedProviderNode(t, repo, "node-kopi", "Kopi AI")
	at := time.Now().UTC().Add(-time.Hour)

	seedProviderLedgerRow(t, repo, 0, "node-nara", 300, 50, at)
	seedProviderLedgerRow(t, repo, 0, "node-nara", 100, 25, at)
	seedProviderLedgerRow(t, repo, 0, "node-kopi", 1_000, 500, at)

	got, err := repo.ProviderTokenUsage(ctx, "node-nara")
	if err != nil {
		t.Fatalf("ProviderTokenUsage: %v", err)
	}
	if got != 475 {
		t.Errorf("node-nara usage = %d, want 475 (its own rows only)", got)
	}
}

func TestProviderTokenUsage_RespectsTheResetMark(t *testing.T) {
	repo := openEphemeralSchemaDB(t)
	ctx := context.Background()
	seedProviderNode(t, repo, "node-nara", "Nara AI")
	at := time.Now().UTC().Add(-time.Hour)
	seedProviderLedgerRow(t, repo, 1, "node-nara", 300, 50, at)
	seedProviderLedgerRow(t, repo, 2, "node-nara", 100, 25, at)

	if _, err := repo.ResetProviderQuota(ctx, "node-nara"); err != nil {
		t.Fatalf("ResetProviderQuota: %v", err)
	}
	// Everything recorded before the reset is forgiven; a row written after it
	// must still count.
	seedProviderLedgerRow(t, repo, 3, "node-nara", 40, 10, at)

	got, err := repo.ProviderTokenUsage(ctx, "node-nara")
	if err != nil {
		t.Fatalf("ProviderTokenUsage: %v", err)
	}
	if got != 50 {
		t.Errorf("usage after a reset = %d, want 50 (only the post-reset row)", got)
	}
}

func TestProviderTokenUsage_AddsTheCarryover(t *testing.T) {
	repo := openEphemeralSchemaDB(t)
	ctx := context.Background()
	seedProviderNode(t, repo, "node-nara", "Nara AI")
	if _, err := repo.RawDB().Exec(
		`INSERT INTO providerLedgerCarryover (providerId, tokens, updatedAt) VALUES (?, ?, ?)`,
		"node-nara", 900, time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		t.Fatalf("seed carryover: %v", err)
	}
	seedProviderLedgerRow(t, repo, 0, "node-nara", 100, 0, time.Now().UTC().Add(-time.Hour))

	got, err := repo.ProviderTokenUsage(ctx, "node-nara")
	if err != nil {
		t.Fatalf("ProviderTokenUsage: %v", err)
	}
	if got != 1_000 {
		t.Errorf("usage with carryover = %d, want 1000 (900 carried + 100 recorded)", got)
	}
}

func TestResetProviderQuota_UnknownNode(t *testing.T) {
	repo := openEphemeralSchemaDB(t)
	if _, err := repo.ResetProviderQuota(context.Background(), "node-missing"); !errors.Is(err, ErrNoSuchProviderNode) {
		t.Fatalf("ResetProviderQuota on an unknown node = %v, want ErrNoSuchProviderNode", err)
	}
}

func TestResetProviderQuota_ClearsCarryover(t *testing.T) {
	repo := openEphemeralSchemaDB(t)
	ctx := context.Background()
	seedProviderNode(t, repo, "node-nara", "Nara AI")
	if _, err := repo.RawDB().Exec(
		`INSERT INTO providerLedgerCarryover (providerId, tokens, updatedAt) VALUES (?, ?, ?)`,
		"node-nara", 900, time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		t.Fatalf("seed carryover: %v", err)
	}

	if _, err := repo.ResetProviderQuota(ctx, "node-nara"); err != nil {
		t.Fatalf("ResetProviderQuota: %v", err)
	}
	if got := providerCarryover(t, repo, "node-nara"); got != 0 {
		t.Errorf("carryover after a reset = %d, want 0 (the mark already covers it)", got)
	}
	got, err := repo.ProviderTokenUsage(ctx, "node-nara")
	if err != nil {
		t.Fatalf("ProviderTokenUsage: %v", err)
	}
	if got != 0 {
		t.Errorf("usage after a reset with no post-reset rows = %d, want 0", got)
	}
}

// TestPruneUsageHistory_ProviderSpendInvariant proves the property the provider
// carryover exists for: deleting ledger rows must not hand a node back budget it
// already spent. Without providerLedgerCarryover the node would read as partly
// unspent the moment an operator pruned.
func TestPruneUsageHistory_ProviderSpendInvariant(t *testing.T) {
	repo := retentionTestRepo(t)
	ctx := context.Background()
	seedProviderNode(t, repo, "node-nara", "Nara AI")
	seedProviderNode(t, repo, "node-kopi", "Kopi AI")

	now := time.Now().UTC().Truncate(time.Second)
	// node-nara: two rows inside the pruned range (350 + 125) and one survivor (50).
	seedProviderLedgerRow(t, repo, 1, "node-nara", 300, 50, now.Add(-100*24*time.Hour))
	seedProviderLedgerRow(t, repo, 2, "node-nara", 100, 25, now.Add(-40*24*time.Hour))
	seedProviderLedgerRow(t, repo, 3, "node-nara", 40, 10, now.Add(-2*24*time.Hour))
	// A second node whose spend must be carried to its own row, not to nara's.
	seedProviderLedgerRow(t, repo, 4, "node-kopi", 1_000, 500, now.Add(-50*24*time.Hour))

	before, err := repo.ProviderTokenUsage(ctx, "node-nara")
	if err != nil {
		t.Fatalf("usage before prune: %v", err)
	}
	if before != 525 {
		t.Fatalf("usage before prune = %d, want 525", before)
	}

	res, err := repo.PruneUsageHistory(ctx, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("PruneUsageHistory: %v", err)
	}
	if res.Rows != 3 {
		t.Errorf("pruned rows = %d, want 3", res.Rows)
	}
	// Both dimensions report into one number: 475 (nara) + 1500 (kopi). Nothing
	// is carried for a client key here, because no apiKeys row matches these
	// rows' spellings.
	if res.CarriedTokens != 1_975 {
		t.Errorf("carried tokens = %d, want 1975", res.CarriedTokens)
	}

	after, err := repo.ProviderTokenUsage(ctx, "node-nara")
	if err != nil {
		t.Fatalf("usage after prune: %v", err)
	}
	if after != before {
		t.Errorf("node-nara usage across a prune = %d, want %d", after, before)
	}
	if got := providerCarryover(t, repo, "node-nara"); got != 475 {
		t.Errorf("node-nara carryover = %d, want 475", got)
	}
	if got := providerCarryover(t, repo, "node-kopi"); got != 1_500 {
		t.Errorf("node-kopi carryover = %d, want 1500", got)
	}
}

// TestPruneUsageHistory_ProviderResetIsNotReBanked: a reset that lands before a
// prune means the doomed rows were already forgiven, so carrying them would
// charge the node for the same tokens twice.
func TestPruneUsageHistory_ProviderResetIsNotReBanked(t *testing.T) {
	repo := retentionTestRepo(t)
	ctx := context.Background()
	seedProviderNode(t, repo, "node-nara", "Nara AI")
	now := time.Now().UTC().Truncate(time.Second)
	seedProviderLedgerRow(t, repo, 1, "node-nara", 300, 50, now.Add(-100*24*time.Hour))
	seedProviderLedgerRow(t, repo, 2, "node-nara", 100, 25, now.Add(-40*24*time.Hour))

	if _, err := repo.ResetProviderQuota(ctx, "node-nara"); err != nil {
		t.Fatalf("ResetProviderQuota: %v", err)
	}
	if _, err := repo.PruneUsageHistory(ctx, 30*24*time.Hour); err != nil {
		t.Fatalf("PruneUsageHistory: %v", err)
	}
	if got := providerCarryover(t, repo, "node-nara"); got != 0 {
		t.Errorf("carryover after a reset + prune = %d, want 0", got)
	}
	usage, err := repo.ProviderTokenUsage(ctx, "node-nara")
	if err != nil {
		t.Fatalf("ProviderTokenUsage: %v", err)
	}
	if usage != 0 {
		t.Errorf("usage after a reset + prune = %d, want 0", usage)
	}
}
