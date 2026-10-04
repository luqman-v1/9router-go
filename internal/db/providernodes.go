package db

import (
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

// DeleteProviderNode deletes a provider node and its associated connections.
func (r *Repo) DeleteProviderNode(id string) error {
	if _, err := r.db.Exec("DELETE FROM providerNodes WHERE id = ?", id); err != nil {
		return fmt.Errorf("delete provider node %s: %w", id, err)
	}
	_, _ = r.db.Exec("DELETE FROM providerConnections WHERE provider = ?", id)
	return nil
}