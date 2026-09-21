package shared

import "sync"

// TokenSaverConfig holds runtime-configurable token saver flags and levels.
// Thread-safe via RWMutex. Zero value = all off.
type TokenSaverConfig struct {
	mu                    sync.RWMutex
	rtkEnabled            bool
	cavemanEnabled        bool
	cavemanLevel          string
	ponytailEnabled       bool
	ponytailLevel         string
	injectionGuardEnabled bool
}

// NewTokenSaverConfig creates config with initial values.
func NewTokenSaverConfig(rtk, caveman, ponytail bool) *TokenSaverConfig {
	return &TokenSaverConfig{
		rtkEnabled:            rtk,
		cavemanEnabled:        caveman,
		cavemanLevel:          "full",
		ponytailEnabled:       ponytail,
		ponytailLevel:         "full",
		injectionGuardEnabled: true, // on by default; toggle via settings
	}
}

// RTKEnabled reports whether RTK input compression is on.
func (c *TokenSaverConfig) RTKEnabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.rtkEnabled
}

// SetRTK toggles RTK input compression.
func (c *TokenSaverConfig) SetRTK(v bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rtkEnabled = v
}

// CavemanEnabled reports whether Caveman terse output style is on.
func (c *TokenSaverConfig) CavemanEnabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cavemanEnabled
}

// CavemanLevel returns the active Caveman level, defaulting to "full".
func (c *TokenSaverConfig) CavemanLevel() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.cavemanLevel == "" {
		return "full"
	}
	return c.cavemanLevel
}

// SetCaveman toggles Caveman style and optionally sets its level.
func (c *TokenSaverConfig) SetCaveman(v bool, level ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cavemanEnabled = v
	if len(level) > 0 && level[0] != "" {
		c.cavemanLevel = level[0]
	}
}

// PonytailEnabled reports whether Ponytail lazy dev code style is on.
func (c *TokenSaverConfig) PonytailEnabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ponytailEnabled
}

// PonytailLevel returns the active Ponytail level, defaulting to "full".
func (c *TokenSaverConfig) PonytailLevel() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.ponytailLevel == "" {
		return "full"
	}
	return c.ponytailLevel
}

// SetPonytail toggles Ponytail style and optionally sets its level.
func (c *TokenSaverConfig) SetPonytail(v bool, level ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ponytailEnabled = v
	if len(level) > 0 && level[0] != "" {
		c.ponytailLevel = level[0]
	}
}

// InjectionGuardEnabled reports whether the prompt-injection detector is on.
func (c *TokenSaverConfig) InjectionGuardEnabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.injectionGuardEnabled
}

// SetInjectionGuard toggles the prompt-injection detector.
func (c *TokenSaverConfig) SetInjectionGuard(v bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.injectionGuardEnabled = v
}

// Snapshot returns all current values.
func (c *TokenSaverConfig) Snapshot() (rtk, caveman, ponytail bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.rtkEnabled, c.cavemanEnabled, c.ponytailEnabled
}

// SetAll sets all flags atomically.
func (c *TokenSaverConfig) SetAll(rtk, caveman, ponytail bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rtkEnabled = rtk
	c.cavemanEnabled = caveman
	c.ponytailEnabled = ponytail
}
