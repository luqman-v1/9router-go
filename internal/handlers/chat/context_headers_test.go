package chat

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/middleware"
	"9router/proxy/internal/models"
)

// TestInjectContextHeadersWritesTheBudget proves the response carries the
// context budget a client needs to size its next turn: the window, how it
// splits, what this request used, and what is left.
func TestInjectContextHeadersWritesTheBudget(t *testing.T) {
	rec := httptest.NewRecorder()
	injectContextHeaders(rec, contextBudget{
		window:    132096,
		maxOutput: 4096,
		input:     130000,
	})

	want := map[string]string{
		"X-9router-Context-Window":          "132096",
		"X-9router-Max-Input-Tokens":        "128000",
		"X-9router-Max-Output-Tokens":       "4096",
		"X-9router-Input-Tokens":            "130000",
		"X-9router-Remaining-Input-Tokens":  "0",
		"X-9router-Remaining-Output-Tokens": "4096",
	}
	for name, value := range want {
		if got := rec.Header().Get(name); got != value {
			t.Errorf("%s = %q, want %q", name, got, value)
		}
	}
}

// TestInjectContextHeadersSkipsAnUnknownWindow proves nothing is written when
// the model declares no window: a zero would read as "no room at all".
func TestInjectContextHeadersSkipsAnUnknownWindow(t *testing.T) {
	rec := httptest.NewRecorder()
	injectContextHeaders(rec, contextBudget{input: 10})
	if got := rec.Header().Get("X-9router-Context-Window"); got != "" {
		t.Errorf("X-9router-Context-Window = %q, want empty", got)
	}
}

// TestResolveContextBudgetPrefersTheKeyCeiling proves the reported window is
// the one the request actually had to fit in: a key with its own ceiling gets
// that ceiling, not the model's wider window.
func TestResolveContextBudgetPrefersTheKeyCeiling(t *testing.T) {
	limit := 4096
	ctx := context.WithValue(context.Background(), middleware.ApiKeyContextKey, &models.APIKey{
		ID:           "k",
		ContextLimit: &limit,
	})
	budget := resolveContextBudget(ctx, "deepseek", "deepseek-chat", []byte(`{"messages":[{"role":"user","content":"hi"}]}`))
	if budget.window != limit {
		t.Errorf("window = %d, want the key's ceiling %d", budget.window, limit)
	}
	if budget.maxInput() > limit {
		t.Errorf("maxInput = %d, want no more than the ceiling %d", budget.maxInput(), limit)
	}
}

// TestResolveContextBudgetWithoutAKeyCeilingKeepsTheModelWindow proves a key
// without a ceiling leaves the model's own window in place.
func TestResolveContextBudgetWithoutAKeyCeilingKeepsTheModelWindow(t *testing.T) {
	ctx := context.WithValue(context.Background(), middleware.ApiKeyContextKey, &models.APIKey{ID: "k"})
	budget := resolveContextBudget(ctx, "deepseek", "deepseek-chat", []byte(`{"messages":[{"role":"user","content":"hi"}]}`))
	if budget.window <= 0 {
		t.Errorf("window = %d, want the model's own window", budget.window)
	}
}

// TestClearContextHeadersRemovesThem proves a failed attempt's budget does not
// leak into an error response.
func TestClearContextHeadersRemovesThem(t *testing.T) {
	rec := httptest.NewRecorder()
	injectContextHeaders(rec, contextBudget{window: 132096, maxOutput: 4096, input: 10})
	clearContextHeaders(rec)

	for _, name := range []string{
		"X-9router-Context-Window",
		"X-9router-Max-Input-Tokens",
		"X-9router-Max-Output-Tokens",
		"X-9router-Input-Tokens",
		"X-9router-Remaining-Input-Tokens",
		"X-9router-Remaining-Output-Tokens",
	} {
		if got := rec.Header().Get(name); got != "" {
			t.Errorf("%s = %q, want empty after clear", name, got)
		}
	}
}

// TestResolveContextBudgetCountsTheRequestInput proves the reported input is
// the request's own size, so a client can see how much of its window a turn
// took.
func TestResolveContextBudgetCountsTheRequestInput(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"` + strings.Repeat("word ", 200) + `"}]}`)
	budget := resolveContextBudget(context.Background(), "deepseek", "deepseek-chat", body)
	if budget.input <= 0 {
		t.Errorf("input = %d, want the request's own size", budget.input)
	}
	if budget.remainingInput() >= budget.maxInput() {
		t.Errorf("remainingInput = %d, want less than the whole budget %d", budget.remainingInput(), budget.maxInput())
	}
}

// TestResolveContextBudgetIgnoresABrokenBody proves an undecodable body still
// produces a budget rather than an error.
func TestResolveContextBudgetIgnoresABrokenBody(t *testing.T) {
	budget := resolveContextBudget(context.Background(), "deepseek", "deepseek-chat", []byte("not json"))
	if budget.input <= 0 {
		t.Errorf("input = %d, want a fallback count", budget.input)
	}
}
