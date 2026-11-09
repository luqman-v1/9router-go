//go:build integration

package integration

import (
	"net/http"
	"testing"

	"github.com/samber/lo"

	"9router/proxy/internal/combopresets"
	"9router/proxy/internal/providers"
)

// presetItem is one GET /api/combos/presets item.
type presetItem struct {
	Name   string   `json:"name"`
	Models []string `json:"models"`
	Exists bool     `json:"exists"`
}

type presetPreview struct {
	Source   string       `json:"source"`
	Items    []presetItem `json:"items"`
	ToCreate int          `json:"toCreate"`
	ToSkip   int          `json:"toSkip"`
}

type presetCreate struct {
	Source       string   `json:"source"`
	Created      []string `json:"created"`
	Skipped      []string `json:"skipped"`
	CreatedCount int      `json:"createdCount"`
	SkippedCount int      `json:"skippedCount"`
}

// claudeAliasPresetCount counts the items that come from the Claude Code alias
// tables rather than the registry: the four defaultModels entries (fable, opus,
// sonnet, haiku) plus the two extra alias targets (default, opusplan). It is
// the only literal here, and it does not move when a model is added to the
// registry — which is what keeps this expectation derivable rather than pinned.
const claudeAliasPresetCount = 6

// expectedClaudePresets derives the count from the registry `cc` row plus the
// alias tables. Pinning 13 would go red the moment a model is added to the
// registry, which the spec calls out as the counting trap.
func expectedClaudePresets() int {
	return len(providers.GetProviderModels(combopresets.ClaudeAlias)) + claudeAliasPresetCount
}

func getPresetPreview(t *testing.T, env *Env, source string) presetPreview {
	t.Helper()
	res := env.Get(t, "/api/combos/presets?source="+source)
	if res.Status != http.StatusOK {
		t.Fatalf("GET presets?source=%s = %d, want 200 (body: %s)", source, res.Status, truncate(res.Body))
	}
	var out presetPreview
	res.Decode(t, &out)
	return out
}

// TestComboPresetsPreviewFlagsExisting pins the preview contract the Combos
// page renders before anything is written: the full derived list, how many are
// missing, and how many are already stored.
func TestComboPresetsPreviewFlagsExisting(t *testing.T) {
	env := newEnv(t)
	env.AddCombo(t, "combo-sonnet", "sonnet", []string{"cc/claude-sonnet-5"})

	got := getPresetPreview(t, env, "claude")

	// The registry may grow, so the expectation is derived, never a literal.
	if want := expectedClaudePresets(); len(got.Items) != want {
		t.Fatalf("claude presets = %d items, want %d (registry %v); names = %v",
			len(got.Items), want, providers.GetProviderModels(combopresets.ClaudeAlias), presetNames(got.Items))
	}
	if got.ToSkip != 1 {
		t.Errorf("toSkip = %d, want 1 (only the seeded %q combo exists)", got.ToSkip, "sonnet")
	}
	if want := len(got.Items) - 1; got.ToCreate != want {
		t.Errorf("toCreate = %d, want %d", got.ToCreate, want)
	}

	// "sonnet" is a defaultModels alias, so it is the one item flagged.
	sonnet, ok := lo.Find(got.Items, func(i presetItem) bool { return i.Name == "sonnet" })
	if !ok {
		t.Fatalf("presets have no %q item; names = %v", "sonnet", presetNames(got.Items))
	}
	if !sonnet.Exists {
		t.Errorf("item %q is not flagged exists after seeding a combo of that name", sonnet.Name)
	}
	for _, item := range got.Items {
		if item.Name == "sonnet" {
			continue
		}
		if item.Exists {
			t.Errorf("item %q flagged exists, but no combo of that name was seeded", item.Name)
		}
	}
}

// TestComboPresetsCreateThenNoop pins that POST writes exactly the missing
// combos, and that a second POST creates nothing — the operation is idempotent,
// not a duplicate-name error.
func TestComboPresetsCreateThenNoop(t *testing.T) {
	env := newEnv(t)
	env.AddCombo(t, "combo-sonnet", "sonnet", []string{"cc/claude-sonnet-5"})
	wantTotal := expectedClaudePresets()

	first := env.Post(t, "/api/combos/presets", map[string]any{"source": "claude"})
	if first.Status != http.StatusOK {
		t.Fatalf("POST presets = %d, want 200 (body: %s)", first.Status, truncate(first.Body))
	}
	var created presetCreate
	first.Decode(t, &created)
	if created.CreatedCount != wantTotal-1 || len(created.Created) != wantTotal-1 {
		t.Fatalf("created %d combos (%v), want %d", created.CreatedCount, created.Created, wantTotal-1)
	}
	if created.SkippedCount != 1 || !lo.Contains(created.Skipped, "sonnet") {
		t.Errorf("skipped %v, want exactly the seeded sonnet", created.Skipped)
	}

	// The seeded combo's seed model must not have been rewritten.
	stored := getPresetPreview(t, env, "claude")
	if stored.ToCreate != 0 || stored.ToSkip != wantTotal {
		t.Errorf("after creation toCreate = %d / toSkip = %d, want 0 / %d", stored.ToCreate, stored.ToSkip, wantTotal)
	}

	second := env.Post(t, "/api/combos/presets", map[string]any{"source": "claude"})
	if second.Status != http.StatusOK {
		t.Fatalf("second POST presets = %d, want 200 (body: %s)", second.Status, truncate(second.Body))
	}
	var again presetCreate
	second.Decode(t, &again)
	if again.CreatedCount != 0 {
		t.Errorf("second POST created %d combos (%v), want 0", again.CreatedCount, again.Created)
	}
	if again.SkippedCount != wantTotal {
		t.Errorf("second POST skipped %d, want %d", again.SkippedCount, wantTotal)
	}
}

