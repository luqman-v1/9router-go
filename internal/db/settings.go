package db

import (
	json "encoding/json/v2"

	"9router/proxy/internal/handlerutil"
)

// ComboStrategy defines routing strategy, sticky limit, and judge model for a combo.
type ComboStrategy struct {
	Strategy    string `json:"strategy,omitempty"`
	StickyLimit int    `json:"stickyLimit,omitempty"`
	JudgeModel  string `json:"judgeModel,omitempty"`
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
	RTKEnabled                 bool                            `json:"rtkEnabled"`
	RTKMode                    string                          `json:"rtkMode,omitempty"`
	RTKIntensity               string                          `json:"rtkIntensity,omitempty"`
	RTKMaxLines                int                             `json:"rtkMaxLines,omitempty"`
	RTKMaxChars                int                             `json:"rtkMaxChars,omitempty"`
	RTKDeduplicate             bool                            `json:"rtkDeduplicate,omitempty"`
	RTKCategories              map[string]bool                 `json:"rtkCategories,omitempty"`
	RTKFilters                 map[string]bool                 `json:"rtkFilters,omitempty"`
	RTKRawRetention            string                          `json:"rtkRawRetention,omitempty"`
	CavemanEnabled             bool                            `json:"cavemanEnabled"`
	CavemanMode                string                          `json:"cavemanMode,omitempty"`
	CavemanLevel               string                          `json:"cavemanLevel"`
	CavemanAutoClarity         bool                            `json:"cavemanAutoClarity,omitempty"`
	CavemanLanguage            string                          `json:"cavemanLanguage,omitempty"`
	CavemanInputMode           bool                            `json:"cavemanInputMode,omitempty"`
	CavemanPreserveKeywords    string                          `json:"cavemanPreserveKeywords,omitempty"`
	PonytailEnabled            bool                            `json:"ponytailEnabled"`
	PonytailLevel              string                          `json:"ponytailLevel"`
	ADHDEnabled                bool                            `json:"adhdEnabled"`
	ADHDLevel                  string                          `json:"adhdLevel"`
	HeadroomUrl                string                          `json:"headroomUrl"`
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
}

// DefaultSettings returns fallback settings.
func DefaultSettings() *SettingsData {
	return &SettingsData{
		RTKEnabled:        true,
		RTKMode:           "simple",
		RTKIntensity:      "standard",
		RTKMaxLines:       100,
		RTKMaxChars:       8000,
		RTKDeduplicate:    true,
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
		HeadroomUrl:       "http://localhost:8787",
		HeadroomKompress:  true,
		HeadroomTimeoutMs: 3000,
		SemanticCacheEnabled:     false,
		SemanticCacheTTL:         1440,
		SemanticCacheMaxEntries:  1000,
		AutoUpdate:        false,
		CapacityAdapter: map[string]CapacityAdapterEntry{
			"vision":     {Enabled: true, RoundRobin: false, Models: []string{}},
			"pdf":        {Enabled: false, RoundRobin: false, Models: []string{}},
			"audioInput": {Enabled: true, RoundRobin: false, Models: []string{}},
			"videoInput": {Enabled: false, RoundRobin: false, Models: []string{}},
		},
	}
}

