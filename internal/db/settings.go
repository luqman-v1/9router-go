package db

import (
	"database/sql"
	"errors"
	"fmt"
)

// ComboStrategy defines routing strategy, sticky limit, and judge model for a combo.
// The Smart* fields configure the "smart" strategy (System One routing, #623).
type ComboStrategy struct {
	Strategy    string `json:"strategy,omitempty"`
	StickyLimit int    `json:"stickyLimit,omitempty"`
	JudgeModel  string `json:"judgeModel,omitempty"`
	// SmartPolicy is "smart" (adaptive switch, default), "strict" (lock the
	// first evaluated model for the session), "per-turn" (no stickiness), or
	// "custom" (SmartCustomThreshold governs switching).
	SmartPolicy string `json:"smartPolicy,omitempty"`
	// SessionSticky nil means true; an explicit false forces per-turn.
	SessionSticky *bool `json:"sessionSticky,omitempty"`
	// ConfidenceThreshold is the 0-1 confidence a System One choice must
	// reach before the session switches away from its pinned model.
	ConfidenceThreshold *float64 `json:"confidenceThreshold,omitempty"`
	// RouterModel overrides the System One model that evaluates the route
	// (default oc/jev-1.13-free).
	RouterModel string `json:"routerModel,omitempty"`
	// RespectStoredOrder skips the rotation strategy and walks the combo's
	// stored model list in order, so the ordering the operator arranged in the
	// editor is the fallback chain the gateway tries. It does NOT skip
	// ReorderByCapabilities: a request that needs vision must still float a
	// capable model to the front, or the chain would offer a text-only model
	// and fail. The "smart" strategy always orders the models itself (System
	// One evaluation plus session stickiness) and keeps the combo's own order
	// on every failure path, so this flag is a no-op there.
	RespectStoredOrder bool `json:"respectStoredOrder,omitempty"`
	// FirstTokenTimeoutMs bounds time-to-first-token for intermediate combo members.
	// If the candidate member takes longer than this to emit its first token, the
	// request falls over to the next combo member. The last candidate member always
	// keeps the normal deadline.
	FirstTokenTimeoutMs int `json:"firstTokenTimeoutMs,omitempty"`
}

// ProviderStrategy defines routing and proxy pool options for a specific
// provider.
//
// The dashboard saves two independent rotations to this provider. The
// `isNoAuth` block of the provider card writes pool rotation to
// `rotateStrategy`, and the round-robin toggle writes connection rotation to
// `fallbackStrategy`. GetSettings used to fold both into RotateStrategy, which
// made saving one silently arm the other.
//
// ProxyRotateStrategy and ConnRotateStrategy now keep them apart.
// RotateStrategy keeps reading `rotateStrategy` first and falling back to
// `fallbackStrategy`, so a provider that sets only one still gets a strategy —
// and that fallback is why pool rotation must stay behind a NoAuth gate: for a
// keyed provider `rotateStrategy` is an account-rotation value and must never
// steer egress. See connRotationStrategy in the chat package.
type ProviderStrategy struct {
	ProxyPoolID           string `json:"proxyPoolId,omitempty"`
	RotateStrategy        string `json:"rotateStrategy,omitempty"` // "none", "round-robin", "random", "sticky"
	StickyLimit           int    `json:"stickyLimit,omitempty"`
	StrictModelAssignment bool   `json:"strictModelAssignment,omitempty"`
	ProxyRotateStrategy   string `json:"proxyRotateStrategy,omitempty"`
	ConnRotateStrategy    string `json:"connRotateStrategy,omitempty"`
}

// CapacityAdapterEntry defines settings for an input-modality capability adapter pool.
type CapacityAdapterEntry struct {
	Enabled    bool     `json:"enabled"`
	RoundRobin bool     `json:"roundRobin"`
	Models     []string `json:"models"`
}

