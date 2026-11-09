//go:build windows

package schedule

import "testing"

// The registry read is the only source of a zone name on a Windows install,
// and it records a Windows name ("SE Asia Standard Time" in Vietnam) that
// time.LoadLocation cannot resolve. The translation through CLDR's
// windowsZones.xml is what makes the answer usable.
//
// Without this test the bug is invisible on a Windows CI runner: the lookup
// answered "" on every install, LocalTimezone() fell back to "UTC", and every
// slot landed a whole offset away from the wall clock the operator set.

// The registry value must translate into a zone the tzdata can load.
//
// The assertion is written so it holds on any Windows machine: a host whose
// registry names a zone CLDR does not know answers "", and the test only
// demands a usable zone when the registry actually named one.
func TestRegistryTimeZoneTranslatesToAResolvableZone(t *testing.T) {
	name := windowsTimeZoneKeyName()
	if name == "" {
		t.Skip("this machine's registry records no time zone key name")
	}
	if _, ok := LoadLocation(name); ok {
		return // the registry name is already an IANA name; nothing to translate
	}
	zone := ianaZoneForWindowsZone(name)
	if zone == "" {
		t.Fatalf("registry time zone %q has no CLDR translation, so LocalTimezone() falls back to UTC", name)
	}
	if _, ok := LoadLocation(zone); !ok {
		t.Errorf("ianaZoneForWindowsZone(%q) = %q, which time.LoadLocation cannot resolve", name, zone)
	}
}

// The whole resolution must answer a zone the tzdata can load, on Windows as
// much as anywhere else.
func TestLocalTimezoneOnWindowsIsResolvable(t *testing.T) {
	name := LocalTimezone()
	if name == "" {
		t.Fatal("LocalTimezone() is empty")
	}
	if name == "Local" {
		t.Fatal(`LocalTimezone() returned "Local", which is not a zone name any other runtime can resolve`)
	}
	if _, ok := LoadLocation(name); !ok {
		t.Errorf("LocalTimezone() = %q, which time.LoadLocation cannot resolve", name)
	}
}
