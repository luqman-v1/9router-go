package chat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/translator"
)

// applyJailbreak is the injection boundary for every chat request. These tests
// pin the gate that matters: a model id with the toggle off (or no document)
// passes through untouched, and an armed model gets the prompt before the
// original content — in both wire formats — with the context flag set so the
// Gemini path can add safetySettings. Keys are the ids /v1/models serves,
// exactly as the client sends them: prefixes and suffixes are part of the key.

func newTestRepo(t *testing.T) *db.Repo {
	t.Helper()
	database, cleanup := setupChatTestDB(t)
	t.Cleanup(cleanup)
	return db.NewRepo(database)
}

// A model whose toggle is off (or no repo at all) must leave the body
// byte-identical. This is the default posture: injection never happens
// because a model exists, only because it was switched on.
func TestModelOffPassesThroughUntouched(t *testing.T) {
	h := &ChatHandler{} // Repo nil: nothing configured anywhere
	body := map[string]any{
		"model":    "wal/glm-5.3-flash",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	}
	before, _ := json.Marshal(body)
	ctx := h.applyJailbreak(context.Background(), body, "wal/glm-5.3-flash")
	if translator.GeminiSafetyRequested(ctx) {
		t.Error("off model must not set the safety flag")
	}
	after, _ := json.Marshal(body)
	if string(before) != string(after) {
		t.Errorf("body was modified while model off: %s -> %s", before, after)
	}
}

func TestModelOnWithDocInjectsPrompt(t *testing.T) {
	repo := newTestRepo(t)
	if err := repo.SaveJailbreakDoc(&db.JailbreakDoc{Prompt: "CUSTOM RULES", GeminiSafety: true}); err != nil {
		t.Fatalf("save doc: %v", err)
	}
	const armed = "wal/glm-5.3-flash"
	if err := repo.SetJailbreakModel(armed, true); err != nil {
		t.Fatalf("enable model: %v", err)
	}

	h := &ChatHandler{Repo: repo}
	body := map[string]any{
		"messages": []any{
			map[string]any{"role": "system", "content": "be helpful"},
			map[string]any{"role": "user", "content": "hi"},
		},
	}
	ctx := h.applyJailbreak(context.Background(), body, armed)
	if !translator.GeminiSafetyRequested(ctx) {
		t.Error("doc.geminiSafety must set the context flag")
	}
	msgs := body["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("message count = %d, want 3 (prompt + original system + user)", len(msgs))
	}
	first, _ := json.Marshal(msgs[0])
	var got map[string]any
	_ = json.Unmarshal(first, &got)
	if got["role"] != "system" || got["content"] != "CUSTOM RULES" {
		t.Errorf("first message = %s, want the prompt as system", first)
	}
	second, _ := json.Marshal(msgs[1])
	_ = json.Unmarshal(second, &got)
	if got["content"] != "be helpful" {
		t.Errorf("original system message damaged: %s", second)
	}

	// A nearby but different model id — no bleed, no prefix matching.
	other := map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}}}
	before, _ := json.Marshal(other)
	h.applyJailbreak(context.Background(), other, "wal/glm-5.3-flash-turbo")
	after, _ := json.Marshal(other)
	if string(before) != string(after) {
		t.Errorf("unrelated model was injected: %s", after)
	}
}

