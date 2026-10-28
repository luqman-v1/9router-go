package schedule

import (
	"testing"
)

// The write → read round trip is the contract the dashboard depends on.
//
// The two halves of one settings key are independent: `connections` says
// which accounts opted in, `schedules` says when each account is pinged on
// its own wall-clock slots. A client that edits one half must not lose the
// other, or an operator who configured a schedule for an account and then
// toggled that account's switch would silently lose the schedule.
//
// These cases are the shapes the dashboard and the backup import produce.
func TestMergeConfigRoundTrip(t *testing.T) {
	seedSchedule := map[string]any{
		"conn-1": map[string]any{
			"enabled":     true,
			"anchor":      "09:30",
			"timezone":    "UTC",
			"windowHours": 5.0,
			"slotsPerDay": 3.0,
		},
	}

	t.Run("nested payload keeps the stored schedules", func(t *testing.T) {
		stored := map[string]any{
			"connections": map[string]any{"conn-1": true},
			"schedules":   seedSchedule,
		}
		payload := map[string]any{
			"connections": map[string]any{"conn-2": true},
		}

		merged, err := MergeConfig(stored, payload)
		if err != nil {
			t.Fatalf("MergeConfig = %v", err)
		}

		conns, _ := merged["connections"].(map[string]any)
		if on, _ := conns["conn-2"].(bool); !on {
			t.Error("the new connection switch was not written")
		}
		schedules, _ := merged["schedules"].(map[string]any)
		if _, ok := schedules["conn-1"]; !ok {
			t.Error("the stored schedule was lost by a connections-only write")
		}
	})

	t.Run("flat payload is read as the connections half", func(t *testing.T) {
		stored := map[string]any{
			"connections": map[string]any{"conn-1": true},
			"schedules":   seedSchedule,
		}
		payload := map[string]any{"conn-2": true}

		merged, err := MergeConfig(stored, payload)
		if err != nil {
			t.Fatalf("MergeConfig = %v", err)
		}
		conns, _ := merged["connections"].(map[string]any)
		if on, _ := conns["conn-2"].(bool); !on {
			t.Error("the flat payload was not read as a connection switch")
		}
		schedules, _ := merged["schedules"].(map[string]any)
		if _, ok := schedules["conn-1"]; !ok {
			t.Error("the stored schedule was lost by a flat write")
		}
	})

	t.Run("output is always the nested shape", func(t *testing.T) {
		for name, payload := range map[string]map[string]any{
			"flat":   {"conn-1": true},
			"nested": {"connections": map[string]any{"conn-1": true}},
		} {
			merged, err := MergeConfig(nil, payload)
			if err != nil {
				t.Fatalf("%s: MergeConfig = %v", name, err)
			}
			if _, ok := merged["connections"]; !ok {
				t.Errorf("%s: output has no connections half: %+v", name, merged)
			}
			if _, ok := merged["schedules"]; !ok {
				t.Errorf("%s: output has no schedules half: %+v", name, merged)
			}
		}
	})

	t.Run("a mixed payload reads both halves", func(t *testing.T) {
		// A client that spreads the flat map over the nested one leaves the
		// switches at the top level beside the two named halves.
		payload := map[string]any{
			"conn-1": true,
			"schedules": map[string]any{
				"conn-2": map[string]any{
					"enabled": true, "anchor": "10:00", "timezone": "UTC",
					"windowHours": 5.0, "slotsPerDay": 3.0,
				},
			},
		}
		merged, err := MergeConfig(nil, payload)
		if err != nil {
			t.Fatalf("MergeConfig = %v", err)
		}
		conns, _ := merged["connections"].(map[string]any)
		if on, _ := conns["conn-1"].(bool); !on {
			t.Error("the top-level switch was not read as a connection")
		}
		schedules, _ := merged["schedules"].(map[string]any)
		if _, ok := schedules["conn-2"]; !ok {
			t.Error("the schedules half was not written")
		}
	})

	t.Run("a disabled schedule is removed", func(t *testing.T) {
		stored := map[string]any{
			"connections": map[string]any{},
			"schedules":   seedSchedule,
		}
		payload := map[string]any{
			"schedules": map[string]any{
				"conn-1": map[string]any{"enabled": false},
			},
		}
		merged, err := MergeConfig(stored, payload)
		if err != nil {
			t.Fatalf("MergeConfig = %v", err)
		}
		schedules, _ := merged["schedules"].(map[string]any)
		if _, ok := schedules["conn-1"]; ok {
			t.Error("a disabled schedule was kept")
		}
	})

	t.Run("a bad connection value is refused", func(t *testing.T) {
		_, err := MergeConfig(nil, map[string]any{
			"connections": map[string]any{"conn-1": "yes"},
		})
		if err == nil {
			t.Fatal("MergeConfig accepted a non-boolean connection value")
		}
	})
}

// ParseConfig reads the stored value on the tick path, so it must accept
// both shapes the dashboard and the backup import can leave behind.
func TestParseConfigAcceptsBothShapes(t *testing.T) {
	t.Run("nested", func(t *testing.T) {
		cfg := ParseConfig(map[string]any{
			"connections": map[string]any{"conn-1": true},
			"schedules": map[string]any{
				"conn-1": map[string]any{
					"enabled": true, "anchor": "09:30", "timezone": "UTC",
					"windowHours": 5.0, "slotsPerDay": 3.0,
				},
			},
		})
		if !cfg.Connections["conn-1"] {
			t.Error("the nested connection switch was not read")
		}
		if cfg.Schedules["conn-1"] == nil {
			t.Error("the nested schedule was not read")
		}
	})

	t.Run("flat", func(t *testing.T) {
		cfg := ParseConfig(map[string]any{"conn-1": true})
		if !cfg.Connections["conn-1"] {
			t.Error("the flat connection switch was not read")
		}
	})

	t.Run("an explicit null connections half is not read as the flat form", func(t *testing.T) {
		// The key is present, so the value is a nested half whose content is
		// missing — not a flat map of switches.
		cfg := ParseConfig(map[string]any{"connections": nil})
		if len(cfg.Connections) != 0 {
			t.Errorf("Connections = %+v, want empty", cfg.Connections)
		}
	})
}
