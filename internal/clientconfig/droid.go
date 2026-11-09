package clientconfig

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Factory Droid: ~/.factory/settings.json, JSON.
// docs/specs/client-schema-matrix.md §3.4.
//
// Droid addresses a custom model by its `index` field, not by its position, so
// inserting one entry without renumbering leaves every later entry pointing at
// the wrong model (§3.4). That is why this adapter rebuilds the whole array:
// no splice inserts without renumbering.

// droidMarker prefixes the `id` of every entry this adapter owns. Ownership is
// keyed on the id rather than the model, so a user whose model has the same
// name keeps their own entry.
const droidMarker = "custom:9Router"

// droidMaxOutputTokens is the ceiling the schema matrix records for Droid. It
// is fixed there rather than derived because Droid reads it to decide how much
// it may ask for.
const droidMaxOutputTokens = 131072

type droidAdapter struct{ baseAdapter }

func newDroidAdapter() Adapter {
	return droidAdapter{baseAdapter{id: "droid", paths: droidPaths}}
}

func (a droidAdapter) Plan(opts Options) ([]FilePlan, error) {
	paths, err := a.Paths(opts)
	if err != nil {
		return nil, err
	}
	entries, err := droidReadRaw(paths.Files[0])
	if err != nil {
		return nil, err
	}
	return []FilePlan{{File: paths.Files[0], Edits: []Edit{{
		KeyPath:         []string{"customModels"},
		ValueJSON:       droidRenumber(droidEntryJSON(opts), droidWithoutOurs(entries)),
		CreateIfMissing: true,
	}}}}, nil
}

func (a droidAdapter) Apply(opts Options) (Result, error) {
	plans, err := a.Plan(opts)
	if err != nil {
		return Result{}, err
	}
	return applyPlans(plans, nil)
}

func (a droidAdapter) Reset(opts Options) (Result, error) {
	paths, err := a.Paths(opts)
	if err != nil {
		return Result{}, err
	}
	entries, err := droidReadRaw(paths.Files[0])
	if err != nil {
		return Result{}, err
	}
	kept := droidWithoutOurs(entries)
	if len(kept) == len(entries) {
		return Result{}, nil // nothing of ours in the file
	}
	return applyPlans([]FilePlan{{File: paths.Files[0], Edits: []Edit{{
		KeyPath:         []string{"customModels"},
		ValueJSON:       droidRenumber(nil, kept),
		CreateIfMissing: true,
	}}}}, nil)
}

// droidEntryJSON is our entry at index 0; droidRenumber fixes the index when it
// builds the array.
func droidEntryJSON(opts Options) json.RawMessage {
	b, err := json.Marshal(droidEntry{
		ID:              droidMarker,
		Model:           opts.Model,
		Index:           0,
		BaseURL:         withV1(opts.BaseURL),
		APIKey:          opts.APIKey,
		DisplayName:     "9Router",
		MaxOutputTokens: droidMaxOutputTokens,
		Provider:        "openai",
	})
	if err != nil {
		// Strings and integers only: Marshal cannot fail, and returning a
		// literal keeps the signature free of an error nobody could act on.
		return json.RawMessage("{}")
	}
	return b
}

// droidEntry is one customModels element in the field order Droid expects.
type droidEntry struct {
	ID              string `json:"id"`
	Model           string `json:"model"`
	Index           int    `json:"index"`
	BaseURL         string `json:"baseUrl"`
	APIKey          string `json:"apiKey"`
	DisplayName     string `json:"displayName"`
	MaxOutputTokens int    `json:"maxOutputTokens"`
	NoImageSupport  bool   `json:"noImageSupport"`
	Provider        string `json:"provider"`
}

// droidReadRaw returns the customModels elements exactly as they sit in the
// file, so the entries this adapter does not own go back unchanged.
func droidReadRaw(file TargetFile) ([]json.RawMessage, error) {
	g, err := resolveGuard(file.Dir, relFromDir(file.Dir, file.Path))
	if err != nil {
		return nil, err
	}
	b, err := readThroughGuard(g)
	if err != nil || len(b) == 0 {
		return nil, err
	}
	p, root, err := parseJSONC(b)
	if err != nil {
		return nil, err
	}
	m := findJSONMember(root, "customModels")
	if m == nil || m.val.kind != jLBracket {
		return nil, nil
	}
	var out []json.RawMessage
	if err := json.Unmarshal(b[m.val.bytesStart(p.toks):m.val.bytesEnd(p.toks)], &out); err != nil {
		return nil, fmt.Errorf("customModels: %w", err)
	}
	return out, nil
}

// droidWithoutOurs drops the entries carrying our marker.
func droidWithoutOurs(in []json.RawMessage) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(in))
	for _, raw := range in {
		if droidIsOurs(raw) {
			continue
		}
		out = append(out, raw)
	}
	return out
}

// droidIsOurs reports whether a raw entry carries our id marker.
func droidIsOurs(raw json.RawMessage) bool {
	var probe struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return false
	}
	return strings.HasPrefix(probe.ID, droidMarker)
}

// droidRenumber builds the array: `first` (when not nil) at index 0, then rest,
// every entry renumbered from zero.
func droidRenumber(first json.RawMessage, rest []json.RawMessage) string {
	all := make([]json.RawMessage, 0, len(rest)+1)
	if first != nil {
		all = append(all, first)
	}
	all = append(all, rest...)

	parts := make([]string, len(all))
	for i, raw := range all {
		// Decode into a map only to replace `index`; Marshal of a
		// map[string]json.RawMessage preserves every other field's bytes.
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			parts[i] = string(raw)
			continue
		}
		idx, _ := json.Marshal(i)
		m["index"] = idx
		rebuilt, err := json.Marshal(m)
		if err != nil {
			parts[i] = string(raw)
			continue
		}
		parts[i] = string(rebuilt)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}