// TestComboPresetsCreatedComboIsRoutable pins that a created preset carries the
// seed model, read back through the production combos endpoint.
func TestComboPresetsCreatedComboIsRoutable(t *testing.T) {
	env := newEnv(t)

	if res := env.Post(t, "/api/combos/presets", map[string]any{"source": "claude"}); res.Status != http.StatusOK {
		t.Fatalf("POST presets = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}

	res := env.Get(t, "/api/combos")
	if res.Status != http.StatusOK {
		t.Fatalf("GET /api/combos = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	// The combos endpoint answers the stored row, where models is the JSON
	// string the column holds rather than an array.
	var combos []struct {
		Name   string `json:"name"`
		Models string `json:"models"`
	}
	res.Decode(t, &combos)

	opusplan, ok := lo.Find(combos, func(c struct {
		Name   string `json:"name"`
		Models string `json:"models"`
	}) bool {
		return c.Name == "opusplan"
	})
	if !ok {
		t.Fatalf("created combos have no %q entry", "opusplan")
	}
	if want := `["cc/claude-opus-5"]`; opusplan.Models != want {
		t.Errorf("opusplan models = %s, want %s", opusplan.Models, want)
	}
}

// TestComboPresetsCursorSeedsRegistryModels pins concrete cursor ids over HTTP.
// Every other cursor assertion in this file counts items, so a mutation that
// replaced a served name's model with an id no provider serves would still pass
// them; these four would not.
func TestComboPresetsCursorSeedsRegistryModels(t *testing.T) {
	env := newEnv(t)

	got := getPresetPreview(t, env, "cursor")
	for _, want := range []struct{ name, model string }{
		{"default", "cu/default"},
		{"claude-4.5-opus", "cu/claude-4.5-opus"},
		{"gpt-5.2-codex", "cu/gpt-5.2-codex"},
		{"gpt-5.3-codex", "cu/gpt-5.3-codex"},
	} {
		item, ok := lo.Find(got.Items, func(i presetItem) bool { return i.Name == want.name })
		if !ok {
			t.Errorf("cursor presets have no %q item; names = %v", want.name, presetNames(got.Items))
			continue
		}
		if len(item.Models) != 1 || item.Models[0] != want.model {
			t.Errorf("preset %q seeded with %v, want [%s]", want.name, item.Models, want.model)
		}
	}
}

// TestComboPresetsCursorCreateStoresSeededModels covers the create path for the
// cursor source, which the claude-only create test leaves untouched: the stored
// row must carry the prefixed model the preview served.
func TestComboPresetsCursorCreateStoresSeededModels(t *testing.T) {
	env := newEnv(t)

	if res := env.Post(t, "/api/combos/presets", map[string]any{"source": "cursor"}); res.Status != http.StatusOK {
		t.Fatalf("POST cursor presets = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}

	res := env.Get(t, "/api/combos")
	if res.Status != http.StatusOK {
		t.Fatalf("GET /api/combos = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	var combos []struct {
		Name   string `json:"name"`
		Models string `json:"models"`
	}
	res.Decode(t, &combos)

	stored := make(map[string]string, len(combos))
	for _, c := range combos {
		stored[c.Name] = c.Models
	}
	for _, want := range []struct{ name, models string }{
		{"default", `["cu/default"]`},
		{"gpt-5.2-codex", `["cu/gpt-5.2-codex"]`},
		{"gpt-5.3-codex", `["cu/gpt-5.3-codex"]`},
	} {
		got, ok := stored[want.name]
		if !ok {
			t.Errorf("created cursor combos have no %q entry", want.name)
			continue
		}
		if got != want.models {
			t.Errorf("combo %q models = %s, want %s", want.name, got, want.models)
		}
	}
}

// TestComboPresetsRejectUnknownSource covers the two negative cases the spec
// names: an unknown source and an empty one both answer 400, so the dashboard
// can tell "bad request" from "nothing to create".
func TestComboPresetsRejectUnknownSource(t *testing.T) {
	env := newEnv(t)

	for _, path := range []string{
		"/api/combos/presets?source=unknown",
		"/api/combos/presets?source=",
	} {
		res := env.Get(t, path)
		if res.Status != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400 (body: %s)", path, res.Status, truncate(res.Body))
		}
	}

	post := env.Post(t, "/api/combos/presets", map[string]any{"source": "unknown"})
	if post.Status != http.StatusBadRequest {
		t.Errorf("POST unknown source = %d, want 400 (body: %s)", post.Status, truncate(post.Body))
	}
}

// TestComboPresetsCursorFallsBackToRegistry pins the cursor source against the
// static registry row, since this tree has no live Cursor catalog resolver.
func TestComboPresetsCursorFallsBackToRegistry(t *testing.T) {
	env := newEnv(t)

	got := getPresetPreview(t, env, "cursor")
	want := len(providers.GetProviderModels(combopresets.CursorAlias))
	if want == 0 {
		t.Fatal("the cu registry row is empty; the fallback cannot be exercised")
	}
	if len(got.Items) != want {
		t.Fatalf("cursor presets = %d items, want %d", len(got.Items), want)
	}
	if _, ok := lo.Find(got.Items, func(i presetItem) bool { return i.Name == "default" }); !ok {
		t.Errorf("cursor presets have no %q item; names = %v", "default", presetNames(got.Items))
	}
}

func presetNames(items []presetItem) []string {
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = item.Name
	}
	return out
}
