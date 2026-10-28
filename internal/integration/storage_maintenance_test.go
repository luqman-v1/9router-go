//go:build integration

package integration

import (
	"net/http"
	"testing"
	"time"

	"9router/proxy/internal/handlerutil"
)

// The storage card in the profile page offers three destructive actions: prune
// the diagnostics, prune the billing ledger, and compact the file. They sit on
// the always-protected list because they delete rows.
//
// An install that does not require a login has no session to present — the SPA
// authenticates with nothing at all in that mode — so the gate refused every
// press of those buttons with "Unauthorized: admin session or CLI token
// required". The operator saw a working dashboard whose storage actions could
// only be run from a curl carrying the CLI token.
//
// This drives the real router with the credential the dashboard actually sends
// in that mode: none at all.
func TestStorageMaintenanceWorksWithoutAdminCredential(t *testing.T) {
	env := newEnv(t)

	// The dashboard state the bug was reported in: login disabled.
	if err := env.Repo.UpdateSettingsRaw(map[string]any{"requireLogin": false}); err != nil {
		t.Fatalf("disable login: %v", err)
	}

	// A ledger row old enough to be pruned, so the action has real work to do.
	now := time.Now().UTC()
	if _, err := env.Repo.RawDB().Exec(`
		INSERT INTO usageHistory (timestamp, provider, model, connectionId, apiKey, clientKey, endpoint, promptTokens, completionTokens, cost, status, tokens, meta)
		VALUES (?, 'deepseek', 'deepseek-chat', 'c1', 'cred', 'cred', '/v1/chat/completions', 100, 50, 0.01, 'success', '{}', '')`,
		now.Add(-90*24*time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatalf("seed usageHistory: %v", err)
	}

	// The dry run the button issues first.
	dry := env.Post(t, "/api/settings/database/prune-usage?dryRun=1&days=30",
		map[string]any{"days": 30}, WithoutAPIKey())
	if dry.Status != http.StatusOK {
		t.Fatalf("prune-usage dry run without an admin credential = %d: %s", dry.Status, truncate(dry.Body))
	}
	var preview struct {
		DryRun      bool `json:"dryRun"`
		WouldDelete int  `json:"wouldDelete"`
	}
	dry.Decode(t, &preview)
	if !preview.DryRun {
		t.Error("expected dryRun=true in the preview response")
	}
	if preview.WouldDelete != 1 {
		t.Errorf("dry run would delete %d rows, want 1", preview.WouldDelete)
	}

	// The action itself.
	prune := env.Post(t, "/api/settings/database/prune-usage",
		map[string]any{"days": 30}, WithoutAPIKey())
	if prune.Status != http.StatusOK {
		t.Fatalf("prune-usage without an admin credential = %d: %s", prune.Status, truncate(prune.Body))
	}
	var res struct {
		Success bool `json:"success"`
		Pruned  int  `json:"pruned"`
	}
	prune.Decode(t, &res)
	if !res.Success || res.Pruned != 1 {
		t.Fatalf("unexpected prune result: %+v", res)
	}
	if rows := countLedgerRows(t, env); rows != 0 {
		t.Errorf("usageHistory rows after the prune = %d, want 0", rows)
	}

	// The other two buttons of the same card.
	for _, path := range []string{"/api/settings/database/prune", "/api/settings/database/vacuum"} {
		got := env.Post(t, path, nil, WithoutAPIKey())
		if got.Status != http.StatusOK {
			t.Errorf("POST %s without an admin credential = %d: %s", path, got.Status, truncate(got.Body))
		}
	}

	// The backup export is NOT one of them: it hands out the whole database,
	// keys included, so it keeps requiring a session or the CLI token.
	export := env.Get(t, "/api/settings/database", WithoutAPIKey())
	if export.Status != http.StatusUnauthorized {
		t.Errorf("anonymous backup export = %d, want 401: the export carries every key", export.Status)
	}
}

// Admitting the caller must not change what a prune does to a per-key budget.
// The ledger prune is the one operation that deletes billing rows, and the
// spend in those rows is what a tokenQuota is measured from — so a prune that
// dropped the spend would refund an exhausted key. The carryover exists for
// exactly that, and this pins that it still holds on the path the dashboard
// takes.
func TestAnonymousPruneStillCarriesKeyQuota(t *testing.T) {
	env := newEnv(t)

	if err := env.Repo.UpdateSettingsRaw(map[string]any{"requireLogin": false}); err != nil {
		t.Fatalf("disable login: %v", err)
	}

	id, plaintext := limitKey(t, env, "anon-prune")
	if put := env.Put(t, "/api/keys/"+id, map[string]any{"tokenQuota": 100}); put.Status != http.StatusOK {
		t.Fatalf("set token quota: status %d: %s", put.Status, truncate(put.Body))
	}

	// Seed 200 tokens of old history: it exhausts the key on its own.
	now := time.Now().UTC()
	masked := handlerutil.MaskAPIKey(plaintext)
	if _, err := env.Repo.RawDB().Exec(`
		INSERT INTO usageHistory (timestamp, provider, model, connectionId, apiKey, clientKey, endpoint, promptTokens, completionTokens, cost, status, tokens, meta)
		VALUES (?, 'deepseek', 'deepseek-chat', 'c1', 'cred', ?, '/v1/chat/completions', 150, 50, 0.01, 'success', '{}', '')`,
		now.Add(-90*24*time.Hour).Format(time.RFC3339), masked); err != nil {
		t.Fatalf("seed usageHistory: %v", err)
	}

	usedBefore := keyUsageFromList(t, env, id)
	if usedBefore != 200 {
		t.Fatalf("usedTokens before the prune = %d, want 200", usedBefore)
	}

	// The prune the dashboard issues, with no credential.
	prune := env.Post(t, "/api/settings/database/prune-usage",
		map[string]any{"days": 30}, WithoutAPIKey())
	if prune.Status != http.StatusOK {
		t.Fatalf("prune-usage = %d: %s", prune.Status, truncate(prune.Body))
	}

	// The spend must survive: the rows are gone, the bill is not.
	if usedAfter := keyUsageFromList(t, env, id); usedAfter != usedBefore {
		t.Errorf("usedTokens after the prune = %d, want %d: the prune refunded the key", usedAfter, usedBefore)
	}

	// And the key is still refused, because its budget is still spent.
	rejected := env.Post(t, "/v1/chat/completions",
		ChatBody("deepseek/deepseek-chat", false), WithAPIKey(plaintext))
	if rejected.Status != http.StatusForbidden {
		t.Errorf("request through the exhausted key = %d, want 403", rejected.Status)
	}
}