// GetSettings reads settings row id = 1 from SQLite settings table.
func (r *Repo) GetSettings() (*SettingsData, error) {
	var rawData string
	err := r.db.QueryRow(`SELECT data FROM settings WHERE id = 1`).Scan(&rawData)
	if err != nil {
		return DefaultSettings(), nil
	}

	var raw map[string]any
	if err := json.Unmarshal([]byte(rawData), &raw); err != nil {
		return DefaultSettings(), nil
	}

	s := DefaultSettings()
	if v, ok := raw["rtkEnabled"].(bool); ok {
		s.RTKEnabled = v
	}
	if v := handlerutil.GetString(raw, "rtkMode"); v != "" {
		s.RTKMode = v
	}
	if v := handlerutil.GetString(raw, "rtkIntensity"); v != "" {
		s.RTKIntensity = v
	}
	if v, ok := raw["rtkMaxLines"].(float64); ok && v > 0 {
		s.RTKMaxLines = int(v)
	}
	if v, ok := raw["rtkMaxChars"].(float64); ok && v > 0 {
		s.RTKMaxChars = int(v)
	}
	if v, ok := raw["rtkDeduplicate"].(bool); ok {
		s.RTKDeduplicate = v
	}
	if cats, ok := raw["rtkCategories"].(map[string]any); ok {
		s.RTKCategories = make(map[string]bool, len(cats))
		for k, cv := range cats {
			if b, ok := cv.(bool); ok {
				s.RTKCategories[k] = b
			}
		}
	}
	if filts, ok := raw["rtkFilters"].(map[string]any); ok {
		s.RTKFilters = make(map[string]bool, len(filts))
		for k, fv := range filts {
			if b, ok := fv.(bool); ok {
				s.RTKFilters[k] = b
			}
		}
	}
	if v := handlerutil.GetString(raw, "rtkRawRetention"); v != "" {
		s.RTKRawRetention = v
	}
	if v, ok := raw["cavemanEnabled"].(bool); ok {
		s.CavemanEnabled = v
	}
	if v := handlerutil.GetString(raw, "cavemanMode"); v != "" {
		s.CavemanMode = v
	}
	if lvl := handlerutil.GetString(raw, "cavemanLevel"); lvl != "" {
		s.CavemanLevel = lvl
	}
	if v, ok := raw["cavemanAutoClarity"].(bool); ok {
		s.CavemanAutoClarity = v
	}
	if v := handlerutil.GetString(raw, "cavemanLanguage"); v != "" {
		s.CavemanLanguage = v
	}
	if v, ok := raw["cavemanInputMode"].(bool); ok {
		s.CavemanInputMode = v
	}
	if v := handlerutil.GetString(raw, "cavemanPreserveKeywords"); v != "" {
		s.CavemanPreserveKeywords = v
	}
	if v, ok := raw["ponytailEnabled"].(bool); ok {
		s.PonytailEnabled = v
	}
	if lvl := handlerutil.GetString(raw, "ponytailLevel"); lvl != "" {
		s.PonytailLevel = lvl
	}
	if v, ok := raw["adhdEnabled"].(bool); ok {
		s.ADHDEnabled = v
	}
	if lvl := handlerutil.GetString(raw, "adhdLevel"); lvl != "" {
		s.ADHDLevel = lvl
	}
	if v := handlerutil.GetString(raw, "headroomUrl"); v != "" {
		s.HeadroomUrl = v
	}
	if v, ok := raw["headroomCodeAware"].(bool); ok {
		s.HeadroomCodeAware = v
	}
	if v, ok := raw["headroomKompress"].(bool); ok {
		s.HeadroomKompress = v
	}
	if v, ok := raw["headroomTimeoutMs"].(float64); ok && v > 0 {
		s.HeadroomTimeoutMs = int(v)
	}
	if v, ok := raw["semanticCacheEnabled"].(bool); ok {
		s.SemanticCacheEnabled = v
	}
	if v, ok := raw["semanticCacheTTL"].(float64); ok && v > 0 {
		s.SemanticCacheTTL = int(v)
	}
	if v, ok := raw["semanticCacheMaxEntries"].(float64); ok && v > 0 {
		s.SemanticCacheMaxEntries = int(v)
	}
	if v, ok := raw["autoUpdate"].(bool); ok {
		s.AutoUpdate = v
	}

	// Global provider fallback strategy
	if fs := handlerutil.GetString(raw, "fallbackStrategy"); fs != "" {
		s.FallbackStrategy = fs
	}
	if v, ok := raw["stickyRoundRobinLimit"].(float64); ok && v > 0 {
		s.StickyRoundRobinLimit = int(v)
	}
	if v, ok := raw["forceFallback"].(bool); ok {
		s.ForceFallback = v
	}

	// Global combo strategy
	if cs := handlerutil.GetString(raw, "comboStrategy"); cs != "" {
		s.ComboStrategy = cs
	}
	if v, ok := raw["comboStickyRoundRobinLimit"].(float64); ok && v > 0 {
		s.ComboStickyRoundRobinLimit = int(v)
	}

	// Per-combo strategies (Next.js & Svelte dashboards write `fallbackStrategy` / `strategy`, `stickyLimit` / `stickyRoundRobinLimit`, `judgeModel`)
	if csMap, ok := raw["comboStrategies"].(map[string]any); ok {
		s.ComboStrategies = make(map[string]ComboStrategy, len(csMap))
		for k, v := range csMap {
			if vm, ok := v.(map[string]any); ok {
				strat := handlerutil.GetString(vm, "strategy")
				if strat == "" {
					strat = handlerutil.GetString(vm, "fallbackStrategy")
				}
				sticky := 0
				if sl, ok := vm["stickyLimit"].(float64); ok && sl > 0 {
					sticky = int(sl)
				} else if sl, ok := vm["stickyRoundRobinLimit"].(float64); ok && sl > 0 {
					sticky = int(sl)
				}
				judge := handlerutil.GetString(vm, "judgeModel")
				s.ComboStrategies[k] = ComboStrategy{
					Strategy:    strat,
					StickyLimit: sticky,
					JudgeModel:  judge,
				}
			}
		}
	}

	// Per-provider strategies. See ProviderStrategy for why the two rotations
	// are carried separately: the provider card saves pool rotation to
	// `rotateStrategy` and connection rotation to `fallbackStrategy`, and
	// folding them into one field made saving either silently arm the other.
	// `proxyPoolId` pins a single pool and outranks both.
	// `stickyRoundRobinLimit` / `stickyLimit` belong to connection rotation.
	if ps, ok := raw["providerStrategies"].(map[string]any); ok {
		s.ProviderStrategies = make(map[string]ProviderStrategy, len(ps))
		for k, v := range ps {
			if vm, ok := v.(map[string]any); ok {
				rotateStrat := handlerutil.GetString(vm, "rotateStrategy")
				if rotateStrat == "" {
					rotateStrat = handlerutil.GetString(vm, "fallbackStrategy")
				}
				sticky := 0
				if sl, ok := vm["stickyLimit"].(float64); ok && sl > 0 {
					sticky = int(sl)
				} else if sl, ok := vm["stickyRoundRobinLimit"].(float64); ok && sl > 0 {
					sticky = int(sl)
				}
				strat := ProviderStrategy{
					ProxyPoolID:         handlerutil.GetString(vm, "proxyPoolId"),
					RotateStrategy:      rotateStrat,
					StickyLimit:         sticky,
					ProxyRotateStrategy: handlerutil.GetString(vm, "rotateStrategy"),
					ConnRotateStrategy:  handlerutil.GetString(vm, "fallbackStrategy"),
				}
				if sma, ok := vm["strictModelAssignment"].(bool); ok {
					strat.StrictModelAssignment = sma
				}
				s.ProviderStrategies[k] = strat
			}
		}
	}

	// Capacity adapter pools (vision, audioInput, etc.)
	if caRaw, ok := raw["capacityAdapter"].(map[string]any); ok {
		s.CapacityAdapter = make(map[string]CapacityAdapterEntry, len(caRaw))
		for k, v := range caRaw {
			if vm, ok := v.(map[string]any); ok {
				enabled := true
				if en, ok := vm["enabled"].(bool); ok {
					enabled = en
				}
				rr := false
				if r, ok := vm["roundRobin"].(bool); ok {
					rr = r
				}
				var models []string
				if rawModels, ok := vm["models"].([]any); ok {
					for _, rm := range rawModels {
						if ms, ok := rm.(string); ok && ms != "" {
							if ms == "oc/mimo-v2.5-free" {
								ms = "oc/mimo-v2.6-flash-free"
							}
							models = append(models, ms)
						}
					}
				}
				s.CapacityAdapter[k] = CapacityAdapterEntry{
					Enabled:    enabled,
					RoundRobin: rr,
					Models:     models,
				}
			}
		}
	}

	// Per-provider header overrides. Parsed like the other maps: the settings
	// blob can arrive from a backup import, so a wrong shape is dropped rather
	// than trusted.
	if po, ok := raw["providerOverrides"].(map[string]any); ok {
		s.ProviderOverrides = make(map[string]ProviderOverrides, len(po))
		for k, v := range po {
			entry, ok := v.(map[string]any)
			if !ok {
				continue
			}
			s.ProviderOverrides[k] = ProviderOverrides{Headers: stringMap(entry["headers"])}
		}
	}

	return s, nil
}

