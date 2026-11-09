package schedule

import (
	"errors"
	"fmt"
)

// Auto-ping settings persistence (the shape upstream reads at
// quotaAutoPing.js:289-299 and writes from its settings route).
//
// One provider's settings key holds two independent maps:
//
//	{"connections": {"<conn>": true}, "schedules": {"<conn>": {...}}}
//
// They are independent on purpose: switching a connection off must not delete
// the schedule configured for it, and saving a schedule must not switch the
// connection back on. A write therefore merges into the stored value instead of
// replacing it, so a client that only knows one half cannot wipe the other.

// Providers are the providers carrying an auto-ping configuration.
var Providers = []string{"claude", "codex"}

// SettingsKey is the settings row key one provider's configuration lives under
// (the existing dashboard contract: claudeAutoPing / codexAutoPing).
func SettingsKey(provider string) string { return provider + "AutoPing" }

// Config is the parsed form of one provider's auto-ping settings.
type Config struct {
	Connections map[string]bool
	Schedules   map[string]*Schedule
}

// ErrBadShape reports a payload whose fields are not the ones this key holds.
var ErrBadShape = errors.New("connections and schedules must be objects")

// ParseConfig reads a stored value, tolerating any shape: the blob is
// hand-editable through the backup import, so a wrong type drops the entry
// rather than failing the whole read.
//
// Both the canonical nested shape and the older flat one are accepted. The
// flat form is a bare {connectionId: boolean} map; it is only read as the
// connections half, never as schedules, because a flat entry carries no
// schedule fields to read.
func ParseConfig(value any) Config {
	out := Config{Connections: map[string]bool{}, Schedules: map[string]*Schedule{}}
	obj, ok := value.(map[string]any)
	if !ok {
		return out
	}
	conns, hasConnections := obj["connections"].(map[string]any)
	if !hasConnections && !hasKey(obj, "connections") && !hasKey(obj, "schedules") {
		// Flat form: every entry is a connection switch.
		conns = obj
	}
	for id, raw := range conns {
		if on, ok := raw.(bool); ok && id != "" {
			out.Connections[id] = on
		}
	}
	if schedules, ok := obj["schedules"].(map[string]any); ok {
		for id, raw := range schedules {
			entry, ok := raw.(map[string]any)
			if !ok || id == "" {
				continue
			}
			if parsed := Normalize(scheduleInput(entry)); parsed != nil {
				out.Schedules[id] = parsed
			}
		}
	}
	return out
}

// hasKey reports whether a key is present, so an explicit null is told apart
// from an absent key.
func hasKey(obj map[string]any, key string) bool {
	_, ok := obj[key]
	return ok
}

// scheduleInput decodes one stored schedule entry.
func scheduleInput(entry map[string]any) *Input {
	in := &Input{}
	if on, ok := entry["enabled"].(bool); ok {
		in.Enabled = &on
	}
	in.Anchor, _ = entry["anchor"].(string)
	in.Timezone, _ = entry["timezone"].(string)
	in.WindowHours = entry["windowHours"]
	in.SlotsPerDay = entry["slotsPerDay"]
	return in
}

// MergeConfig applies a write payload on top of the stored value and returns
// the canonical value to persist. Only the halves present in the payload
// change; the other is carried over untouched.
//
// Every schedule is passed through Normalize, so an out-of-range window or an
// unknown timezone is repaired here rather than reaching the tick loop.
func MergeConfig(stored, payload any) (map[string]any, error) {
	obj, ok := payload.(map[string]any)
	if !ok {
		return nil, ErrBadShape
	}

	merged := ParseConfig(stored)

	// A payload names its halves in two ways. The canonical shape is
	// {"connections": {...}, "schedules": {...}}; the older flat shape is a
	// bare {connectionId: boolean} map with no wrapper. A client that mixes
	// the two — a flat switch spread next to a nested schedules map — is read
	// as both, because a top-level boolean is a connection switch either way.
	flat := !hasKey(obj, "connections") && !hasKey(obj, "schedules")

	if flat {
		if err := applyConnections(&merged, obj); err != nil {
			return nil, err
		}
		return merged.toMap(), nil
	}

	if raw, present := obj["connections"]; present {
		conns, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("connections must be an object of {connectionId: boolean}")
		}
		if err := applyConnections(&merged, conns); err != nil {
			return nil, err
		}
	}

	// The stray top-level booleans are the flat form's switches: a client
	// that spreads the flat map over the nested one leaves them beside the
	// two named halves. Reading them keeps such a write from silently
	// dropping the toggle the operator made.
	if stray := straySwitches(obj); len(stray) > 0 {
		if err := applyConnections(&merged, stray); err != nil {
			return nil, err
		}
	}

	if raw, present := obj["schedules"]; present {
		schedules, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("schedules must be an object of {connectionId: schedule}")
		}
		for id, value := range schedules {
			entry, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("schedules[%q] must be an object", id)
			}
			if id == "" {
				continue
			}
			if enabled, _ := entry["enabled"].(bool); !enabled {
				// A disabled entry removes the schedule: keeping the old
				// numbers would resurrect one the operator stopped.
				delete(merged.Schedules, id)
				continue
			}
			parsed := Normalize(scheduleInput(entry))
			if parsed == nil {
				return nil, fmt.Errorf("schedules[%q] is not a usable schedule", id)
			}
			merged.Schedules[id] = parsed
		}
	}

	return merged.toMap(), nil
}

// applyConnections writes one connections half into the merged value.
//
// A wrong value type is refused rather than dropped: a client that sent
// "true" as a string meant to switch an account on, and reading it as "off"
// would silently undo the operator's action.
func applyConnections(merged *Config, conns map[string]any) error {
	for id, value := range conns {
		on, ok := value.(bool)
		if !ok {
			return fmt.Errorf("connections[%q] must be a boolean", id)
		}
		if id != "" {
			merged.Connections[id] = on
		}
	}
	return nil
}

// straySwitches collects the top-level booleans of a mixed payload — the flat
// form's switches sitting beside the two named halves.
func straySwitches(obj map[string]any) map[string]any {
	out := map[string]any{}
	for id, value := range obj {
		if id == "connections" || id == "schedules" {
			continue
		}
		if _, isBool := value.(bool); isBool {
			out[id] = value
		}
	}
	return out
}

// toMap renders the canonical stored shape.
func (c Config) toMap() map[string]any {
	connections := make(map[string]any, len(c.Connections))
	for id, on := range c.Connections {
		connections[id] = on
	}
	schedules := make(map[string]any, len(c.Schedules))
	for id, s := range c.Schedules {
		schedules[id] = map[string]any{
			"enabled":     true,
			"anchor":      s.Anchor,
			"timezone":    s.Timezone,
			"windowHours": s.WindowHours,
			"slotsPerDay": s.SlotsPerDay,
		}
	}
	return map[string]any{"connections": connections, "schedules": schedules}
}
