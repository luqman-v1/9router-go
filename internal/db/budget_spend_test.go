package db

import (
	"context"
	"testing"
	"time"

	"9router/proxy/internal/budget"
	"9router/proxy/internal/handlerutil"
)

// TestSpendForKeySinceSumsCostAndTokens proves the budget reader folds cost
// into integer micros and sums prompt+completion tokens over the window, and
// that a row belonging to another key is not counted.
func TestSpendForKeySinceSumsCostAndTokens(t *testing.T) {
	r := openEphemeralSchemaDB(t)
	const rawKey = "sk-budget-spend-abcdefghij"
	if err := r.CreateApiKey("kb", rawKey, "budget key", "m"); err != nil {
		t.Fatalf("CreateApiKey: %v", err)
	}
	// Empty history is zero spend, not an error.
	got, err := r.SpendForKeySince(context.Background(), rawKey, "")
	if err != nil {
		t.Fatalf("SpendForKeySince (empty): %v", err)
	}
	if got != (budget.Spend{}) {
		t.Errorf("empty spend = %+v, want zero", got)
	}
	// Two rows: $0.50 + 100/50 tokens, and $1.25 + 200/25 tokens. The micros
	// conversion must round, not truncate, so 0.5 and 1.25 both land exactly.
	if err := r.InsertUsageHistory("openai", "gpt-4o", "c1", handlerutil.MaskAPIKey(rawKey), "/v1/chat/completions", 100, 50, 0.50, "success", 150, "{}", "{}"); err != nil {
		t.Fatalf("insert 1: %v", err)
	}
	if err := r.InsertUsageHistory("openai", "gpt-4o", "c1", handlerutil.MaskAPIKey(rawKey), "/v1/chat/completions", 200, 25, 1.25, "success", 225, "{}", "{}"); err != nil {
		t.Fatalf("insert 2: %v", err)
	}
	// A row for a different key must not be counted.
	if err := r.InsertUsageHistory("openai", "gpt-4o", "c1", "sk-someone-else-00000000", "/v1/chat/completions", 999, 999, 99.0, "success", 1998, "{}", "{}"); err != nil {
		t.Fatalf("insert other: %v", err)
	}
	got, err = r.SpendForKeySince(context.Background(), rawKey, "")
	if err != nil {
		t.Fatalf("SpendForKeySince: %v", err)
	}
	if want := (budget.Spend{Micros: 1_750_000, Tokens: 375}); got != want {
		t.Errorf("spend = %+v, want %+v", got, want)
	}
	// An empty key matches nothing and must short-circuit to zero.
	if got, err := r.SpendForKeySince(context.Background(), "", ""); err != nil || got != (budget.Spend{}) {
		t.Errorf(`SpendForKeySince("") = (%+v, %v), want zero`, got, err)
	}
}

// TestSpendForKeySinceWindowFloor proves the timestamp floor excludes older
// rows, which is what makes a daily window read only today's spend.
func TestSpendForKeySinceWindowFloor(t *testing.T) {
	r := openEphemeralSchemaDB(t)
	const rawKey = "sk-budget-window-abcdefgh"
	if err := r.CreateApiKey("kw", rawKey, "window key", "m"); err != nil {
		t.Fatalf("CreateApiKey: %v", err)
	}
	// A row outside the window and one inside it. The timestamps are written
	// directly because InsertUsageHistory stamps "now".
	old := time.Now().UTC().Add(-48 * time.Hour).Format("2006-01-02T15:04:05.000Z")
	recent := time.Now().UTC().Add(-time.Hour).Format("2006-01-02T15:04:05.000Z")
	masked := handlerutil.MaskAPIKey(rawKey)
	for _, row := range []struct{ ts string; cost float64; pt, ct int }{
		{old, 5.0, 1000, 1000},
		{recent, 1.0, 100, 50},
	} {
		if _, err := r.RawDB().Exec(
			`INSERT INTO usageHistory (timestamp, provider, model, connectionId, apiKey, clientKey, endpoint, promptTokens, completionTokens, cost, status, tokens, meta)
			 VALUES (?, 'openai', 'gpt-4o', 'c1', ?, ?, '/v1/chat/completions', ?, ?, ?, 'success', '{}', '{}')`,
			row.ts, masked, masked, row.pt, row.ct, row.cost,
		); err != nil {
			t.Fatalf("insert %s: %v", row.ts, err)
		}
	}
	floor := time.Now().UTC().Add(-24 * time.Hour).Format("2006-01-02T15:04:05.000Z")
	got, err := r.SpendForKeySince(context.Background(), rawKey, floor)
	if err != nil {
		t.Fatalf("SpendForKeySince: %v", err)
	}
	if want := (budget.Spend{Micros: 1_000_000, Tokens: 150}); got != want {
		t.Errorf("spend inside window = %+v, want %+v", got, want)
	}
	// An empty floor reads the whole ledger, so the old row counts again.
	got, err = r.SpendForKeySince(context.Background(), rawKey, "")
	if err != nil {
		t.Fatalf("SpendForKeySince (unbounded): %v", err)
	}
	if want := (budget.Spend{Micros: 6_000_000, Tokens: 2150}); got != want {
		t.Errorf("spend unbounded = %+v, want %+v", got, want)
	}
}

// TestSpendForKeySinceCountsUpstreamDashboardRows proves the attribution terms
// cover the raw-key spelling the upstream Next.js dashboard wrote into apiKey,
// so a shared database does not silently drop that spend out of the budget.
func TestSpendForKeySinceCountsUpstreamDashboardRows(t *testing.T) {
	r := openEphemeralSchemaDB(t)
	const rawKey = "sk-budget-upstream-abcdefg"
	if err := r.CreateApiKey("ku", rawKey, "upstream key", "m"); err != nil {
		t.Fatalf("CreateApiKey: %v", err)
	}
	// The upstream dashboard stored the client key in apiKey verbatim.
	if err := r.InsertUsageHistory("openai", "gpt-4o", "c1", rawKey, "/v1/chat/completions", 300, 100, 2.0, "success", 400, "{}", "{}"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err := r.SpendForKeySince(context.Background(), rawKey, "")
	if err != nil {
		t.Fatalf("SpendForKeySince: %v", err)
	}
	if want := (budget.Spend{Micros: 2_000_000, Tokens: 400}); got != want {
		t.Errorf("spend = %+v, want %+v", got, want)
	}
}

// TestSpendForKeySinceRoundsMicros proves the float-to-micros conversion rounds
// rather than truncates. A cost of $0.0000005 must land on 1 micro, not 0, or a
// long-running key accumulates a drift the operator cannot see.
func TestSpendForKeySinceRoundsMicros(t *testing.T) {
	r := openEphemeralSchemaDB(t)
	const rawKey = "sk-budget-round-abcdefghijk"
	if err := r.CreateApiKey("kr", rawKey, "round key", "m"); err != nil {
		t.Fatalf("CreateApiKey: %v", err)
	}
	masked := handlerutil.MaskAPIKey(rawKey)
	// 0.0000005 USD = 0.5 micros, which must round up to 1.
	if err := r.InsertUsageHistory("openai", "gpt-4o", "c1", masked, "/v1/chat/completions", 1, 0, 0.0000005, "success", 1, "{}", "{}"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err := r.SpendForKeySince(context.Background(), rawKey, "")
	if err != nil {
		t.Fatalf("SpendForKeySince: %v", err)
	}
	if got.Micros != 1 {
		t.Errorf("Micros = %d, want 1 (rounded, not truncated)", got.Micros)
	}
}
