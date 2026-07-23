package db

import (
	"encoding/json"

	"9router/proxy/internal/handlerutil"
)

// SettingsData represents token saver and general settings stored in the settings table.
type SettingsData struct {
	RTKEnabled      bool   `json:"rtkEnabled"`
	CavemanEnabled  bool   `json:"cavemanEnabled"`
	CavemanLevel    string `json:"cavemanLevel"`
	PonytailEnabled bool   `json:"ponytailEnabled"`
	PonytailLevel   string `json:"ponytailLevel"`
}

// DefaultSettings returns fallback settings.
func DefaultSettings() *SettingsData {
	return &SettingsData{
		RTKEnabled:      true,
		CavemanEnabled:  false,
		CavemanLevel:    "full",
		PonytailEnabled: false,
		PonytailLevel:   "full",
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

	return s, nil
}
