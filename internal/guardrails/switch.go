package guardrails

// Switch is the global kill-switch both taps consult.
//
// A policy row is an explicit operator decision, so the default is on: a
// configured policy must not be silently ignored because a setting was never
// written. But a false positive that blocks real traffic needs an off switch
// that does not require deleting the policy, which would take the audit trail
// and the configuration with it.
//
// A nil Switch means on.
type Switch func() bool

// Enabled reports whether the taps should act. A nil switch is on.
func (s Switch) Enabled() bool {
	return s == nil || s()
}
