package db

import (
	"context"
	"database/sql"
	json "encoding/json/v2"
	"fmt"
	"time"

	"9router/proxy/internal/models"
)

// providerNodeColumns is the shared column list for every providerNodes read.
const providerNodeColumns = `id, type, name, data, createdAt, updatedAt`

// scanProviderNode fills a node from a row shaped like providerNodeColumns.
func scanProviderNode(scan func(dest ...any) error, node *models.ProviderNode) error {
	return scan(&node.ID, &node.Type, &node.Name, &node.Data, &node.CreatedAt, &node.UpdatedAt)
}

// ProviderNodeData holds parsed fields from the providerNodes.data JSON blob.
type ProviderNodeData struct {
	Prefix  string `json:"prefix"`
	APIType string `json:"apiType"`
	BaseURL string `json:"baseUrl"`
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

// GetProviderNodeByID retrieves a provider node by its primary key.
// It parses the embedded data JSON to populate BaseURL, Prefix, and APIType.
// Returns nil, nil when no row matches.
func (r *Repo) GetProviderNodeByID(id string) (*models.ProviderNode, *ProviderNodeData, error) {
	var node models.ProviderNode
	err := scanProviderNode(r.db.QueryRow(
		"SELECT "+providerNodeColumns+" FROM providerNodes WHERE id = ? LIMIT 1",
		id,
	).Scan, &node)

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
	rows, err := r.db.Query("SELECT " + providerNodeColumns + " FROM providerNodes")
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var node models.ProviderNode
		if err := scanProviderNode(rows.Scan, &node); err != nil {
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
		"SELECT " + providerNodeColumns + " FROM providerNodes ORDER BY createdAt ASC",
	)
	if err != nil {
		return nil, fmt.Errorf("get provider nodes: %w", err)
	}
	defer rows.Close()

	var nodes []*models.ProviderNode
	for rows.Next() {
		var node models.ProviderNode
		if err := scanProviderNode(rows.Scan, &node); err != nil {
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
	if err := scanProviderNode(r.db.QueryRow(
		"SELECT "+providerNodeColumns+" FROM providerNodes WHERE id = ? LIMIT 1",
		id,
	).Scan, &node); err != nil {
		return nil, fmt.Errorf("reload provider node %s: %w", id, err)
	}
	return &node, nil
}

// DeleteProviderNode deletes a provider node, its associated connections,
// and any custom models registered under its alias.
func (r *Repo) DeleteProviderNode(ctx context.Context, id string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("delete provider node %s: begin transaction: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }()

	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM providerNodes WHERE id = ?`, id).Scan(&exists); err != nil {
		return fmt.Errorf("delete provider node %s: check existence: %w", id, err)
	}
	if exists == 0 {
		return nil
	}

	if _, err := tx.ExecContext(ctx, "DELETE FROM providerNodes WHERE id = ?", id); err != nil {
		return fmt.Errorf("delete provider node %s: %w", id, err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM providerConnections WHERE provider = ?", id); err != nil {
		return fmt.Errorf("delete provider node %s: delete connections: %w", id, err)
	}

	if err := pruneCustomModels(ctx, tx, id); err != nil {
		return fmt.Errorf("delete provider node %s: prune custom models: %w", id, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("delete provider node %s: commit: %w", id, err)
	}
	return nil
}

// pruneCustomModels removes any customModels kv rows whose key starts with the
// node's alias or whose JSON value declares the node as its providerAlias.
func pruneCustomModels(ctx context.Context, tx *sql.Tx, id string) error {
	rows, err := tx.QueryContext(ctx, `SELECT key, value FROM kv WHERE scope = 'customModels'`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()

	var toDelete []string
	for rows.Next() {
		var key, raw string
		if err := rows.Scan(&key, &raw); err != nil {
			return err
		}
		keyAlias, _, ok := splitCustomModelKey(key)
		if !ok {
			keyAlias = ""
		}
		valAlias := customModelValueAlias(raw)
		if keyAlias == id || valAlias == id {
			toDelete = append(toDelete, key)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	for _, k := range toDelete {
		if _, err := tx.ExecContext(ctx, `DELETE FROM kv WHERE scope = 'customModels' AND key = ?`, k); err != nil {
			return err
		}
	}
	return nil
}

// customModelValueAlias extracts the providerAlias from the JSON-encoded value,
// returning an empty string if unmarshaling fails or providerAlias is absent.
func customModelValueAlias(value string) string {
	var decoded map[string]any
	if err := json.Unmarshal([]byte(value), &decoded); err != nil {
		return ""
	}
	alias, _ := decoded["providerAlias"].(string)
	return alias
}
