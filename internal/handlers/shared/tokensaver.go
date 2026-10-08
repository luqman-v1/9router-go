package shared

import (
	"sync"

	"9router/proxy/internal/db"
	"9router/proxy/internal/tokensaver"
)

// TokenSaverConfig holds runtime-configurable token saver flags and levels.
// Thread-safe via RWMutex. Zero value = all off.
type TokenSaverConfig struct {
	mu                      sync.RWMutex
	rtkEnabled              bool
	rtkMode                 string
	rtkIntensity            string
	rtkMaxLines             int
	rtkMaxChars             int
	rtkDeduplicate          bool
	rtkCategories           map[string]bool
	rtkFilters              map[string]bool
	rtkRawRetention         string
	cavemanEnabled          bool
	cavemanMode             string
	cavemanLevel            string
	cavemanAutoClarity      bool
	cavemanLanguage         string
	cavemanInputMode        bool
	cavemanPreserveKeywords string
	ponytailEnabled         bool
	ponytailLevel           string
	adhdEnabled             bool
	adhdLevel               string
	injectionGuardEnabled   bool
	semanticCacheEnabled    bool
	semanticCacheTTL        int
	semanticCacheMaxEntries int
}

// NewTokenSaverConfig creates config with initial values.
func NewTokenSaverConfig(rtk, caveman, ponytail bool) *TokenSaverConfig {
	return &TokenSaverConfig{
		rtkEnabled:              rtk,
		rtkMode:                 "simple",
		rtkIntensity:            "standard",
		rtkMaxLines:             100,
		rtkMaxChars:             8000,
		rtkDeduplicate:          true,
		rtkCategories: map[string]bool{
			"git":     true,
			"build":   true,
			"test":    true,
			"package": true,
			"docker":  true,
			"system":  true,
		},
		rtkRawRetention:         "never",
		cavemanEnabled:          caveman,
		cavemanMode:             "simple",
		cavemanLevel:            "full",
		cavemanAutoClarity:      true,
		cavemanLanguage:         "en",
		cavemanInputMode:        false,
		cavemanPreserveKeywords: "",
		ponytailEnabled:         ponytail,
		ponytailLevel:           "full",
		adhdEnabled:             false,
		adhdLevel:               "full",
		injectionGuardEnabled:   true, // on by default; toggle via settings
		semanticCacheEnabled:    false,
		semanticCacheTTL:        1440,
		semanticCacheMaxEntries: 1000,
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

// ADHDEnabled reports whether ADHD action-first output style is on.
func (c *TokenSaverConfig) ADHDEnabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.adhdEnabled
}

// ADHDLevel returns the active ADHD level, defaulting to "full".
func (c *TokenSaverConfig) ADHDLevel() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.adhdLevel == "" {
		return "full"
	}
	return c.adhdLevel
}

// SetADHD toggles ADHD style and optionally sets its level.
func (c *TokenSaverConfig) SetADHD(v bool, level ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.adhdEnabled = v
	if len(level) > 0 && level[0] != "" {
		c.adhdLevel = level[0]
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

// SemanticCacheEnabled reports whether prompt/response caching is active.
func (c *TokenSaverConfig) SemanticCacheEnabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.semanticCacheEnabled
}

// SemanticCacheTTL returns cached response lifetime in minutes.
func (c *TokenSaverConfig) SemanticCacheTTL() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.semanticCacheTTL <= 0 {
		return 1440
	}
	return c.semanticCacheTTL
}

// SemanticCacheMaxEntries returns max entries capacity.
func (c *TokenSaverConfig) SemanticCacheMaxEntries() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.semanticCacheMaxEntries <= 0 {
		return 1000
	}
	return c.semanticCacheMaxEntries
}

// SetSemanticCache toggles the prompt/response cache.
func (c *TokenSaverConfig) SetSemanticCache(v bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.semanticCacheEnabled = v
}

// Snapshot returns all current values.
func (c *TokenSaverConfig) Snapshot() (rtk, caveman, ponytail bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.rtkEnabled, c.cavemanEnabled, c.ponytailEnabled
}

