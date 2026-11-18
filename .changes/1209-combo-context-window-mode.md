### Combos: support advertised context window mode (min, max, custom)

Allow combo strategies to configure how their context window is advertised in `/v1/models` (`context_length`) and the dashboard `ComboCard`. This is a local enhancement: it is not a port of any upstream 9router change.

- `ComboStrategy.ContextWindowMode` (`min` | `max` | `custom`) and `ContextWindow` integer.
- `min` (default): advertises the narrowest seat window (safe for panel queries where every seat sees the request).
- `max`: advertises the widest seat window (for rollover fallback chains where an oversized request advances to larger-window seats).
- `custom`: advertises an explicit operator-chosen window, falling back to `min` when unset or 0.
- Frontend: adds mode selector and custom token input in `ComboCard.svelte` and `CombosView.svelte`.
- Unit tests in `internal/db/settings_test.go` and `internal/handlers/chat/combo_seat_limits_test.go`.