// SetAutoUpdate updates the autoUpdate flag in the settings table.
func (r *Repo) SetAutoUpdate(enabled bool) error {
	return r.UpdateSettingsRaw(map[string]any{
		"autoUpdate": enabled,
	})
}

// SetProviderStrategy updates or sets the routing strategy and proxy pool for a provider without clobbering other settings.
func (r *Repo) SetProviderStrategy(provider string, strat ProviderStrategy) error {
	raw, err := r.GetSettingsRaw()
	if err != nil || raw == nil {
		raw = make(map[string]any)
	}

	var currentMap map[string]any
	if ps, ok := raw["providerStrategies"].(map[string]any); ok {
		currentMap = ps
	} else {
		currentMap = make(map[string]any)
	}

	entry := map[string]any{}
	if existingEntry, ok := currentMap[provider].(map[string]any); ok {
		for ek, ev := range existingEntry {
			entry[ek] = ev
		}
	}

	if strat.ProxyPoolID != "" {
		entry["proxyPoolId"] = strat.ProxyPoolID
	}
	if strat.RotateStrategy != "" {
		entry["rotateStrategy"] = strat.RotateStrategy
		entry["fallbackStrategy"] = strat.RotateStrategy
	}
	if strat.StickyLimit > 0 {
		entry["stickyLimit"] = strat.StickyLimit
		entry["stickyRoundRobinLimit"] = strat.StickyLimit
	}
	entry["strictModelAssignment"] = strat.StrictModelAssignment

	currentMap[provider] = entry
	return r.UpdateSettingsRaw(map[string]any{
		"providerStrategies": currentMap,
	})
}

