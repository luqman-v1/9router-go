package dashboard

import (
	"context"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"

	"github.com/go-chi/chi/v5"
)

// liveCatalogModel is one entry of a live catalogue as the dashboard serves it.
// The capability keys are the contract the pickers read, so they are decoded
// into typed fields rather than a map: a wrong type (a string where a number
// belongs) has to fail the decode instead of being stringified.
type liveCatalogModel struct {
}

// fetchLiveCatalog calls GET /api/providers/{id}/models and decodes the models
// array with their capability fields.
func fetchLiveCatalog(t *testing.T, repo *db.Repo, connID string) []liveCatalogModel {
	t.Helper()
	r := chi.NewRouter()
	r.Get("/api/providers/{id}/models", NewDashboardHandler(repo).HandleGetConnectionModels)
	req := httptest.NewRequest(http.MethodGet, "/api/providers/"+connID+"/models", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Models []liveCatalogModel `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode models: %v", err)
	}
	return out.Models
}

func liveCatalogByID(t *testing.T, models []liveCatalogModel, id string) liveCatalogModel {
	t.Helper()
	idx := slices.IndexFunc(models, func(m liveCatalogModel) bool { return m.ID == id })
	if idx < 0 {
		t.Fatalf("model %q missing from catalogue: %+v", id, models)
	}
	return models[idx]
}

// withModelCapabilities is the one normalisation every live branch shares, so
// it is tested directly on the shapes the feeds actually use: a typed struct
// (ZedModel, CopilotModel), an already-decoded []any and the map slice the
// compatible-node branch builds.
func TestWithModelCapabilities_EnrichesEveryInputShape(t *testing.T) {
	t.Run("typed structs go through their json tags", func(t *testing.T) {
		got := withModelCapabilities("zed", []ZedModel{{ID: "claude-sonnet-5-5", Name: "Sonnet"}})
		if len(got) != 1 {
			t.Fatalf("expected 1 model, got %d", len(got))
		}
		if got[0]["name"] != "Sonnet" {
			t.Errorf("name = %v, want Sonnet", got[0]["name"])
		}
		if got[0]["contextLength"] == nil {
			t.Errorf("contextLength missing: %v", got[0])
		}
	})

	t.Run("decoded feed keeps its own stated values", func(t *testing.T) {
		got := withModelCapabilities("zed", []any{
			map[string]any{"id": "claude-sonnet-5-5", "contextLength": 42, "isVL": false},
		})
		if got[0]["contextLength"] != 42 {
			t.Errorf("contextLength = %v, want the feed's 42", got[0]["contextLength"])
		}
		if got[0]["isVL"] != false {
			t.Errorf("isVL = %v, want the feed's false", got[0]["isVL"])
		}
	})

	t.Run("slug and model name the id when id is absent", func(t *testing.T) {
		got := withModelCapabilities("commandcode", []map[string]any{
			{"slug": "moonshotai/Kimi-K2.7-Code"},
			{"model": "deepseek/deepseek-v4-pro"},
		})
		if len(got) != 2 {
			t.Fatalf("expected 2 models, got %d", len(got))
		}
		if got[0]["isReasoning"] != true || got[1]["isReasoning"] != true {
			t.Errorf("reasoning flag not resolved from slug/model: %v", got)
		}
	})

	t.Run("an entry with no id is dropped", func(t *testing.T) {
		if got := withModelCapabilities("claude", []any{map[string]any{"name": "nameless"}}); len(got) != 0 {
			t.Fatalf("expected the id-less entry to be dropped, got %v", got)
		}
	})
}

// A Claude live catalogue carried only id+name, so the picker showed the model
// with no reasoning icon and no window. The enrichment is what closes that.
func TestHandleGetConnectionModels_ClaudeCarriesCapabilities(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	connID := "conn-claude-caps"
	if err := repo.CreateProviderConnectionFull(t.Context(), connID, "claude", "oauth", "Claude", nil,
		`{"accessToken":"sk-ant-oat-caps"}`); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	withProbeStub(t, func(_ int, _ capturedProbe) (int, []byte, error) {
		return http.StatusOK, []byte(`{"data":[
			{"id":"claude-sonnet-5-5","display_name":"Claude Sonnet 5.5"},
			{"id":"claude-fable-5-2","display_name":"Claude Fable 5.2"}
		]}`), nil
	})

	models := fetchLiveCatalog(t, repo, connID)
	sonnet := liveCatalogByID(t, models, "claude-sonnet-5-5")
	if sonnet.IsReasoning == nil || !*sonnet.IsReasoning {
		t.Errorf("claude-sonnet-5-5 isReasoning = %v, want true", sonnet.IsReasoning)
	}
	if sonnet.ContextLength == nil || *sonnet.ContextLength == 0 {
		t.Errorf("claude-sonnet-5-5 contextLength = %v, want a resolved window", sonnet.ContextLength)
	}
}

// CommandCode reaches the catalogue through the ModelsListURL branch, which is
// the one the issue names: a live feed the static registry snapshot predates.
func TestHandleGetConnectionModels_CommandCodeCarriesCapabilities(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	connID := "conn-commandcode-caps"
	if err := repo.CreateProviderConnectionFull(context.Background(), connID, "commandcode", "apikey", "CC", nil,
		`{"apiKey":"user_test"}`); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	withProbeStub(t, func(_ int, p capturedProbe) (int, []byte, error) {
		if p.url != "https://api.commandcode.ai/provider/v1/models" {
			t.Errorf("unexpected url %q", p.url)
		}
		// The feed spells the window context_length; the picker reads
		// contextLength. CommandCode declares 1M for the whole Provider API
		// lane, which outranks a feed's per-row number — the enrichment fills
		// only what the feed left unset, and normalises the spelling.
		return http.StatusOK, []byte(`{"data":[
			{"id":"moonshotai/Kimi-K2.7-Code","name":"Kimi K2.7 Code"}
		]}`), nil
	})

	models := fetchLiveCatalog(t, repo, connID)
	kimi := liveCatalogByID(t, models, "moonshotai/Kimi-K2.7-Code")
	if kimi.ContextLength == nil || *kimi.ContextLength != 1000000 {
		t.Errorf("contextLength = %v, want CommandCode's declared 1000000", kimi.ContextLength)
	}
	if kimi.IsVL == nil || !*kimi.IsVL {
		t.Errorf("isVL = %v, want true", kimi.IsVL)
	}
	if len(kimi.ThinkingLevels) == 0 {
		t.Errorf("thinking_levels missing for a reasoning model: %v", kimi)
	}
}

// The enrichment must agree with GET /api/models/caps, which resolves the same
// tables: a live entry and the static entry for one id carry the same window.
func TestWithModelCapabilities_MatchesModelCapsEndpoint(t *testing.T) {
	const provider = "commandcode"
	const model = "moonshotai/Kimi-K2.7-Code"

	detail := providers.GetCapabilitiesDetailForModel(provider, model)
	got := withModelCapabilities(provider, []any{map[string]any{"id": model}})
	if len(got) != 1 {
		t.Fatalf("expected 1 model, got %d", len(got))
	}
	if got[0]["contextLength"] != detail.ContextWindow {
		t.Errorf("contextLength = %v, /api/models/caps reports %d", got[0]["contextLength"], detail.ContextWindow)
	}
	if got[0]["isVL"] != detail.Vision {
		t.Errorf("isVL = %v, /api/models/caps reports %v", got[0]["isVL"], detail.Vision)
	}
	if got[0]["isReasoning"] != detail.Reasoning {
		t.Errorf("isReasoning = %v, /api/models/caps reports %v", got[0]["isReasoning"], detail.Reasoning)
	}
}