package schedule

import "testing"

// TestMergeConfigKeepsHalvesIndependent pins the contract the settings route
// relies on: writing connections leaves schedules alone and vice versa, because
// an account can run only on its slots with the reactive ping off.
func TestMergeConfigKeepsHalvesIndependent(t *testing.T) {
	stored := map[string]any{
		"connections": map[string]any{"conn-a": true},
		"schedules": map[string]any{
			"conn-a": map[string]any{
				"enabled": true, "anchor": "09:30", "timezone": "UTC",
				"windowHours": 5.0, "slotsPerDay": 3.0,
			},
		},
	}

	// Turn the reactive ping off. The schedule must survive.
	merged, err := MergeConfig(stored, map[string]any{
		"connections": map[string]any{"conn-a": false},
	})
	if err != nil {
		t.Fatalf("MergeConfig(connections) = %v", err)
	}
	if on := ParseConfig(merged).Connections["conn-a"]; on {
		t.Error("connections[conn-a] = true after writing false")
	}
	sched := ParseConfig(merged).Schedules["conn-a"]
	if sched == nil {
		t.Fatal("schedule for conn-a was deleted by a connections-only write")
	}
	if sched.Anchor != "09:30" || sched.SlotsPerDay != 3 {
		t.Errorf("schedule = %+v, want it untouched", sched)
	}

	// And the reverse: saving a schedule must not switch the connection on.
	merged, err = MergeConfig(merged, map[string]any{
		"schedules": map[string]any{
			"conn-b": map[string]any{
				"enabled": true, "anchor": "10:00", "timezone": "UTC",
				"windowHours": 6.0, "slotsPerDay": 2.0,
			},
		},
	})
	if err != nil {
		t.Fatalf("MergeConfig(schedules) = %v", err)
	}
	cfg := ParseConfig(merged)
	if on, present := cfg.Connections["conn-b"]; present && on {
		t.Error("saving a schedule switched connections[conn-b] on")
	}
	if on := cfg.Connections["conn-a"]; on {
		t.Error("connections[conn-a] flipped back to true by a schedules-only write")
	}
	if cfg.Schedules["conn-b"] == nil {
		t.Fatal("schedules[conn-b] was not stored")
	}
}

// TestMergeConfigNormalizesAndReplaces pins that a stored schedule is the
// normalized form, and that a disabled entry deletes rather than lingers.
func TestMergeConfigNormalizesAndReplaces(t *testing.T) {
	merged, err := MergeConfig(nil, map[string]any{
		"schedules": map[string]any{
			"c1": map[string]any{
				"enabled": true, "anchor": "9:05", "timezone": "Not/AZone",
				"windowHours": 0.1, "slotsPerDay": 99.0,
			},
		},
	})
	if err != nil {
		t.Fatalf("MergeConfig = %v", err)
	}
	sched := ParseConfig(merged).Schedules["c1"]
	if sched == nil {
		t.Fatal("schedule c1 was not stored")
	}
	if sched.Anchor != "09:05" {
		t.Errorf("anchor = %q, want the normalized 09:05", sched.Anchor)
	}
	if sched.WindowHours != minWindowHours {
		t.Errorf("windowHours = %v, want the clamped %v", sched.WindowHours, minWindowHours)
	}
	if sched.SlotsPerDay != MaxSlotsPerDay(minWindowHours) {
		t.Errorf("slotsPerDay = %d, want %d", sched.SlotsPerDay, MaxSlotsPerDay(minWindowHours))
	}
	if sched.Timezone == "Not/AZone" {
		t.Error("an unusable timezone was stored verbatim")
	}

	if _, err := MergeConfig(merged, map[string]any{
		"schedules": map[string]any{"c1": map[string]any{"enabled": false}},
	}); err != nil {
		t.Fatalf("MergeConfig(disable) = %v", err)
	}
}

// TestMergeConfigRejectsBadShapes covers the payloads the settings route must
// refuse rather than repair: a non-object, a non-boolean connection, a
// schedule that is not an object.
func TestMergeConfigRejectsBadShapes(t *testing.T) {
	cases := map[string]any{
		"payload is not an object":       []any{1, 2},
		"connections is not an object":   map[string]any{"connections": "yes"},
		"connection value is not a bool": map[string]any{"connections": map[string]any{"c1": "true"}},
		"schedules is not an object":     map[string]any{"schedules": []any{}},
		"schedule is not an object":      map[string]any{"schedules": map[string]any{"c1": "09:30"}},
	}
	for name, payload := range cases {
		if _, err := MergeConfig(nil, payload); err == nil {
			t.Errorf("%s: MergeConfig accepted a bad payload", name)
		}
	}
}
