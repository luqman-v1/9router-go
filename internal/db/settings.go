package db

import (
	json "encoding/json/v2"

	"9router/proxy/internal/handlerutil"
)

// ProviderStrategy defines routing and proxy pool options for a specific provider.
type ProviderStrategy struct {
	ProxyPoolID    string `json:"proxyPoolId"`
	RotateStrategy string `json:"rotateStrategy"` // "none", "round-robin", "random"
}

// SettingsData represents token saver and general settings stored in the settings table.
type SettingsData struct {
	RTKEnabled         bool                        `json:"rtkEnabled"`
	CavemanEnabled     bool                        `json:"cavemanEnabled"`
	CavemanLevel       string                      `json:"cavemanLevel"`
	PonytailEnabled    bool                        `json:"ponytailEnabled"`
	PonytailLevel      string                      `json:"ponytailLevel"`
	HeadroomUrl        string                      `json:"headroomUrl"`
	HeadroomCodeAware  bool                        `json:"headroomCodeAware"`
	HeadroomKompress   bool                        `json:"headroomKompress"`
	HeadroomTimeoutMs  int                         `json:"headroomTimeoutMs"`
	AutoUpdate         bool                        `json:"autoUpdate"`
	ProviderStrategies map[string]ProviderStrategy `json:"providerStrategies,omitempty"`
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
	if ps, ok := raw["providerStrategies"].(map[string]any); ok {
		s.ProviderStrategies = make(map[string]ProviderStrategy)
		for k, v := range ps {
			if vm, ok := v.(map[string]any); ok {
				s.ProviderStrategies[k] = ProviderStrategy{
					ProxyPoolID:    handlerutil.GetString(vm, "proxyPoolId"),
					RotateStrategy: handlerutil.GetString(vm, "rotateStrategy"),
				}
			}
		}
	}

	return s, nil
}

// SetAutoUpdate updates the autoUpdate flag in the settings table.
func (r *Repo) SetAutoUpdate(enabled bool) error {
	s, err := r.GetSettings()
	if err != nil {
		s = DefaultSettings()
	}
	s.AutoUpdate = enabled
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = r.db.Exec(`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`, string(b))
	return err
}
