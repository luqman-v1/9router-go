package schedule

// ianaZoneForWindowsZone translates a Windows zone name into its IANA
// equivalent and reports whether the result is usable.
//
// The mapping comes from CLDR's windowsZones.xml, restricted to territory
// "001" — the world default, which is what an install without a
// country-specific Windows zone setting resolves to. Where a zone has a
// country-specific entry as well ("SE Asia Standard Time" territory="VN" ->
// Asia/Saigon), both names carry the same UTC offset and the same DST rules,
// so the schedule lands on the same wall-clock slots either way.
//
// A name CLDR does not know, or a zone the tzdata cannot load, answers ""
// and the caller falls back to "UTC" — a wrong zone would silently shift every
// slot, while "UTC" is at least a name every runtime agrees on.
//
// The function carries no build tag: the table it reads is platform-neutral
// (CLDR maps Windows zone names wherever the host runs), so the translation
// is exercised by the test suite on every OS rather than only on Windows.
func ianaZoneForWindowsZone(name string) string {
	zone, ok := windowsZoneIANA[name]
	if !ok {
		return ""
	}
	if _, ok := LoadLocation(zone); !ok {
		return ""
	}
	return zone
}
