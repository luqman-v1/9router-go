package chat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/middleware"
	"9router/proxy/internal/models"

	_ "modernc.org/sqlite"
)

// TestApplyCliModelMapping_EndToEnd verifies the full happy path: a scoped key
// with a Claude Code User-Agent gets "opus" rewritten to its mapped target.
func TestApplyCliModelMapping_EndToEnd(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	database, err := db.OpenDatabase(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	repo := db.NewRepo(database)

	if err := db.EnsureCoreSchema(database); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}

	// Seed a mapping: key k1 + claude tool, slot opus → openai/gpt-5.2
	mapping := db.CliToolMapping{
		ToolID:      "claude",
		APIKeyID:    "k1",
		SlotKey:     "opus",
		TargetModel: "openai/gpt-5.2",
		Enabled:     1,
	}
	if err := repo.UpsertCliToolMapping(mapping); err != nil {
		t.Fatalf("upsert mapping: %v", err)
	}

	// Settings default to enabled/remote — verify
	settings, err := repo.GetCliToolSettings("k1", "claude")
	if err != nil {
		t.Fatalf("get settings: %v", err)
	}
	if settings.MappingsEnabled != 1 || settings.Mode != "remote" {
		t.Fatalf("default settings must be enabled/remote, got %+v", settings)
	}

	// Resolve via repo directly
	target, err := repo.ResolveCliModel("k1", "claude", "opus")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if target != "openai/gpt-5.2" {
		t.Fatalf("ResolveCliModel = %q, want openai/gpt-5.2", target)
	}

	// Now through the handler gate
	h := &ChatHandler{Repo: repo}
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set("User-Agent", "claude-cli/1.0.0 (external, cli)")

	// Without the scope on the key, mapping must not apply
	keyNoScope := &models.APIKey{ID: "k1"}
	t.Setenv("X9R_TEST_KEY", "1")
	_ = keyNoScope
	got := h.applyCliModelMapping(withTestKey(r, nil), "opus")
	if got != "opus" {
		t.Errorf("keyless: got %q, want opus", got)
	}

	// With the scope, mapping must apply
	got = h.applyCliModelMapping(withTestKey(r, &models.APIKey{ID: "k1", Scopes: []string{"routing:cli_mapping"}}), "opus")
	if got != "openai/gpt-5.2" {
		t.Errorf("scoped key: got %q, want openai/gpt-5.2", got)
	}

	// A different slot with no mapping must pass through
	got = h.applyCliModelMapping(withTestKey(r, &models.APIKey{ID: "k1", Scopes: []string{"routing:cli_mapping"}}), "custom-model")
	if got != "custom-model" {
		t.Errorf("unmapped slot: got %q, want custom-model", got)
	}

	// Disabled settings must not apply
	settings.MappingsEnabled = 0
	if err := repo.SetCliToolSettings(settings); err != nil {
		t.Fatalf("disable settings: %v", err)
	}
	got = h.applyCliModelMapping(withTestKey(r, &models.APIKey{ID: "k1", Scopes: []string{"routing:cli_mapping"}}), "opus")
	if got != "opus" {
		t.Errorf("disabled settings: got %q, want opus", got)
	}

	// Local mode must not apply
	settings.MappingsEnabled = 1
	settings.Mode = "local"
	if err := repo.SetCliToolSettings(settings); err != nil {
		t.Fatalf("local mode: %v", err)
	}
	got = h.applyCliModelMapping(withTestKey(r, &models.APIKey{ID: "k1", Scopes: []string{"routing:cli_mapping"}}), "opus")
	if got != "opus" {
		t.Errorf("local mode: got %q, want opus", got)
	}

	_ = filepath.Join(dir, dbPath) // suppress unused warning
}

// withTestKey attaches an authenticated key to the request context the same
// way the auth middleware does, without going through RequireApiKey.
func withTestKey(r *http.Request, key *models.APIKey) *http.Request {
	ctx := context.WithValue(r.Context(), middleware.ApiKeyContextKey, key)
	return r.WithContext(ctx)
}
