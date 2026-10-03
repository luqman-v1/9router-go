package chat

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	json "encoding/json/v2"

	"9router/proxy/internal/db"
)

// Setup for the "main combo has no vision model" experiment:
//
//   combo "main-combo"  -> ["deepseek/deepseek-chat"]   (text-only)
//   capacityAdapter     -> {"vision": {"enabled": true, "models": ["ag/gemini-3.8-flash-high"]}}
//
// Question under test: does a request carrying an image get served by the
// vision adapter model, or is it handed to the text-only model anyway?

const visionProbeImage = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

type visionComboFixture struct {
	database *sql.DB
	repo     *db.Repo
	h        *ChatHandler

	mainHits atomic.Int32 // deepseek (text-only, inside main-combo)
	agHits   atomic.Int32 // antigravity (vision adapter pool)

	// mainModels records the "model" field of every request the deepseek
	// upstream received, so a test can assert which combo entry answered.
	mainMu     sync.Mutex
	mainModels []string

	// withAntigravityConn controls whether the adapter model has a usable account.
	withAntigravityConn bool
}

func newVisionComboFixture(t *testing.T, opts ...func(*visionComboFixture)) *visionComboFixture {
	t.Helper()

	f := &visionComboFixture{withAntigravityConn: true}
	for _, o := range opts {
		o(f)
	}

	database, cleanup := setupChatTestDB(t)
	t.Cleanup(cleanup)
	f.database = database
	f.repo = db.NewRepo(database)

	// The seeded conn-1 deepseek connection has no baseUrl; replace it with the mock.
	if _, err := database.Exec(`DELETE FROM providerConnections`); err != nil {
		t.Fatalf("clear connections: %v", err)
	}

	mainUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mainHits.Add(1)
		f.recordMainModel(r)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"ds","choices":[{"message":{"content":"TEXT-ONLY-ANSWER"}}]}`))
	}))
	t.Cleanup(mainUpstream.Close)

	seedConnDB(t, database, "deepseek", "conn-ds-main", "sk-test", mainUpstream.URL)

	if f.withAntigravityConn {
		agUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			f.agHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"VISION-ANSWER"}],"role":"model"}}]}`))
		}))
		t.Cleanup(agUpstream.Close)

		agData, _ := json.Marshal(map[string]any{
			"apiKey":      "sk-test-ag",
			"accessToken": "sk-test-ag",
			"baseUrl":     agUpstream.URL,
			"projectId":   "mock-project",
			"expiresAt":   "2099-01-01T00:00:00Z",
		})
		if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
			('conn-ag', 'antigravity', 'apikey', 'Mock AG', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(agData)); err != nil {
			t.Fatalf("seed antigravity connection: %v", err)
		}
	}

	// Main combo: one text-only model.
	if _, err := database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES
		('c-main', 'main-combo', 'fallback', '["deepseek/deepseek-chat"]', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`); err != nil {
		t.Fatalf("seed combo: %v", err)
	}

	// Vision adapter pool enabled.
	settingsJSON, _ := json.Marshal(map[string]any{
		"capacityAdapter": map[string]any{
			"vision": map[string]any{
				"enabled": true,
				"models":  []string{"ag/gemini-3.8-flash-high"},
			},
		},
	})
	if _, err := database.Exec(`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`, string(settingsJSON)); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	f.h = NewChatHandler(f.repo)
	return f
}

func withoutAntigravityConn() func(*visionComboFixture) {
	return func(f *visionComboFixture) { f.withAntigravityConn = false }
}

// recordMainModel notes which combo entry the deepseek upstream was asked for,
// so a test can assert the routing decision rather than the connection picked.
func (f *visionComboFixture) recordMainModel(r *http.Request) {
	var payload struct {
		Model string `json:"model"`
	}
	body, _ := io.ReadAll(r.Body)
	if err := json.Unmarshal(body, &payload); err != nil {
		return
	}
	f.mainMu.Lock()
	f.mainModels = append(f.mainModels, payload.Model)
	f.mainMu.Unlock()
}

func (f *visionComboFixture) servedModels() []string {
	f.mainMu.Lock()
	defer f.mainMu.Unlock()
	return append([]string(nil), f.mainModels...)
}

func (f *visionComboFixture) servedModel(model string) bool {
	return slices.Contains(f.servedModels(), model)
}

func chatBodyWithImage(model string) string {
	return `{"model":"` + model + `","messages":[{"role":"user","content":[` +
		`{"type":"text","text":"what is this?"},` +
		`{"type":"image_url","image_url":{"url":"data:image/png;base64,` + visionProbeImage + `"}}` +
		`]}],"stream":false}`
}

func claudeBodyWithImage(model string) string {
	return `{"model":"` + model + `","messages":[{"role":"user","content":[` +
		`{"type":"text","text":"what is this?"},` +
		`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + visionProbeImage + `"}}` +
		`]}],"max_tokens":64,"stream":false}`
}

// A combo whose only model has no vision must hand an image request to the
// vision adapter model instead of the text-only model inside the combo.
func TestVisionComboFallback_ChatCompletions_SwitchesToAdapter(t *testing.T) {
	f := newVisionComboFixture(t)

	rec := httptest.NewRecorder()
	f.h.HandleChatCompletions(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(chatBodyWithImage("main-combo"))))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "VISION-ANSWER") {
		t.Errorf("body should carry the vision adapter answer, got: %s", rec.Body.String())
	}
	if f.agHits.Load() != 1 {
		t.Errorf("vision adapter hits = %d, want 1", f.agHits.Load())
	}
	if f.mainHits.Load() != 0 {
		t.Errorf("text-only combo model was called %d times; it cannot read images", f.mainHits.Load())
	}
}

// Same switch on the Anthropic /v1/messages lane.
func TestVisionComboFallback_Messages_SwitchesToAdapter(t *testing.T) {
	f := newVisionComboFixture(t)

	rec := httptest.NewRecorder()
	f.h.HandleMessages(rec, httptest.NewRequest("POST", "/v1/messages",
		strings.NewReader(claudeBodyWithImage("main-combo"))))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "VISION-ANSWER") {
		t.Errorf("body should carry the vision adapter answer, got: %s", rec.Body.String())
	}
	if f.agHits.Load() != 1 {
		t.Errorf("vision adapter hits = %d, want 1", f.agHits.Load())
	}
	if f.mainHits.Load() != 0 {
		t.Errorf("text-only combo model was called %d times; it cannot read images", f.mainHits.Load())
	}
}

// A text-only turn must stay inside the combo: no adapter model on a request
// that needs no vision.
func TestVisionComboFallback_TextOnlyTurn_StaysOnCombo(t *testing.T) {
	f := newVisionComboFixture(t)

	rec := httptest.NewRecorder()
	f.h.HandleChatCompletions(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"main-combo","messages":[{"role":"user","content":"hi"}],"stream":false}`)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if f.mainHits.Load() != 1 {
		t.Errorf("combo model hits = %d, want 1", f.mainHits.Load())
	}
	if f.agHits.Load() != 0 {
		t.Errorf("vision adapter must stay out of a text-only turn, got %d hits", f.agHits.Load())
	}
}

// When the adapter model has no usable account, the request must still land
// somewhere: the combo's own model is tried after the adapter fails.
func TestVisionComboFallback_AdapterUnreachable_FallsThroughToCombo(t *testing.T) {
	f := newVisionComboFixture(t, withoutAntigravityConn())

	rec := httptest.NewRecorder()
	f.h.HandleChatCompletions(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(chatBodyWithImage("main-combo"))))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if f.mainHits.Load() != 1 {
		t.Errorf("combo model hits = %d, want 1 after the adapter failed to resolve", f.mainHits.Load())
	}
}

// A combo that already holds a vision-capable model must serve the image
// itself; the adapter pool must not be injected.
func TestVisionComboFallback_ComboAlreadyVisionCapable_NoAdapter(t *testing.T) {
	f := newVisionComboFixture(t)

	if _, err := f.database.Exec(`UPDATE combos SET models = ? WHERE id = 'c-main'`,
		`["deepseek/deepseek-chat","deepseek/deepseek-v4-vision"]`); err != nil {
		t.Fatalf("update combo: %v", err)
	}

	rec := httptest.NewRecorder()
	f.h.HandleChatCompletions(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(chatBodyWithImage("main-combo"))))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if f.agHits.Load() != 0 {
		t.Errorf("adapter pool must not be injected when the combo is already vision-capable, got %d hits", f.agHits.Load())
	}
	if !f.servedModel("deepseek-v4-vision") {
		t.Errorf("expected the combo's own vision model to be tried first, upstream saw %v", f.servedModels())
	}
}
