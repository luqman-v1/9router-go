package dashboard

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"9router/proxy/internal/db"
)

// fetchCaps calls GET /api/models/caps?provider=<provider> and returns the
// decoded caps map keyed by model id.
func fetchCaps(t *testing.T, provider string) (int, map[string]modelCaps) {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/api/models/caps?provider="+provider, nil)
	rec := httptest.NewRecorder()
	setupTestRouter(setupTestRepoForCaps(t)).ServeHTTP(rec, req)

	var body struct {
		Caps map[string]modelCaps `json:"caps"`
	}
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("unmarshal caps response: %v", err)
		}
	}
	return rec.Code, body.Caps
}

func setupTestRepoForCaps(t *testing.T) *db.Repo {
	t.Helper()
	repo, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	return repo
}

// TestHandleGetModelCaps_CommandCode covers what the provider detail page
// consumes: the vision/reasoning icons, the declared limits, and the thinking
// level list the "Thinking:" picker offers.
func TestHandleGetModelCaps_CommandCode(t *testing.T) {
	code, caps := fetchCaps(t, "commandcode")
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if len(caps) != 23 {
		t.Fatalf("expected 23 commandcode models, got %d", len(caps))
	}

	tests := []struct {
		model            string
		wantVision       bool
		wantContext      int
		wantMaxOutput    int
		wantThinkingLvls []string
	}{
		{
			// denylisted: no image input, but the effort picker still applies
			model: "deepseek/deepseek-v4-pro", wantVision: false,
			wantContext: 1000000, wantMaxOutput: 384000,
			wantThinkingLvls: []string{"none", "low", "medium", "high", "xhigh", "max"},
		},
		{
			model: "moonshotai/Kimi-K2.7-Code", wantVision: true,
			wantContext: 1000000, wantMaxOutput: 384000,
			wantThinkingLvls: []string{"none", "low", "medium", "high", "xhigh", "max"},
		},
		{
			model: "nvidia/nemotron-3-ultra-550b-a55b", wantVision: false,
			wantContext: 1000000, wantMaxOutput: 384000,
			wantThinkingLvls: []string{"none", "low", "medium", "high", "xhigh", "max"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			got, ok := caps[tt.model]
			if !ok {
				t.Fatalf("model %q missing from caps", tt.model)
			}
			if got.Vision != tt.wantVision {
				t.Errorf("Vision = %v, want %v", got.Vision, tt.wantVision)
			}
			if !got.Reasoning {
				t.Error("Reasoning = false, want true")
			}
			if got.ContextWindow != tt.wantContext {
				t.Errorf("ContextWindow = %d, want %d", got.ContextWindow, tt.wantContext)
			}
			if got.MaxOutput != tt.wantMaxOutput {
				t.Errorf("MaxOutput = %d, want %d", got.MaxOutput, tt.wantMaxOutput)
			}
			if !slices.Equal(got.ThinkingLevels, tt.wantThinkingLvls) {
				t.Errorf("ThinkingLevels = %v, want %v", got.ThinkingLevels, tt.wantThinkingLvls)
			}
		})
	}
}

// TestHandleGetModelCaps_AliasResolution checks that the storage prefix the
// dashboard links models with ("cmc/…") resolves to the same block as the
// provider id the route carries.
func TestHandleGetModelCaps_AliasResolution(t *testing.T) {
	_, byID := fetchCaps(t, "commandcode")
	_, byAlias := fetchCaps(t, "cmc")

	if len(byAlias) != len(byID) {
		t.Fatalf("alias resolved %d models, id resolved %d", len(byAlias), len(byID))
	}
	for model, want := range byID {
		got, ok := byAlias[model]
		if !ok {
			t.Fatalf("model %q missing when resolved via cmc", model)
		}
		if !slices.Equal(got.ThinkingLevels, want.ThinkingLevels) {
			t.Errorf("%s thinking levels via cmc = %v, want %v", model, got.ThinkingLevels, want.ThinkingLevels)
		}
	}
}

