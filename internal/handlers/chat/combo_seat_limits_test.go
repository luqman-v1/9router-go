package chat

import (
	"context"

	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/db"
)

// comboSeatLeaf is the /v1/models entry shape these tests read: the top-level
// token limits are pointers because a combo may legitimately omit them.
type comboSeatLeaf struct {
	ID                  string `json:"id"`
	OwnedBy             string `json:"owned_by"`
	ContextLength       *int   `json:"context_length"`
	MaxCompletionTokens *int   `json:"max_completion_tokens"`
}

// comboModelsResponse reads the advertised entries of one /v1/models call.
func comboModelsResponse(t *testing.T, h *ChatHandler) []comboSeatLeaf {
	t.Helper()
	w := httptest.NewRecorder()
	h.HandleModels(w, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /v1/models = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	var resp struct {
		Data []comboSeatLeaf `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal /v1/models: %v (body: %s)", err, w.Body.String())
	}
	return resp.Data
}

// comboEntry returns the advertised entry for a combo id, failing when the
// handler dropped it.
func comboEntry(t *testing.T, entries []comboSeatLeaf, id string) comboSeatLeaf {
	t.Helper()
	for _, entry := range entries {
		if entry.ID == id {
			return entry
		}
	}
	t.Fatalf("combo %q missing from /v1/models", id)
	return comboSeatLeaf{}
}

// wantLimit asserts a limit is published with the exact value.
func wantLimit(t *testing.T, field string, got *int, want int) {
	t.Helper()
	if got == nil {
		t.Errorf("%s = absent, want %d", field, want)
		return
	}
	if *got != want {
		t.Errorf("%s = %d, want %d", field, *got, want)
	}
}

// wantAbsent asserts a limit is not published at all — an unknown limit has to
// be omitted, not filled in with a guess the client would then trust.
func wantAbsent(t *testing.T, field string, got *int) {
	t.Helper()
	if got != nil {
		t.Errorf("%s = %d, want the key to be absent", field, *got)
	}
}

// A combo hands the request to whichever seat answers, so the only window it
// can promise is its narrowest. Upstream takes Math.min over the seats
// (comboSeatLimits in src/app/api/v1/models/route.js); before this fix a combo
// entry published no limits at all, leaving the client to guess from the name.
func TestHandleModels_ComboPublishesSmallestSeatContext(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	// 128000 for deepseek/deepseek-chat (upstream *deepseek-chat* row) and
	// 1000000 for anthropic/claude-sonnet-4-6; the combo must publish the smaller.
	if _, err := database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES
		('c-limits', 'limits-combo', 'llm', '["deepseek/deepseek-chat","anthropic/claude-sonnet-4-6"]', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`); err != nil {
		t.Fatalf("seed combo: %v", err)
	}

	entry := comboEntry(t, comboModelsResponse(t, NewChatHandler(db.NewRepo(database))), "limits-combo")

	if entry.OwnedBy != "combo" {
		t.Fatalf("owned_by = %q, want \"combo\"", entry.OwnedBy)
	}
	wantLimit(t, "context_length", entry.ContextLength, 128000)
	wantLimit(t, "max_completion_tokens", entry.MaxCompletionTokens, 8192)
}

// max_output is promised on the minimum across the whole seat tree too, so a
// nested combo whose leaves disagree publishes the smaller of the two.
func TestHandleModels_NestedComboPublishesSmallestLeafLimit(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	// inner holds the two windows (1000000 for anthropic/claude-sonnet-4-6 and
	// 128000 for openai/gpt-4o); outer adds groq/llama-3-70b at its own 4096
	// max output, so the tree minimum is the inner pair's 128000 window with
	// outer's 4096 output — the numbers only line up if the nested seats are
	// walked as well as the outer ones.
	seeds := []struct{ id, name, models string }{
		{"c-inner", "inner-combo", `["anthropic/claude-sonnet-4-6","openai/gpt-4o"]`},
		{"c-outer", "outer-combo", `["inner-combo","groq/llama-3-70b"]`},
	}
	for _, s := range seeds {
		if _, err := database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES (?, ?, 'llm', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
			s.id, s.name, s.models); err != nil {
			t.Fatalf("seed combo %s: %v", s.name, err)
		}
	}

	entries := comboModelsResponse(t, NewChatHandler(db.NewRepo(database)))
	outer := comboEntry(t, entries, "outer-combo")
	inner := comboEntry(t, entries, "inner-combo")

	wantLimit(t, "outer context_length", outer.ContextLength, 128000)
	wantLimit(t, "outer max_completion_tokens", outer.MaxCompletionTokens, 4096)
	wantLimit(t, "inner context_length", inner.ContextLength, 128000)
	wantLimit(t, "inner max_completion_tokens", inner.MaxCompletionTokens, 16384)
}

// A combo that (transitively) lists itself has no defined seat set. Listing
// it must terminate rather than recurse forever, and the cycle must not swallow
// the concrete leaves that sit beside the recursive seat.
func TestHandleModels_SelfReferencingComboTerminates(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	// dead-combo has no concrete seat at all: its only seat is itself, so the
	// 128000/4096 floor an unknown seat resolves to must not be published as
	// if it were the combo's window (#90).
	seeds := []struct{ id, name, models string }{
		{"c-self", "self-combo", `["self-combo","deepseek/deepseek-chat"]`},
		{"c-mutual", "mutual-combo", `["loop-combo","groq/llama-3-70b"]`},
		{"c-loop", "loop-combo", `["mutual-combo"]`},
		{"c-dead", "dead-combo", `["dead-combo"]`},
	}
	for _, s := range seeds {
		if _, err := database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES (?, ?, 'llm', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
			s.id, s.name, s.models); err != nil {
			t.Fatalf("seed combo %s: %v", s.name, err)
		}
	}

	entries := comboModelsResponse(t, NewChatHandler(db.NewRepo(database)))

	// The concrete leaf beside the recursive seat still sets the window.
	wantLimit(t, "self-combo context_length", comboEntry(t, entries, "self-combo").ContextLength, 128000)
	// loop-combo reaches mutual-combo, whose own concrete seat is still
	// reachable across the cycle: cutting it must not swallow that leaf.
	wantLimit(t, "loop-combo context_length", comboEntry(t, entries, "loop-combo").ContextLength, 128000)
	wantLimit(t, "loop-combo max_completion_tokens", comboEntry(t, entries, "loop-combo").MaxCompletionTokens, 4096)
	wantLimit(t, "mutual-combo context_length", comboEntry(t, entries, "mutual-combo").ContextLength, 128000)
	wantLimit(t, "mutual-combo max_completion_tokens", comboEntry(t, entries, "mutual-combo").MaxCompletionTokens, 4096)
	// dead-combo's seat list is nothing but the cycle, so it promises nothing.
	wantAbsent(t, "dead-combo context_length", comboEntry(t, entries, "dead-combo").ContextLength)
	wantAbsent(t, "dead-combo max_completion_tokens", comboEntry(t, entries, "dead-combo").MaxCompletionTokens)
}

// A combo whose seats are all web endpoints carries no window at all. The
// capability floor (128000/4096) that an unknown model resolves to must not be
// published as the combo's limit — see #90, where a guessed number is worse than
// an absent one.
func TestHandleModels_WebOnlyComboPublishesNoLimits(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES
		('c-web', 'web-combo', 'llm', '["xquik/search","tavily/fetch"]', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`); err != nil {
		t.Fatalf("seed combo: %v", err)
	}

	entry := comboEntry(t, comboModelsResponse(t, NewChatHandler(db.NewRepo(database))), "web-combo")

	wantAbsent(t, "context_length", entry.ContextLength)
	wantAbsent(t, "max_completion_tokens", entry.MaxCompletionTokens)
}

// A web seat must not vote on the minimum, but a real chat seat beside it still
// does: the floor an unknown seat resolves to is not a window that seat can
// serve, and letting it win would cap every mixed combo at the floor.
func TestHandleModels_WebSeatDoesNotDragMinimumDown(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES
		('c-mixed', 'mixed-combo', 'llm', '["xquik/search","deepseek/deepseek-chat"]', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`); err != nil {
		t.Fatalf("seed combo: %v", err)
	}

	entry := comboEntry(t, comboModelsResponse(t, NewChatHandler(db.NewRepo(database))), "mixed-combo")

	wantLimit(t, "context_length", entry.ContextLength, 128000)
	wantLimit(t, "max_completion_tokens", entry.MaxCompletionTokens, 8192)
}

// A thinking suffix like "(max)" on a combo seat is not part of the model id:
// comboSeatCapabilities strips it before the capability lookup, so the seat
// publishes the bare model's own block instead of the floor capability.
func TestHandleModels_ComboSeatThinkingSuffixStrippedBeforeCaps(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	// opencode's deepseek-v4.1-flash-free declares 330000 context_length and
	// 128000 max_completion_tokens (providerCapabilities["opencode"]). A seat
	// that kept the "(max)" suffix missed that row and fell through to the
	// pattern-table default, so the suffixed seat must publish the bare
	// model's own block.
	if _, err := database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES
			('c-think', 'thinking-combo', 'llm', '["oc/deepseek-v4.1-flash-free(max)"]', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`); err != nil {
		t.Fatalf("seed combo: %v", err)
	}

	entry := comboEntry(t, comboModelsResponse(t, NewChatHandler(db.NewRepo(database))), "thinking-combo")
	wantLimit(t, "context_length", entry.ContextLength, 330000)
	wantLimit(t, "max_completion_tokens", entry.MaxCompletionTokens, 128000)
}

func TestHandleModels_ComboSeatConnectionAliasResolvesCapabilities(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	if err := db.NewRepo(database).CreateProviderConnection(context.Background(), "custom-conn-id", "openai", "apikey", "my-openai-alias", `{"apiKey":"sk-test"}`); err != nil {
		t.Fatalf("create connection: %v", err)
	}

	if _, err := database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES
		('c-alias', 'alias-combo', 'llm', '["custom-conn-id/gpt-4o"]', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`); err != nil {
		t.Fatalf("seed combo: %v", err)
	}

	entry := comboEntry(t, comboModelsResponse(t, NewChatHandler(db.NewRepo(database))), "alias-combo")
	wantLimit(t, "context_length", entry.ContextLength, 128000)
	wantLimit(t, "max_completion_tokens", entry.MaxCompletionTokens, 16384)
}

// max mode advertises the widest seat window instead of the narrowest, so a
// fallback chain that can fail past a small-window seat still advertises its
// full window.
func TestHandleModels_ComboMaxModeAdvertisesWidestWindow(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	if err := repo.SetComboStrategy("max-combo", db.ComboStrategy{
		Strategy:          "fallback",
		ContextWindowMode: db.ComboWindowModeMax,
	}); err != nil {
		t.Fatalf("set combo strategy: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES
		('c-max', 'max-combo', 'llm', '["anthropic/claude-sonnet-4-6","deepseek/deepseek-chat"]', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`); err != nil {
		t.Fatalf("seed combo: %v", err)
	}

	entry := comboEntry(t, comboModelsResponse(t, NewChatHandler(repo)), "max-combo")
	// min would publish 128000 (deepseek-chat's narrower window);
	// max publishes 1000000 from claude-sonnet-4-6.
	wantLimit(t, "context_length", entry.ContextLength, 1000000)
}

// custom mode advertises the operator-declared window regardless of what the
// seats report.
func TestHandleModels_ComboCustomModeAdvertisesDeclaredWindow(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	if err := repo.SetComboStrategy("custom-combo", db.ComboStrategy{
		Strategy:          "fallback",
		ContextWindowMode: db.ComboWindowModeCustom,
		ContextWindow:     1000000,
	}); err != nil {
		t.Fatalf("set combo strategy: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES
		('c-custom', 'custom-combo', 'llm', '["deepseek/deepseek-chat","groq/llama-3-70b"]', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`); err != nil {
		t.Fatalf("seed combo: %v", err)
	}

	entry := comboEntry(t, comboModelsResponse(t, NewChatHandler(repo)), "custom-combo")
	wantLimit(t, "context_length", entry.ContextLength, 1000000)
}

// custom mode without a declared window falls back to min rather than
// advertising a guess.
func TestHandleModels_ComboCustomModeWithoutWindowFallsBackToMin(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	if err := repo.SetComboStrategy("custom-combo", db.ComboStrategy{
		Strategy:          "fallback",
		ContextWindowMode: db.ComboWindowModeCustom,
	}); err != nil {
		t.Fatalf("set combo strategy: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES
		('c-custom', 'custom-combo', 'llm', '["deepseek/deepseek-chat","groq/llama-3-70b"]', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`); err != nil {
		t.Fatalf("seed combo: %v", err)
	}

	entry := comboEntry(t, comboModelsResponse(t, NewChatHandler(repo)), "custom-combo")
	wantLimit(t, "context_length", entry.ContextLength, 128000)
}

// A nested combo's own mode is resolved before its parent folds it, which is
// what upstream does (route.js comboSeatLimits calls resolveComboContextWindow
// on each nested result). Folding raw leaves instead made inner's "max" choice
// invisible: outer in the default min mode advertised inner's narrowest leaf
// even though inner promised its widest.
func TestHandleModels_NestedComboModeResolvedBeforeParentFolds(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	// inner spans 128000 (deepseek-chat) to 1000000 (claude-sonnet-4-6) and
	// advertises the widest. Outer holds only inner plus another 1000000 seat,
	// so folding inner's raw leaves would publish 128000 while folding inner's
	// resolved window publishes 1000000 — the two readings differ only when the
	// nested mode is honoured.
	if err := repo.SetComboStrategy("inner-max", db.ComboStrategy{
		Strategy:          "fallback",
		ContextWindowMode: db.ComboWindowModeMax,
	}); err != nil {
		t.Fatalf("set inner strategy: %v", err)
	}
	seeds := []struct{ id, name, models string }{
		{"c-inner", "inner-max", `["deepseek/deepseek-chat","anthropic/claude-sonnet-4-6"]`},
		{"c-outer", "outer-wrap", `["inner-max","openai/gpt-4.1"]`},
	}
	for _, s := range seeds {
		if _, err := database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES (?, ?, 'llm', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
			s.id, s.name, s.models); err != nil {
			t.Fatalf("seed combo %s: %v", s.name, err)
		}
	}

	entries := comboModelsResponse(t, NewChatHandler(repo))
	wantLimit(t, "inner context_length", comboEntry(t, entries, "inner-max").ContextLength, 1000000)
	wantLimit(t, "outer context_length", comboEntry(t, entries, "outer-wrap").ContextLength, 1000000)
}

// The same propagation in max mode: a parent in max mode must see the nested
// combo's resolved window, not its widest raw leaf, so a nested combo pinned to
// a small custom window does not widen the parent past what it can serve.
func TestHandleModels_NestedComboCustomWindowCapsParentMax(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	if err := repo.SetComboStrategy("inner-capped", db.ComboStrategy{
		Strategy:          "fallback",
		ContextWindowMode: db.ComboWindowModeCustom,
		ContextWindow:     200000,
	}); err != nil {
		t.Fatalf("set inner strategy: %v", err)
	}
	if err := repo.SetComboStrategy("outer-max", db.ComboStrategy{
		Strategy:          "fallback",
		ContextWindowMode: db.ComboWindowModeMax,
	}); err != nil {
		t.Fatalf("set outer strategy: %v", err)
	}
	seeds := []struct{ id, name, models string }{
		{"c-inner2", "inner-capped", `["deepseek/deepseek-chat","anthropic/claude-sonnet-4-6"]`},
		{"c-outer2", "outer-max", `["inner-capped"]`},
	}
	for _, s := range seeds {
		if _, err := database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES (?, ?, 'llm', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
			s.id, s.name, s.models); err != nil {
			t.Fatalf("seed combo %s: %v", s.name, err)
		}
	}

	entries := comboModelsResponse(t, NewChatHandler(repo))
	// Max over the single nested effective window (200000), not over the raw
	// leaves (which would give 1000000).
	wantLimit(t, "outer context_length", comboEntry(t, entries, "outer-max").ContextLength, 200000)
}

// Invalid mode strings resolve to min instead of publishing a bogus window.
func TestHandleModels_ComboInvalidModeResolvesToMin(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	if _, err := database.Exec(`INSERT INTO settings (id, data) VALUES (1, ?)`,
		`{"comboStrategies":{"bad-mode-combo":{"contextWindowMode":"bogus"}}}`); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES
		('c-bad', 'bad-mode-combo', 'llm', '["deepseek/deepseek-chat"]', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`); err != nil {
		t.Fatalf("seed combo: %v", err)
	}

	entry := comboEntry(t, comboModelsResponse(t, NewChatHandler(repo)), "bad-mode-combo")
	wantLimit(t, "context_length", entry.ContextLength, 128000)
}
