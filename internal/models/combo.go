package models

import (
	json "encoding/json/v2"
	"strings"
)

// ComboModel represents an individual model within a combo, optionally carrying
// an integer weight for weighted round-robin routing.
type ComboModel struct {
	Model  string `json:"model"`
	Weight int    `json:"weight,omitempty"`
}

// UnmarshalJSON unmarshals a ComboModel from either a plain string ("provider/model")
// or an object ({"model": "provider/model", "weight": 2}).
// If weight is unset or negative, it defaults to 1 (or 0 if explicitly 0).
func (cm *ComboModel) UnmarshalJSON(data []byte) error {
	// 1. Plain string
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		cm.Model = s
		cm.Weight = 1
		return nil
	}

	// 2. Object shape
	var raw struct {
		Model     string `json:"model"`
		FullModel string `json:"fullModel"`
		Name      string `json:"name"`
		ID        string `json:"id"`
		Weight    *int   `json:"weight"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	if raw.Model != "" {
		cm.Model = raw.Model
	} else if raw.FullModel != "" {
		cm.Model = raw.FullModel
	} else if raw.Name != "" {
		cm.Model = raw.Name
	} else if raw.ID != "" {
		cm.Model = raw.ID
	}

	if raw.Weight != nil {
		w := *raw.Weight
		if w < 0 {
			w = 0
		} else if w > 1000 {
			w = 1000
		}
		cm.Weight = w
	} else {
		cm.Weight = 1
	}
	return nil
}

// ParsedModels parses the combo's Models JSON string into []ComboModel.
// Supports both string arrays and object arrays.
func (c *Combo) ParsedModels() []ComboModel {
	if c == nil || strings.TrimSpace(c.Models) == "" {
		return nil
	}
	var cms []ComboModel
	if err := json.Unmarshal([]byte(c.Models), &cms); err != nil {
		return nil
	}
	return cms
}

// ModelNames returns the list of model name strings from combo.Models.
func (c *Combo) ModelNames() []string {
	cms := c.ParsedModels()
	if len(cms) == 0 {
		return nil
	}
	names := make([]string, len(cms))
	for i, m := range cms {
		names[i] = m.Model
	}
	return names
}

// ModelWeights returns a map of model name to weight from combo.Models.
func (c *Combo) ModelWeights() map[string]int {
	cms := c.ParsedModels()
	if len(cms) == 0 {
		return nil
	}
	weights := make(map[string]int, len(cms))
	for _, m := range cms {
		if m.Model != "" {
			weights[m.Model] = m.Weight
		}
	}
	return weights
}
