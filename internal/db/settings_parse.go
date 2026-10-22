package db

import (
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/thinking"
	json "encoding/json/v2"
)

// parseSettings parses the settings row. The bool is false when the blob is
// not readable.
func parseSettings(rawData string) (*SettingsData, bool) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(rawData), &raw); err != nil {
		return nil, false
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
	if v, ok := raw["systemPromptEnabled"].(bool); ok {
		s.SystemPromptEnabled = v
	}
	if v := handlerutil.GetString(raw, "systemPromptPrefix"); v != "" {
		s.SystemPromptPrefix = v
	}
	if v := handlerutil.GetString(raw, "systemPromptSuffix"); v != "" {
		s.SystemPromptSuffix = v
	}
	if v := handlerutil.GetString(raw, "headroomUrl"); v != "" {
		s.HeadroomUrl = v
	}
	if v, ok := raw["headroomEnabled"].(bool); ok {
		s.HeadroomEnabled = v
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

	// Observability & Storage retention settings
	if v, ok := raw["enableObservability"].(bool); ok {
		s.EnableObservability = v
	}
	if v := handlerutil.GetString(raw, "observabilityMode"); v != "" {
		s.ObservabilityMode = v
	}
	if v := settingsInt(raw["retentionDays"]); v >= 0 && raw["retentionDays"] != nil {
		s.RetentionDays = v
	}
	if v := settingsInt(raw["maxRequestRecords"]); v > 0 {
		s.MaxRequestRecords = v
	}

	// The ledger window. Explicit 0 means "keep forever" and is the default, so
	// the value is only taken when the key is actually present — a missing key
	// must not read as an instruction to prune at zero days.
	if _, present := raw["usageHistoryRetentionDays"]; present {
		s.UsageHistoryRetentionDays = settingsInt(raw["usageHistoryRetentionDays"])
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
				entry := ComboStrategy{
					Strategy:    strat,
					StickyLimit: sticky,
					JudgeModel:  judge,
				}
				entry.SmartPolicy = handlerutil.GetString(vm, "smartPolicy")
				if ss, ok := vm["sessionSticky"].(bool); ok {
					entry.SessionSticky = &ss
				}
				if ct, ok := vm["confidenceThreshold"].(float64); ok && ct > 0 {
					entry.ConfidenceThreshold = &ct
				}
				entry.RouterModel = handlerutil.GetString(vm, "routerModel")
				// An absent flag reads as false, which is the behaviour every
				// combo had before the flag existed: rotation stays on unless
				// the operator turned it off for this combo.
				if rso, ok := vm["respectStoredOrder"].(bool); ok {
					entry.RespectStoredOrder = rso
				}
				s.ComboStrategies[k] = entry
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
	// Per-provider reasoning overrides (dashboard provider card). Only a mode
	// inside the allowed vocabulary is kept: the mode reaches reasoning_effort
	// on the wire, and a provider rejects an unknown effort.
	if pt, ok := raw["providerThinking"].(map[string]any); ok {
		s.ProviderThinking = make(map[string]ProviderThinking, len(pt))
		for k, v := range pt {
			if vm, ok := v.(map[string]any); ok {
				if mode, valid := thinking.NormalizeProviderThinkingMode(handlerutil.GetString(vm, "mode")); valid {
					s.ProviderThinking[k] = ProviderThinking{Mode: mode}
				}
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

	return s, true
}