func TestHandleGetModelCaps_BadRequest(t *testing.T) {
	t.Run("missing provider", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/models/caps", nil)
		rec := httptest.NewRecorder()
		setupTestRouter(setupTestRepoForCaps(t)).ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", rec.Code)
		}
	})

	t.Run("unknown provider", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/models/caps?provider=does-not-exist", nil)
		rec := httptest.NewRecorder()
		setupTestRouter(setupTestRepoForCaps(t)).ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("expected 404, got %d", rec.Code)
		}
	})
}

func TestHandleGetModelCaps_CompatibleNode(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/models/caps?provider=openai-compatible-chat-2eb28394-dbf3-4c93-8477-2690aeea7041", nil)
	rec := httptest.NewRecorder()
	setupTestRouter(setupTestRepoForCaps(t)).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// A provider node whose models are all custom rows has no registry catalog,
// so the caps map used to come back empty and the dashboard rendered nothing
// for the models it had just been given (issue #90).
func TestHandleGetModelCaps_CustomModelsOnNode(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	if _, err := repo.CreateProviderNode("node-nara", "openai-compatible", "Nara AI",
		`{"prefix":"nara","apiType":"openai-compatible"}`); err != nil {
		t.Fatalf("seed providerNode: %v", err)
	}
	if err := repo.SetKV("customModels", "node-nara|declared-model|llm",
		`{"providerAlias":"node-nara","id":"declared-model","type":"llm","name":"declared-model","caps":{"vision":true},"contextWindow":256000,"maxOutput":16000}`); err != nil {
		t.Fatalf("seed customModels: %v", err)
	}
	// An image row is not a chat model and must not appear in a chat caps map.
	if err := repo.SetKV("customModels", "node-nara|a-drawing|llm",
		`{"providerAlias":"node-nara","id":"a-drawing","type":"image","name":"a-drawing"}`); err != nil {
		t.Fatalf("seed image custom model: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/models/caps?provider=nara", nil)
	rec := httptest.NewRecorder()
	setupTestRouter(repo).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Caps map[string]modelCaps `json:"caps"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal caps response: %v", err)
	}
	entry, ok := body.Caps["declared-model"]
	if !ok {
		t.Fatalf("custom model missing from caps map: %s", rec.Body.String())
	}
	if !entry.Vision {
		t.Error("saved vision cap not carried into the caps map")
	}
	if entry.ContextWindow != 256000 || entry.MaxOutput != 16000 {
		t.Errorf("limits = (%d, %d), want (256000, 16000)", entry.ContextWindow, entry.MaxOutput)
	}
	if _, present := body.Caps["a-drawing"]; present {
		t.Error("an image-typed custom row must not appear in a chat capability map")
	}
}

func TestHandleGetModelCaps_FreeTier(t *testing.T) {
	code, caps := fetchCaps(t, "opencode-zen")
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}

	tests := []struct {
		model    string
		wantFree bool
	}{
		{model: "mimo-v2.6-flash-free", wantFree: true},
		{model: "nemotron-3-ultra-free", wantFree: true},
		{model: "deepseek-v4-flash-free", wantFree: true}, // suffix precedence over table rate (0.14/0.28)
		{model: "big-pickle", wantFree: true},             // zero pricing path via KnownFreeModels
		{model: "gpt-5.5", wantFree: false},
		{model: "kimi-k3", wantFree: false},
		{model: "claude-opus-5", wantFree: false},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			entry, ok := caps[tt.model]
			if !ok {
				t.Fatalf("model %q missing from caps", tt.model)
			}
			if entry.Free != tt.wantFree {
				t.Errorf("model %q Free = %v, want %v", tt.model, entry.Free, tt.wantFree)
			}
		})
	}
}

func TestIsModelFree_ThinkingSuffix(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		modelID  string
		want     bool
	}{
		{name: "thinking suffix on free model", provider: "opencode-zen", modelID: "nemotron-3-ultra-free(high)", want: true},
		{name: "medium thinking suffix on free model", provider: "opencode", modelID: "deepseek-v4-flash-free(medium)", want: true},
		{name: "thinking suffix on paid model", provider: "opencode-zen", modelID: "gpt-5.5(high)", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isModelFree(tt.provider, tt.modelID)
			if got != tt.want {
				t.Errorf("isModelFree(%q, %q) = %v, want %v", tt.provider, tt.modelID, got, tt.want)
			}
		})
	}
}

func TestHandleGetModelCaps_CustomModelMergeAndStaticPrecedence(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	// Seed a custom imported free model with -free suffix for antigravity-zen (alias opencode-zen)
	if err := repo.SetKV("customModels", "opencode-zen|custom-flash-free|llm",
		`{"providerAlias":"opencode-zen","id":"custom-flash-free","type":"llm","name":"custom-flash-free"}`); err != nil {
		t.Fatalf("seed custom model: %v", err)
	}

	// Seed a custom model whose ID collides with a static catalog model (e.g. mimo-v2.6-flash-free)
	if err := repo.SetKV("customModels", "opencode-zen|mimo-v2.6-flash-free|llm",
		`{"providerAlias":"opencode-zen","id":"mimo-v2.6-flash-free","type":"llm","name":"overridden-name"}`); err != nil {
		t.Fatalf("seed colliding custom model: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/models/caps?provider=opencode-zen", nil)
	rec := httptest.NewRecorder()
	setupTestRouter(repo).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Caps map[string]modelCaps `json:"caps"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal caps response: %v", err)
	}

	// 1. Proves imported model with -free suffix appears in caps map with Free = true
	customEntry, ok := body.Caps["custom-flash-free"]
	if !ok {
		t.Fatal("imported custom model 'custom-flash-free' missing from caps map")
	}
	if !customEntry.Free {
		t.Error("imported custom model 'custom-flash-free' expected Free = true")
	}

	// 2. Proves static catalog entries are not overwritten by custom entries
	staticEntry, ok := body.Caps["mimo-v2.6-flash-free"]
	if !ok {
		t.Fatal("static model 'mimo-v2.6-flash-free' missing from caps map")
	}
	if !staticEntry.Free {
		t.Error("static catalog entry 'mimo-v2.6-flash-free' should remain free")
	}
}

func TestHandleGetModelCaps_ConsecutiveCallsStaticPrecedence(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	// Seed a custom model whose ID collides with a static catalog model (mimo-v2.6-flash-free, static ContextWindow 4096)
	if err := repo.SetKV("customModels", "opencode-zen|mimo-v2.6-flash-free|llm",
		`{"providerAlias":"opencode-zen","id":"mimo-v2.6-flash-free","type":"llm","name":"overridden-name","contextWindow":2000000}`); err != nil {
		t.Fatalf("seed colliding custom model: %v", err)
	}
	// Seed a non-colliding imported free model
	if err := repo.SetKV("customModels", "opencode-zen|deepseek-v4.1-flash-free|llm",
		`{"providerAlias":"opencode-zen","id":"deepseek-v4.1-flash-free","type":"llm","name":"deepseek-v4.1-flash-free"}`); err != nil {
		t.Fatalf("seed non-colliding custom model: %v", err)
	}

	router := setupTestRouter(repo)

	// Call 1
	req1 := httptest.NewRequest(http.MethodGet, "/api/models/caps?provider=opencode-zen", nil)
	rec1 := httptest.NewRecorder()
	router.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("call 1 expected 200, got %d: %s", rec1.Code, rec1.Body.String())
	}
	var body1 struct {
		Caps map[string]modelCaps `json:"caps"`
	}
	if err := json.Unmarshal(rec1.Body.Bytes(), &body1); err != nil {
		t.Fatalf("call 1 unmarshal: %v", err)
	}

	// Call 2 (consecutive call on same repo / process state)
	req2 := httptest.NewRequest(http.MethodGet, "/api/models/caps?provider=opencode-zen", nil)
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("call 2 expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	var body2 struct {
		Caps map[string]modelCaps `json:"caps"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &body2); err != nil {
		t.Fatalf("call 2 unmarshal: %v", err)
	}

	// Verify call 1 & 2 for colliding static ID (mimo-v2.6-flash-free) and non-colliding imported -free ID (deepseek-v4.1-flash-free)
	for i, body := range []struct {
		callName string
		caps     map[string]modelCaps
	}{
		{"call 1", body1.Caps},
		{"call 2", body2.Caps},
	} {
		_ = i
		// Colliding ID: should return static catalog caps (ContextWindow should be static catalog value, e.g. 4096, not custom 2000000)
		mimo, ok := body.caps["mimo-v2.6-flash-free"]
		if !ok {
			t.Fatalf("%s: 'mimo-v2.6-flash-free' missing from caps", body.callName)
		}
		if mimo.ContextWindow == 2000000 {
			t.Errorf("%s: 'mimo-v2.6-flash-free' ContextWindow incorrectly took custom value 2000000 instead of static catalog", body.callName)
		}

		// Non-colliding imported -free ID: should return Free = true
		ds, ok := body.caps["deepseek-v4.1-flash-free"]
		if !ok {
			t.Fatalf("%s: 'deepseek-v4.1-flash-free' missing from caps", body.callName)
		}
		if !ds.Free {
			t.Errorf("%s: non-colliding imported free model 'deepseek-v4.1-flash-free' expected Free = true", body.callName)
		}
	}
}

// TestHandleGetModelCaps_Jailbreak pins the three ways a row can end up
// flagged uncensored on the dashboard: the operator's saved flag (under any
// of its three names), and the id heuristic for a model nobody marked. The
// chip and the row icon both read this one field, so a regression here is
// invisible until an operator wonders why the filter is empty.
func TestHandleGetModelCaps_Jailbreak(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	if _, err := repo.CreateProviderNode("node-nara", "openai-compatible", "Nara AI",
		`{"prefix":"nara","apiType":"openai-compatible"}`); err != nil {
		t.Fatalf("seed providerNode: %v", err)
	}
	// One row per saved name — the publisher maps all three onto the field.
	for _, name := range []string{"jailbreak", "uncensored", "unrestricted"} {
		key := "node-nara|marked-" + name + "|llm"
		val := `{"providerAlias":"node-nara","id":"marked-` + name + `","type":"llm","name":"marked","caps":{"` + name + `":true}}`
		if err := repo.SetKV("customModels", key, val); err != nil {
			t.Fatalf("seed customModels %s: %v", key, err)
		}
	}
	// A neutral id with no saved flag: the name heuristic must not invent one.
	if err := repo.SetKV("customModels", "node-nara|perfectly-polite|llm",
		`{"providerAlias":"node-nara","id":"perfectly-polite","type":"llm","name":"polite"}`); err != nil {
		t.Fatalf("seed customModels polite: %v", err)
	}
	// An id that names itself: flagged without anyone opening the dialog.
	if err := repo.SetKV("customModels", "node-nara|llama-3-uncensored-70b|llm",
		`{"providerAlias":"node-nara","id":"llama-3-uncensored-70b","type":"llm","name":"Llama"}`); err != nil {
		t.Fatalf("seed customModels llama: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/models/caps?provider=nara", nil)
	rec := httptest.NewRecorder()
	setupTestRouter(repo).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Caps map[string]modelCaps `json:"caps"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal caps response: %v", err)
	}

	for _, name := range []string{"jailbreak", "uncensored", "unrestricted"} {
		entry, ok := body.Caps["marked-"+name]
		if !ok {
			t.Fatalf("marked-%s missing from caps map: %s", name, rec.Body.String())
		}
		if !entry.Jailbreak {
			t.Errorf("marked-%s: Jailbreak = false, want true", name)
		}
	}

	entry, ok := body.Caps["llama-3-uncensored-70b"]
	if !ok {
		t.Fatalf("llama-3-uncensored-70b missing from caps map: %s", rec.Body.String())
	}
	if !entry.Jailbreak {
		t.Error("llama-3-uncensored-70b: Jailbreak = false, want true from the id heuristic")
	}

	entry, ok = body.Caps["perfectly-polite"]
	if !ok {
		t.Fatalf("perfectly-polite missing from caps map: %s", rec.Body.String())
	}
	if entry.Jailbreak {
		t.Error("perfectly-polite: Jailbreak = true, want false — a neutral id must not be flagged")
	}
}
