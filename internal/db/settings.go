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

// ProviderStrategy defines routing and proxy pool options for a specific provider.
type ProviderStrategy struct {
	ProxyPoolID           string `json:"proxyPoolId,omitempty"`
	RotateStrategy        string `json:"rotateStrategy,omitempty"` // "none", "round-robin", "random", "sticky"
	StickyLimit           int    `json:"stickyLimit,omitempty"`
	StrictModelAssignment bool   `json:"strictModelAssignment,omitempty"`
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
	CavemanEnabled             bool                            `json:"cavemanEnabled"`
	CavemanLevel               string                          `json:"cavemanLevel"`
	PonytailEnabled            bool                            `json:"ponytailEnabled"`
	PonytailLevel              string                          `json:"ponytailLevel"`
	HeadroomUrl                string                          `json:"headroomUrl"`
	HeadroomCodeAware          bool                            `json:"headroomCodeAware"`
	HeadroomKompress           bool                            `json:"headroomKompress"`
	HeadroomTimeoutMs          int                             `json:"headroomTimeoutMs"`
	AutoUpdate                 bool                            `json:"autoUpdate"`
	FallbackStrategy           string                          `json:"fallbackStrategy,omitempty"`
	StickyRoundRobinLimit      int                             `json:"stickyRoundRobinLimit,omitempty"`
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
		CavemanEnabled:    false,
		CavemanLevel:      "full",
		PonytailEnabled:   false,
		PonytailLevel:     "full",
		HeadroomUrl:       "http://localhost:8787",
		HeadroomKompress:  true,
		HeadroomTimeoutMs: 3000,
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
	if v, ok := raw["cavemanEnabled"].(bool); ok {
		s.CavemanEnabled = v
	}
	if lvl := handlerutil.GetString(raw, "cavemanLevel"); lvl != "" {
		s.CavemanLevel = lvl
	}
	if v, ok := raw["ponytailEnabled"].(bool); ok {
		s.PonytailEnabled = v
	}
	if lvl := handlerutil.GetString(raw, "ponytailLevel"); lvl != "" {
		s.PonytailLevel = lvl
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

	// Per-provider strategies (Dashboards write `fallbackStrategy` / `rotateStrategy`, `stickyRoundRobinLimit` / `stickyLimit`)
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
					ProxyPoolID:    handlerutil.GetString(vm, "proxyPoolId"),
					RotateStrategy: rotateStrat,
					StickyLimit:    sticky,
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
