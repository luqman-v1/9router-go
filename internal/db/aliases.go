package db

import (
	"database/sql"
	json "encoding/json/v2"
	"fmt"
	"strings"
)

// mapKey is a helper type to avoid linter complaining about general map keys
type mapKey = string

// parseJSONString helper unquotes/unmarshals a JSON string if it is JSON-encoded.
// Otherwise, it returns the string as-is.
func parseJSONString(raw string) string {
	var val string
	// Check if it looks like a JSON string representation (enclosed in quotes)
	if strings.HasPrefix(raw, "\"") && strings.HasSuffix(raw, "\"") {
		if err := json.Unmarshal([]byte(raw), &val); err == nil {
			return val
		}
	}
	return raw
}

// GetModelAlias retrieves the target model string for a given alias.
// It parses the stored JSON value correctly (removing JSON string quotes if present).
func (r *Repo) GetModelAlias(alias string) (string, error) {
	var rawVal string
	err := r.db.QueryRow(
		"SELECT value FROM kv WHERE scope = 'modelAliases' AND key = ? LIMIT 1",
		alias,
	).Scan(&rawVal)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get model alias %s: %w", alias, err)
	}
	return parseJSONString(rawVal), nil
}

// GetModelAliases returns all model aliases as a key-value map.
func (r *Repo) GetModelAliases() (map[mapKey]string, error) {
	rows, err := r.db.Query("SELECT key, value FROM kv WHERE scope = 'modelAliases'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	aliases := make(map[mapKey]string)
	for rows.Next() {
		var key, rawVal string
		if err := rows.Scan(&key, &rawVal); err != nil {
			return nil, err
		}
		aliases[key] = parseJSONString(rawVal)
	}

	if err = rows.Err(); err != nil {
		return nil, err
	}

	return aliases, nil
}

// CustomModel represents a user-defined model stored in kv scope customModels.
//
// ContextWindow and MaxOutput are the operator's own declaration of what the
// endpoint can actually take. Zero means "not declared": a provider node's
// model id is usually an open-source name that matches no capability pattern,
// so without these fields /v1/models had nothing better than a substring guess
// or the 128k floor.
type CustomModel struct {
	ProviderAlias string          `json:"providerAlias"`
	ID            string          `json:"id"`
	Type          string          `json:"type"`
	Name          string          `json:"name"`
	Caps          map[string]bool `json:"caps"`
	ContextWindow int             `json:"contextWindow,omitempty"`
	MaxOutput     int             `json:"maxOutput,omitempty"`
}

// GetCustomModels returns all custom models from kv scope customModels.
func (r *Repo) GetCustomModels() ([]*CustomModel, error) {
	rows, err := r.db.Query("SELECT key, value FROM kv WHERE scope = 'customModels'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*CustomModel
	for rows.Next() {
		var key, raw string
		if err := rows.Scan(&key, &raw); err != nil {
			return nil, err
		}
		var cm CustomModel
		_ = json.Unmarshal([]byte(raw), &cm)

		// Fallback parse from key if fields are missing in JSON value:
		// key format in Next.js: <providerAlias>|<modelId>|<kind> or <providerAlias>/<modelId>/<kind>
		if cm.ProviderAlias == "" || cm.ID == "" {
			parts := strings.Split(key, "|")
			if len(parts) < 2 {
				parts = strings.Split(key, "/")
			}
			if len(parts) >= 2 {
				if cm.ProviderAlias == "" {
					cm.ProviderAlias = parts[0]
				}
				if cm.ID == "" {
					cm.ID = parts[1]
				}
				if cm.Type == "" && len(parts) >= 3 {
					cm.Type = parts[2]
				}
			}
		}

		if cm.ID == "" || cm.ProviderAlias == "" {
			continue
		}
		out = append(out, &cm)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}