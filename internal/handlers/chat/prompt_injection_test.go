package chat

import (
	"context"
	"testing"

	"9router/proxy/internal/db"
)

// seedPromptInjectionRepo builds an in-memory repo with the prompt injection
// tables and one enabled system-mode injection for the requested model.
func seedPromptInjectionRepo(t *testing.T) *db.Repo {
	t.Helper()
	database, cleanup := setupChatTestDB(t)
	t.Cleanup(cleanup)
	repo := db.NewRepo(database)
	if err := db.EnsurePromptInjectionTables(database); err != nil {
		t.Fatalf("EnsurePromptInjectionTables: %v", err)
	}
	if err := repo.SetPromptInjectionEnabled(true); err != nil {
		t.Fatalf("SetPromptInjectionEnabled: %v", err)
	}
	if err := repo.SavePromptInjection(&db.PromptInjectionDoc{
		TargetType: "model",
		TargetID:   "github/text-embedding-ada-002",
		Mode:       "system",
		Content:    "injected-system-prompt",
		Enabled:    true,
	}); err != nil {
		t.Fatalf("SavePromptInjection: %v", err)
	}
	return repo
}

// TestApplyPromptInjection_SystemModePrepends verifies the wiring: an enabled
// injection prepends its content to the outbound body, after the jailbreak
// layer, before the marshal. The system prompt lands in front of the first
// message, per prependPromptInjection.
func TestApplyPromptInjection_SystemModePrepends(t *testing.T) {
	repo := seedPromptInjectionRepo(t)
	h := &ChatHandler{Repo: repo}

	body := map[string]any{
		"model": "github/text-embedding-ada-002",
		"messages": []any{
			map[string]any{"role": "system", "content": "base system"},
			map[string]any{"role": "user", "content": "hi"},
		},
	}
	h.applyPromptInjection(context.Background(), body, "github/text-embedding-ada-002")

	msgs, ok := body["messages"].([]any)
	if !ok || len(msgs) != 3 {
		t.Fatalf("injected message not prepended: %v", body["messages"])
	}
	first, _ := msgs[0].(map[string]any)
	if first["role"] != "system" {
		t.Fatalf("first message role = %v, want system", first["role"])
	}
	content, _ := first["content"].(string)
	if content != "injected-system-prompt" {
		t.Fatalf("injected content = %q, want the injection doc", content)
	}
	second, _ := msgs[1].(map[string]any)
	if c, _ := second["content"].(string); c != "base system" {
		t.Fatalf("original system message displaced: %v", second["content"])
	}
}

// TestApplyPromptInjection_DisabledIsInert verifies the feature toggle: with
// PromptInjectionEnabled off the body must pass through untouched.
func TestApplyPromptInjection_DisabledIsInert(t *testing.T) {
	repo := seedPromptInjectionRepo(t)
	if err := repo.SetPromptInjectionEnabled(false); err != nil {
		t.Fatalf("SetPromptInjectionEnabled(false): %v", err)
	}
	h := &ChatHandler{Repo: repo}

	body := map[string]any{"model": "github/text-embedding-ada-002", "messages": []any{
		map[string]any{"role": "system", "content": "base system"},
		map[string]any{"role": "user", "content": "hi"},
	}}
	h.applyPromptInjection(context.Background(), body, "github/text-embedding-ada-002")

	msgs := body["messages"].([]any)
	first := msgs[0].(map[string]any)
	if content, _ := first["content"].(string); content != "base system" {
		t.Fatalf("disabled feature mutated the body: %v", content)
	}
}

// TestApplyPromptInjection_NoMatchIsInert verifies a request for an
// unconfigured model leaves the body untouched.
func TestApplyPromptInjection_NoMatchIsInert(t *testing.T) {
	repo := seedPromptInjectionRepo(t)
	h := &ChatHandler{Repo: repo}

	body := map[string]any{"model": "other/model", "messages": []any{
		map[string]any{"role": "system", "content": "base system"},
	}}
	h.applyPromptInjection(context.Background(), body, "other/model")

	msgs := body["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("unmatched model was mutated: %v", body["messages"])
	}
	first := msgs[0].(map[string]any)
	if content, _ := first["content"].(string); content != "base system" {
		t.Fatalf("unmatched model was mutated: %v", first["content"])
	}
}