// SettingsData represents token saver, combo routing, and general settings stored in the settings table.
type SettingsData struct {
	RTKEnabled              bool            `json:"rtkEnabled"`
	RTKMode                 string          `json:"rtkMode,omitempty"`
	RTKIntensity            string          `json:"rtkIntensity,omitempty"`
	RTKMaxLines             int             `json:"rtkMaxLines,omitempty"`
	RTKMaxChars             int             `json:"rtkMaxChars,omitempty"`
	RTKDeduplicate          bool            `json:"rtkDeduplicate,omitempty"`
	RTKCategories           map[string]bool `json:"rtkCategories,omitempty"`
	RTKFilters              map[string]bool `json:"rtkFilters,omitempty"`
	RTKRawRetention         string          `json:"rtkRawRetention,omitempty"`
	CavemanEnabled          bool            `json:"cavemanEnabled"`
	CavemanMode             string          `json:"cavemanMode,omitempty"`
	CavemanLevel            string          `json:"cavemanLevel"`
	CavemanAutoClarity      bool            `json:"cavemanAutoClarity,omitempty"`
	CavemanLanguage         string          `json:"cavemanLanguage,omitempty"`
	CavemanInputMode        bool            `json:"cavemanInputMode,omitempty"`
	CavemanPreserveKeywords string          `json:"cavemanPreserveKeywords,omitempty"`
	PonytailEnabled         bool            `json:"ponytailEnabled"`
	PonytailLevel           string          `json:"ponytailLevel"`
	ADHDEnabled             bool            `json:"adhdEnabled"`
	ADHDLevel               string          `json:"adhdLevel"`
	SystemPromptEnabled     bool            `json:"systemPromptEnabled"`
	SystemPromptPrefix      string          `json:"systemPromptPrefix,omitempty"`
	SystemPromptSuffix      string          `json:"systemPromptSuffix,omitempty"`
	HeadroomUrl             string          `json:"headroomUrl"`
	// HeadroomEnabled is the master switch for in-flight compression. It is
	// separate from HeadroomUrl because the URL has a default the panel fills
	// in, so "a URL exists" cannot stand in for "the operator turned this on".
	// Off by default: an unconfigured proxy must not be a slow path on every
	// request.
	HeadroomEnabled            bool                            `json:"headroomEnabled"`
	HeadroomCodeAware          bool                            `json:"headroomCodeAware"`
	HeadroomKompress           bool                            `json:"headroomKompress"`
	HeadroomTimeoutMs          int                             `json:"headroomTimeoutMs"`
	SemanticCacheEnabled       bool                            `json:"semanticCacheEnabled"`
	SemanticCacheTTL           int                             `json:"semanticCacheTTL,omitempty"`
	SemanticCacheMaxEntries    int                             `json:"semanticCacheMaxEntries,omitempty"`
	AutoUpdate                 bool                            `json:"autoUpdate"`
	FallbackStrategy           string                          `json:"fallbackStrategy,omitempty"`
	StickyRoundRobinLimit      int                             `json:"stickyRoundRobinLimit,omitempty"`
	ForceFallback              bool                            `json:"forceFallback,omitempty"`
	ComboStrategy              string                          `json:"comboStrategy,omitempty"`
	ComboStickyRoundRobinLimit int                             `json:"comboStickyRoundRobinLimit,omitempty"`
	ComboStrategies            map[string]ComboStrategy        `json:"comboStrategies,omitempty"`
	ProviderStrategies         map[string]ProviderStrategy     `json:"providerStrategies,omitempty"`
	CapacityAdapter            map[string]CapacityAdapterEntry `json:"capacityAdapter,omitempty"`
	ProviderOverrides          map[string]ProviderOverrides    `json:"providerOverrides,omitempty"`
	EnableObservability        bool                            `json:"enableObservability"`
	ObservabilityMode          string                          `json:"observabilityMode,omitempty"`
	RetentionDays              int                             `json:"retentionDays,omitempty"`
	MaxRequestRecords          int                             `json:"maxRequestRecords,omitempty"`
	// UsageHistoryRetentionDays bounds how far back the usage ledger is kept.
	// It is a separate knob from RetentionDays on purpose: that one governs
	// requestDetails, which is diagnostics nothing bills from, while this one
	// deletes billing rows. Folding them into one control would mean an operator
	// trimming a diagnostic log silently shortened their usage history — and
	// the two are wanted at different lengths, since a month of request bodies
	// is a lot of disk and a month of per-request counters is not.
	//
	// 0 means "keep forever", which is the default: the ledger is only pruned
	// when an operator asks for it. See usage_retention_ledger.go.
	UsageHistoryRetentionDays int                         `json:"usageHistoryRetentionDays,omitempty"`
	ProviderThinking          map[string]ProviderThinking `json:"providerThinking,omitempty"`
}

// ProviderThinking is one provider's operator reasoning override, mirroring
// upstream chatSettings.providerThinking (chatCore.js:117-131).
type ProviderThinking struct {
	Mode string `json:"mode"` // "on" | "off" | "auto" | a thinking level
}

// DefaultSettings returns fallback settings.
func DefaultSettings() *SettingsData {
	return &SettingsData{
		RTKEnabled:     true,
		RTKMode:        "simple",
		RTKIntensity:   "standard",
		RTKMaxLines:    250,
		RTKMaxChars:    10 * 1024 * 1024,
		RTKDeduplicate: true,
		RTKCategories: map[string]bool{
			"git":     true,
			"build":   true,
			"test":    true,
			"package": true,
			"docker":  true,
			"system":  true,
		},
		RTKRawRetention:         "never",
		CavemanEnabled:          false,
		CavemanMode:             "simple",
		CavemanLevel:            "full",
		CavemanAutoClarity:      true,
		CavemanLanguage:         "en",
		CavemanInputMode:        false,
		CavemanPreserveKeywords: "",
		PonytailEnabled:         false,
		PonytailLevel:           "full",
		ADHDEnabled:             false,
		ADHDLevel:               "full",
		SystemPromptEnabled:     false,
		SystemPromptPrefix:      "",
		SystemPromptSuffix:      "",
		HeadroomUrl:             "http://localhost:8787",
		HeadroomKompress:        true,
		HeadroomTimeoutMs:       3000,
		SemanticCacheEnabled:    false,
		SemanticCacheTTL:        1440,
		SemanticCacheMaxEntries: 1000,
		AutoUpdate:              false,
		CapacityAdapter: map[string]CapacityAdapterEntry{
			"vision":     {Enabled: true, RoundRobin: false, Models: []string{}},
			"pdf":        {Enabled: false, RoundRobin: false, Models: []string{}},
			"audioInput": {Enabled: true, RoundRobin: false, Models: []string{}},
			"videoInput": {Enabled: false, RoundRobin: false, Models: []string{}},
		},
	}
}