// SetComboStrategy updates or sets the routing strategy for a combo without clobbering other settings.
func (r *Repo) SetComboStrategy(comboName string, strat ComboStrategy) error {
	raw, err := r.GetSettingsRaw()
	if err != nil || raw == nil {
		raw = make(map[string]any)
	}

	var currentMap map[string]any
	if cs, ok := raw["comboStrategies"].(map[string]any); ok {
		currentMap = cs
	} else {
		currentMap = make(map[string]any)
	}

	entry := map[string]any{}
	if existingEntry, ok := currentMap[comboName].(map[string]any); ok {
		for ek, ev := range existingEntry {
			entry[ek] = ev
		}
	}

	if strat.Strategy != "" {
		entry["strategy"] = strat.Strategy
		entry["fallbackStrategy"] = strat.Strategy
	}
	if strat.StickyLimit > 0 {
		entry["stickyLimit"] = strat.StickyLimit
		entry["stickyRoundRobinLimit"] = strat.StickyLimit
	}
	if strat.JudgeModel != "" {
		entry["judgeModel"] = strat.JudgeModel
	}

	if strat.Strategy == "fallback" && strat.JudgeModel == "" {
		delete(currentMap, comboName)
	} else {
		currentMap[comboName] = entry
	}

	return r.UpdateSettingsRaw(map[string]any{
		"comboStrategies": currentMap,
	})
}

// GetProviderOverride returns the stored override for one provider, or nil
// when it has none. It reads the settings blob directly rather than through
// GetSettings because the request path needs one key, not the whole struct.
func (r *Repo) GetProviderOverride(provider string) (*ProviderOverrides, error) {
	raw, err := r.GetSettingsRaw()
	if err != nil {
		return nil, err
	}
	all, ok := raw["providerOverrides"].(map[string]any)
	if !ok {
		return nil, nil
	}
	entry, ok := all[provider].(map[string]any)
	if !ok {
		return nil, nil
	}
	return &ProviderOverrides{Headers: stringMap(entry["headers"])}, nil
}

