package db

import (
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// newCliToolTestRepo opens a throwaway SQLite database with the core schema
// (including the CLI mapping tables) applied.
func newCliToolTestRepo(t *testing.T) *Repo {
	t.Helper()
	database, err := OpenDatabase(filepath.Join(t.TempDir(), "cli-mapping.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := EnsureCoreSchema(database); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	return NewRepo(database)
}

func TestCliToolMapping_RoundTrip(t *testing.T) {
	r := newCliToolTestRepo(t)

	m := CliToolMapping{
		ToolID:      "claude",
		APIKeyID:    "k1",
		SlotKey:     "opus",
		TargetModel: "openai/gpt-5.2",
		SourceModel: "claude-opus-4",
		Enabled:     1,
	}
	if err := r.UpsertCliToolMapping(m); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := r.GetCliToolMappings("k1", "claude")
	if err != nil {
		t.Fatalf("get mappings: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 mapping, got %d", len(got))
	}
	if got[0].SlotKey != "opus" || got[0].TargetModel != "openai/gpt-5.2" || got[0].SourceModel != "claude-opus-4" {
		t.Errorf("round-trip mismatch: %+v", got[0])
	}

	// Upsert again with a new target — same (tool, key, slot) must replace.
	m.TargetModel = "google/gemini-3-pro"
	if err := r.UpsertCliToolMapping(m); err != nil {
		t.Fatalf("upsert 2: %v", err)
	}
	got, _ = r.GetCliToolMappings("k1", "claude")
	if len(got) != 1 {
		t.Fatalf("upsert must replace, got %d rows", len(got))
	}
	if got[0].TargetModel != "google/gemini-3-pro" {
		t.Errorf("target = %q, want google/gemini-3-pro", got[0].TargetModel)
	}
}

func TestCliToolMapping_SlotIsolation(t *testing.T) {
	r := newCliToolTestRepo(t)

	_ = r.UpsertCliToolMapping(CliToolMapping{ToolID: "claude", APIKeyID: "k1", SlotKey: "opus", TargetModel: "a/one", Enabled: 1})
	_ = r.UpsertCliToolMapping(CliToolMapping{ToolID: "claude", APIKeyID: "k1", SlotKey: "sonnet", TargetModel: "a/two", Enabled: 1})
	_ = r.UpsertCliToolMapping(CliToolMapping{ToolID: "claude", APIKeyID: "k2", SlotKey: "opus", TargetModel: "a/three", Enabled: 1})

	got1, _ := r.ResolveCliModel("k1", "claude", "opus")
	if got1 != "a/one" {
		t.Errorf("k1/claude/opus = %q, want a/one", got1)
	}
	got2, _ := r.ResolveCliModel("k1", "claude", "sonnet")
	if got2 != "a/two" {
		t.Errorf("k1/claude/sonnet = %q, want a/two", got2)
	}
	got3, _ := r.ResolveCliModel("k2", "claude", "opus")
	if got3 != "a/three" {
		t.Errorf("k2/claude/opus = %q, want a/three", got3)
	}
	// Slot with no row falls through.
	got4, _ := r.ResolveCliModel("k1", "claude", "haiku")
	if got4 != "" {
		t.Errorf("k1/claude/haiku = %q, want empty", got4)
	}
}

func TestCliToolSettings_GatesResolution(t *testing.T) {
	r := newCliToolTestRepo(t)
	_ = r.UpsertCliToolMapping(CliToolMapping{ToolID: "claude", APIKeyID: "k1", SlotKey: "opus", TargetModel: "a/one", Enabled: 1})

	// Defaults: enabled + remote → resolves.
	got, err := r.ResolveCliModel("k1", "claude", "opus")
	if err != nil || got != "a/one" {
		t.Fatalf("default settings: got %q err %v, want a/one", got, err)
	}

	// Disabled → no mapping.
	s, _ := r.GetCliToolSettings("k1", "claude")
	s.MappingsEnabled = 0
	if err := r.SetCliToolSettings(s); err != nil {
		t.Fatalf("disable: %v", err)
	}
	got, _ = r.ResolveCliModel("k1", "claude", "opus")
	if got != "" {
		t.Errorf("disabled settings: got %q, want empty", got)
	}

	// Local mode → no mapping.
	s.MappingsEnabled = 1
	s.Mode = "local"
	if err := r.SetCliToolSettings(s); err != nil {
		t.Fatalf("local: %v", err)
	}
	got, _ = r.ResolveCliModel("k1", "claude", "opus")
	if got != "" {
		t.Errorf("local mode: got %q, want empty", got)
	}
}

func TestCliToolMapping_Delete(t *testing.T) {
	r := newCliToolTestRepo(t)
	m := CliToolMapping{ToolID: "claude", APIKeyID: "k1", SlotKey: "opus", TargetModel: "a/one", Enabled: 1}
	_ = r.UpsertCliToolMapping(m)
	rows, _ := r.GetCliToolMappings("k1", "claude")
	if len(rows) != 1 {
		t.Fatalf("seed failed: %d rows", len(rows))
	}
	if err := r.DeleteCliToolMapping(rows[0].ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	rows, _ = r.GetCliToolMappings("k1", "claude")
	if len(rows) != 0 {
		t.Errorf("after delete: %d rows, want 0", len(rows))
	}
}
