//go:build integration

package integration

import (
	"net/http"
	"testing"
	"time"

	"9router/proxy/internal/auth"
	"9router/proxy/internal/handlerutil"
)

// TestLedgerPrunePreservesKeyQuotaEnforcement drives the whole chain:
//  1. A key with a budget spends past it in the ledger.
//  2. The middleware refuses with 429.
//  3. POST /api/settings/database/prune-usage deletes the ledger rows that pushed
//     it over the budget.
//  4. The very next request through the real router is STILL refused with 429,
//     and /api/keys reports the exact same usedTokens count because the carryover
//     kept the spend intact.
func TestLedgerPrunePreservesKeyQuotaEnforcement(t *testing.T) {
	env, _ := newProviderEnv(t)
	id, plaintext := limitKey(t, env, "ledger-prune")

	// Set a 100-token budget.
	if put := env.Put(t, "/api/keys/"+id, map[string]any{"tokenQuota": 100}); put.Status != http.StatusOK {
		t.Fatalf("set token quota: status %d: %s", put.Status, truncate(put.Body))
	}

	// Seed 200 tokens 60 days ago: this exhausts the key on old history alone.
	now := time.Now().UTC()
	masked := handlerutil.MaskAPIKey(plaintext)
	if _, err := env.Repo.RawDB().Exec(`
		INSERT INTO usageHistory (timestamp, provider, model, connectionId, apiKey, clientKey, endpoint, promptTokens, completionTokens, cost, status, tokens, meta)
		VALUES (?, 'deepseek', 'deepseek-chat', 'c1', 'cred', ?, '/v1/chat/completions', 150, 50, 0.01, 'success', '{}', '')`,
		now.Add(-60*24*time.Hour).Format(time.RFC3339), masked,
	); err != nil {
		t.Fatalf("seed usageHistory: %v", err)
	}

	// First request must be refused before dispatch.
	rejectedBefore := env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-chat", false), WithAPIKey(plaintext))
	if rejectedBefore.Status != http.StatusTooManyRequests {
		t.Fatalf("expected 429 before prune, got %d: %s", rejectedBefore.Status, truncate(rejectedBefore.Body))
	}
	// Read spend through the dashboard listing API
	usedBefore := keyUsageFromList(t, env, id)
	if usedBefore != 200 {
		t.Fatalf("usedTokens before prune = %d, want 200", usedBefore)
	}

	// Prune the ledger older than 30 days: deletes the 60-day-old row.
	//
	// The CLI token is required, not decorative: prune-usage is an
	// always-protected path (it deletes billing rows), so the dashboard session
	// alone is refused with 401. That is the same gate /prune and /vacuum sit
	// behind, and this test is where a route accidentally left off that list
	// would show up.
	prune := env.Post(t, "/api/settings/database/prune-usage", map[string]any{"days": 30},
		WithoutAPIKey(), WithHeader(auth.CLITokenHeader, auth.CLIToken()))
	if prune.Status != http.StatusOK {
		t.Fatalf("prune-usage = %d: %s", prune.Status, truncate(prune.Body))
	}
	var res struct {
		Success       bool `json:"success"`
		Pruned        int  `json:"pruned"`
		CarriedTokens int  `json:"carriedTokens"`
	}
	prune.Decode(t, &res)
	if !res.Success || res.Pruned != 1 || res.CarriedTokens != 200 {
		t.Fatalf("unexpected prune result: %+v", res)
	}

	// INVARIANT 1: The very next request through the router must STILL be refused with 429.
	// If the prune refunded spend, this would slip through as 200.
	rejectedAfter := env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-chat", false), WithAPIKey(plaintext))
	if rejectedAfter.Status != http.StatusTooManyRequests {
		t.Fatalf("CRITICAL REGRESSION: key was un-exhausted by ledger prune! got %d: %s", rejectedAfter.Status, truncate(rejectedAfter.Body))
	}

	// INVARIANT 2: /api/keys must report the exact same usedTokens as before the prune.
	usedAfter := keyUsageFromList(t, env, id)
	if usedAfter != usedBefore {
		t.Fatalf("usedTokens after prune = %d, want %d", usedAfter, usedBefore)
	}

	// And the route stays gated: a caller holding only a dashboard session must be
	// refused. It deletes billing rows, so leaving it off the always-protected
	// list would let an unauthenticated local caller truncate the ledger whenever
	// requireLogin is off — the mistake this very PR made and fixed.
	beforeRefused := countLedgerRows(t, env)
	unauthorised := env.Post(t, "/api/settings/database/prune-usage", map[string]any{"days": 30})
	if unauthorised.Status == http.StatusOK {
		t.Fatal("prune-usage accepted a caller with no admin credential")
	}
	if afterRefused := countLedgerRows(t, env); afterRefused != beforeRefused {
		t.Errorf("usageHistory rows after the refused call = %d, want %d: a refused prune must delete nothing",
			afterRefused, beforeRefused)
	}
}

func countLedgerRows(t *testing.T, env *Env) int {
	t.Helper()
	var n int
	if err := env.Repo.RawDB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM usageHistory`).Scan(&n); err != nil {
		t.Fatalf("count usageHistory: %v", err)
	}
	return n
}
