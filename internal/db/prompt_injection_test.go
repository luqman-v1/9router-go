package db

import (
	"testing"
)

func newPromptInjectionRepo(t *testing.T) *Repo {
	t.Helper()
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	return NewRepo(database)
}

func TestPromptInjectionCRUD(t *testing.T) {
	repo := newPromptInjectionRepo(t)

	// Ensure tables exist
	requireNoError(t, EnsurePromptInjectionTables(repo.db))

	// Enable prompt injection
	requireNoError(t, repo.SetPromptInjectionEnabled(true))
	requireTrue(t, repo.GetPromptInjectionEnabled())

	// Create global injection
	global := &PromptInjectionDoc{
		TargetType: "global",
		TargetID:   "global",
		Mode:       "system",
		Content:    "You are a helpful assistant.",
		Enabled:    true,
	}
	requireNoError(t, repo.SavePromptInjection(global))
	requireNotEmpty(t, global.ID)

	// Create model injection
	model := &PromptInjectionDoc{
		TargetType: "model",
		TargetID:   "gpt-4o",
		Mode:       "system",
		Content:    "You are GPT-4o.",
		Enabled:    true,
	}
	requireNoError(t, repo.SavePromptInjection(model))

	// Create key injection
	key := &PromptInjectionDoc{
		TargetType: "key",
		TargetID:   "key-123",
		Mode:       "system",
		Content:    "You are a custom assistant.",
		Enabled:    true,
	}
	requireNoError(t, repo.SavePromptInjection(key))

	// Create key-model injection (most specific)
	keyModel := &PromptInjectionDoc{
		TargetType: "key-model",
		TargetID:   "key-123:gpt-4o",
		Mode:       "script",
		Content:    `[{"role":"user","content":"hello"}]`,
		Enabled:    true,
	}
	requireNoError(t, repo.SavePromptInjection(keyModel))

	// List all
	all, err := repo.ListPromptInjections("")
	requireNoError(t, err)
	requireLen(t, len(all), 4)

	// List by target type
	modelOnly, err := repo.ListPromptInjections("model")
	requireNoError(t, err)
	requireLen(t, len(modelOnly), 1)
	requireEqual(t, modelOnly[0].TargetType, "model")

	// Get one
	got, err := repo.GetPromptInjection(model.ID)
	requireNoError(t, err)
	requireEqual(t, got.TargetID, "gpt-4o")

	// Resolve: key-model should win
	resolved, err := repo.ResolvePromptInjection("gpt-4o", "key-123")
	requireNoError(t, err)
	requireNotNil(t, resolved)
	requireEqual(t, resolved.TargetType, "key-model")
	requireEqual(t, resolved.Mode, "script")

	// Resolve: model only (no key)
	resolved, err = repo.ResolvePromptInjection("gpt-4o", "")
	requireNoError(t, err)
	requireNotNil(t, resolved)
	requireEqual(t, resolved.TargetType, "model")

	// Resolve: key only (no model)
	resolved, err = repo.ResolvePromptInjection("", "key-123")
	requireNoError(t, err)
	requireNotNil(t, resolved)
	requireEqual(t, resolved.TargetType, "key")

	// Resolve: global fallback
	resolved, err = repo.ResolvePromptInjection("unknown-model", "")
	requireNoError(t, err)
	requireNotNil(t, resolved)
	requireEqual(t, resolved.TargetType, "global")

	// Disable key-model, key should now win (more specific than model)
	keyModel.Enabled = false
	requireNoError(t, repo.SavePromptInjection(keyModel))
	resolved, err = repo.ResolvePromptInjection("gpt-4o", "key-123")
	requireNoError(t, err)
	requireNotNil(t, resolved)
	requireEqual(t, resolved.TargetType, "key")

	// Delete key, model should win
	requireNoError(t, repo.DeletePromptInjection(key.ID))
	resolved, err = repo.ResolvePromptInjection("gpt-4o", "key-123")
	requireNoError(t, err)
	requireNotNil(t, resolved)
	requireEqual(t, resolved.TargetType, "model")

	// Delete
	requireNoError(t, repo.DeletePromptInjection(model.ID))
	all, err = repo.ListPromptInjections("")
	requireNoError(t, err)
	requireLen(t, len(all), 2)
}

func TestPromptInjectionTemplates(t *testing.T) {
	repo := newPromptInjectionRepo(t)

	requireNoError(t, EnsurePromptInjectionTables(repo.db))

	// Save template
	tpl := &PromptInjectionTemplate{
		Name:    "Test Template",
		Content: "Template content here.",
		Kind:    "custom",
	}
	requireNoError(t, repo.SavePromptInjectionTemplate(tpl))
	requireNotEmpty(t, tpl.ID)

	// List templates
	all, err := repo.ListPromptInjectionTemplates()
	requireNoError(t, err)
	requireLen(t, len(all), 1)
	requireEqual(t, all[0].Name, "Test Template")

	// Delete template
	requireNoError(t, repo.DeletePromptInjectionTemplate(tpl.ID))
	all, err = repo.ListPromptInjectionTemplates()
	requireNoError(t, err)
	requireLen(t, len(all), 0)
}

func TestPromptInjectionSetting(t *testing.T) {
	repo := newPromptInjectionRepo(t)

	requireNoError(t, EnsureCoreSchema(repo.db))

	// Default is off
	requireFalse(t, repo.GetPromptInjectionEnabled())

	// Enable
	requireNoError(t, repo.SetPromptInjectionEnabled(true))
	requireTrue(t, repo.GetPromptInjectionEnabled())

	// Disable
	requireNoError(t, repo.SetPromptInjectionEnabled(false))
	requireFalse(t, repo.GetPromptInjectionEnabled())
}

// Helpers to avoid importing testify in every test file if not already used.
func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func requireNotEmpty(t *testing.T, s string) {
	t.Helper()
	if s == "" {
		t.Fatal("expected non-empty string")
	}
}

func requireNotNil(t *testing.T, v interface{}) {
	t.Helper()
	if v == nil {
		t.Fatal("expected non-nil value")
	}
}

func requireEqual(t *testing.T, a, b interface{}) {
	t.Helper()
	if a != b {
		t.Fatalf("expected %v == %v", a, b)
	}
}

func requireLen(t *testing.T, length, expected int) {
	t.Helper()
	if length != expected {
		t.Fatalf("expected length %d, got %d", expected, length)
	}
}

func requireTrue(t *testing.T, v bool) {
	t.Helper()
	if !v {
		t.Fatal("expected true")
	}
}

func requireFalse(t *testing.T, v bool) {
	t.Helper()
	if v {
		t.Fatal("expected false")
	}
}