func TestModelWithSpecificPresetInjectsCorrectPrompt(t *testing.T) {
	repo := newTestRepo(t)
	presetCoder := &db.JailbreakDoc{ID: "coder", Name: "Coder", Prompt: "CODER INSTRUCTION", GeminiSafety: true}
	presetWriter := &db.JailbreakDoc{ID: "writer", Name: "Writer", Prompt: "WRITER INSTRUCTION", GeminiSafety: false}
	if err := repo.SaveJailbreakPreset(presetCoder); err != nil {
		t.Fatalf("save coder: %v", err)
	}
	if err := repo.SaveJailbreakPreset(presetWriter); err != nil {
		t.Fatalf("save writer: %v", err)
	}

	const model1 = "ag/gemini-3.1-pro-high"
	const model2 = "anthropic/claude-3-7-sonnet"

	if err := repo.SetJailbreakModelPreset(model1, "coder", true); err != nil {
		t.Fatalf("bind model1: %v", err)
	}
	if err := repo.SetJailbreakModelPreset(model2, "writer", true); err != nil {
		t.Fatalf("bind model2: %v", err)
	}

	h := &ChatHandler{Repo: repo}

	// Model 1 gets coder
	body1 := map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": "write code"}},
	}
	ctx1 := h.applyJailbreak(context.Background(), body1, model1)
	if !translator.GeminiSafetyRequested(ctx1) {
		t.Error("model1 should request gemini safety")
	}
	msgs1 := body1["messages"].([]any)
	if len(msgs1) != 2 || msgs1[0].(map[string]any)["content"] != "CODER INSTRUCTION" {
		t.Errorf("model1 prompt mismatch: %+v", msgs1)
	}

	// Model 2 gets writer
	body2 := map[string]any{
		"system":   "base system",
		"messages": []any{map[string]any{"role": "user", "content": "write poem"}},
	}
	ctx2 := h.applyJailbreak(context.Background(), body2, model2)
	if translator.GeminiSafetyRequested(ctx2) {
		t.Error("model2 should not request gemini safety")
	}
	sys2 := body2["system"].(string)
	if sys2 != "WRITER INSTRUCTION\n\nbase system" {
		t.Errorf("model2 system mismatch: %s", sys2)
	}
}

func TestModelOnWithEmptyDocInjectsNothing(t *testing.T) {
	repo := newTestRepo(t)
	if err := repo.SetJailbreakModel("wal/glm-5.3-flash", true); err != nil {
		t.Fatalf("enable model: %v", err)
	}
	h := &ChatHandler{Repo: repo}
	body := map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}}}
	before, _ := json.Marshal(body)
	h.applyJailbreak(context.Background(), body, "wal/glm-5.3-flash")
	after, _ := json.Marshal(body)
	if string(before) != string(after) {
		t.Errorf("empty doc was injected: %s -> %s", before, after)
	}
}

func TestSystemPromptPrependedToClaudeStringSystem(t *testing.T) {
	body := map[string]any{"system": "be concise"}
	prependSystemPrompt(body, "CUSTOM RULES")
	if got := body["system"].(string); got != "CUSTOM RULES\n\nbe concise" {
		t.Errorf("system = %q, want prompt prepended", got)
	}
}

func TestSystemPromptPrependedToClaudeBlockSystem(t *testing.T) {
	orig := []any{map[string]any{"type": "text", "text": "be concise"}}
	body := map[string]any{"system": orig}
	prependSystemPrompt(body, "CUSTOM RULES")
	blocks := body["system"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("blocks = %d, want 2", len(blocks))
	}
	first, _ := json.Marshal(blocks[0])
	var got map[string]any
	_ = json.Unmarshal(first, &got)
	if got["type"] != "text" || got["text"] != "CUSTOM RULES" {
		t.Errorf("first block = %s, want the prompt", first)
	}
	second, _ := json.Marshal(blocks[1])
	var origBack map[string]any
	_ = json.Unmarshal(second, &origBack)
	if origBack["text"] != "be concise" {
		t.Errorf("original block damaged: %s", second)
	}
}

func TestGeminiSafetyContextRoundTrip(t *testing.T) {
	ctx := context.Background()
	if translator.GeminiSafetyRequested(ctx) {
		t.Error("default must be off")
	}
	ctx = translator.WithGeminiSafety(ctx, true)
	if !translator.GeminiSafetyRequested(ctx) {
		t.Error("flag lost in round trip")
	}
}

func TestGeminiSafetySettingsInjectedAndPreserved(t *testing.T) {
	src := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	out, err := translator.ApplyGeminiSafetySettings(src)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	ss, ok := got["safetySettings"].([]any)
	if !ok || len(ss) != 4 {
		t.Fatalf("safetySettings = %v, want 4 categories", got["safetySettings"])
	}
	if got["contents"] == nil {
		t.Error("original contents lost")
	}

	// A body that already carries safetySettings is not second-guessed.
	withOwn := []byte(`{"safetySettings":[{"category":"X","threshold":"BLOCK_ONLY"}],"contents":[]}`)
	out2, err := translator.ApplyGeminiSafetySettings(withOwn)
	if err != nil {
		t.Fatalf("apply second: %v", err)
	}
	if string(out2) != string(withOwn) {
		t.Error("existing safetySettings must be returned untouched")
	}
}

