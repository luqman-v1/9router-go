package db

import (
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/thinking"
)

func applyComboStrategies(s *SettingsData, raw map[string]any) {
	csMap, ok := raw["comboStrategies"].(map[string]any)
	if !ok {
		return
	}
	s.ComboStrategies = make(map[string]ComboStrategy, len(csMap))
	for k, v := range csMap {
		vm, ok := v.(map[string]any)
		if !ok {
			continue
		}
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
		entry := ComboStrategy{
			Strategy:    strat,
			StickyLimit: sticky,
			JudgeModel:  handlerutil.GetString(vm, "judgeModel"),
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
		if ft, ok := vm["firstTokenTimeoutMs"].(float64); ok && ft > 0 {
			entry.FirstTokenTimeoutMs = int(ft)
		} else if ft, ok := vm["firstTokenTimeout"].(float64); ok && ft > 0 {
			entry.FirstTokenTimeoutMs = int(ft)
		}
		s.ComboStrategies[k] = entry
	}
}

// applyProviderStrategies reads the per-provider strategy map.
//
// The two rotations are carried separately: the provider card saves pool
// rotation to `rotateStrategy` and connection rotation to `fallbackStrategy`,
// and folding them into one field made saving either silently arm the other.
// `proxyPoolId` pins a single pool and outranks both.
// `stickyRoundRobinLimit` / `stickyLimit` belong to connection rotation.
func applyProviderStrategies(s *SettingsData, raw map[string]any) {
	ps, ok := raw["providerStrategies"].(map[string]any)
	if !ok {
		return
	}
	s.ProviderStrategies = make(map[string]ProviderStrategy, len(ps))
	for k, v := range ps {
		vm, ok := v.(map[string]any)
		if !ok {
			continue
		}
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

// applyProviderThinking reads the per-provider reasoning overrides.
//
// Only a mode inside the allowed vocabulary is kept: the mode reaches
// reasoning_effort on the wire, and a provider rejects an unknown effort.
func applyProviderThinking(s *SettingsData, raw map[string]any) {
	pt, ok := raw["providerThinking"].(map[string]any)
	if !ok {
		return
	}
	s.ProviderThinking = make(map[string]ProviderThinking, len(pt))
	for k, v := range pt {
		vm, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if mode, valid := thinking.NormalizeProviderThinkingMode(handlerutil.GetString(vm, "mode")); valid {
			s.ProviderThinking[k] = ProviderThinking{Mode: mode}
		}
	}
}

func applyCapacityAdapters(s *SettingsData, raw map[string]any) {
	caRaw, ok := raw["capacityAdapter"].(map[string]any)
	if !ok {
		return
	}
	s.CapacityAdapter = make(map[string]CapacityAdapterEntry, len(caRaw))
	for k, v := range caRaw {
		vm, ok := v.(map[string]any)
		if !ok {
			continue
		}
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
					models = append(models, providers.CanonicalModelName(ms))
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

// applyProviderOverrides reads the per-provider header overrides.
//
// The settings blob can arrive from a backup import, so a wrong shape is
// dropped rather than trusted.
func applyProviderOverrides(s *SettingsData, raw map[string]any) {
	po, ok := raw["providerOverrides"].(map[string]any)
	if !ok {
		return
	}
	s.ProviderOverrides = make(map[string]ProviderOverrides, len(po))
	for k, v := range po {
		entry, ok := v.(map[string]any)
		if !ok {
			continue
		}
		s.ProviderOverrides[k] = ProviderOverrides{Headers: stringMap(entry["headers"])}
	}
}
