package db

import (
	"errors"
	"testing"
	"time"

	"9router/proxy/internal/providers"
)

func TestModelDeprecationStore(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewRepo(database)

	if _, err := repo.GetModelDeprecation("deepseek", "deepseek-chat"); !errors.Is(err, ErrModelNotDeprecated) {
		t.Fatalf("empty store err = %v, want ErrModelNotDeprecated", err)
	}

	err := repo.RecordModelDeprecation(providers.ModelDeprecation{
		Provider:  "deepseek",
		Model:     "deepseek-chat",
		Status:    providers.DeprecationGone,
		Message:   "retired upstream",
		Successor: "qwen3-32b",
	})
	if err != nil {
		t.Fatalf("RecordModelDeprecation: %v", err)
	}

	// A combo entry, a dashboard query and a connection row spell the provider
	// differently often enough that case must not split one model into two
	// rows, so the lookup is case-insensitive.
	got, err := repo.GetModelDeprecation("DeepSeek", "DeepSeek-Chat")
	if err != nil {
		t.Fatalf("GetModelDeprecation case-insensitive: %v", err)
	}
	if got.Status != providers.DeprecationGone || got.Successor != "qwen3-32b" {
		t.Errorf("got %+v, want gone/qwen3-32b", got)
	}
	if got.DetectedAt == "" {
		t.Error("DetectedAt must be stamped on write")
	}

	// Re-recording is an upsert: a second 410 with a better payload replaces
	// the first rather than appending a second row.
	if err := repo.RecordModelDeprecation(providers.ModelDeprecation{
		Provider: "deepseek",
		Model:    "deepseek-chat",
		Status:   providers.DeprecationRetired,
	}); err != nil {
		t.Fatalf("re-record: %v", err)
	}
	all, err := repo.ListModelDeprecations("deepseek")
	if err != nil {
		t.Fatalf("ListModelDeprecations: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("got %d rows, want 1 after an upsert", len(all))
	}
	if all["deepseek/deepseek-chat"].Status != providers.DeprecationRetired {
		t.Errorf("Status = %q, want the re-recorded %q", all["deepseek/deepseek-chat"].Status, providers.DeprecationRetired)
	}

	// A second provider is listed only when the query asks for it.
	if err := repo.RecordModelDeprecation(providers.ModelDeprecation{
		Provider: "groq", Model: "qwen3-32b", Status: providers.DeprecationGone,
	}); err != nil {
		t.Fatalf("record second provider: %v", err)
	}
	scoped, err := repo.ListModelDeprecations("groq")
	if err != nil {
		t.Fatalf("ListModelDeprecations(groq): %v", err)
	}
	if len(scoped) != 1 {
		t.Errorf("groq scope returned %d rows, want 1", len(scoped))
	}
	if _, leaked := scoped["deepseek/deepseek-chat"]; leaked {
		t.Error("provider filter leaked another provider's row")
	}
	unscoped, err := repo.ListModelDeprecations("")
	if err != nil {
		t.Fatalf("ListModelDeprecations(all): %v", err)
	}
	if len(unscoped) != 2 {
		t.Errorf("unscoped returned %d rows, want 2", len(unscoped))
	}

	if err := repo.ClearModelDeprecation("deepseek", "deepseek-chat"); err != nil {
		t.Fatalf("ClearModelDeprecation: %v", err)
	}
	if _, err := repo.GetModelDeprecation("deepseek", "deepseek-chat"); !errors.Is(err, ErrModelNotDeprecated) {
		t.Errorf("after clear err = %v, want ErrModelNotDeprecated", err)
	}
}

// A sync can only clear what the catalogue still lists. Absence from a
// catalogue is weak evidence, so RetainLiveModels must not touch it — that is
// the difference between reviving a restored model and blacklisting a healthy
// one that a scoped key does not list.
func TestRetainLiveModelsOnlyClearsListedModels(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewRepo(database)

	for _, model := range []string{"listed-model", "absent-model"} {
		if err := repo.RecordModelDeprecation(providers.ModelDeprecation{
			Provider: "deepseek", Model: model, Status: providers.DeprecationGone,
		}); err != nil {
			t.Fatalf("seed %s: %v", model, err)
		}
	}

	live := map[string]bool{
		providers.DeprecationKey("deepseek", "listed-model"): true,
		// The successor of a revived model is very often not yet in the
		// catalogue under its own id, so nothing else must be inferred.
		providers.DeprecationKey("deepseek", "other-provider-model"): true,
	}
	cleared, err := repo.RetainLiveModels("deepseek", live)
	if err != nil {
		t.Fatalf("RetainLiveModels: %v", err)
	}
	if cleared != 1 {
		t.Errorf("cleared = %d, want 1", cleared)
	}

	if _, err := repo.GetModelDeprecation("deepseek", "listed-model"); !errors.Is(err, ErrModelNotDeprecated) {
		t.Error("a listed model must lose its badge")
	}
	if _, err := repo.GetModelDeprecation("deepseek", "absent-model"); err != nil {
		t.Errorf("an unlisted model must keep its badge (err = %v): a catalogue is routinely partial", err)
	}
}

// A record missing its provider, model or status is a bug in the caller, and
// silently storing it would produce a row nothing ever reads or clears.
func TestRecordModelDeprecationRejectsIncompleteRows(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewRepo(database)

	invalid := []providers.ModelDeprecation{
		{Model: "m", Status: providers.DeprecationGone},
		{Provider: "p", Status: providers.DeprecationGone},
		{Provider: "p", Model: "m"},
	}
	for i, dep := range invalid {
		if err := repo.RecordModelDeprecation(dep); err == nil {
			t.Errorf("case %d: RecordModelDeprecation(%+v) = nil, want an error", i, dep)
		}
	}
}

// A transient cooldown must never overwrite a permanent "gone" record: a
// 5-minute cool-down is not evidence that a model is dead for good.
func TestRecordCooldownNeverOverwritesPermanent(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewRepo(database)

	// Seed a permanent deprecation first.
	if err := repo.RecordModelDeprecation(providers.ModelDeprecation{
		Provider: "deepseek", Model: "deepseek-chat",
		Status: providers.DeprecationGone, Message: "retired upstream",
	}); err != nil {
		t.Fatalf("seed gone: %v", err)
	}
	// Now a live failure tries to file a cooldown for the same model.
	if err := repo.RecordModelCooldown("deepseek", "deepseek-chat"); err != nil {
		t.Fatalf("RecordModelCooldown: %v", err)
	}
	got, err := repo.GetModelDeprecation("deepseek", "deepseek-chat")
	if err != nil {
		t.Fatalf("GetModelDeprecation: %v", err)
	}
	if got.Status != providers.DeprecationGone {
		t.Errorf("Status = %q, want %q (cooldown must not demote gone)", got.Status, providers.DeprecationGone)
	}
	if got.CooldownUntil != "" {
		t.Errorf("CooldownUntil = %q, want empty (cooldown did not write)", got.CooldownUntil)
	}
}

// A cooldown on a fresh model records cooldownUntil 5 minutes out.
func TestRecordCooldownStampsWindow(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewRepo(database)

	if err := repo.RecordModelCooldown("deepseek", "deepseek-chat"); err != nil {
		t.Fatalf("RecordModelCooldown: %v", err)
	}
	got, err := repo.GetModelDeprecation("deepseek", "deepseek-chat")
	if err != nil {
		t.Fatalf("GetModelDeprecation: %v", err)
	}
	if got.Status != providers.DeprecationCooldown {
		t.Errorf("Status = %q, want %q", got.Status, providers.DeprecationCooldown)
	}
	if got.CooldownUntil == "" {
		t.Error("CooldownUntil must be stamped on write")
	}
	until, err := time.Parse(time.RFC3339, got.CooldownUntil)
	if err != nil {
		t.Fatalf("CooldownUntil unparseable: %v", err)
	}
	want := time.Now().UTC().Add(providers.ModelCooldownSec * time.Second)
	if until.Before(want.Add(-time.Second)) || until.After(want.Add(time.Second)) {
		t.Errorf("CooldownUntil = %s, want ~%s", until, want)
	}
}

// ClearModelDeprecation must not erase an active cooldown: a model cooling
// down from a live failure keeps its badge until the window lapses.
func TestClearDoesNotEraseActiveCooldown(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewRepo(database)

	if err := repo.RecordModelCooldown("deepseek", "deepseek-chat"); err != nil {
		t.Fatalf("RecordModelCooldown: %v", err)
	}
	if err := repo.ClearModelDeprecation("deepseek", "deepseek-chat"); err != nil {
		t.Fatalf("ClearModelDeprecation: %v", err)
	}
	// Still there: the cooldown is active.
	if _, err := repo.GetModelDeprecation("deepseek", "deepseek-chat"); err != nil {
		t.Errorf("active cooldown must survive ClearModelDeprecation, err = %v", err)
	}
}

// ClearModelDeprecation clears a lapsed cooldown (window expired).
func TestClearErasesLapsedCooldown(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewRepo(database)

	// Write a cooldown whose window is already in the past.
	dep := providers.ModelDeprecation{
		Provider:      "deepseek",
		Model:         "deepseek-chat",
		Status:        providers.DeprecationCooldown,
		CooldownUntil: time.Now().UTC().Add(-time.Hour).Format(time.RFC3339),
	}
	if err := repo.RecordModelDeprecation(dep); err != nil {
		t.Fatalf("RecordModelDeprecation: %v", err)
	}
	if err := repo.ClearModelDeprecation("deepseek", "deepseek-chat"); err != nil {
		t.Fatalf("ClearModelDeprecation: %v", err)
	}
	if _, err := repo.GetModelDeprecation("deepseek", "deepseek-chat"); !errors.Is(err, ErrModelNotDeprecated) {
		t.Errorf("lapsed cooldown must be cleared, err = %v", err)
	}
}

// RetainLiveModels must not clear cooldowns: a catalogue sync that re-lists
// a model must not erase a cooldown the request path earned.
func TestRetainLiveModelsSkipsCooldowns(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewRepo(database)

	for _, model := range []string{"listed-model"} {
		if err := repo.RecordModelDeprecation(providers.ModelDeprecation{
			Provider: "deepseek", Model: model, Status: providers.DeprecationGone,
		}); err != nil {
			t.Fatalf("seed %s: %v", model, err)
		}
	}
	// Seed cooled-model directly as an active cooldown (no prior record, so
	// RecordModelDeprecation writes it rather than being blocked by the
	// never-demote-gone rule).
	dep := providers.ModelDeprecation{
		Provider:      "deepseek",
		Model:         "cooled-model",
		Status:        providers.DeprecationCooldown,
		CooldownUntil: time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
	}
	if err := repo.RecordModelDeprecation(dep); err != nil {
		t.Fatalf("seed cooldown: %v", err)
	}

	live := map[string]bool{
		providers.DeprecationKey("deepseek", "listed-model"): true,
		providers.DeprecationKey("deepseek", "cooled-model"): true,
	}
	cleared, err := repo.RetainLiveModels("deepseek", live)
	if err != nil {
		t.Fatalf("RetainLiveModels: %v", err)
	}
	if cleared != 1 {
		t.Errorf("cleared = %d, want 1 (only the gone record)", cleared)
	}
	// listed-model must lose its gone badge.
	if _, err := repo.GetModelDeprecation("deepseek", "listed-model"); !errors.Is(err, ErrModelNotDeprecated) {
		t.Error("a listed model must lose its gone badge")
	}
	// cooled-model must keep its cooldown badge.
	if _, err := repo.GetModelDeprecation("deepseek", "cooled-model"); err != nil {
		t.Errorf("a cooled model must keep its cooldown badge, err = %v", err)
	}
}
