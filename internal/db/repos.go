package db

import (
	"database/sql"
	"encoding/json"
	"strings"

	"9router/proxy/internal/models"
)

type Repo struct {
	db *sql.DB
}

// NewRepo creates a new repository instance using the provided SQL database connection.
func NewRepo(db *sql.DB) *Repo {
	return &Repo{db: db}
}

// RawDB returns the underlying *sql.DB connection for direct queries.
func (r *Repo) RawDB() *sql.DB {
	return r.db
}

// ValidateApiKey checks if the given API key exists and is active.
func (r *Repo) ValidateApiKey(key string) (bool, error) {
	var active int
	err := r.db.QueryRow("SELECT isActive FROM apiKeys WHERE key = ? LIMIT 1", key).Scan(&active)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return active == 1, nil
}

// GetApiKeyByKey retrieves detailed APIKey information by key.
func (r *Repo) GetApiKeyByKey(key string) (*models.APIKey, error) {
	var apiKey models.APIKey
	err := r.db.QueryRow(
		"SELECT id, key, name, machineId, isActive, createdAt FROM apiKeys WHERE key = ? LIMIT 1",
		key,
	).Scan(&apiKey.ID, &apiKey.Key, &apiKey.Name, &apiKey.MachineID, &apiKey.IsActive, &apiKey.CreatedAt)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &apiKey, nil
}

// GetProviderConnectionByID retrieves a single provider connection by primary key.
// Returns nil, nil when no row matches.
func (r *Repo) GetProviderConnectionByID(id string) (*models.ProviderConnection, error) {
	var conn models.ProviderConnection
	err := r.db.QueryRow(
		"SELECT id, provider, authType, name, email, priority, isActive, data, createdAt, updatedAt FROM providerConnections WHERE id = ? LIMIT 1",
		id,
	).Scan(&conn.ID, &conn.Provider, &conn.AuthType, &conn.Name, &conn.Email,
		&conn.Priority, &conn.IsActive, &conn.Data, &conn.CreatedAt, &conn.UpdatedAt)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &conn, nil
}

