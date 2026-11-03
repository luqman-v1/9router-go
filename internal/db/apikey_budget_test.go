package db

import (
	"testing"

	"9router/proxy/internal/handlerutil"
)

// TestApiKeyBudgetRoundTrip proves tokenQuota and queueTimeoutMs survive a
// write and a read through the shared column list, and that a fresh key reads
// them as zero (unlimited / server default).
func TestApiKeyBudgetRoundTrip(t *testing.T) {
	r := openEphemeralSchemaDB(t)

	if err := r.CreateApiKey("k1", "sk-test-key-0001", "budget key", "machine-1"); err != nil {
		t.Fatalf("CreateApiKey: %v", err)
	}

	// A freshly created key carries no budget.
	got, err := r.GetApiKeyByID("k1")
	if err != nil {
		t.Fatalf("GetApiKeyByID: %v", err)
	}
	if got == nil {
		t.Fatal("key not found after create")
	}
	if v := derefIntTest(got.TokenQuota); v != 0 {
		t.Errorf("fresh TokenQuota = %d, want 0", v)
	}
	if v := derefIntTest(got.QueueTimeoutMs); v != 0 {
		t.Errorf("fresh QueueTimeoutMs = %d, want 0", v)
	}

	if err := r.SetApiKeyBudget("k1", 250000, 30000); err != nil {
		t.Fatalf("SetApiKeyBudget: %v", err)
	}

	got, err = r.GetApiKeyByID("k1")
	if err != nil {
		t.Fatalf("GetApiKeyByID after set: %v", err)
	}
	if v := derefIntTest(got.TokenQuota); v != 250000 {
		t.Errorf("TokenQuota = %d, want 250000", v)
	}
	if v := derefIntTest(got.QueueTimeoutMs); v != 30000 {
		t.Errorf("QueueTimeoutMs = %d, want 30000", v)
	}

	// The listing path uses the same scanner and must agree.
	keys, err := r.GetApiKeys()
	if err != nil {
		t.Fatalf("GetApiKeys: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("GetApiKeys returned %d keys, want 1", len(keys))
	}
	if v := derefIntTest(keys[0].TokenQuota); v != 250000 {
		t.Errorf("listed TokenQuota = %d, want 250000", v)
	}
	if v := derefIntTest(keys[0].QueueTimeoutMs); v != 30000 {
		t.Errorf("listed QueueTimeoutMs = %d, want 30000", v)
	}
}

// TestGetApiKeyTokenUsageSumsMatchedForms proves the quota reader sums the
// usage rows this gateway writes (masked key) and the ones the upstream
// dashboard wrote (raw key), and returns 0 for a key with no history.
func TestGetApiKeyTokenUsageSumsMatchedForms(t *testing.T) {
	r := openEphemeralSchemaDB(t)

	const rawKey = "sk-quota-test-abcdefghijklmnop"

	if err := r.CreateApiKey("kq", rawKey, "quota key", "m"); err != nil {
		t.Fatalf("CreateApiKey: %v", err)
	}

	// No history yet: zero, not an error.
	used, err := r.GetApiKeyTokenUsage(rawKey)
	if err != nil {
		t.Fatalf("GetApiKeyTokenUsage (empty): %v", err)
	}
	if used != 0 {
		t.Errorf("empty history used = %d, want 0", used)
	}

	// One row under the masked form (what this gateway writes) and one under
	// the raw form (what the upstream dashboard wrote).
	if err := r.InsertUsageHistory("openai", "gpt-4o", "conn1", handlerutil.MaskAPIKey(rawKey), "/v1/chat/completions", 100, 50, 0, "success", 150, "{}", "{}"); err != nil {
		t.Fatalf("InsertUsageHistory masked: %v", err)
	}
	if err := r.InsertUsageHistory("openai", "gpt-4o", "conn1", rawKey, "/v1/chat/completions", 200, 25, 0, "success", 225, "{}", "{}"); err != nil {
		t.Fatalf("InsertUsageHistory raw: %v", err)
	}
	// A row for a different key must not be counted.
	if err := r.InsertUsageHistory("openai", "gpt-4o", "conn1", "sk-other-key-000000000000", "/v1/chat/completions", 999, 999, 0, "success", 1998, "{}", "{}"); err != nil {
		t.Fatalf("InsertUsageHistory other: %v", err)
	}

	used, err = r.GetApiKeyTokenUsage(rawKey)
	if err != nil {
		t.Fatalf("GetApiKeyTokenUsage: %v", err)
	}
	if want := 100 + 50 + 200 + 25; used != want {
		t.Errorf("used = %d, want %d", used, want)
	}

	// An empty raw key can match no row and must short-circuit to 0.
	if used, err := r.GetApiKeyTokenUsage(""); err != nil || used != 0 {
		t.Errorf(`GetApiKeyTokenUsage("") = (%d, %v), want (0, nil)`, used, err)
	}
}

func derefIntTest(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

// TestRateLimitAndBudgetAreIndependentColumns proves the two setters write
// disjoint column sets: updating the budget must not disturb the rate limits
// and vice versa. The dashboard calls both on every save, so a shared or
// overlapping UPDATE would make one editor silently overwrite the other.
func TestRateLimitAndBudgetAreIndependentColumns(t *testing.T) {
	r := openEphemeralSchemaDB(t)
	if err := r.CreateApiKey("kind", "sk-independent-abcdefghijkl", "kind", "m"); err != nil {
		t.Fatalf("CreateApiKey: %v", err)
	}

	if err := r.SetApiKeyRateLimit("kind", 11, 22, 33); err != nil {
		t.Fatalf("SetApiKeyRateLimit: %v", err)
	}
	if err := r.SetApiKeyBudget("kind", 44, 55); err != nil {
		t.Fatalf("SetApiKeyBudget: %v", err)
	}

	got, err := r.GetApiKeyByID("kind")
	if err != nil || got == nil {
		t.Fatalf("GetApiKeyByID: %v", err)
	}
	assertIntPtr(t, "RateLimitRPM", got.RateLimitRPM, 11)
	assertIntPtr(t, "RateLimitTPM", got.RateLimitTPM, 22)
	assertIntPtr(t, "RateLimitConcurrency", got.RateLimitConcurrency, 33)
	assertIntPtr(t, "TokenQuota", got.TokenQuota, 44)
	assertIntPtr(t, "QueueTimeoutMs", got.QueueTimeoutMs, 55)

	// Rewriting only the rate limits must leave the budget as set.
	if err := r.SetApiKeyRateLimit("kind", 1, 2, 3); err != nil {
		t.Fatalf("SetApiKeyRateLimit (second): %v", err)
	}
	got, _ = r.GetApiKeyByID("kind")
	assertIntPtr(t, "RateLimitRPM", got.RateLimitRPM, 1)
	assertIntPtr(t, "TokenQuota", got.TokenQuota, 44)
	assertIntPtr(t, "QueueTimeoutMs", got.QueueTimeoutMs, 55)
}

func assertIntPtr(t *testing.T, name string, got *int, want int) {
	t.Helper()
	if got == nil {
		t.Errorf("%s is nil, want %d", name, want)
		return
	}
	if *got != want {
		t.Errorf("%s = %d, want %d", name, *got, want)
	}
}

// TestGetApiKeysLegacySchemaHasNoBudgetColumns proves the key reader degrades on
// a database whose apiKeys table predates the F-2 budget columns: the listing
// still returns every key, with the budget axes at zero, instead of failing the
// whole read. A fixture-built table (or a backup restored from before F-2) is
// exactly this shape.
func TestGetApiKeysLegacySchemaHasNoBudgetColumns(t *testing.T) {
	r := openLegacyQuotaDB(t)
	if err := r.CreateApiKey("lg", "sk-legacy-budget-key-0001", "legacy key", "m"); err != nil {
		t.Fatalf("CreateApiKey: %v", err)
	}
	keys, err := r.GetApiKeys()
	if err != nil {
		t.Fatalf("GetApiKeys on a legacy schema must not fail: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("GetApiKeys returned %d keys, want 1", len(keys))
	}
	if keys[0].LimitMicros != 0 || keys[0].LimitTokens != 0 || keys[0].BudgetPeriod != "" {
		t.Errorf("legacy key budget = %d/%d/%q, want all zero",
			keys[0].LimitMicros, keys[0].LimitTokens, keys[0].BudgetPeriod)
	}
	// The single-key reader takes the same path.
	got, err := r.GetApiKeyByID("lg")
	if err != nil || got == nil {
		t.Fatalf("GetApiKeyByID on a legacy schema: %v", err)
	}
	if got.LimitMicros != 0 {
		t.Errorf("legacy key LimitMicros = %d, want 0", got.LimitMicros)
	}
}

// TestSetApiKeyPeriodBudgetRoundTrip proves the per-period budget columns
// survive a write and a read through the shared key reader, and that a fresh
// key reads them as zero (no budget) rather than as a configured one.
func TestSetApiKeyPeriodBudgetRoundTrip(t *testing.T) {
	r := openEphemeralSchemaDB(t)
	if err := r.CreateApiKey("pb", "sk-period-budget-key-001", "period budget key", "m"); err != nil {
		t.Fatalf("CreateApiKey: %v", err)
	}
	got, err := r.GetApiKeyByID("pb")
	if err != nil || got == nil {
		t.Fatalf("GetApiKeyByID: %v", err)
	}
	if got.LimitMicros != 0 || got.BudgetPeriod != "" || got.HardCutoff {
		t.Errorf("fresh key budget = %d/%q/%v, want all unset",
			got.LimitMicros, got.BudgetPeriod, got.HardCutoff)
	}
	if err := r.SetApiKeyPeriodBudget("pb", 2_500_000, 500_000, "daily", 90, true); err != nil {
		t.Fatalf("SetApiKeyPeriodBudget: %v", err)
	}
	got, err = r.GetApiKeyByID("pb")
	if err != nil || got == nil {
		t.Fatalf("GetApiKeyByID after set: %v", err)
	}
	if got.LimitMicros != 2_500_000 || got.LimitTokens != 500_000 {
		t.Errorf("limits = %d/%d, want 2500000/500000", got.LimitMicros, got.LimitTokens)
	}
	if got.BudgetPeriod != "daily" || got.AlertPct != 90 || !got.HardCutoff {
		t.Errorf("budget = %q/%d/%v, want daily/90/true", got.BudgetPeriod, got.AlertPct, got.HardCutoff)
	}
	// The listing path uses the same reader and must agree.
	keys, err := r.GetApiKeys()
	if err != nil {
		t.Fatalf("GetApiKeys: %v", err)
	}
	if len(keys) != 1 || keys[0].LimitMicros != 2_500_000 {
		t.Errorf("listed LimitMicros = %d, want 2500000", keys[0].LimitMicros)
	}
}
