package dashboard

import (
	"regexp"
	"slices"
	"strings"
)

// navVisibilitySettingKey is the settings key the dashboard's Settings > Sidebar
// switch writes and the sidebar reads. web/src/lib/nav.ts owns the canonical id
// list; this file only keeps the stored shape honest, so a stale or hand-crafted
// payload can never hand the sidebar an id it cannot address.
const navVisibilitySettingKey = "navVisibility"

// navHiddenIDPattern matches the ids the sidebar can emit: lowercase tab ids
// such as `quota`, `usage-cache` and `media-tts`.
var navHiddenIDPattern = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// maxHiddenNavEntries caps the stored list so a hostile payload cannot grow the
// settings row without bound. The sidebar renders 20 entries today.
const maxHiddenNavEntries = 32

// maxNavOrderEntries caps the stored arrangement the same way. It has to hold
// every entry the sidebar can draw exactly once — 20 today.
const maxNavOrderEntries = 64

// navAlwaysVisibleEntries are entries the visibility panel itself lives behind.
// Hiding `settings` would remove the only way back, so it is dropped on write
// and again on read, whatever the client sent.
var navAlwaysVisibleEntries = []string{"settings"}

// normalizeNavVisibility coerces a client payload into the stored shape
// {"hidden": [...], "order": [...]}: lower-cased, de-duplicated, capped, and
// with the always-visible entries removed from the hide list.
//
// `order` is the operator's sidebar arrangement. It carries every entry the
// sidebar can draw, including `settings` — that entry cannot be hidden, but it
// can be moved — and an absent `order` means "registry order", so a client that
// sends only a hide list keeps writing the shape it always did.
//
// It returns nil when the payload carries no usable list, which callers treat as
// "clear the setting" — a write that cannot be understood must not leave a
// half-applied list behind.
func normalizeNavVisibility(value any) map[string]any {
	container, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	raw, present := container["hidden"]
	if !present {
		return nil
	}
	hidden, ok := normalizeHiddenNavIDs(raw)
	if !ok {
		return nil
	}
	normalized := map[string]any{"hidden": hidden}
	if rawOrder, present := container["order"]; present {
		order, ok := normalizeNavOrderIDs(rawOrder)
		if !ok {
			return nil
		}
		normalized["order"] = order
	}
	return normalized
}

// normalizeHiddenNavIDs keeps only the ids that can address a sidebar entry, and
// never keeps one the panel itself lives behind. The second result reports
// whether the payload was a list at all, so a scalar `"hidden": "quota"` clears
// the setting instead of silently storing nothing.
func normalizeHiddenNavIDs(value any) ([]string, bool) {
	candidates, ok := navIDsFromList(value)
	if !ok {
		return nil, false
	}
	return navIDsInShape(candidates, navAlwaysVisibleEntries, maxHiddenNavEntries), true
}

// normalizeNavOrderIDs keeps only the ids that can address a sidebar entry, in
// the order they arrived — the arrangement is the list, so nothing is reordered
// here. Nothing is excluded either: `settings` is reachable from the sidebar, it
// is only the hide list that refuses it.
func normalizeNavOrderIDs(value any) ([]string, bool) {
	candidates, ok := navIDsFromList(value)
	if !ok {
		return nil, false
	}
	return navIDsInShape(candidates, nil, maxNavOrderEntries), true
}

// navIDsFromList reads the string members of a JSON list. The second result says
// whether the payload was a list at all.
func navIDsFromList(value any) ([]string, bool) {
	switch typed := value.(type) {
	case []any:
		candidates := make([]string, 0, len(typed))
		for _, item := range typed {
			if id, ok := item.(string); ok {
				candidates = append(candidates, id)
			}
		}
		return candidates, true
	case []string:
		return typed, true
	default:
		return nil, false
	}
}

// navIDsInShape lower-cases, filters by the id pattern, drops `excluded` and
// de-duplicates, stopping at `limit`.
func navIDsInShape(candidates []string, excluded []string, limit int) []string {
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		id := strings.ToLower(strings.TrimSpace(candidate))
		if !navHiddenIDPattern.MatchString(id) {
			continue
		}
		if slices.Contains(excluded, id) || slices.Contains(ids, id) {
			continue
		}
		if len(ids) == limit {
			break
		}
		ids = append(ids, id)
	}
	return ids
}

// applyNavVisibilityUpdate normalizes the key in HandleUpdateSettings' payload.
//
// An unparsable payload is stored as an explicit null rather than deleted:
// UpdateSettingsRaw merges into the stored row, so dropping the map entry would
// leave the previous list in place and the panel would keep hiding entries the
// operator just tried to restore.
func applyNavVisibilityUpdate(updates map[string]any) {
	raw, present := updates[navVisibilitySettingKey]
	if !present {
		return
	}
	if normalized := normalizeNavVisibility(raw); normalized != nil {
		updates[navVisibilitySettingKey] = normalized
		return
	}
	updates[navVisibilitySettingKey] = nil
}

// sanitizeNavVisibility re-reads the stored value through the same normalizer,
// so a row written by an older build (or by hand) reaches the dashboard in the
// shape the sidebar expects. An entry that normalizes to nothing is dropped
// instead of being sent as a value the client would have to defend against.
func sanitizeNavVisibility(settings map[string]any) {
	raw, present := settings[navVisibilitySettingKey]
	if !present {
		return
	}
	if normalized := normalizeNavVisibility(raw); normalized != nil {
		settings[navVisibilitySettingKey] = normalized
		return
	}
	delete(settings, navVisibilitySettingKey)
}
