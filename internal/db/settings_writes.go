package db

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"sync"
)

// settingsWriteMu serialises the settings read-modify-write cycle.
//
// The settings row is one JSON blob: a writer reads it, merges its own keys
// into it and writes it back. Two writers that interleave lose one another's
// keys, so the whole cycle runs under one lock.
var settingsWriteMu sync.Mutex

// updateSettings merges into the settings row under the write lock.
//
// The merge runs on the row as it is, so a writer that has to change a nested
// key does not read the row itself. A row that cannot be read is not
// replaced: the merge would write back only its own keys and drop everything
// else the operator had set.
func (r *Repo) updateSettings(merge func(existing map[string]any) error) error {
	settingsWriteMu.Lock()
	defer settingsWriteMu.Unlock()

	existing, err := r.GetSettingsRaw()
	if err != nil {
		return fmt.Errorf("update settings: %w", err)
	}
	if err := merge(existing); err != nil {
		return err
	}
	b, err := json.Marshal(existing)
	if err != nil {
		return fmt.Errorf("marshal settings data: %w", err)
	}
	// The write carries no request context: a settings save that is cancelled
	// with the caller leaves the operator with a half-applied change, and the
	// statement itself is atomic.
	if _, err := r.db.ExecContext(
		context.Background(),
		`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`,
		string(b),
	); err != nil {
		return fmt.Errorf("update settings raw: %w", err)
	}
	return nil
}

// SetAutoUpdate updates the autoUpdate flag in the settings table.
func (r *Repo) SetAutoUpdate(enabled bool) error {
	return r.UpdateSettingsRaw(map[string]any{
		"autoUpdate": enabled,
	})
}

// SetProviderStrategy updates or sets the routing strategy and proxy pool for a provider without clobbering other settings.
func (r *Repo) SetProviderStrategy(provider string, strat ProviderStrategy) error {
	return r.updateSettings(func(existing map[string]any) error {
		currentMap, _ := existing["providerStrategies"].(map[string]any)
		if currentMap == nil {
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
		existing["providerStrategies"] = currentMap
		return nil
	})
}

// SetComboStrategy updates or sets the routing strategy for a combo without clobbering other settings.
func (r *Repo) SetComboStrategy(comboName string, strat ComboStrategy) error {
	return r.updateSettings(func(existing map[string]any) error {
		currentMap, _ := existing["comboStrategies"].(map[string]any)
		if currentMap == nil {
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
		if strat.FirstTokenTimeoutMs > 0 {
			entry["firstTokenTimeoutMs"] = strat.FirstTokenTimeoutMs
		}
		if len(strat.FallbackCombos) > 0 {
			entry["fallbackCombos"] = strat.FallbackCombos
		}

		if strat.Strategy == "fallback" && strat.JudgeModel == "" && len(strat.FallbackCombos) == 0 && len(entry) <= 2 {
			delete(currentMap, comboName)
		} else {
			currentMap[comboName] = entry
		}
		existing["comboStrategies"] = currentMap
		return nil
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
	return r.updateSettings(func(existing map[string]any) error {
		all, _ := existing["providerOverrides"].(map[string]any)
		if all == nil {
			all = make(map[string]any)
		}
		if override == nil || len(override.Headers) == 0 {
			delete(all, provider)
		} else {
			all[provider] = map[string]any{"headers": override.Headers}
		}
		if len(all) == 0 {
			existing["providerOverrides"] = nil
		} else {
			existing["providerOverrides"] = all
		}
		return nil
	})
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
