package schedule

import (
	"strings"

	"golang.org/x/sys/windows/registry"
)

// windowsTimeZoneKeyName reads TimeZoneKeyName from the Windows time zone
// registry key.
//
// The value is a Windows zone name ("SE Asia Standard Time" in Vietnam,
// "Romance Standard Time" in France), which is not an IANA name and which
// time.LoadLocation cannot resolve — the tzdata database Go embeds
// understands IANA names only. The translation into an IANA name is the
// caller's job (timezoneFromRegistry), because the mapping table is
// platform-neutral.
//
// The read is best-effort: a locked-down machine, a service account without
// registry access, or a key that does not exist all answer "" and the caller
// falls back to "UTC".
func windowsTimeZoneKeyName() string {
	key, err := registry.OpenKey(
		registry.LOCAL_MACHINE,
		`SYSTEM\CurrentControlSet\Control\TimeZoneInformation`,
		registry.QUERY_VALUE,
	)
	if err != nil {
		return ""
	}
	defer key.Close()

	name, _, err := key.GetStringValue("TimeZoneKeyName")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(name)
}
