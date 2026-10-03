package chat

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// Edge probes for the vision adapter auto-switch. These map the boundaries
// where an image can still reach a text-only model, and pin the upstream
// contract: a combo name is never a capability source, a disabled pool injects
// nothing, and media in an older turn must not pin the combo.

// Adapter disabled in settings: the toggle must be honored, so no vision
// model is injected and the image goes to the text-only combo model.
func TestVisionComboEdge_AdapterDisabled_HonorsToggle(t *testing.T) {
	f := newVisionComboFixture(t)

	settingsJSON := `{"capacityAdapter":{"vision":{"enabled":false,"models":["ag/gemini-3.8-flash-high"]}}}`
	if _, err := f.database.Exec(`UPDATE settings SET data = ? WHERE id = 1`, settingsJSON); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	rec := httptest.NewRecorder()
	f.h.HandleChatCompletions(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(chatBodyWithImage("main-combo"))))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if f.agHits.Load() != 0 {
		t.Errorf("a disabled adapter must inject nothing, got %d hits", f.agHits.Load())
	}
	if !f.servedModel("deepseek-chat") {
		t.Errorf("expected the combo's text-only model to take the image, upstream saw %v", f.servedModels())
	}
}

// The user's actual question: can the vision pool point at a COMBO that holds
// a vision model, so a text-only main combo falls back to a vision combo?
//
// Upstream answers no: modelSatisfies splits on "/" exactly as ours does, so a
// bare combo name carries no capability, is dropped from the pool, and — with
// no other enabled pool member — nothing is injected.
func TestVisionComboEdge_ComboPoolIsNotACapabilitySource(t *testing.T) {
	f := newVisionComboFixture(t, withoutAntigravityConn())

	if _, err := f.database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES
		('c-vis', 'vision-combo', 'fallback', '["deepseek/deepseek-v4-vision"]', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`); err != nil {
		t.Fatalf("seed vision combo: %v", err)
	}
	settingsJSON := `{"capacityAdapter":{"vision":{"enabled":true,"models":["vision-combo"]}}}`
	if _, err := f.database.Exec(`UPDATE settings SET data = ? WHERE id = 1`, settingsJSON); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	rec := httptest.NewRecorder()
	f.h.HandleChatCompletions(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(chatBodyWithImage("main-combo"))))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if f.servedModel("deepseek-v4-vision") {
		t.Errorf("a combo name in the pool must not be expanded into its leaves, got %v", f.servedModels())
	}
	if !f.servedModel("deepseek-chat") {
		t.Errorf("with nothing injected the combo's own model serves the request, upstream saw %v", f.servedModels())
	}
}

// An image sitting in an EARLIER turn must not pin the combo to a vision
// model: upstream scans only the trailing user run, because that history is
// stripped or placeholdered downstream.
func TestVisionComboEdge_ImageInEarlierTurn_DoesNotPin(t *testing.T) {
	f := newVisionComboFixture(t)

	body := `{"model":"main-combo","messages":[` +
		`{"role":"user","content":[{"type":"text","text":"what is this?"},` +
		`{"type":"image_url","image_url":{"url":"data:image/png;base64,` + visionProbeImage + `"}}]},` +
		`{"role":"assistant","content":"a red square"},` +
		`{"role":"user","content":"and now describe the mood"}` +
		`],"stream":false}`

	rec := httptest.NewRecorder()
	f.h.HandleChatCompletions(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if f.agHits.Load() != 0 {
		t.Errorf("history media must not trigger a vision switch, got %d adapter hits", f.agHits.Load())
	}
	if !f.servedModel("deepseek-chat") {
		t.Errorf("expected the combo's own model to serve the text-only turn, upstream saw %v", f.servedModels())
	}
}

// A fusion combo whose panel is entirely text-only: upstream passes the
// ORIGINAL combo models to the fusion path (never the augmented list), so the
// panel is fanned out as configured and the adapter is not consulted.
func TestVisionComboEdge_FusionUsesOriginalModels(t *testing.T) {
	f := newVisionComboFixture(t)

	if _, err := f.database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES
		('c-fus', 'fusion-combo', 'fallback', '["deepseek/deepseek-chat","groq/llama-3-70b"]', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`); err != nil {
		t.Fatalf("seed fusion combo: %v", err)
	}
	if _, err := f.database.Exec(`UPDATE settings SET data = ? WHERE id = 1`,
		`{"comboStrategies":{"fusion-combo":{"fallbackStrategy":"fusion"}},`+
			`"capacityAdapter":{"vision":{"enabled":true,"models":["ag/gemini-3.8-flash-high"]}}}`); err != nil {
		t.Fatalf("seed fusion strategy: %v", err)
	}

	groqHits := atomic.Int32{}
	groqUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		groqHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"groq","choices":[{"message":{"content":"GROQ-PANEL-ANSWER"}}]}`))
	}))
	t.Cleanup(groqUpstream.Close)
	seedConnDB(t, f.database, "groq", "conn-groq", "sk-test", groqUpstream.URL)

	rec := httptest.NewRecorder()
	f.h.HandleChatCompletions(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(chatBodyWithImage("fusion-combo"))))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if f.agHits.Load() != 0 {
		t.Errorf("fusion must fan out the combo's own panel, got %d adapter hits", f.agHits.Load())
	}
}

// Round-robin combo whose only vision-capable member is NOT the rotated one:
// the reorder must still put the capable model first on every turn.
func TestVisionComboEdge_RoundRobinFloatsVisionModelEveryTurn(t *testing.T) {
	f := newVisionComboFixture(t)

	if _, err := f.database.Exec(`UPDATE combos SET models = ? WHERE id = 'c-main'`,
		`["deepseek/deepseek-chat","deepseek/deepseek-v4-vision"]`); err != nil {
		t.Fatalf("update combo: %v", err)
	}
	if _, err := f.database.Exec(`UPDATE settings SET data = ? WHERE id = 1`,
		`{"capacityAdapter":{"vision":{"enabled":true,"models":["ag/gemini-3.8-flash-high"]}},`+
			`"comboStrategies":{"main-combo":{"fallbackStrategy":"round-robin","stickyRoundRobinLimit":1}}}`); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	for turn := range 4 {
		rec := httptest.NewRecorder()
		f.h.HandleChatCompletions(rec, httptest.NewRequest("POST", "/v1/chat/completions",
			strings.NewReader(chatBodyWithImage("main-combo"))))
		if rec.Code != http.StatusOK {
			t.Fatalf("turn %d: status = %d, body = %s", turn, rec.Code, rec.Body.String())
		}
	}

	if f.agHits.Load() != 0 {
		t.Errorf("combo is already vision-capable; adapter must stay out, got %d hits", f.agHits.Load())
	}
	for _, m := range f.servedModels() {
		if m != "deepseek-v4-vision" {
			t.Errorf("round-robin leaked a text-only model onto an image turn: %v", f.servedModels())
			break
		}
	}
}
