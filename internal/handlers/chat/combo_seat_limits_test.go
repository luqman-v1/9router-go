package chat

import (
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
	// 131072 for deepseek/deepseek-chat and 200000 for anthropic/claude-sonnet-4-6
	// per the ports' capability tables; the combo must publish the smaller.
	if _, err := database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES
		('c-limits', 'limits-combo', 'llm', '["deepseek/deepseek-chat","anthropic/claude-sonnet-4-6"]', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`); err != nil {
		t.Fatalf("seed combo: %v", err)
	}

	entry := comboEntry(t, comboModelsResponse(t, NewChatHandler(db.NewRepo(database))), "limits-combo")

	if entry.OwnedBy != "combo" {
		t.Fatalf("owned_by = %q, want \"combo\"", entry.OwnedBy)
	}
	wantLimit(t, "context_length", entry.ContextLength, 131072)
	wantLimit(t, "max_completion_tokens", entry.MaxCompletionTokens, 8192)
}

// max_output is promised on the minimum across the whole seat tree too, so a
// nested combo whose leaves disagree publishes the smaller of the two.
func TestHandleModels_NestedComboPublishesSmallestLeafLimit(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	// inner holds the two windows (200000 for anthropic/claude-sonnet-4-6 and
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
	wantLimit(t, "inner max_completion_tokens", inner.MaxCompletionTokens, 8192)
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
	wantLimit(t, "self-combo context_length", comboEntry(t, entries, "self-combo").ContextLength, 131072)
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

	wantLimit(t, "context_length", entry.ContextLength, 131072)
	wantLimit(t, "max_completion_tokens", entry.MaxCompletionTokens, 8192)
}