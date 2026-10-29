package db

import (
	"context"
	"fmt"
	"testing"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/models"
)

// The ledger scale below mirrors the production install this endpoint was
// profiled against: 5 client keys whose request counts (24,053 / 205 / 178 /
// 0 / 83,956) put roughly 108k rows in usageHistory, written the way this
// gateway writes them (apiKey = upstream credential, clientKey = masked
// caller key).
func seedApiKeysUsageBench(b *testing.B) (*Repo, []*models.APIKey) {
	b.Helper()

	dir := b.TempDir()
	conn, err := OpenDatabase(dir + "/data.sqlite")
	if err != nil {
		b.Fatalf("OpenDatabase: %v", err)
	}
	b.Cleanup(func() { conn.Close() })
	if err := EnsureCoreSchema(conn); err != nil {
		b.Fatalf("EnsureCoreSchema: %v", err)
	}
	r := NewRepo(conn)

	specs := []struct {
		id   string
		rows int
	}{
		{"k1", 24053}, {"k2", 205}, {"k3", 178}, {"k4", 0}, {"k5", 83956},
	}

	type seed struct {
		raw    string
		masked string
		rowN   int
	}
	seeds := make([]seed, 0, len(specs))
	for i, s := range specs {
		raw := fmt.Sprintf("sk-%08x%08x%08x%08x", i, i+7, i+13, i+29)
		if err := r.CreateApiKey(s.id, raw, s.id, ""); err != nil {
			b.Fatalf("CreateApiKey: %v", err)
		}
		seeds = append(seeds, seed{raw: raw, masked: handlerutil.MaskAPIKey(raw), rowN: s.rows})
	}

	tx, err := r.db.Begin()
	if err != nil {
		b.Fatalf("begin seed tx: %v", err)
	}
	stmt, err := tx.Prepare(`INSERT INTO usageHistory
		(provider, model, connectionId, apiKey, endpoint, promptTokens, completionTokens, cost, status, tokens, meta, clientKey, timestamp)
		VALUES ('openai','gpt-4o','conn','upstream-node-1','/v1/chat/completions',50000,59000,0,'success','{}','{}',?,'2026-10-20T00:00:00Z')`)
	if err != nil {
		b.Fatalf("prepare seed stmt: %v", err)
	}
	// A tx-prepared statement is closed by Commit; the deferred close only
	// covers the Fatalf exits before that.
	defer stmt.Close()
	for _, s := range seeds {
		for j := 0; j < s.rowN; j++ {
			if _, err := stmt.Exec(s.masked); err != nil {
				b.Fatalf("seed usage row: %v", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatalf("commit seed tx: %v", err)
	}

	keys := make([]*models.APIKey, 0, len(specs))
	for _, s := range specs {
		k, err := r.GetApiKeyByID(s.id)
		if err != nil || k == nil {
			b.Fatalf("GetApiKeyByID(%s): %v", s.id, err)
		}
		keys = append(keys, k)
	}
	return r, keys
}

// BenchmarkApiKeyTokenUsageByID is the cost GET /api/keys paid before the
// batched read: one usage read per key row, what apiKeyUsedTokens looped over.
func BenchmarkApiKeyTokenUsageByID(b *testing.B) {
	r, keys := seedApiKeysUsageBench(b)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		total := 0
		for _, k := range keys {
			used, err := r.GetApiKeyTokenUsageByID(ctx, k.ID)
			if err != nil {
				b.Fatalf("GetApiKeyTokenUsageByID: %v", err)
			}
			total += used
		}
		if total == 0 {
			b.Fatal("expected nonzero usage")
		}
	}
}

// BenchmarkApiKeyTokenUsageForKeys is the batched read: two statements for
// the whole page.
func BenchmarkApiKeyTokenUsageForKeys(b *testing.B) {
	r, keys := seedApiKeysUsageBench(b)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		usage, err := r.GetApiKeyTokenUsageForKeys(ctx, keys)
		if err != nil {
			b.Fatalf("GetApiKeyTokenUsageForKeys: %v", err)
		}
		total := 0
		for _, used := range usage {
			total += used
		}
		if total == 0 {
			b.Fatal("expected nonzero usage")
		}
	}
}
