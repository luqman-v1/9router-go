package schedule

import (
	"strings"
	"testing"
)

// The Windows registry records a Windows zone name, and the schedule needs an
// IANA one. CLDR's windowsZones.xml is the mapping between them, compiled
// into the binary (windowszones_gen.go).
//
// These assertions run on every OS, not only on Windows: the table itself is
// platform-neutral, and a mapping that is wrong there is wrong everywhere the
// gateway ships.

// Every zone the table names must be one the tzdata can load. A zone that
// cannot be loaded is dropped at lookup time, so a broken entry silently
// costs the operator their local zone — which is exactly the bug this table
// exists to fix.
func TestWindowsZoneTableEntriesAllResolve(t *testing.T) {
	for windowsName, iana := range windowsZoneIANA {
		if _, ok := LoadLocation(iana); !ok {
			t.Errorf("windowsZoneIANA[%q] = %q, which time.LoadLocation cannot resolve", windowsName, iana)
		}
	}
}

// The table must be keyed by Windows zone name and valued by an IANA zone
// name. A Windows name on the value side would mean the translation did not
// happen, and the schedule would keep the name nothing can resolve.
func TestWindowsZoneTableMapsWindowsNamesToIANANames(t *testing.T) {
	for windowsName, iana := range windowsZoneIANA {
		if windowsName == "" {
			t.Error("windowsZoneIANA has an empty key")
		}
		if strings.Contains(iana, " ") {
			t.Errorf("windowsZoneIANA[%q] = %q, which is not a single zone name", windowsName, iana)
		}
		if strings.Contains(iana, "Standard Time") {
			t.Errorf("windowsZoneIANA[%q] = %q, which is a Windows name, not an IANA one", windowsName, iana)
		}
		// The key side must be a Windows name — one the tzdata cannot load.
		// A key that resolves is an IANA name that was put on the wrong side
		// of the mapping, and the lookup would answer "" for it instead of
		// the zone it names. "UTC" is the one name that is both.
		if windowsName != "UTC" {
			if _, ok := LoadLocation(windowsName); ok {
				t.Errorf("windowsZoneIANA key %q is an IANA name, so it belongs on the value side", windowsName)
			}
		}
	}
}

// The zone this brief was written against: a Vietnamese install. The Windows
// name is "SE Asia Standard Time" and the territory-001 entry is Asia/Bangkok.
// Asia/Saigon (the VN-specific entry) carries the same +07:00 offset and no
// DST, so both put the slots on the same wall clock.
func TestIanaZoneForWindowsZoneTranslatesKnownWindowsNames(t *testing.T) {
	cases := []struct {
		windowsName string
		want        string
	}{
		{"SE Asia Standard Time", "Asia/Bangkok"},
		{"Tokyo Standard Time", "Asia/Tokyo"},
		{"Romance Standard Time", "Europe/Paris"},
		{"W. Europe Standard Time", "Europe/Berlin"},
		{"China Standard Time", "Asia/Shanghai"},
		{"Singapore Standard Time", "Asia/Singapore"},
		{"India Standard Time", "Asia/Calcutta"},
		{"Korea Standard Time", "Asia/Seoul"},
		{"AUS Eastern Standard Time", "Australia/Sydney"},
	}
	for _, tc := range cases {
		got := ianaZoneForWindowsZone(tc.windowsName)
		if got != tc.want {
			t.Errorf("ianaZoneForWindowsZone(%q) = %q, want %q", tc.windowsName, got, tc.want)
		}
	}
}

// A name CLDR does not know answers "" so the caller falls back to "UTC" —
// an unknown name must never be stored.
func TestIanaZoneForWindowsZoneRefusesUnknownNames(t *testing.T) {
	for _, name := range []string{"", "  ", "Not A Windows Zone", "SE Asia Standard Time "} {
		if got := ianaZoneForWindowsZone(name); got != "" {
			t.Errorf("ianaZoneForWindowsZone(%q) = %q, want \"\" (refused)", name, got)
		}
	}
}

// Every answer the translation gives must be a zone name a Node runtime can
// resolve too, because the same name is written into the settings blob the
// Node dashboard reads. A zone only this binary understands is a zone the
// dashboard cannot honour.
func TestIanaZoneForWindowsZoneAnswersResolvableNames(t *testing.T) {
	for windowsName := range windowsZoneIANA {
		got := ianaZoneForWindowsZone(windowsName)
		if got == "" {
			t.Errorf("ianaZoneForWindowsZone(%q) = \"\", want a usable zone", windowsName)
			continue
		}
		if _, ok := LoadLocation(got); !ok {
			t.Errorf("ianaZoneForWindowsZone(%q) = %q, which time.LoadLocation cannot resolve", windowsName, got)
		}
	}
}
