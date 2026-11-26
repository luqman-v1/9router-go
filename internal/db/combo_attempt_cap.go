package db

// ComboMemberAttemptCap bounds how many failed accounts one combo member may
// spend before the combo moves to its next member.
//
// The two combo fallback loops used to try up to ten connections per member
// unconditionally: a lane whose whole pool is down ground all ten while the
// client waited, and only then advanced. This cap is the operator's answer to
// that, and it is off by default because turning it on shortens a member's
// chance to find a working account.
//
// Zero means no cap (the existing behaviour). A positive value is clamped to
// MaxComboMemberAttemptCap.
//
// Resolution order, matching upstream Pro's documented rules: the provider's
// own override when it sets one (a zero there disables the cap for that
// provider even when a global cap exists), then the global setting, then off.
const (
	// DefaultComboMemberAttemptCap is what the dashboard offers as a starting
	// point when an operator enables the cap. It is not applied unless enabled
	// because a default that changes fallback behaviour on upgrade is a
	// regression nobody asked for.
	DefaultComboMemberAttemptCap = 3
	// MaxComboMemberAttemptCap is the ceiling the loops themselves impose.
	MaxComboMemberAttemptCap = 10
)

// ComboMemberAttemptCapFor resolves the cap for one provider.
//
// settings may be nil (a failed settings read), which reads as "off": the
// caller must never lose a working fallback because bookkeeping failed.
func ComboMemberAttemptCapFor(settings *SettingsData, provider string) int {
	if settings == nil {
		return 0
	}
	if ov, ok := settings.ProviderOverrides[provider]; ok && ov.ComboMemberAttemptCap != nil {
		return clampComboMemberAttemptCap(*ov.ComboMemberAttemptCap)
	}
	return clampComboMemberAttemptCap(settings.ComboMemberAttemptCap)
}

// clampComboMemberAttemptCap keeps a stored value inside the range the loops
// can honour. Negative reads as off rather than as a zero-attempt member,
// which would send no request at all.
func clampComboMemberAttemptCap(cap int) int {
	if cap <= 0 {
		return 0
	}
	if cap > MaxComboMemberAttemptCap {
		return MaxComboMemberAttemptCap
	}
	return cap
}