// TestComboArmedByComboNameInjectsIntoUpstream proves the jailbreak toggle
// works through the combo path, not just at the applyJailbreak unit boundary.
// The toggle is keyed on the combo NAME (what the client sends), and the
// injected prompt must reach the real outbound body for every leg.
func TestComboArmedByComboNameInjectsIntoUpstream(t *testing.T) {
	var gotBody atomic.Value
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		raw, _ := io.ReadAll(r.Body)
		gotBody.Store(string(raw))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"x","choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`DELETE FROM providerConnections WHERE id IN ('conn-1','conn-2')`); err != nil {
		t.Fatalf("clear seeded connections: %v", err)
	}
	seedConnDB(t, database, "deepseek", "conn-jb", "sk-jb", srv.URL)

	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	comboModels := []string{"deepseek/deepseek-chat"}
	modelsJSON, _ := json.Marshal(comboModels)
	if _, err := database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES ('c-jb','combo-jb','fallback',?,'2026-07-18T00:00:00Z','2026-07-18T00:00:00Z')`, string(modelsJSON)); err != nil {
		t.Fatalf("seed combo: %v", err)
	}

	// Arm the COMBO NAME, not the leg model.
	if err := repo.SaveJailbreakPreset(&db.JailbreakDoc{ID: "jb", Name: "JB", Prompt: "COMBO INJECTED RULE"}); err != nil {
		t.Fatalf("save preset: %v", err)
	}
	if err := repo.SetJailbreakModelPreset("combo-jb", "jb", true); err != nil {
		t.Fatalf("arm combo: %v", err)
	}

	translatedReq := map[string]any{
		"model":      "deepseek-chat",
		"max_tokens": 50,
		"messages":   []map[string]any{{"role": "user", "content": "hi"}},
	}
	rec := httptest.NewRecorder()
	h.handleMessagesComboFallback(context.Background(), rec, translatedReq, comboModels, "fallback", false, "combo-jb", 0)

	if hits.Load() == 0 {
		t.Fatal("upstream never called")
	}
	body, _ := gotBody.Load().(string)
	if !strings.Contains(body, "COMBO INJECTED RULE") {
		t.Errorf("combo jailbreak did not reach upstream body: %s", body)
	}

	// The client body must not be mutated: the same map is reused per leg.
	reqMsgs := translatedReq["messages"].([]map[string]any)
	if len(reqMsgs) != 1 {
		t.Errorf("request body mutated: %d messages, want 1", len(reqMsgs))
	}
}

// TestComboWithoutArmInjectsNothing is the control: an unarmed combo must
// forward the body untouched, proving the test above is not a false positive.
func TestComboWithoutArmInjectsNothing(t *testing.T) {
	var gotBody atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotBody.Store(string(raw))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"x","choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`DELETE FROM providerConnections WHERE id IN ('conn-1','conn-2')`); err != nil {
		t.Fatalf("clear seeded connections: %v", err)
	}
	seedConnDB(t, database, "deepseek", "conn-jb2", "sk-jb2", srv.URL)

	h := NewChatHandler(db.NewRepo(database))
	comboModels := []string{"deepseek/deepseek-chat"}
	modelsJSON, _ := json.Marshal(comboModels)
	database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES ('c-jb2','combo-jb2','fallback',?,'2026-07-18T00:00:00Z','2026-07-18T00:00:00Z')`, string(modelsJSON))

	translatedReq := map[string]any{
		"model":    "deepseek-chat",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	}
	h.handleMessagesComboFallback(context.Background(), httptest.NewRecorder(), translatedReq, comboModels, "fallback", false, "combo-jb2", 0)

	body, _ := gotBody.Load().(string)
	if body == "" {
		t.Fatal("upstream never called")
	}
	if strings.Contains(body, `"role":"system"`) {
		t.Errorf("unarmed combo still injected a system prompt: %s", body)
	}
}

