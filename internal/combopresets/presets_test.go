package combopresets

import (
	"testing"

	"9router/proxy/internal/providers"
)

// claudeExpectedCount derives the preset total from the same three sources the
// builder reads, exactly as the acceptance row requires. Pinning 13 here would
// make adding a model to the `cc` registry row turn a correct build red.
func claudeExpectedCount() int {
	return len(providers.GetProviderModels(ClaudeAlias)) +
		len(claudeDefaultModels) +
		len(claudeExtraAliasTargets)
}

func names(items []Item) []string {
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = item.Name
	}
	return out
}

func findItem(items []Item, name string) (Item, bool) {
	for _, item := range items {
		if item.Name == name {
			return item, true
		}
	}
	return Item{}, false
}

func TestBuildClaudeDerivesEveryPreset(t *testing.T) {
	items, err := Build("claude", nil, nil)
	if err != nil {
		t.Fatalf("Build(claude) error = %v", err)
	}

	if got, want := len(items), claudeExpectedCount(); got != want {
		t.Fatalf("Build(claude) returned %d items, want %d derived from registry %v + %d default aliases + %d extra aliases",
			got, want, providers.GetProviderModels(ClaudeAlias), len(claudeDefaultModels), len(claudeExtraAliasTargets))
	}

	// Every registry model must appear under its own name, seeded with the
	// prefixed id — this is the part that follows the registry.
	for _, id := range providers.GetProviderModels(ClaudeAlias) {
		item, ok := findItem(items, id)
		if !ok {
			t.Errorf("registry model %q has no preset; names = %v", id, names(items))
			continue
		}
		if want := ClaudeAlias + "/" + id; len(item.Models) != 1 || item.Models[0] != want {
			t.Errorf("preset %q seeded with %v, want [%s]", id, item.Models, want)
		}
	}

	// The alias presets the acceptance row names explicitly.
	wantAliases := map[string]string{
		"fable":    "cc/claude-fable-5",
		"opus":     "cc/claude-opus-5",
		"sonnet":   "cc/claude-sonnet-5",
		"haiku":    "cc/claude-haiku-4-5-20251001",
		"default":  "cc/claude-sonnet-5",
		"opusplan": "cc/claude-opus-5",
	}
	for name, model := range wantAliases {
		item, ok := findItem(items, name)
		if !ok {
			t.Errorf("alias preset %q missing; names = %v", name, names(items))
			continue
		}
		if len(item.Models) != 1 || item.Models[0] != model {
			t.Errorf("preset %q seeded with %v, want [%s]", name, item.Models, model)
		}
	}

	// A combo is addressed bare, so a name carrying "/" or a space would be
	// unreachable; the builder must never emit one.
	for _, item := range items {
		if !isValidName(item.Name) {
			t.Errorf("preset name %q is not addressable as a combo name", item.Name)
		}
	}
}

func TestBuildClaudeFlagsExistingNames(t *testing.T) {
	items, err := Build("claude", nil, []string{" sonnet ", "not-a-preset"})
	if err != nil {
		t.Fatalf("Build(claude) error = %v", err)
	}

	skipped := 0
	for _, item := range items {
		if !item.Exists {
			continue
		}
		skipped++
		if item.Name != "sonnet" {
			t.Errorf("item %q flagged exists, want only sonnet", item.Name)
		}
	}
	if skipped != 1 {
		t.Errorf("flagged %d items as existing, want 1 (an unknown stored name must not match)", skipped)
	}
}

func TestBuildCursorUsesLiveCatalogWhenGiven(t *testing.T) {
	live := []string{"default", "gpt-5.3-codex"}

	items, err := Build("cursor", live, nil)
	if err != nil {
		t.Fatalf("Build(cursor) error = %v", err)
	}
	if got, want := len(items), len(live); got != want {
		t.Fatalf("live catalog yielded %d items, want %d (%v)", got, want, names(items))
	}
	for i, item := range items {
		if item.Name != live[i] {
			t.Errorf("item %d = %q, want %q (live order must be preserved)", i, item.Name, live[i])
		}
		if want := "cu/" + live[i]; item.Models[0] != want {
			t.Errorf("preset %q seeded with %v, want [%s]", item.Name, item.Models, want)
		}
	}
}

func TestBuildCursorFallsBackToRegistry(t *testing.T) {
	static := providers.GetProviderModels(CursorAlias)
	if len(static) == 0 {
		t.Fatal("the cu registry row is empty; the fallback cannot be exercised")
	}

	items, err := Build("cursor", nil, nil)
	if err != nil {
		t.Fatalf("Build(cursor) error = %v", err)
	}
	if got, want := len(items), len(static); got != want {
		t.Fatalf("registry fallback yielded %d items, want %d", got, want)
	}
	// Upstream's own contract for the cursor list: a "default" entry exists and
	// no name carries a "/". Neither pins the count.
	if _, ok := findItem(items, "default"); !ok {
		t.Errorf("cursor presets have no %q entry; names = %v", "default", names(items))
	}
	for _, item := range items {
		if !isValidName(item.Name) {
			t.Errorf("cursor preset name %q is not addressable", item.Name)
		}
	}
}

func TestBuildRejectsUnknownSource(t *testing.T) {
	for _, source := range []string{"", "unknown", "Claude", "cursor "} {
		if _, err := Build(source, nil, nil); err == nil {
			t.Errorf("Build(%q) returned no error, want a rejection", source)
		}
	}
}
