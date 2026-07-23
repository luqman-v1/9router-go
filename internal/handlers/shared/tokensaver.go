package shared

import "sync"

// TokenSaverConfig holds runtime-configurable token saver flags and levels.
// Thread-safe via RWMutex. Zero value = all off.
type TokenSaverConfig struct {
	mu              sync.RWMutex
	rtkEnabled      bool
	cavemanEnabled  bool
	cavemanLevel    string
	ponytailEnabled bool
	ponytailLevel   string
}

// NewTokenSaverConfig creates config with initial values.
func NewTokenSaverConfig(rtk, caveman, ponytail bool) *TokenSaverConfig {
	return &TokenSaverConfig{
		rtkEnabled:      rtk,
		cavemanEnabled:  caveman,
		cavemanLevel:    "full",
		ponytailEnabled: ponytail,
		ponytailLevel:   "full",
	}
}

func (c *TokenSaverConfig) RTKEnabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.rtkEnabled
}

func (c *TokenSaverConfig) SetRTK(v bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rtkEnabled = v
}

func (c *TokenSaverConfig) CavemanEnabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cavemanEnabled
}

func (c *TokenSaverConfig) CavemanLevel() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.cavemanLevel == "" {
		return "full"
	}
	return c.cavemanLevel
}

func (c *TokenSaverConfig) SetCaveman(v bool, level ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cavemanEnabled = v
	if len(level) > 0 && level[0] != "" {
		c.cavemanLevel = level[0]
	}
}

func (c *TokenSaverConfig) PonytailEnabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ponytailEnabled
}

func (c *TokenSaverConfig) PonytailLevel() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.ponytailLevel == "" {
		return "full"
	}
	return c.ponytailLevel
}

func (c *TokenSaverConfig) SetPonytail(v bool, level ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ponytailEnabled = v
	if len(level) > 0 && level[0] != "" {
		c.ponytailLevel = level[0]
	}
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
