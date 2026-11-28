package chat

import (
	"database/sql"
	json "encoding/json/v2"
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

// seedCombo inserts a combo row directly, since setupChatTestDB only seeds
// a single-member one and the preferences apply to multi-member lists.
func seedCombo(t *testing.T, database *sql.DB, name string, models []string) {
	t.Helper()
	encoded, err := json.Marshal(models)
	if err != nil {
		t.Fatalf("encode combo models: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES
		(?, ?, 'llm', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		name, name, string(encoded)); err != nil {
		t.Fatalf("seed combo %s: %v", name, err)
	}
}

func TestResolveModelWithPrefs_ComboFilterAndOrder(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	seedCombo(t, database, "duo", []string{"deepseek/deepseek-chat", "groq/llama-3.3-70b-versatile"})

	handler := NewChatHandler(db.NewRepo(database))

	t.Run("no prefs leaves the combo untouched", func(t *testing.T) {
		info, err := handler.resolveModel("duo")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(info.ComboModels) != 2 {
			t.Fatalf("ComboModels = %v, want both members", info.ComboModels)
		}
	})

	t.Run("ignore drops the member and moves the primary", func(t *testing.T) {
		info, err := handler.resolveModelWithPrefs("duo", providerPreferences{Ignore: []string{"deepseek"}})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Join(info.ComboModels, ",") != "groq/llama-3.3-70b-versatile" {
			t.Errorf("ComboModels = %v, want only the groq member", info.ComboModels)
		}
		if info.Provider != "groq" || info.Model != "llama-3.3-70b-versatile" {
			t.Errorf("primary = %s/%s, want the first surviving member", info.Provider, info.Model)
		}
		if info.ConnectionID != "" {
			t.Errorf("ConnectionID = %q, want it cleared: the member picks its own connection", info.ConnectionID)
		}
	})

	t.Run("order reorders without dropping", func(t *testing.T) {
		info, err := handler.resolveModelWithPrefs("duo", providerPreferences{Order: []string{"groq"}})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Join(info.ComboModels, ",") != "groq/llama-3.3-70b-versatile,deepseek/deepseek-chat" {
			t.Errorf("ComboModels = %v, want the ordered member first", info.ComboModels)
		}
	})

	t.Run("only that matches nothing is a 400, not a silent fall-through", func(t *testing.T) {
		_, err := handler.resolveModelWithPrefs("duo", providerPreferences{Only: []string{"anthropic"}})
		if err == nil {
			t.Fatal("expected an error when the preferences exclude every member")
		}
		if !strings.Contains(err.Error(), "provider") {
			t.Errorf("error = %q, want it to name the preferences", err)
		}
	})
}

func TestResolveModelWithPrefs_SingleModel(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	handler := NewChatHandler(db.NewRepo(database))

	t.Run("no prefs resolves as before", func(t *testing.T) {
		info, err := handler.resolveModelWithPrefs("deepseek/deepseek-chat", providerPreferences{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if info.Provider != "deepseek" {
			t.Errorf("Provider = %q, want deepseek", info.Provider)
		}
	})

	t.Run("excluded provider fails resolution", func(t *testing.T) {
		_, err := handler.resolveModelWithPrefs("deepseek/deepseek-chat", providerPreferences{Ignore: []string{"deepseek"}})
		if err == nil {
			t.Fatal("expected an error when the single provider is excluded")
		}
	})

	t.Run("allowed provider resolves", func(t *testing.T) {
		info, err := handler.resolveModelWithPrefs("deepseek/deepseek-chat", providerPreferences{Only: []string{"deepseek"}})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if info.Model != "deepseek-chat" {
			t.Errorf("Model = %q, want deepseek-chat", info.Model)
		}
	})
}
