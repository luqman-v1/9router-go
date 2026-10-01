package updater

import (
	"cmp"
	"fmt"
	"strconv"
	"strings"
)

// semver is a version split into its semver core and, when present, its
// prerelease identifiers. Build metadata after "+" is dropped at parse time
// because semver gives it no precedence.
type semver struct {
	core       [3]int
	prerelease []string
}

// hasPrerelease reports whether the version carries any prerelease identifier.
func (s semver) hasPrerelease() bool {
	return len(s.prerelease) > 0
}

// parseSemver splits a semver string. A leading "v" and any "+build" suffix are
// dropped; a missing or malformed core component stays 0, as before.
func parseSemver(v string) semver {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if idx := strings.IndexByte(v, '+'); idx != -1 {
		v = v[:idx]
	}

	var parsed semver
	core := v
	if idx := strings.IndexByte(v, '-'); idx != -1 {
		core, parsed.prerelease = v[:idx], strings.Split(v[idx+1:], ".")
	}
	fmt.Sscanf(core, "%d.%d.%d", &parsed.core[0], &parsed.core[1], &parsed.core[2])
	return parsed
}

// compareSemver orders two versions by semver precedence: the core first, then
// the prerelease identifiers, where the one with more identifiers wins when all
// shared ones are equal (so 1.9.7-rc < 1.9.7-rc.1).
func compareSemver(a, b semver) int {
	for i := range a.core {
		if c := cmp.Compare(a.core[i], b.core[i]); c != 0 {
			return c
		}
	}
	return comparePrerelease(a.prerelease, b.prerelease)
}

func comparePrerelease(a, b []string) int {
	switch {
	case len(a) == 0 && len(b) == 0:
		return 0
	case len(a) == 0:
		return 1 // a final release outranks every prerelease of the same core
	case len(b) == 0:
		return -1
	}

	for i := range min(len(a), len(b)) {
		if c := compareIdentifier(a[i], b[i]); c != 0 {
			return c
		}
	}
	return cmp.Compare(len(a), len(b))
}

func compareIdentifier(a, b string) int {
	an, aNum := parseNumericIdentifier(a)
	bn, bNum := parseNumericIdentifier(b)
	switch {
	case aNum && bNum:
		return cmp.Compare(an, bn)
	case aNum:
		return -1 // numeric identifiers always rank below alphanumeric ones
	case bNum:
		return 1
	default:
		return strings.Compare(a, b)
	}
}

// parseNumericIdentifier reports whether id is a numeric identifier and returns
// its value. Leading zeroes still count as numeric: the semver spec forbids
// them in valid versions, and treating such an identifier as alphanumeric
// would only reorder already-invalid input.
func parseNumericIdentifier(id string) (int, bool) {
	if id == "" {
		return 0, false
	}
	for i := range len(id) {
		if id[i] < '0' || id[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(id)
	if err != nil {
		return 0, false
	}
	return n, true
}
