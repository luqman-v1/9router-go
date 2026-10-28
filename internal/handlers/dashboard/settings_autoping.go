package dashboard

import (
	"fmt"

	"9router/proxy/internal/schedule"
)

// mergeAutoPingSchedules replaces each auto-ping payload in `updates` with the
// merged, normalized value to persist.
//
// It reads the stored configuration first because a write must not delete what
// the payload did not mention: the connections switch and the schedule for one
// account are independent (upstream quotaAutoPing.js:294-299), and the dashboard
// saves them from two different controls.
//
// The merged value is written through UpdateSettingsRaw like any other key, so
// GET /api/settings returns it unchanged — no extra read path, no shape that
// only the scheduler understands.
//
// The stored value is read even when the payload already carries both halves,
// because the stored half is what carries a schedule the payload does not
// mention: a client that knows only the connections map must not wipe the
// schedules on its way through.
func (h *DashboardHandler) mergeAutoPingSchedules(updates map[string]any) error {
	var stored map[string]any
	for _, provider := range schedule.Providers {
		key := schedule.SettingsKey(provider)
		payload, present := updates[key]
		if !present {
			continue
		}
		if stored == nil {
			var err error
			if stored, err = h.Repo.GetSettingsRaw(); err != nil {
				stored = map[string]any{}
			}
		}
		merged, err := schedule.MergeConfig(stored[key], payload)
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		updates[key] = merged
	}
	return nil
}
