package thinking

import "strings"

// ProviderThinkingModes is the vocabulary of the operator's per-provider
// reasoning override: the switches and the effort levels. Upstream's override
// accepts on/off and an effort (chatCore.js:117-131), and "none" is one of
// the efforts it names. The mode reaches reasoning_effort on the wire, so a
// value outside this set is rejected rather than forwarded.
var ProviderThinkingModes = append([]string{"auto", "on", "off", "none"}, EffortLevels...)

// NormalizeProviderThinkingMode returns the canonical form of a mode and
// whether it is one of the allowed values. The canonical form is lower-cased
// and trimmed, so a mode written as "HIGH" is the same as "high".
func NormalizeProviderThinkingMode(mode string) (string, bool) {
	m := strings.ToLower(strings.TrimSpace(mode))
	if m == "" {
		return "", false
	}
	for _, allowed := range ProviderThinkingModes {
		if m == allowed {
			return m, true
		}
	}
	return "", false
}
