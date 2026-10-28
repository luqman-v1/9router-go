// Package combopresets builds the Cursor / Claude default combo lists the
// dashboard offers on the Combos page (port of upstream src/lib/comboPresets.js).
//
// Every list is DERIVED, never written out: the Claude items are whatever the
// `cc` registry row currently holds, plus the Claude Code aliases. Adding a
// model to internal/providers therefore adds a preset here with no second edit,
// which is why no test may pin a count.
package combopresets

import (
	"errors"
	"regexp"
	"strings"

	"9router/proxy/internal/providers"
)

// ErrUnknownSource is returned by Build for anything that is not a preset
// family, so the HTTP edge can answer 400 rather than an empty list.
var ErrUnknownSource = errors.New("source must be 'cursor' or 'claude'")

// CursorAlias and ClaudeAlias are the registry aliases the presets seed from.
const (
	CursorAlias = "cu"
	ClaudeAlias = "cc"
)

// validNamePattern is upstream's VALID_COMBO_NAME_REGEX. A combo is addressed
// bare, so a name carrying "/" or a space could never be reached.
var validNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_.\-]+$`)

// claudeDefaultModel is one CLI_TOOLS.claude.defaultModels entry: the alias the
// client sends and the prefixed model a combo of that name should route to.
type claudeDefaultModel struct {
	Alias        string
	DefaultValue string
}

// claudeDefaultModels mirrors src/shared/constants/cliTools.js:99-104.
var claudeDefaultModels = []claudeDefaultModel{
	{Alias: "fable", DefaultValue: "cc/claude-fable-5"},
	{Alias: "opus", DefaultValue: "cc/claude-opus-5"},
	{Alias: "sonnet", DefaultValue: "cc/claude-sonnet-5"},
	{Alias: "haiku", DefaultValue: "cc/claude-haiku-4-5-20251001"},
}

// claudeModelAliases mirrors CLI_TOOLS.claude.modelAliases. Only the names that
// also appear in claudeExtraAliasTargets become presets; the rest are already
// covered by claudeDefaultModels.
var claudeModelAliases = []string{"default", "sonnet", "opus", "fable", "haiku", "opusplan"}

// claudeExtraAliasTargets are the Claude Code aliases defaultModels does not
// list, with the model each one should route to (comboPresets.js:17-20).
var claudeExtraAliasTargets = map[string]string{
	"default":  "cc/claude-sonnet-5",
	"opusplan": "cc/claude-opus-5",
}

// Item is one preset: the combo name to create and the models to seed it with.
type Item struct {
	Name   string   `json:"name"`
	Models []string `json:"models"`
	// Exists reports whether a combo of that name is already stored. It is only
	// meaningful on the output of Build, where it is filled from the caller's
	// existing names.
	Exists bool `json:"exists"`
}

// IsValidSource reports whether source names a preset family. Anything else is
// a 400 at the HTTP edge rather than an empty list.
func IsValidSource(source string) bool {
	return source == "cursor" || source == "claude"
}

// isValidName mirrors isValidComboPresetName.
func isValidName(name string) bool {
	return name != "" && validNamePattern.MatchString(name)
}

// pushItem appends an item unless the name is invalid, already taken, or the
// model list is empty. taken is mutated only when the item is kept.
func pushItem(out []Item, name string, models []string, taken map[string]bool) []Item {
	if !isValidName(name) || taken[name] || len(models) == 0 {
		return out
	}
	taken[name] = true
	return append(out, Item{Name: name, Models: models})
}

// itemsFromModels turns a registry model list into `<alias>/<id>` presets.
func itemsFromModels(out []Item, modelIDs []string, providerAlias string, taken map[string]bool) []Item {
	for _, id := range modelIDs {
		if id == "" {
			continue
		}
		out = pushItem(out, id, []string{providerAlias + "/" + id}, taken)
	}
	return out
}

// buildClaudeItems is buildClaudePresetItems: registry `cc` first, then the
// CLI default aliases, then the remaining aliases with an explicit target.
func buildClaudeItems() []Item {
	taken := make(map[string]bool, len(providers.GetProviderModels(ClaudeAlias))+len(claudeDefaultModels)+len(claudeExtraAliasTargets))
	out := itemsFromModels(nil, providers.GetProviderModels(ClaudeAlias), ClaudeAlias, taken)

	for _, entry := range claudeDefaultModels {
		if entry.Alias == "" || entry.DefaultValue == "" {
			continue
		}
		out = pushItem(out, entry.Alias, []string{entry.DefaultValue}, taken)
	}

	for _, alias := range claudeModelAliases {
		if taken[alias] {
			continue
		}
		target, ok := claudeExtraAliasTargets[alias]
		if !ok {
			continue
		}
		out = pushItem(out, alias, []string{target}, taken)
	}
	return out
}

// buildCursorItems prefers a live catalog (already-resolved model ids) and
// falls back to the static `cu` registry row when there is none.
func buildCursorItems(liveModels []string) []Item {
	taken := make(map[string]bool, len(liveModels))
	if len(liveModels) > 0 {
		return itemsFromModels(nil, liveModels, CursorAlias, taken)
	}
	return itemsFromModels(nil, providers.GetProviderModels(CursorAlias), CursorAlias, taken)
}

// Build returns the presets for one source, each flagged with whether a combo
// of that name is already stored. An unknown source returns an error so the
// caller can answer 400 instead of an empty list that reads as "nothing to do".
//
// liveModels is the Cursor catalog resolved from an active connection, or nil
// when there is none; it is ignored for the Claude source.
func Build(source string, liveModels []string, existingNames []string) ([]Item, error) {
	if !IsValidSource(source) {
		return nil, ErrUnknownSource
	}
	items := buildClaudeItems()
	if source == "cursor" {
		items = buildCursorItems(liveModels)
	}

	existing := make(map[string]bool, len(existingNames))
	for _, name := range existingNames {
		existing[strings.TrimSpace(name)] = true
	}
	for i := range items {
		items[i].Exists = existing[items[i].Name]
	}
	return items, nil
}