// SetProviderOverride stores or clears one provider's override. A nil override
// deletes the entry, which is how an empty PUT clears it.
func (r *Repo) SetProviderOverride(provider string, override *ProviderOverrides) error {
	raw, err := r.GetSettingsRaw()
	if err != nil || raw == nil {
		raw = make(map[string]any)
	}
	all, ok := raw["providerOverrides"].(map[string]any)
	if !ok {
		all = make(map[string]any)
	}
	if override == nil || len(override.Headers) == 0 {
		delete(all, provider)
	} else {
		all[provider] = map[string]any{"headers": override.Headers}
	}
	if len(all) == 0 {
		return r.UpdateSettingsRaw(map[string]any{"providerOverrides": nil})
	}
	return r.UpdateSettingsRaw(map[string]any{"providerOverrides": all})
}

// GetGuardrailsEnabled reports whether the guardrail taps are active at all.
//
// It reads the settings blob directly rather than through GetSettings because
// it is consulted on the request path, twice per request, and needs one flag
// rather than the whole struct.
//
// The default is true: a policy row is an explicit decision by the operator,
// and silently ignoring it because a setting was never written would be the
// more dangerous reading.
func (r *Repo) GetGuardrailsEnabled() bool {
	raw, err := r.GetSettingsRaw()
	if err != nil || raw == nil {
		return true
	}
	switch v := raw["guardrailsEnabled"].(type) {
	case bool:
		return v
	case string:
		// The blob is hand-editable through the backup import, so a quoted
		// "false" is a real input rather than a bug to panic on.
		return v != "false" && v != "0"
	default:
		return true
	}
}

// SetGuardrailsEnabled stores the global guardrail kill-switch.
func (r *Repo) SetGuardrailsEnabled(enabled bool) error {
	return r.UpdateSettingsRaw(map[string]any{"guardrailsEnabled": enabled})
}

// RateLimitDefaults is the operator's fallback for keys that set no limit of
// their own.
//
// The defaults apply only where a key's column is 0, so configuring a global
// default never silently caps a key that was deliberately given a higher or
// unlimited budget — and never lifts one that was capped. Resolution order is
// key column, then plan (not ported yet), then this.
type RateLimitDefaults struct {
	// Enabled is the master switch. False leaves every key enforcing only what
	// its own columns say.
	Enabled     bool
	RPM         int
	TPM         int
	Concurrency int
	// WindowSeconds is the RPM/TPM window. Zero means the 60s default.
	WindowSeconds int
}

// DefaultRateLimitSeconds is the sliding window used when the operator has not
// chosen one. It matches the in-memory limiter's own default.
const DefaultRateLimitSeconds = 60

// GetRateLimitDefaults reads the global fallback from the settings blob.
//
// It is read on the request path, so it goes straight to the raw blob rather
// than through GetSettings: a failed read must not stop a key's own limits from
// being enforced, so every field falls back to the zero value rather than
// erroring.
func (r *Repo) GetRateLimitDefaults() RateLimitDefaults {
	out := RateLimitDefaults{WindowSeconds: DefaultRateLimitSeconds}
	raw, err := r.GetSettingsRaw()
	if err != nil || raw == nil {
		return out
	}
	if v, ok := raw["rateLimitEnabled"].(bool); ok {
		out.Enabled = v
	}
	out.RPM = settingsInt(raw["defaultRpm"])
	out.TPM = settingsInt(raw["defaultTpm"])
	out.Concurrency = settingsInt(raw["defaultConcurrency"])
	if w := settingsInt(raw["rateWindowSeconds"]); w > 0 {
		out.WindowSeconds = w
	}
	return out
}

// SetRateLimitDefaults stores the global fallback.
func (r *Repo) SetRateLimitDefaults(d RateLimitDefaults) error {
	return r.UpdateSettingsRaw(map[string]any{
		"rateLimitEnabled":   d.Enabled,
		"defaultRpm":         d.RPM,
		"defaultTpm":         d.TPM,
		"defaultConcurrency": d.Concurrency,
		"rateWindowSeconds":  d.WindowSeconds,
	})
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