// TestSingleModelArmedInjectsIntoUpstream proves the plain (non-combo) path
// also injects: handleMessagesSingleModel arms on the client's model id and the
// prompt must reach the real outbound body. This is the path a single
// provider/model request takes, so "every model" is covered end to end and not
// only through the combo machinery above.
func TestSingleModelArmedInjectsIntoUpstream(t *testing.T) {
	var gotBody atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotBody.Store(string(raw))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"x","content":[{"type":"text","text":"ok"}]}`))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`DELETE FROM providerConnections WHERE id IN ('conn-1','conn-2')`); err != nil {
		t.Fatalf("clear seeded connections: %v", err)
	}
	seedConnDB(t, database, "deepseek", "conn-jb3", "sk-jb3", srv.URL)

	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	if err := repo.SaveJailbreakPreset(&db.JailbreakDoc{ID: "s", Name: "S", Prompt: "SINGLE INJECTED RULE"}); err != nil {
		t.Fatalf("save preset: %v", err)
	}
	// Arm the id the client sends (provider/model), not the upstream model.
	if err := repo.SetJailbreakModelPreset("deepseek/deepseek-chat", "s", true); err != nil {
		t.Fatalf("arm model: %v", err)
	}

	translatedReq := map[string]any{
		"model":      "deepseek/deepseek-chat",
		"max_tokens": 50,
		"messages":   []map[string]any{{"role": "user", "content": "hi"}},
	}
	modelInfo := &ModelInfo{Provider: "deepseek", Model: "deepseek-chat", ConnectionID: "conn-jb3"}
	h.handleMessagesSingleModel(context.Background(), httptest.NewRecorder(), translatedReq, modelInfo, false, false)

	body, _ := gotBody.Load().(string)
	if body == "" {
		t.Fatal("upstream never called")
	}
	if !strings.Contains(body, "SINGLE INJECTED RULE") {
		t.Errorf("single-model jailbreak did not reach upstream body: %s", body)
	}
}

// TestSingleModelOpenAIArmedInjectsIntoUpstream covers the OpenAI chat path
// (handleSingleModel), the most common single-model entry, end to end.
func TestSingleModelOpenAIArmedInjectsIntoUpstream(t *testing.T) {
	var gotBody atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotBody.Store(string(raw))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"x","choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`DELETE FROM providerConnections WHERE id IN ('conn-1','conn-2')`); err != nil {
		t.Fatalf("clear seeded connections: %v", err)
	}
	seedConnDB(t, database, "deepseek", "conn-jb4", "sk-jb4", srv.URL)

	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	if err := repo.SaveJailbreakPreset(&db.JailbreakDoc{ID: "o", Name: "O", Prompt: "SINGLE OPENAI RULE"}); err != nil {
		t.Fatalf("save preset: %v", err)
	}
	if err := repo.SetJailbreakModelPreset("deepseek/deepseek-chat", "o", true); err != nil {
		t.Fatalf("arm model: %v", err)
	}

	body := []byte(`{"model":"deepseek/deepseek-chat","max_tokens":50,"messages":[{"role":"user","content":"hi"}]}`)
	modelInfo := &ModelInfo{Provider: "deepseek", Model: "deepseek-chat", ConnectionID: "conn-jb4"}
	h.handleSingleModel(context.Background(), httptest.NewRecorder(), body, modelInfo, false, false)

	got, _ := gotBody.Load().(string)
	if got == "" {
		t.Fatal("upstream never called")
	}
	if !strings.Contains(got, "SINGLE OPENAI RULE") {
		t.Errorf("single-model (openai) jailbreak did not reach upstream body: %s", got)
	}
}

// TestKeywordArmedPresetInjectsIntoUpstream proves a preset armed by keyword
// alone — no per-model binding anywhere — injects on the real forward path.
// The keyword matches the leg ids the request is forwarded to
// ("deepseek/deepseek-chat" and "deepseek-chat"), which is what lets one
// preset cover the same model across providers and combos.
func TestKeywordArmedPresetInjectsIntoUpstream(t *testing.T) {
	var gotBody atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotBody.Store(string(raw))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"x","choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`DELETE FROM providerConnections WHERE id IN ('conn-1','conn-2')`); err != nil {
		t.Fatalf("clear seeded connections: %v", err)
	}
	seedConnDB(t, database, "deepseek", "conn-jb5", "sk-jb5", srv.URL)

	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	if err := repo.SaveJailbreakPreset(&db.JailbreakDoc{
		ID:       "kw",
		Name:     "KW",
		Prompt:   "KEYWORD INJECTED RULE",
		Keywords: []db.JailbreakKeyword{{Text: "deepseek"}},
	}); err != nil {
		t.Fatalf("save preset: %v", err)
	}

	body := []byte(`{"model":"deepseek/deepseek-chat","max_tokens":50,"messages":[{"role":"user","content":"hi"}]}`)
	modelInfo := &ModelInfo{Provider: "deepseek", Model: "deepseek-chat", ConnectionID: "conn-jb5"}
	h.handleSingleModel(context.Background(), httptest.NewRecorder(), body, modelInfo, false, false)

	got, _ := gotBody.Load().(string)
	if got == "" {
		t.Fatal("upstream never called")
	}
	if !strings.Contains(got, "KEYWORD INJECTED RULE") {
		t.Errorf("keyword-armed preset did not reach upstream body: %s", got)
	}
}

// TestPresetExcludeBlocksKeywordInjection is the control for the test above:
// the same keyword match, denied by the preset's own exclude list, must
// forward the body untouched.
func TestPresetExcludeBlocksKeywordInjection(t *testing.T) {
	var gotBody atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotBody.Store(string(raw))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"x","choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`DELETE FROM providerConnections WHERE id IN ('conn-1','conn-2')`); err != nil {
		t.Fatalf("clear seeded connections: %v", err)
	}
	seedConnDB(t, database, "deepseek", "conn-jb6", "sk-jb6", srv.URL)

	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	if err := repo.SaveJailbreakPreset(&db.JailbreakDoc{
		ID:       "kw",
		Name:     "KW",
		Prompt:   "KEYWORD INJECTED RULE",
		Keywords: []db.JailbreakKeyword{{Text: "deepseek"}},
		Excludes: []db.JailbreakKeyword{{Text: "deepseek-chat"}},
	}); err != nil {
		t.Fatalf("save preset: %v", err)
	}

	body := []byte(`{"model":"deepseek/deepseek-chat","max_tokens":50,"messages":[{"role":"user","content":"hi"}]}`)
	modelInfo := &ModelInfo{Provider: "deepseek", Model: "deepseek-chat", ConnectionID: "conn-jb6"}
	h.handleSingleModel(context.Background(), httptest.NewRecorder(), body, modelInfo, false, false)

	got, _ := gotBody.Load().(string)
	if got == "" {
		t.Fatal("upstream never called")
	}
	if strings.Contains(got, "KEYWORD INJECTED RULE") {
		t.Errorf("preset exclude did not block injection: %s", got)
	}
	if strings.Contains(got, `"role":"system"`) {
		t.Errorf("body was modified by an excluded preset: %s", got)
	}
}

// TestComboLegKeywordInjectsIntoUpstream proves the combo path resolves a
// keyword against the leg ids, not only against the combo name: the combo is
// unbound, yet its deepseek leg is injected because the preset keyword
// matches it. That is what makes one preset cover a combo whose legs come
// from different providers.
func TestComboLegKeywordInjectsIntoUpstream(t *testing.T) {
	var gotBody atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotBody.Store(string(raw))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"x","choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`DELETE FROM providerConnections WHERE id IN ('conn-1','conn-2')`); err != nil {
		t.Fatalf("clear seeded connections: %v", err)
	}
	seedConnDB(t, database, "deepseek", "conn-jb7", "sk-jb7", srv.URL)

	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	comboModels := []string{"deepseek/deepseek-chat"}
	modelsJSON, _ := json.Marshal(comboModels)
	if _, err := database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES ('c-jbkw','combo-jbkw','fallback',?,'2026-07-18T00:00:00Z','2026-07-18T00:00:00Z')`, string(modelsJSON)); err != nil {
		t.Fatalf("seed combo: %v", err)
	}

	// No binding on the combo name and none on the leg: the keyword is the
	// only thing arming this request.
	if err := repo.SaveJailbreakPreset(&db.JailbreakDoc{
		ID:       "kw",
		Name:     "KW",
		Prompt:   "COMBO LEG KEYWORD RULE",
		Keywords: []db.JailbreakKeyword{{Text: "deepseek"}},
	}); err != nil {
		t.Fatalf("save preset: %v", err)
	}

	translatedReq := map[string]any{
		"model":      "deepseek-chat",
		"max_tokens": 50,
		"messages":   []map[string]any{{"role": "user", "content": "hi"}},
	}
	rec := httptest.NewRecorder()
	h.handleMessagesComboFallback(context.Background(), rec, translatedReq, comboModels, "fallback", false, "combo-jbkw", 0)

	got, _ := gotBody.Load().(string)
	if got == "" {
		t.Fatal("upstream never called")
	}
	if !strings.Contains(got, "COMBO LEG KEYWORD RULE") {
		t.Errorf("combo leg keyword did not reach upstream body: %s", got)
	}
}
