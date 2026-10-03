package db

import (
	"context"
	"database/sql"
	json "encoding/json/v2"
	"fmt"
	"strings"
	"time"

	"9router/proxy/internal/models"
)

// Repo wraps the SQLite handle and groups all persistence queries.
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

// CreateProviderConnection inserts a new provider connection.
func (r *Repo) CreateProviderConnection(id, provider, authType, name string, apiKey string) error {
	data, err := json.Marshal(map[string]string{"apiKey": apiKey})
	if err != nil {
		return fmt.Errorf("marshal provider connection data: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = r.db.Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, isActive, data, createdAt, updatedAt) VALUES (?, ?, ?, ?, 1, ?, ?, ?)`,
		id, provider, authType, name, string(data), now, now,
	)
	if err != nil {
		return fmt.Errorf("create provider connection: %w", err)
	}
	return nil
}

// CreateProviderConnectionFull inserts a provider connection with an explicit
// priority and a full data payload (apiKey + providerSpecificData + testStatus
// + proxyPoolId, as the dashboard add-key modal sends them). A nil priority
// falls back to max(existing priority for the provider) + 1, matching upstream
// connectionsRepo when the caller omits priority.
func (r *Repo) CreateProviderConnectionFull(id, provider, authType, name string, priority *int, dataJSON string) error {
	if dataJSON == "" {
		dataJSON = "{}"
	}
	priorityVal := 1
	if priority != nil {
		priorityVal = *priority
	} else if next, err := r.NextConnectionPriority(provider); err == nil && next > 0 {
		priorityVal = next
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := r.db.Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?)`,
		id, provider, authType, name, priorityVal, dataJSON, now, now,
	)
	if err != nil {
		return fmt.Errorf("create provider connection: %w", err)
	}
	return nil
}

// NextConnectionPriority returns max(priority) + 1 for a provider, or 1 when the
// provider has no connections yet.
func (r *Repo) NextConnectionPriority(provider string) (int, error) {
	var maxPriority sql.NullInt64
	if err := r.db.QueryRow(
		`SELECT MAX(priority) FROM providerConnections WHERE provider = ?`, provider,
	).Scan(&maxPriority); err != nil {
		return 0, fmt.Errorf("next connection priority for %s: %w", provider, err)
	}
	if !maxPriority.Valid {
		return 1, nil
	}
	return int(maxPriority.Int64) + 1, nil
}

// GetProviderConnectionByName returns the apikey connection carrying this
// (provider, authType, name), or nil when the name is free. Apikey connections
// are the ones deduped by name: oauth and access_token rows are identified by
// their account, and the user manages their duplicates by hand.
func (r *Repo) GetProviderConnectionByName(provider, authType, name string) (*models.ProviderConnection, error) {
	if name == "" {
		return nil, nil
	}
	var conn models.ProviderConnection
	err := r.db.QueryRow(
		`SELECT id, provider, authType, name, email, priority, isActive, data, lastUsedAt, consecutiveUseCount, createdAt, updatedAt
		 FROM providerConnections WHERE provider = ? AND authType = ? AND name = ? LIMIT 1`,
		provider, authType, name,
	).Scan(&conn.ID, &conn.Provider, &conn.AuthType, &conn.Name, &conn.Email,
		&conn.Priority, &conn.IsActive, &conn.Data, &conn.LastUsedAt, &conn.ConsecutiveUseCount,
		&conn.CreatedAt, &conn.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get provider connection by name for %s: %w", provider, err)
	}
	return &conn, nil
}

// ReplaceProviderConnectionPayload rewrites an existing connection's name and
// data in place, keeping its id and position in the rotation. This is what an
// explicit overwrite does: the caller asked to change the key behind a name
// they already own, not to add a second row with the same name.
func (r *Repo) ReplaceProviderConnectionPayload(id, name, dataJSON string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := r.db.Exec(
		`UPDATE providerConnections SET name = ?, data = ?, updatedAt = ? WHERE id = ?`,
		name, dataJSON, now, id,
	)
	if err != nil {
		return fmt.Errorf("replace provider connection %s: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("replace provider connection %s: no such connection", id)
	}
	return nil
}

func (r *Repo) GetProviderConnectionByID(id string) (*models.ProviderConnection, error) {
	var conn models.ProviderConnection
	err := r.db.QueryRow(
		"SELECT id, provider, authType, name, email, priority, isActive, data, lastUsedAt, consecutiveUseCount, createdAt, updatedAt FROM providerConnections WHERE id = ? LIMIT 1",
		id,
	).Scan(&conn.ID, &conn.Provider, &conn.AuthType, &conn.Name, &conn.Email,
		&conn.Priority, &conn.IsActive, &conn.Data, &conn.LastUsedAt, &conn.ConsecutiveUseCount,
		&conn.CreatedAt, &conn.UpdatedAt)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &conn, nil
}

// GetConnectedProviders returns the set of provider ids that have at least one
// connection row. Used to seed the auto free-tier combo with models that are
// actually reachable from the user's configured providers.
func (r *Repo) GetConnectedProviders(ctx context.Context) (map[string]struct{}, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT DISTINCT provider FROM providerConnections")
	if err != nil {
		return nil, fmt.Errorf("get connected providers: %w", err)
	}
	defer rows.Close()

	out := make(map[string]struct{})
	for rows.Next() {
		var provider string
		if err := rows.Scan(&provider); err != nil {
			return nil, fmt.Errorf("get connected providers: scan: %w", err)
		}
		if provider != "" {
			out[provider] = struct{}{}
		}
	}
	return out, rows.Err()
}

// GetProviderConnections retrieves provider connections. If activeOnly is true, only returns active ones.
// Sorts them by priority ASC (using a fallback value of 999999 for null priority to match JavaScript behavior).
func (r *Repo) GetProviderConnections(provider string, activeOnly bool) ([]*models.ProviderConnection, error) {
	var query string
	var args []any

	const connCols = `id, provider, authType, name, email, priority, isActive, data, lastUsedAt, consecutiveUseCount, createdAt, updatedAt`
	const orderBy = `ORDER BY CASE WHEN priority IS NULL THEN 999999 ELSE priority END ASC, updatedAt DESC`

	if provider != "" {
		if activeOnly {
			query = "SELECT " + connCols + " FROM providerConnections WHERE provider = ? AND isActive = 1 " + orderBy
		} else {
			query = "SELECT " + connCols + " FROM providerConnections WHERE provider = ? " + orderBy
		}
		args = append(args, provider)
	} else {
		if activeOnly {
			query = "SELECT " + connCols + " FROM providerConnections WHERE isActive = 1 " + orderBy
		} else {
			query = "SELECT " + connCols + " FROM providerConnections " + orderBy
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
			&conn.Priority, &conn.IsActive, &conn.Data, &conn.LastUsedAt, &conn.ConsecutiveUseCount,
			&conn.CreatedAt, &conn.UpdatedAt,
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

// GetProviderNodePrefixMap returns a mapping of providerNode.id -> prefix from the data JSON.
func (r *Repo) GetProviderNodePrefixMap() (map[string]string, error) {
	rows, err := r.db.Query("SELECT id, data FROM providerNodes")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	prefixMap := make(map[string]string)
	for rows.Next() {
		var id, data string
		if err := rows.Scan(&id, &data); err != nil {
			return nil, err
		}
		if nd := parseProviderNodeData(data); nd != nil && nd.Prefix != "" {
			prefixMap[id] = nd.Prefix
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return prefixMap, nil
}

// GetProviderNodes returns all provider nodes from the database.
func (r *Repo) GetProviderNodes() ([]*models.ProviderNode, error) {
	rows, err := r.db.Query(
		"SELECT id, type, name, data, createdAt, updatedAt FROM providerNodes ORDER BY createdAt ASC",
	)
	if err != nil {
		return nil, fmt.Errorf("get provider nodes: %w", err)
	}
	defer rows.Close()

	var nodes []*models.ProviderNode
	for rows.Next() {
		var node models.ProviderNode
		if err := rows.Scan(&node.ID, &node.Type, &node.Name, &node.Data, &node.CreatedAt, &node.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan provider node: %w", err)
		}
		nodes = append(nodes, &node)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate provider nodes: %w", err)
	}
	return nodes, nil
}

// CreateProviderNode inserts a new provider node.
func (r *Repo) CreateProviderNode(id, nodeType, name, data string) (*models.ProviderNode, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	var nameVal any
	if name != "" {
		nameVal = name
	}
	var typeVal any
	if nodeType != "" {
		typeVal = nodeType
	}
	_, err := r.db.Exec(
		"INSERT INTO providerNodes (id, type, name, data, createdAt, updatedAt) VALUES (?, ?, ?, ?, ?, ?)",
		id, typeVal, nameVal, data, now, now,
	)
	if err != nil {
		return nil, fmt.Errorf("create provider node: %w", err)
	}
	t := nodeType
	n := name
	return &models.ProviderNode{
		ID:        id,
		Type:      &t,
		Name:      &n,
		Data:      data,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// UpdateProviderNode updates a provider node's name and data JSON, returning
// the refreshed row. Mirrors upstream PUT /api/provider-nodes/[id]: name and
// prefix are required; apiType/baseUrl are stored in the data blob.
func (r *Repo) UpdateProviderNode(id, name, data string) (*models.ProviderNode, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	var nameVal any
	if name != "" {
		nameVal = name
	}
	if _, err := r.db.Exec(
		"UPDATE providerNodes SET name = ?, data = ?, updatedAt = ? WHERE id = ?",
		nameVal, data, now, id,
	); err != nil {
		return nil, fmt.Errorf("update provider node %s: %w", id, err)
	}
	var node models.ProviderNode
	err := r.db.QueryRow(
		"SELECT id, type, name, data, createdAt, updatedAt FROM providerNodes WHERE id = ? LIMIT 1",
		id,
	).Scan(&node.ID, &node.Type, &node.Name, &node.Data, &node.CreatedAt, &node.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("reload provider node %s: %w", id, err)
	}
	return &node, nil
}

// DeleteProviderNode deletes a provider node and its associated connections.
func (r *Repo) DeleteProviderNode(id string) error {
	if _, err := r.db.Exec("DELETE FROM providerNodes WHERE id = ?", id); err != nil {
		return fmt.Errorf("delete provider node %s: %w", id, err)
	}
	_, _ = r.db.Exec("DELETE FROM providerConnections WHERE provider = ?", id)
	return nil
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
	if s, err := r.GetSettings(); err == nil && s != nil {
		if cs, ok := s.ComboStrategies[combo.Name]; ok && cs.Strategy != "" {
			combo.Strategy = cs.Strategy
		} else if s.ComboStrategy != "" {
			combo.Strategy = s.ComboStrategy
		}
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
	combo.Strategy = "fallback" // default
	if s, err := r.GetSettings(); err == nil && s != nil {
		if cs, ok := s.ComboStrategies[combo.Name]; ok && cs.Strategy != "" {
			combo.Strategy = cs.Strategy
		} else if s.ComboStrategy != "" {
			combo.Strategy = s.ComboStrategy
		}
	}
	return &combo, nil
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

// GetCombos retrieves all combos from the database.
func (r *Repo) GetCombos() ([]*models.Combo, error) {
	rows, err := r.db.Query("SELECT id, name, kind, models, createdAt, updatedAt FROM combos ORDER BY createdAt ASC")
	if err != nil {
		return nil, err
	}
	// Drain and close the rows BEFORE reading settings: holding an open rows set
	// occupies one pooled connection (MaxOpenConns is 4), so a nested query
	// here could deadlock the whole pool once four concurrent callers pile up.
	var combos []*models.Combo
	for rows.Next() {
		var combo models.Combo
		if err := rows.Scan(&combo.ID, &combo.Name, &combo.Kind, &combo.Models, &combo.CreatedAt, &combo.UpdatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		combos = append(combos, &combo)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	settings, _ := r.GetSettings()

	// Strategy resolution runs after the rows are closed so the nested settings
	// read can never hold two pooled connections at once.
	for _, combo := range combos {
		combo.Strategy = "fallback"
		if settings != nil {
			if cs, ok := settings.ComboStrategies[combo.Name]; ok && cs.Strategy != "" {
				combo.Strategy = cs.Strategy
			} else if settings.ComboStrategy != "" {
				combo.Strategy = settings.ComboStrategy
			}
		}
	}
	return combos, nil
}