// GetProviderConnections retrieves provider connections. If activeOnly is true, only returns active ones.
// Sorts them by priority ASC (using a fallback value of 999999 for null priority to match JavaScript behavior).
func (r *Repo) GetProviderConnections(provider string, activeOnly bool) ([]*models.ProviderConnection, error) {
	var query string
	var args []any

	if provider != "" {
		if activeOnly {
			query = `SELECT id, provider, authType, name, email, priority, isActive, data, createdAt, updatedAt
				FROM providerConnections
				WHERE provider = ? AND isActive = 1
				ORDER BY CASE WHEN priority IS NULL THEN 999999 ELSE priority END ASC, updatedAt DESC`
		} else {
			query = `SELECT id, provider, authType, name, email, priority, isActive, data, createdAt, updatedAt
				FROM providerConnections
				WHERE provider = ?
				ORDER BY CASE WHEN priority IS NULL THEN 999999 ELSE priority END ASC, updatedAt DESC`
		}
		args = append(args, provider)
	} else {
		if activeOnly {
			query = `SELECT id, provider, authType, name, email, priority, isActive, data, createdAt, updatedAt
				FROM providerConnections
				WHERE isActive = 1
				ORDER BY CASE WHEN priority IS NULL THEN 999999 ELSE priority END ASC, updatedAt DESC`
		} else {
			query = `SELECT id, provider, authType, name, email, priority, isActive, data, createdAt, updatedAt
				FROM providerConnections
				ORDER BY CASE WHEN priority IS NULL THEN 999999 ELSE priority END ASC, updatedAt DESC`
		}
	}

	rows, err := r.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var connections []*models.ProviderConnection
	for rows.Next() {
		var conn models.ProviderConnection
		err := rows.Scan(
			&conn.ID, &conn.Provider, &conn.AuthType, &conn.Name, &conn.Email,
			&conn.Priority, &conn.IsActive, &conn.Data, &conn.CreatedAt, &conn.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		connections = append(connections, &conn)
	}

	if err = rows.Err(); err != nil {
		return nil, err
	}

	return connections, nil
}

// GetModelAlias retrieves the target model string for a given alias.
// It parses the stored JSON value correctly (removing JSON string quotes if present).
func (r *Repo) GetModelAlias(alias string) (string, error) {
	var rawVal string
	err := r.db.QueryRow(
		"SELECT value FROM kv WHERE scope = 'modelAliases' AND key = ? LIMIT 1",
		alias,
	).Scan(&rawVal)
	if err != nil {
		return "", err
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

// ProviderNodeData holds parsed fields from the providerNodes.data JSON blob.
type ProviderNodeData struct {
	Prefix  string `json:"prefix"`
	APIType string `json:"apiType"`
	BaseURL string `json:"baseUrl"`
}

// GetProviderNodeByID retrieves a provider node by its primary key.
// It parses the embedded data JSON to populate BaseURL, Prefix, and APIType.
// Returns nil, nil when no row matches.
func (r *Repo) GetProviderNodeByID(id string) (*models.ProviderNode, *ProviderNodeData, error) {
	var node models.ProviderNode
	err := r.db.QueryRow(
		"SELECT id, type, name, data, createdAt, updatedAt FROM providerNodes WHERE id = ? LIMIT 1",
		id,
	).Scan(&node.ID, &node.Type, &node.Name, &node.Data, &node.CreatedAt, &node.UpdatedAt)

	if err == sql.ErrNoRows {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}

	nodeData := parseProviderNodeData(node.Data)
	return &node, nodeData, nil
}

// GetProviderNodeByPrefix searches providerNodes for one whose data JSON "prefix" field matches.
// Returns nil, nil when no row matches.
func (r *Repo) GetProviderNodeByPrefix(prefix string) (*models.ProviderNode, *ProviderNodeData, error) {
	rows, err := r.db.Query(
		"SELECT id, type, name, data, createdAt, updatedAt FROM providerNodes",
	)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var node models.ProviderNode
		if err := rows.Scan(&node.ID, &node.Type, &node.Name, &node.Data, &node.CreatedAt, &node.UpdatedAt); err != nil {
			return nil, nil, err
		}
		nodeData := parseProviderNodeData(node.Data)
		if nodeData != nil && nodeData.Prefix == prefix {
			return &node, nodeData, nil
		}
	}
	if err = rows.Err(); err != nil {
		return nil, nil, err
	}

	return nil, nil, nil
}

// parseProviderNodeData extracts the JSON-encoded data field from a providerNode row.
func parseProviderNodeData(raw string) *ProviderNodeData {
	if raw == "" {
		return nil
	}
	var d ProviderNodeData
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return nil
	}
	return &d
}

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

// GetComboByName retrieves a combo configuration by its name.
func (r *Repo) GetComboByName(name string) (*models.Combo, error) {
	var combo models.Combo
	err := r.db.QueryRow(
		"SELECT id, name, kind, models, createdAt, updatedAt FROM combos WHERE name = ? LIMIT 1",
		name,
	).Scan(&combo.ID, &combo.Name, &combo.Kind, &combo.Models, &combo.CreatedAt, &combo.UpdatedAt)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	combo.Strategy = "fallback" // default

	// strategy column may exist in newer DBs
	var strat string
	if err := r.db.QueryRow("SELECT strategy FROM combos WHERE id = ?", combo.ID).Scan(&strat); err == nil && strat != "" {
		combo.Strategy = strat
	}

	return &combo, nil
}

// GetComboById retrieves a combo configuration by its ID.
func (r *Repo) GetComboById(id string) (*models.Combo, error) {
	var combo models.Combo
	err := r.db.QueryRow(
		"SELECT id, name, kind, models, createdAt, updatedAt FROM combos WHERE id = ? LIMIT 1",
		id,
	).Scan(&combo.ID, &combo.Name, &combo.Kind, &combo.Models, &combo.CreatedAt, &combo.UpdatedAt)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &combo, nil
}

// GetCombos retrieves all combos from the database.
func (r *Repo) GetCombos() ([]*models.Combo, error) {
	rows, err := r.db.Query("SELECT id, name, kind, models, createdAt, updatedAt FROM combos ORDER BY createdAt ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var combos []*models.Combo
	for rows.Next() {
		var combo models.Combo
		err := rows.Scan(&combo.ID, &combo.Name, &combo.Kind, &combo.Models, &combo.CreatedAt, &combo.UpdatedAt)
		if err != nil {
			return nil, err
		}
		combos = append(combos, &combo)
	}

	if err = rows.Err(); err != nil {
		return nil, err
	}

	return combos, nil
}
