package db

import (
	"9router/proxy/internal/handlerutil"
	json "encoding/json/v2"
)

// parseSettings parses the settings row. The bool is false when the blob is
// not readable.
//
// The row is one JSON blob with one section per feature, so the work is split
// into one function per section: a single function with sixty conditionals
// hides the field a section forgot to read.
func parseSettings(rawData string) (*SettingsData, bool) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(rawData), &raw); err != nil {
		return nil, false
	}

	s := DefaultSettings()
	applyRTKSettings(s, raw)
	applyPromptSettings(s, raw)
	applyHeadroomSettings(s, raw)
	applyCacheSettings(s, raw)
	applyObservabilitySettings(s, raw)
	applyFallbackSettings(s, raw)
	applyComboStrategies(s, raw)
	applyProviderStrategies(s, raw)
	applyProviderThinking(s, raw)
	applyCapacityAdapters(s, raw)
	applyProviderOverrides(s, raw)
	return s, true
}

func applyRTKSettings(s *SettingsData, raw map[string]any) {
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
}

func applyPromptSettings(s *SettingsData, raw map[string]any) {
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
}

func applyHeadroomSettings(s *SettingsData, raw map[string]any) {
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
}

func applyCacheSettings(s *SettingsData, raw map[string]any) {
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
}

func applyObservabilitySettings(s *SettingsData, raw map[string]any) {
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
}

func applyFallbackSettings(s *SettingsData, raw map[string]any) {
	if fs := handlerutil.GetString(raw, "fallbackStrategy"); fs != "" {
		s.FallbackStrategy = fs
	}
	if v, ok := raw["stickyRoundRobinLimit"].(float64); ok && v > 0 {
		s.StickyRoundRobinLimit = int(v)
	}
	if v, ok := raw["forceFallback"].(bool); ok {
		s.ForceFallback = v
	}
	if cs := handlerutil.GetString(raw, "comboStrategy"); cs != "" {
		s.ComboStrategy = cs
	}
	if v, ok := raw["comboStickyRoundRobinLimit"].(float64); ok && v > 0 {
		s.ComboStickyRoundRobinLimit = int(v)
	}
}
