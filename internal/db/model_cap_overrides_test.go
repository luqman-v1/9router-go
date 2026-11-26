package db

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"testing"
)

// The model editor stores one sparse override per provider+model. These tests
// pin the two properties the capability path depends on: a stored row reads
// back byte-faithful, and every "nothing pinned" spelling is a delete rather
// than a row that would pin garbage.
func TestModelCapOverrideRoundTrip(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	if err := EnsureKeiRouterTables(database); err != nil {
		t.Fatalf("ensure keirouter tables: %v", err)
	}
	repo := NewRepo(database)

	if got, err := repo.GetModelCapOverride("antigravity", "claude-sonnet-4"); err != nil || got != nil {
		t.Fatalf("fresh lookup = %v, %v; want nil, nil", got, err)
	}

	override := jsontext.Value(`{"vision":true,"contextWindow":200000}`)
	if err := repo.SetModelCapOverride("antigravity", "claude-sonnet-4", override); err != nil {
		t.Fatalf("set: %v", err)
	}

	got, err := repo.GetModelCapOverride("antigravity", "claude-sonnet-4")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("override vanished after write")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(got.CapsOverride), &parsed); err != nil {
		t.Fatalf("stored override is not valid JSON: %v", err)
	}
	if parsed["vision"] != true {
		t.Errorf("vision = %v, want true", parsed["vision"])
	}
	if parsed["contextWindow"] != float64(200000) {
		t.Errorf("contextWindow = %v, want 200000", parsed["contextWindow"])
	}

	// A second write upserts rather than duplicating: the table has a composite
	// primary key, and two rows for one pair would make the read arbitrary.
	if err := repo.SetModelCapOverride("antigravity", "claude-sonnet-4", jsontext.Value(`{"pdf":true}`)); err != nil {
		t.Fatalf("second set: %v", err)
	}
	all, err := repo.AllModelCapOverrides()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("stored %d rows for one pair, want 1: %#v", len(all), all)
	}
	if _, ok := all["antigravity|claude-sonnet-4"]; !ok {
		t.Errorf("row key = %#v, want antigravity|claude-sonnet-4", all)
	}
}

// An empty object is the clear action, so writing one must delete the row.
// A row left behind would read as "no override" and be filtered anyway, but it
// would also keep answering a stale updated_at and grow the table per edit.
func TestModelCapOverrideEmptyObjectDeletes(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	if err := EnsureKeiRouterTables(database); err != nil {
		t.Fatalf("ensure keirouter tables: %v", err)
	}
	repo := NewRepo(database)

	if err := repo.SetModelCapOverride("openai", "gpt-4o", jsontext.Value(`{"tools":true}`)); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := repo.SetModelCapOverride("openai", "gpt-4o", jsontext.Value(`{}`)); err != nil {
		t.Fatalf("clear: %v", err)
	}
	got, err := repo.GetModelCapOverride("openai", "gpt-4o")
	if err != nil {
		t.Fatalf("get after clear: %v", err)
	}
	if got != nil {
		t.Fatalf("row survived a {} write: %#v", got)
	}
}

// A corrupt or empty stored value reads as nil, so a hand-edited row cannot
// pin garbage onto a model: "unreadable" and "absent" answer identically.
func TestModelCapOverrideBlankRowReadsAsAbsent(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	if err := EnsureKeiRouterTables(database); err != nil {
		t.Fatalf("ensure keirouter tables: %v", err)
	}
	repo := NewRepo(database)

	if _, err := database.Exec(
		"INSERT INTO model_cap_overrides (provider, model, caps_override, updated_at) VALUES (?, ?, ?, ?)",
		"openai", "gpt-4o", "   ", "2026-01-01T00:00:00Z",
	); err != nil {
		t.Fatalf("insert blank row: %v", err)
	}
	got, err := repo.GetModelCapOverride("openai", "gpt-4o")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != nil {
		t.Fatalf("blank row read as an override: %#v", got)
	}
	all, err := repo.AllModelCapOverrides()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("list included a blank row: %#v", all)
	}
}
