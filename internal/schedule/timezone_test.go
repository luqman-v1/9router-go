package schedule

import (
	"os"
	"testing"
)

// The zone name must be one every runtime can resolve.
//
// time.Local.String() answers the literal "Local", and time.LoadLocation
// accepts it — so "Local" passes every check in this package and then lands
// in the settings blob as a zone name that no other runtime (the Node
// dashboard, a Node backup restore) can resolve. Upstream reads the IANA
// name from Intl, so that is what this must answer too.
func TestLocalTimezoneIsAResolvableZoneName(t *testing.T) {
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

// The TZ environment variable is the first answer: a container image sets it
// deliberately, and it overrides the system's own idea of the zone.
func TestTimezoneFromEnvReadsTZ(t *testing.T) {
	t.Setenv("TZ", "Asia/Ho_Chi_Minh")
	if got := timezoneFromEnv(); got != "Asia/Ho_Chi_Minh" {
		t.Errorf("timezoneFromEnv() = %q, want %q", got, "Asia/Ho_Chi_Minh")
	}
}

// A TZ that names no zone is refused rather than stored: "Local" would pass
// LoadLocation, and an unresolvable name would fail every later lookup.
func TestTimezoneFromEnvRejectsUnusableZone(t *testing.T) {
	for _, tz := range []string{"", "  ", "Not/AZone", "Local"} {
		t.Setenv("TZ", tz)
		if got := timezoneFromEnv(); got != "" {
			t.Errorf("timezoneFromEnv() with TZ=%q = %q, want \"\" (refused)", tz, got)
		}
	}
}

// "UTC" is the one name every zone database agrees on, so it is accepted
// without a lookup — and it is the fallback when nothing else can be read.
func TestTimezoneFromEnvAcceptsUTC(t *testing.T) {
	t.Setenv("TZ", "UTC")
	if got := timezoneFromEnv(); got != "UTC" {
		t.Errorf("timezoneFromEnv() = %q, want %q", got, "UTC")
	}
}

// The Unix lookup reads /etc/localtime's symlink target. The path is a
// parameter so the parsing can be tested without a Unix host — this repo's
// CI runs on Windows too.
func TestTimezoneFromLocaltimePath(t *testing.T) {
	cases := []struct {
		target string
		want   string
	}{
		{"/usr/share/zoneinfo/Asia/Ho_Chi_Minh", "Asia/Ho_Chi_Minh"},
		{"/usr/share/zoneinfo/Europe/Berlin", "Europe/Berlin"},
		{"/usr/share/zoneinfo/UTC", "UTC"},
		// A target that does not name a zone is refused.
		{"/etc/localtime", ""},
		{"/usr/share/zoneinfo/", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := timezoneFromLocaltimePath(tc.target); got != tc.want {
			t.Errorf("timezoneFromLocaltimePath(%q) = %q, want %q", tc.target, got, tc.want)
		}
	}
}

// The registry read is Windows-only. Off Windows the stub answers nothing,
// which is what the fallback expects.
func TestTimezoneFromRegistryIsWindowsOnly(t *testing.T) {
	got := timezoneFromRegistry()
	if got == "" {
		return // correct off Windows
	}
	if _, ok := LoadLocation(got); !ok {
		t.Errorf("timezoneFromRegistry() = %q, which time.LoadLocation cannot resolve", got)
	}
}

// The whole resolution must never answer a name that cannot be loaded.
func TestLocalTimezoneNeverAnswersAnUnusableName(t *testing.T) {
	// An unusable TZ must not leak into the answer.
	t.Setenv("TZ", "Not/AZone")
	name := LocalTimezone()
	if _, ok := LoadLocation(name); !ok {
		t.Errorf("LocalTimezone() = %q, which time.LoadLocation cannot resolve", name)
	}
}

// The symlink lookup reads the real path on Unix. On Windows there is no
// /etc/localtime, so the answer is empty — which is what the registry read
// then covers.
func TestTimezoneFromLocaltimeSymlinkIsSafe(t *testing.T) {
	got := timezoneFromLocaltimeSymlink()
	if got == "" {
		return // no Unix symlink to read
	}
	if _, ok := LoadLocation(got); !ok {
		t.Errorf("timezoneFromLocaltimeSymlink() = %q, which time.LoadLocation cannot resolve", got)
	}
}

// The environment read must not leak a stale value between tests.
func TestTimezoneFromEnvHonoursUnset(t *testing.T) {
	original, had := os.LookupEnv("TZ")
	t.Cleanup(func() {
		if had {
			os.Setenv("TZ", original)
		} else {
			os.Unsetenv("TZ")
		}
	})
	os.Unsetenv("TZ")
	if got := timezoneFromEnv(); got != "" {
		t.Errorf("timezoneFromEnv() with TZ unset = %q, want \"\"", got)
	}
}