// GetSettings reads settings row id = 1 from SQLite settings table.
//
// The row is always read, so the answer is never stale. The parsed value is
// reused when the row content is unchanged: the thinking pass reads the
// providerThinking override on every request, and parsing the blob plus
// rebuilding the maps is about half of the read (measured 94.5 µs and 53
// allocations for the whole read, of which the point query is 47 µs).
//
// A read that fails is an error, not a row of defaults: a transient database
// failure would otherwise silently turn the operator's settings off for the
// length of the failure — the prompt compression back on, the reasoning
// override gone — and nothing would say so. A row that does not exist yet is
// not a failure: that is a fresh install.
func (r *Repo) GetSettings() (*SettingsData, error) {
	var rawData string
	err := r.db.QueryRow(`SELECT data FROM settings WHERE id = 1`).Scan(&rawData)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return DefaultSettings(), nil
		}
		return nil, fmt.Errorf("get settings: %w", err)
	}
	if c := r.settingsCache.Load(); c != nil && c.raw == rawData {
		return cloneSettings(c.value), nil
	}
	s, ok := parseSettings(rawData)
	if !ok {
		return nil, errors.New("parse settings: the stored settings row is not readable")
	}
	r.settingsCache.Store(&cachedSettings{raw: rawData, value: s})
	return cloneSettings(s), nil
}

// cloneSettings copies the settings struct so a caller that replaces a field
// cannot poison the cached value. The maps are copied too: the cache holds
// the one parsed row, and a caller that writes into a map it was handed would
// otherwise change the answer every other reader sees.
func cloneSettings(s *SettingsData) *SettingsData {
	if s == nil {
		return nil
	}
	out := *s
	out.RTKCategories = cloneBoolMap(s.RTKCategories)
	out.RTKFilters = cloneBoolMap(s.RTKFilters)
	out.ComboStrategies = cloneComboStrategies(s.ComboStrategies)
	out.ProviderStrategies = cloneProviderStrategies(s.ProviderStrategies)
	out.CapacityAdapter = cloneCapacityAdapters(s.CapacityAdapter)
	out.ProviderOverrides = cloneProviderOverrides(s.ProviderOverrides)
	out.ProviderThinking = cloneProviderThinking(s.ProviderThinking)
	return &out
}

func cloneBoolMap(m map[string]bool) map[string]bool {
	if m == nil {
		return nil
	}
	out := make(map[string]bool, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func cloneComboStrategies(m map[string]ComboStrategy) map[string]ComboStrategy {
	if m == nil {
		return nil
	}
	out := make(map[string]ComboStrategy, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func cloneProviderStrategies(m map[string]ProviderStrategy) map[string]ProviderStrategy {
	if m == nil {
		return nil
	}
	out := make(map[string]ProviderStrategy, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func cloneCapacityAdapters(m map[string]CapacityAdapterEntry) map[string]CapacityAdapterEntry {
	if m == nil {
		return nil
	}
	out := make(map[string]CapacityAdapterEntry, len(m))
	for k, v := range m {
		v.Models = append([]string(nil), v.Models...)
		out[k] = v
	}
	return out
}

func cloneProviderOverrides(m map[string]ProviderOverrides) map[string]ProviderOverrides {
	if m == nil {
		return nil
	}
	out := make(map[string]ProviderOverrides, len(m))
	for k, v := range m {
		v.Headers = cloneStringMap(v.Headers)
		out[k] = v
	}
	return out
}

func cloneStringMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func cloneProviderThinking(m map[string]ProviderThinking) map[string]ProviderThinking {
	if m == nil {
		return nil
	}
	out := make(map[string]ProviderThinking, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// settingsInt coerces a settings value to a non-negative int, treating a
// missing or wrongly-typed entry as unset. The blob is hand-editable through
// the backup import, so a string where a number belongs is a real input.
func settingsInt(v any) int {
	switch t := v.(type) {
	case float64:
		if t > 0 {
			return int(t)
		}
	case int:
		if t > 0 {
			return t
		}
	}
	return 0
}

// stringMap coerces a decoded JSON value into map[string]string, skipping
// anything that is not a string. The settings blob is hand-editable through
// the backup import, so a wrong type there is a real input, not a bug to
// panic on.
func stringMap(v any) map[string]string {
	raw, ok := v.(map[string]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, val := range raw {
		s, ok := val.(string)
		if !ok {
			continue
		}
		out[k] = s
	}
	return out
}
