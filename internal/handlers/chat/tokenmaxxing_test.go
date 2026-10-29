package chat

import (
	"context"
	"testing"
	"time"

	"9router/proxy/internal/db"
)

func TestScoreQuotas_AnchorPicking(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	resetLater := now.Add(10 * 24 * time.Hour)
	resetMonthly := now.Add(5 * 24 * time.Hour)

	quotas := []QuotaWindow{
		{
			Name:                "daily",
			RemainingPercentage: floatPtr(80),
			ResetAt:             &resetLater,
		},
		{
			Name:                "monthly quota",
			RemainingPercentage: floatPtr(50),
			ResetAt:             &resetMonthly,
		},
	}

	score := ScoreQuotas(quotas, now)
	if score == nil {
		t.Fatal("expected non-nil score")
	}
	expected := 50.0 / 5.0
	if *score != expected {
		t.Fatalf("expected score %f, got %f", expected, *score)
	}
}

func TestScoreQuotas_FallbackLatestReset(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	resetSoon := now.Add(2 * 24 * time.Hour)
	resetLate := now.Add(10 * 24 * time.Hour)

	quotas := []QuotaWindow{
		{
			Name:                "window1",
			RemainingPercentage: floatPtr(60),
			ResetAt:             &resetSoon,
		},
		{
			Name:                "window2",
			RemainingPercentage: floatPtr(40),
			ResetAt:             &resetLate,
		},
	}

	score := ScoreQuotas(quotas, now)
	if score == nil {
		t.Fatal("expected non-nil score")
	}
	expected := 40.0 / 10.0
	if *score != expected {
		t.Fatalf("expected score %f, got %f", expected, *score)
	}
}

func TestScoreQuotas_DerivePercentage(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	resetAt := now.Add(5 * 24 * time.Hour)

	quotas := []QuotaWindow{
		{
			Name:    "derived",
			Used:    floatPtr(25),
			Total:   floatPtr(100),
			ResetAt: &resetAt,
		},
	}

	score := ScoreQuotas(quotas, now)
	if score == nil {
		t.Fatal("expected non-nil score")
	}
	expected := 75.0 / 5.0
	if *score != expected {
		t.Fatalf("expected score %f, got %f", expected, *score)
	}
}

func TestScoreQuotas_ZeroQuota(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	quotas := []QuotaWindow{
		{
			Name:                "exhausted",
			RemainingPercentage: floatPtr(0),
		},
	}

	score := ScoreQuotas(quotas, now)
	if score == nil || *score != 0.0 {
		t.Fatalf("expected 0.0 score for exhausted quota, got %v", score)
	}
}

func TestOrderByTokenmaxxingScores_Ordering(t *testing.T) {
	models := []string{"modelA", "modelB", "modelC", "modelD"}
	scores := map[string]*float64{
		"modelA": floatPtr(2.0),
		"modelB": nil,
		"modelC": floatPtr(0.0),
		"modelD": floatPtr(5.0),
	}

	ordered := OrderByTokenmaxxingScores(models, scores)

	expected := []string{"modelD", "modelA", "modelB", "modelC"}
	if len(ordered) != len(expected) {
		t.Fatalf("expected length %d, got %d", len(expected), len(ordered))
	}
	for i, m := range expected {
		if ordered[i] != m {
			t.Fatalf("at index %d: expected %s, got %s", i, m, ordered[i])
		}
	}
}

func TestHandleComboFallback_TokenmaxxingStrategy(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	if _, err := database.Exec(`
		DELETE FROM providerConnections;
		INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		VALUES
		('conn-ds', 'deepseek', 'apikey', 'DS High', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z'),
		('conn-oa', 'openai', 'apikey', 'OA Low', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')
	`, `{"quotas":{"monthly":{"remainingPercentage":90,"resetAt":"2026-02-01T00:00:00Z"}}}`,
		`{"quotas":{"monthly":{"remainingPercentage":10,"resetAt":"2026-02-01T00:00:00Z"}}}`); err != nil {
		t.Fatalf("seed connections: %v", err)
	}

	handler := NewChatHandler(db.NewRepo(database))
	models := []string{"openai/gpt-4o", "deepseek/deepseek-chat"}

	ordered := handler.applyTokenmaxxingRouting(context.Background(), models)
	if len(ordered) != 2 {
		t.Fatalf("expected 2 models, got %d", len(ordered))
	}
	if ordered[0] != "deepseek/deepseek-chat" {
		t.Errorf("expected deepseek/deepseek-chat first due to higher quota, got %s", ordered[0])
	}
}

func floatPtr(v float64) *float64 {
	return &v
}
