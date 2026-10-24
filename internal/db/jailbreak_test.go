package db

import "testing"

func newSettingsRepo(t *testing.T) *Repo {
	t.Helper()
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	return NewRepo(database)
}

func TestJailbreakDocRoundTrip(t *testing.T) {
	repo := newSettingsRepo(t)

	// No doc saved yet: every model is silently left alone.
	doc, err := repo.GetJailbreakDoc()
	if err != nil {
		t.Fatalf("get missing: %v", err)
	}
	if doc.Prompt != "" || doc.GeminiSafety {
		t.Fatalf("expected zero doc, got %+v", doc)
	}

	in := &JailbreakDoc{Prompt: "agents.md text", GeminiSafety: true}
	if err := repo.SaveJailbreakDoc(in); err != nil {
		t.Fatalf("save: %v", err)
	}
	out, err := repo.GetJailbreakDoc()
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if out.Prompt != "agents.md text" || !out.GeminiSafety {
		t.Fatalf("round trip mismatch: %+v", out)
	}
}

func TestJailbreakModelToggleOptIn(t *testing.T) {
	repo := newSettingsRepo(t)

	// The key is the id /v1/models lists — prefixes and suffixes are part of
	// the id, exactly as the client sends them.
	const model = "tkh/mimo-v2.6-flash:free"

	// Missing entry must read as off: a model never gets injected just because
	// it exists.
	on, err := repo.JailbreakModelEnabled(model)
	if err != nil {
		t.Fatalf("enabled(missing): %v", err)
	}
	if on {
		t.Fatal("default must be off")
	}

	if err := repo.SetJailbreakModel(model, true); err != nil {
		t.Fatalf("set on: %v", err)
	}
	on, err = repo.JailbreakModelEnabled(model)
	if err != nil || !on {
		t.Fatalf("after enable: on=%v err=%v", on, err)
	}

	// A different model id stays off — no prefix/wildcard bleed.
	on, _ = repo.JailbreakModelEnabled("tkh/mimo-v2.6-flash")
	if on {
		t.Fatal("nearby model id must stay off")
	}

	if err := repo.SetJailbreakModel(model, false); err != nil {
		t.Fatalf("set off: %v", err)
	}
	on, err = repo.JailbreakModelEnabled(model)
	if err != nil || on {
		t.Fatalf("after disable: on=%v err=%v", on, err)
	}

	// List only reflects models currently opted in.
	if err := repo.SetJailbreakModel("gemini-flash", true); err != nil {
		t.Fatalf("set gemini-flash: %v", err)
	}
	list, err := repo.ListJailbreakModels()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || !list["gemini-flash"] {
		t.Fatalf("list = %v, want only gemini-flash", list)
	}
}

func TestJailbreakPresetsAndModelBinding(t *testing.T) {
	repo := newSettingsRepo(t)

	// Save two presets
	p1 := &JailbreakDoc{ID: "coder", Name: "Coding Agent", Prompt: "You are a senior coder.", GeminiSafety: true, Source: "coder.md"}
	p2 := &JailbreakDoc{ID: "writer", Name: "Creative Writer", Prompt: "You are a creative writer.", GeminiSafety: false, Source: "writer.md"}

	if err := repo.SaveJailbreakPreset(p1); err != nil {
		t.Fatalf("save p1: %v", err)
	}
	if err := repo.SaveJailbreakPreset(p2); err != nil {
		t.Fatalf("save p2: %v", err)
	}

	presets, err := repo.ListJailbreakPresets()
	if err != nil {
		t.Fatalf("list presets: %v", err)
	}
	if len(presets) < 2 {
		t.Fatalf("expected at least 2 presets, got %d", len(presets))
	}

	got1, err := repo.GetJailbreakPreset("coder")
	if err != nil || got1.Prompt != p1.Prompt || !got1.GeminiSafety {
		t.Fatalf("get coder preset failed: %+v, err: %v", got1, err)
	}

	// Model bindings
	const modelA = "openai/gpt-4o"
	const modelB = "anthropic/claude-3-5-sonnet"
	const modelC = "google/gemini-1.5-pro"

	if err := repo.SetJailbreakModelPreset(modelA, "coder", true); err != nil {
		t.Fatalf("set modelA: %v", err)
	}
	if err := repo.SetJailbreakModelPreset(modelB, "writer", true); err != nil {
		t.Fatalf("set modelB: %v", err)
	}
	// modelC disabled
	if err := repo.SetJailbreakModelPreset(modelC, "off", false); err != nil {
		t.Fatalf("set modelC: %v", err)
	}

	docA, err := repo.GetJailbreakDocForModel(modelA)
	if err != nil || docA == nil || docA.Prompt != p1.Prompt {
		t.Fatalf("docA mismatch: %+v, err: %v", docA, err)
	}

	docB, err := repo.GetJailbreakDocForModel(modelB)
	if err != nil || docB == nil || docB.Prompt != p2.Prompt {
		t.Fatalf("docB mismatch: %+v, err: %v", docB, err)
	}

	docC, err := repo.GetJailbreakDocForModel(modelC)
	if err != nil || docC != nil {
		t.Fatalf("docC should be nil, got: %+v, err: %v", docC, err)
	}

	// List configs
	cfgs, err := repo.ListJailbreakModelConfigs()
	if err != nil {
		t.Fatalf("list configs: %v", err)
	}
	if cfgs[modelA].PresetID != "coder" || !cfgs[modelA].Enabled {
		t.Fatalf("expected modelA coder config, got %+v", cfgs[modelA])
	}
	if cfgs[modelB].PresetID != "writer" || !cfgs[modelB].Enabled {
		t.Fatalf("expected modelB writer config, got %+v", cfgs[modelB])
	}
	if cfgs[modelC].Enabled {
		t.Fatalf("expected modelC disabled, got %+v", cfgs[modelC])
	}

	// Delete preset
	if err := repo.DeleteJailbreakPreset("writer"); err != nil {
		t.Fatalf("delete writer: %v", err)
	}
	// After deleting writer, modelB gets nil doc and is automatically unbound to off
	docBDeleted, err := repo.GetJailbreakDocForModel(modelB)
	if err != nil || docBDeleted != nil {
		t.Fatalf("expected nil after preset deletion, got %+v, err: %v", docBDeleted, err)
	}
	modelBEnabled, err := repo.JailbreakModelEnabled(modelB)
	if err != nil {
		t.Fatalf("modelB enabled check: %v", err)
	}
	if modelBEnabled {
		t.Fatalf("expected modelB to be automatically unbound/disabled after preset deletion")
	}

	cfgsAfter, err := repo.ListJailbreakModelConfigs()
	if err != nil {
		t.Fatalf("list configs after delete: %v", err)
	}
	if cfgsAfter[modelB].Enabled {
		t.Fatalf("expected modelB to not be enabled in configs after preset deletion")
	}
	if !cfgsAfter[modelA].Enabled || cfgsAfter[modelA].PresetID != "coder" {
		t.Fatalf("modelA should remain armed to coder, got: %+v", cfgsAfter[modelA])
	}
}
