package handlers

import "sync"

// TokenSaverConfig holds runtime-configurable token saver flags.
// Thread-safe via RWMutex. Zero value = all off.
type TokenSaverConfig struct {
	mu              sync.RWMutex
	rtkEnabled      bool
	cavemanEnabled  bool
	ponytailEnabled bool
}

// NewTokenSaverConfig creates config with initial values.
func NewTokenSaverConfig(rtk, caveman, ponytail bool) *TokenSaverConfig {
	return &TokenSaverConfig{
		rtkEnabled:      rtk,
		cavemanEnabled:  caveman,
		ponytailEnabled: ponytail,
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

func (c *TokenSaverConfig) SetCaveman(v bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cavemanEnabled = v
}

func (c *TokenSaverConfig) PonytailEnabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ponytailEnabled
}

func (c *TokenSaverConfig) SetPonytail(v bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ponytailEnabled = v
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