// RTKConfig returns the current RTKConfig snapshot.
func (c *TokenSaverConfig) RTKConfig() tokensaver.RTKConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	cats := make(map[string]bool, len(c.rtkCategories))
	for k, v := range c.rtkCategories {
		cats[k] = v
	}
	filts := make(map[string]bool, len(c.rtkFilters))
	for k, v := range c.rtkFilters {
		filts[k] = v
	}
	return tokensaver.RTKConfig{
		Mode:         c.rtkMode,
		Intensity:    c.rtkIntensity,
		MaxLines:     c.rtkMaxLines,
		MaxChars:     c.rtkMaxChars,
		Deduplicate:  c.rtkDeduplicate,
		Categories:   cats,
		Filters:      filts,
		RawRetention: c.rtkRawRetention,
	}
}

// CavemanAutoClarity reports whether auto-clarity bypass is enabled.
func (c *TokenSaverConfig) CavemanAutoClarity() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cavemanAutoClarity
}

// CavemanLanguage returns the target language for Caveman prompts.
func (c *TokenSaverConfig) CavemanLanguage() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.cavemanLanguage == "" {
		return "en"
	}
	return c.cavemanLanguage
}

// CavemanInputMode reports whether input prompt pre-compression is enabled.
func (c *TokenSaverConfig) CavemanInputMode() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cavemanInputMode
}

// CavemanPreserveKeywords returns custom keywords that should not be stripped.
func (c *TokenSaverConfig) CavemanPreserveKeywords() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cavemanPreserveKeywords
}

// UpdateFromSettings updates TokenSaverConfig fields from a database SettingsData struct.
func (c *TokenSaverConfig) UpdateFromSettings(s *db.SettingsData) {
	if s == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rtkEnabled = s.RTKEnabled
	if s.RTKMode != "" {
		c.rtkMode = s.RTKMode
	}
	if s.RTKIntensity != "" {
		c.rtkIntensity = s.RTKIntensity
	}
	if s.RTKMaxLines > 0 {
		c.rtkMaxLines = s.RTKMaxLines
	}
	if s.RTKMaxChars > 0 {
		c.rtkMaxChars = s.RTKMaxChars
	}
	c.rtkDeduplicate = s.RTKDeduplicate
	if s.RTKCategories != nil {
		c.rtkCategories = make(map[string]bool, len(s.RTKCategories))
		for k, v := range s.RTKCategories {
			c.rtkCategories[k] = v
		}
	}
	if s.RTKFilters != nil {
		c.rtkFilters = make(map[string]bool, len(s.RTKFilters))
		for k, v := range s.RTKFilters {
			c.rtkFilters[k] = v
		}
	}
	if s.RTKRawRetention != "" {
		c.rtkRawRetention = s.RTKRawRetention
	}

	c.cavemanEnabled = s.CavemanEnabled
	if s.CavemanMode != "" {
		c.cavemanMode = s.CavemanMode
	}
	if s.CavemanLevel != "" {
		c.cavemanLevel = s.CavemanLevel
	}
	c.cavemanAutoClarity = s.CavemanAutoClarity
	if s.CavemanLanguage != "" {
		c.cavemanLanguage = s.CavemanLanguage
	}
	c.cavemanInputMode = s.CavemanInputMode
	c.cavemanPreserveKeywords = s.CavemanPreserveKeywords

	c.ponytailEnabled = s.PonytailEnabled
	if s.PonytailLevel != "" {
		c.ponytailLevel = s.PonytailLevel
	}
	c.adhdEnabled = s.ADHDEnabled
	if s.ADHDLevel != "" {
		c.adhdLevel = s.ADHDLevel
	}
	c.semanticCacheEnabled = s.SemanticCacheEnabled
	if s.SemanticCacheTTL > 0 {
		c.semanticCacheTTL = s.SemanticCacheTTL
	}
	if s.SemanticCacheMaxEntries > 0 {
		c.semanticCacheMaxEntries = s.SemanticCacheMaxEntries
	}
}

// SetAll sets all flags atomically.
func (c *TokenSaverConfig) SetAll(rtk, caveman, ponytail bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rtkEnabled = rtk
	c.cavemanEnabled = caveman
	c.ponytailEnabled = ponytail
}
