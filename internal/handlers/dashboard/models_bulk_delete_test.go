package dashboard

import (
	"bytes"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// Removing one provider's models must not touch another node's rows, the shared
// alias namespace, or a row whose key carries no provider segment. The handler is
// the only place that decides this, so the fixture seeds every one of those cases.
func TestHandleDeleteProviderModels_RemovesOnlyThatNodesRows(t *testing.T) {
	const (
		nodeID      = "openai-compatible-chat-nara"
		otherNodeID = "openai-compatible-chat-other"
	)

	repo, cleanup := setupTestDB(t)
	defer cleanup()

	seedModel := func(key, value string) {
		t.Helper()
		if err := repo.SetKV("customModels", key, value); err != nil {
			t.Fatalf("seed custom model %s: %v", key, err)
		}
	}

	// (1) keyed "<alias>|<id>|llm" with a matching providerAlias — the shape the
	// import path writes.
	seedModel(nodeID+"|alpha|llm", `{"id":"alpha","providerAlias":"`+nodeID+`","type":"llm"}`)
	// (2) keyed with a bare id but carrying the alias in the value — the
	// single-model form. The key alone would not find this row.
	seedModel("beta", `{"id":"beta","providerAlias":"`+nodeID+`","type":"llm"}`)
	// (3) another node's row: must survive.
	seedModel(otherNodeID+"|gamma|llm", `{"id":"gamma","providerAlias":"`+otherNodeID+`","type":"llm"}`)
	// (4) a row belonging to nobody: must survive.
	seedModel("orphan-row", `{"id":"orphan-row","type":"llm"}`)

	if err := repo.SetKV("modelAliases", "nara-fast", nodeID+"/alpha"); err != nil {
		t.Fatalf("seed alias: %v", err)
	}
	if err := repo.SetKV("modelAliases", "other-fast", otherNodeID+"/gamma"); err != nil {
		t.Fatalf("seed alias: %v", err)
	}

	h := NewDashboardHandler(repo)
	router := chi.NewRouter()
	RegisterRoutes(router, h)

	req := httptest.NewRequest(http.MethodDelete, "/api/providers/"+nodeID+"/models", bytes.NewReader(nil))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Provider       string   `json:"provider"`
		RemovedModels  []string `json:"removedModels"`
		RemovedAliases []string `json:"removedAliases"`
		Count          int      `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Provider != nodeID {
		t.Errorf("provider = %q, want %q", resp.Provider, nodeID)
	}
	if resp.Count != 3 || len(resp.RemovedModels) != 2 || len(resp.RemovedAliases) != 1 {
		t.Errorf("count=%d models=%v aliases=%v, want count 3, 2 models, 1 alias",
			resp.Count, resp.RemovedModels, resp.RemovedAliases)
	}

	remaining, err := repo.GetKVScope("customModels")
	if err != nil {
		t.Fatalf("read customModels: %v", err)
	}
	if len(remaining) != 2 {
		t.Fatalf("expected 2 surviving rows, got %d: %v", len(remaining), remaining)
	}
	for _, key := range []string{otherNodeID + "|gamma|llm", "orphan-row"} {
		if _, ok := remaining[key]; !ok {
			t.Errorf("row %q was removed but belongs to another scope", key)
		}
	}
	for _, key := range []string{nodeID + "|alpha|llm", "beta"} {
		if _, ok := remaining[key]; ok {
			t.Errorf("row %q survived the sweep", key)
		}
	}

	aliases, err := repo.GetModelAliases()
	if err != nil {
		t.Fatalf("read aliases: %v", err)
	}
	if _, ok := aliases["other-fast"]; !ok {
		t.Errorf("alias belonging to another node was removed: %v", aliases)
	}
	if _, ok := aliases["nara-fast"]; ok {
		t.Errorf("alias of the swept node survived: %v", aliases)
	}
}

// A second sweep of the same node is a no-op rather than an error, so a button
// pressed twice cannot fail the request it just satisfied.
func TestHandleDeleteProviderModels_SecondSweepIsEmpty(t *testing.T) {
	const nodeID = "openai-compatible-chat-empty"

	repo, cleanup := setupTestDB(t)
	defer cleanup()

	h := NewDashboardHandler(repo)
	router := chi.NewRouter()
	RegisterRoutes(router, h)

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodDelete, "/api/providers/"+nodeID+"/models", bytes.NewReader(nil))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("sweep %d: expected 200, got %d: %s", i+1, rec.Code, rec.Body.String())
		}
	}
}
