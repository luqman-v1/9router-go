package db

import (
	"testing"
	"time"
)

// The dashboard derives its provider table and its Top Model card from the
// model-level rows instead of querying them again (see HandleUsageDashboard).
// That is only safe while the fold agrees with the SQL it replaced, so this
// pins the two against each other on a multi-provider, multi-model ledger.
func TestFoldUsageGroupByProviderMatchesQuery(t *testing.T) {
	conn, err := OpenDatabase(t.TempDir() + "/fold.sqlite")
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := EnsureCoreSchema(conn); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	repo := NewRepo(conn)

	// Two providers, three models, one failing row, and one provider whose
	// name also appears as a model bucket — the shapes a fold can get wrong.
	seed := []struct {
		provider, model, status string
		prompt, completion      int
		cost                    float64
	}{
		{"deepseek", "deepseek-chat", "success", 100, 50, 0.01},
		{"deepseek", "deepseek-chat", "error", 200, 60, 0.02},
		{"deepseek", "deepseek-reasoner", "success", 300, 70, 0.03},
		{"openai", "gpt-5", "success", 400, 80, 0.04},
		{"openai", "openai", "success", 500, 90, 0.05},
	}
	for _, row := range seed {
		if err := repo.InsertUsageHistory(row.provider, row.model, "c-"+row.provider, "sk", "/v1/chat/completions",
			row.prompt, row.completion, row.cost, row.status, row.prompt+row.completion, `{}`, `{}`); err != nil {
			t.Fatalf("InsertUsageHistory: %v", err)
		}
	}

	since := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	want, err := repo.GetUsageGroupByProviderSince(since)
	if err != nil {
		t.Fatalf("GetUsageGroupByProviderSince: %v", err)
	}
	modelRows, err := repo.GetUsageGroupByModelSince(since)
	if err != nil {
		t.Fatalf("GetUsageGroupByModelSince: %v", err)
	}
	got := FoldUsageGroupByProvider(modelRows)

	if len(got) != len(want) {
		t.Fatalf("folded %d providers, query returned %d", len(got), len(want))
	}
	byProvider := make(map[string]UsageGroupRow, len(got))
	for _, row := range got {
		byProvider[row.Provider] = row
	}
	for _, w := range want {
		g, ok := byProvider[w.Provider]
		if !ok {
			t.Errorf("provider %q missing from the fold", w.Provider)
			continue
		}
		if g.Requests != w.Requests || g.Errors != w.Errors {
			t.Errorf("%s counts: fold %d/%d, query %d/%d", w.Provider, g.Requests, g.Errors, w.Requests, w.Errors)
		}
		if g.PromptTokens != w.PromptTokens || g.CompletionTokens != w.CompletionTokens {
			t.Errorf("%s tokens: fold %d/%d, query %d/%d", w.Provider, g.PromptTokens, g.CompletionTokens, w.PromptTokens, w.CompletionTokens)
		}
		if g.Cost != w.Cost {
			t.Errorf("%s cost: fold %v, query %v", w.Provider, g.Cost, w.Cost)
		}
		if g.LastUsed != w.LastUsed {
			t.Errorf("%s lastUsed: fold %q, query %q", w.Provider, g.LastUsed, w.LastUsed)
		}
		if g.Model != "" {
			t.Errorf("%s carries a model %q; provider rows have none", w.Provider, g.Model)
		}
	}
}

// TopUsageGroupByTokens replaced a third GROUP BY over the same window, so its
// answer has to match that query exactly — including the tie-break, which is
// what makes the card stable between two models with the same token total.
func TestTopUsageGroupByTokensMatchesQuery(t *testing.T) {
	conn, err := OpenDatabase(t.TempDir() + "/top.sqlite")
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := EnsureCoreSchema(conn); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	repo := NewRepo(conn)

	// "alpha" and "beta" tie at 500 tokens; the query orders by provider then
	// model, so the fold has to pick "alpha" for the same reason.
	seed := []struct {
		provider, model string
		prompt, compl   int
	}{
		{"zeta", "m-small", 100, 20},
		{"alpha", "m-tie", 400, 100},
		{"beta", "m-tie", 450, 50},
		{"alpha", "m-zero", 0, 0},
	}
	for _, row := range seed {
		if err := repo.InsertUsageHistory(row.provider, row.model, "c-"+row.provider, "sk", "/v1/chat/completions",
			row.prompt, row.compl, 0.01, "success", row.prompt+row.compl, `{}`, `{}`); err != nil {
			t.Fatalf("InsertUsageHistory: %v", err)
		}
	}

	since := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	wantProvider, wantModel, wantTokens, wantOK, err := repo.GetUsageTopModelSince(since)
	if err != nil {
		t.Fatalf("GetUsageTopModelSince: %v", err)
	}
	modelRows, err := repo.GetUsageGroupByModelSince(since)
	if err != nil {
		t.Fatalf("GetUsageGroupByModelSince: %v", err)
	}
	gotProvider, gotModel, gotTokens, gotOK := TopUsageGroupByTokens(modelRows)

	if gotOK != wantOK || gotProvider != wantProvider || gotModel != wantModel || gotTokens != wantTokens {
		t.Fatalf("fold = (%q, %q, %d, %v), query = (%q, %q, %d, %v)",
			gotProvider, gotModel, gotTokens, gotOK, wantProvider, wantModel, wantTokens, wantOK)
	}
}

// An empty ledger must not report a winner: the card renders "—" from ok=false,
// and a fold that returned the zero row would print an empty model name.
func TestTopUsageGroupByTokensEmpty(t *testing.T) {
	if provider, model, tokens, ok := TopUsageGroupByTokens(nil); ok || provider != "" || model != "" || tokens != 0 {
		t.Fatalf("empty fold = (%q, %q, %d, %v), want no winner", provider, model, tokens, ok)
	}
}
