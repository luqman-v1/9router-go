//go:build integration

package integration

import (
	"net/http"
	"testing"
)

// autoPingValue is one provider's stored auto-ping configuration as
// GET /api/settings returns it.
type autoPingValue struct {
	Connections map[string]bool `json:"connections"`
	Schedules   map[string]struct {
		Enabled     bool    `json:"enabled"`
		Anchor      string  `json:"anchor"`
		Timezone    string  `json:"timezone"`
		WindowHours float64 `json:"windowHours"`
		SlotsPerDay int     `json:"slotsPerDay"`
	} `json:"schedules"`
}

type settingsWithAutoPing struct {
	ClaudeAutoPing autoPingValue `json:"claudeAutoPing"`
}

func readAutoPing(t *testing.T, env *Env) autoPingValue {
	t.Helper()
	res := env.Get(t, "/api/settings")
	if res.Status != http.StatusOK {
		t.Fatalf("GET /api/settings = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	var out settingsWithAutoPing
	res.Decode(t, &out)
	return out.ClaudeAutoPing
}

func putSettings(t *testing.T, env *Env, body map[string]any) {
	t.Helper()
	res := env.Put(t, "/api/settings", body)
	if res.Status != http.StatusOK {
		t.Fatalf("PUT /api/settings = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
}

// TestSettingsScheduleRoundTrip pins that a schedule written through PUT comes
// back field for field on GET — the acceptance row's "compare every field".
func TestSettingsScheduleRoundTrip(t *testing.T) {
	env := newEnv(t)

	putSettings(t, env, map[string]any{
		"claudeAutoPing": map[string]any{
			"schedules": map[string]any{
				"conn-1": map[string]any{
					"enabled":     true,
					"anchor":      "09:30",
					"timezone":    "UTC",
					"windowHours": 5.0,
					"slotsPerDay": 3.0,
				},
			},
		},
	})

	got := readAutoPing(t, env)
	sched, ok := got.Schedules["conn-1"]
	if !ok {
		t.Fatalf("claudeAutoPing.schedules has no conn-1; got %+v", got)
	}
	if !sched.Enabled {
		t.Error("enabled = false")
	}
	if sched.Anchor != "09:30" {
		t.Errorf("anchor = %q, want 09:30", sched.Anchor)
	}
	if sched.Timezone != "UTC" {
		t.Errorf("timezone = %q, want UTC", sched.Timezone)
	}
	if sched.WindowHours != 5 {
		t.Errorf("windowHours = %v, want 5", sched.WindowHours)
	}
	if sched.SlotsPerDay != 3 {
		t.Errorf("slotsPerDay = %d, want 3", sched.SlotsPerDay)
	}
}

// TestSettingsScheduleIsIndependentOfTheSwitch pins the property upstream calls
// out at quotaAutoPing.js:294-299: an account can run only on its slots with the
// reactive ping off, so the two halves must survive each other's writes.
func TestSettingsScheduleIsIndependentOfTheSwitch(t *testing.T) {
	env := newEnv(t)

	putSettings(t, env, map[string]any{
		"claudeAutoPing": map[string]any{
			"schedules": map[string]any{
				"conn-1": map[string]any{
					"enabled": true, "anchor": "09:30", "timezone": "UTC",
					"windowHours": 5.0, "slotsPerDay": 3.0,
				},
			},
		},
	})

	// Switching the reactive ping off must not take the schedule with it.
	putSettings(t, env, map[string]any{
		"claudeAutoPing": map[string]any{"connections": map[string]any{"conn-1": false}},
	})
	got := readAutoPing(t, env)
	if got.Connections["conn-1"] {
		t.Error("connections[conn-1] = true after writing false")
	}
	if _, still := got.Schedules["conn-1"]; !still {
		t.Fatal("a connections-only write deleted the schedule")
	}

	// And saving a schedule must not switch the connection back on.
	putSettings(t, env, map[string]any{
		"claudeAutoPing": map[string]any{
			"schedules": map[string]any{
				"conn-1": map[string]any{
					"enabled": true, "anchor": "10:15", "timezone": "UTC",
					"windowHours": 4.0, "slotsPerDay": 2.0,
				},
			},
		},
	})
	got = readAutoPing(t, env)
	if got.Connections["conn-1"] {
		t.Error("a schedules-only write switched connections[conn-1] back on")
	}
	if sched := got.Schedules["conn-1"]; sched.Anchor != "10:15" {
		t.Errorf("anchor = %q, want the updated 10:15", sched.Anchor)
	}
}

// TestSettingsScheduleRejectsBadPayload pins that an unusable schedule is a 400
// and leaves the stored one intact — a refused write must not half-apply.
func TestSettingsScheduleRejectsBadPayload(t *testing.T) {
	env := newEnv(t)

	putSettings(t, env, map[string]any{
		"claudeAutoPing": map[string]any{
			"schedules": map[string]any{
				"conn-1": map[string]any{
					"enabled": true, "anchor": "09:30", "timezone": "UTC",
					"windowHours": 5.0, "slotsPerDay": 3.0,
				},
			},
		},
	})

	for name, body := range map[string]map[string]any{
		"schedules is not an object":     {"claudeAutoPing": map[string]any{"schedules": []any{}}},
		"connection value is not a bool": {"claudeAutoPing": map[string]any{"connections": map[string]any{"conn-2": "on"}}},
		"auto-ping is not an object":     {"claudeAutoPing": "on"},
	} {
		res := env.Put(t, "/api/settings", body)
		if res.Status != http.StatusBadRequest {
			t.Errorf("%s: PUT = %d, want 400 (body: %s)", name, res.Status, truncate(res.Body))
		}
	}

	got := readAutoPing(t, env)
	if sched, ok := got.Schedules["conn-1"]; !ok || sched.Anchor != "09:30" {
		t.Errorf("stored schedule changed after refused payloads: %+v", got.Schedules)
	}
}

// TestSettingsScheduleNormalizesOnWrite pins that the stored value is the
// normalized one: an out-of-range window is repaired at the trust boundary, not
// carried into the tick loop.
func TestSettingsScheduleNormalizesOnWrite(t *testing.T) {
	env := newEnv(t)

	putSettings(t, env, map[string]any{
		"claudeAutoPing": map[string]any{
			"schedules": map[string]any{
				"conn-1": map[string]any{
					"enabled": true, "anchor": "09:30", "timezone": "UTC",
					"windowHours": 48.0, "slotsPerDay": 99.0,
				},
			},
		},
	})

	sched := readAutoPing(t, env).Schedules["conn-1"]
	if sched.WindowHours != 24 {
		t.Errorf("windowHours = %v, want it clamped to 24", sched.WindowHours)
	}
	if sched.SlotsPerDay != 1 {
		t.Errorf("slotsPerDay = %d, want 1 (floor(24/24))", sched.SlotsPerDay)
	}
}
