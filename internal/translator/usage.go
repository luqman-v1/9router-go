package translator

import "sync"

var (
	statesMu sync.Mutex
	states   = make(map[string]*StreamState)
)

// GetStreamUsage returns the accumulated usage for a stream session and removes it.
func GetStreamUsage(sessionKey string) *OpenAIUsage {
	statesMu.Lock()
	defer statesMu.Unlock()
	if state, ok := states[sessionKey]; ok && state.Usage != nil {
		usage := *state.Usage
		return &usage
	}
	return nil
}

// Global last-usage capture for the non-streaming path.
var lastUsage *OpenAIUsage
var lastUsageMu sync.Mutex

// SetLastUsage stores usage from a completed stream translation.
func SetLastUsage(u *OpenAIUsage) {
	lastUsageMu.Lock()
	defer lastUsageMu.Unlock()
	lastUsage = u
}

// GetAndClearLastUsage returns and clears the stored last usage.
func GetAndClearLastUsage() *OpenAIUsage {
	lastUsageMu.Lock()
	defer lastUsageMu.Unlock()
	u := lastUsage
	lastUsage = nil
	return u
}
