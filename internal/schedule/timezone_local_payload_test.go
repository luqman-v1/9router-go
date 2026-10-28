package schedule

import "testing"

// TestSchedulePayloadCannotStoreLocalAsATimezone pins the other door into the
// failure the env-var guard already closes.
//
// time.LoadLocation special-cases "Local", so a schedule payload carrying
// timezone:"Local" passed Normalize's lookup and was written to the settings
// blob verbatim — a zone name no other runtime can resolve. The env var is
// refused; the payload was not.
func TestSchedulePayloadCannotStoreLocalAsATimezone(t *testing.T) {
	for _, spelling := range []string{"Local", "local", "LOCAL"} {
		enabled := true
		shrunk := Normalize(&Input{
			Enabled:  &enabled,
			Anchor:   "09:30",
			Timezone: spelling,
		})
		if shrunk == nil {
			// A nil schedule is already a refusal and satisfies the rule.
			continue
		}
		if shrunk.Timezone == spelling {
			t.Errorf("timezone %q was stored verbatim; a zone name every runtime can resolve is required", spelling)
		}
		if _, resolvable := LoadLocation(shrunk.Timezone); !resolvable {
			t.Errorf("timezone %q does not resolve; a schedule stored with it would not survive another host", shrunk.Timezone)
		}
	}
}

// TestDefaultTimezoneOrRefusesLocal directly pins the helper the payload path
// uses, so the rule holds even if Normalize's call site changes.
func TestDefaultTimezoneOrRefusesLocal(t *testing.T) {
	for _, spelling := range []string{"Local", "local", "LOCAL"} {
		if got := DefaultTimezoneOr(spelling); got == spelling {
			t.Errorf("DefaultTimezoneOr(%q) = %q; Local must fall back to the process zone", spelling, got)
		}
	}
	// The control: a real zone still passes through unchanged.
	if got := DefaultTimezoneOr("UTC"); got != "UTC" {
		t.Errorf("DefaultTimezoneOr(\"UTC\") = %q, want UTC", got)
	}
}
