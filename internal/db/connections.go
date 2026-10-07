package db

import (
	"context"
	"database/sql"
	json "encoding/json/v2"
	"fmt"

	"9router/proxy/internal/models"
)

// providerConnectionColumns is the single column list every connection read
// selects, so a schema addition lands in one place instead of three literals
// that have to stay in step by hand.
const providerConnectionColumns = `id, provider, authType, name, email, priority, isActive, data, lastUsedAt, consecutiveUseCount, createdAt, updatedAt`

// connectionSortOrder mirrors the JavaScript ordering: rows with no priority sort
// last, ties break on the most recently updated row.
const connectionSortOrder = `ORDER BY CASE WHEN priority IS NULL THEN 999999 ELSE priority END ASC, updatedAt DESC`

// scanConnection fills a connection from a row shaped like
// providerConnectionColumns. Both *sql.Row and *sql.Rows satisfy it, so the
// single-row and list readers cannot drift apart in column order.
func scanConnection(scan func(dest ...any) error, conn *models.ProviderConnection) error {
	return scan(
		&conn.ID, &conn.Provider, &conn.AuthType, &conn.Name, &conn.Email,
		&conn.Priority, &conn.IsActive, &conn.Data, &conn.LastUsedAt, &conn.ConsecutiveUseCount,
		&conn.CreatedAt, &conn.UpdatedAt,
	)
}

// CreateProviderConnection inserts a new provider connection carrying just an
// API key, the shape OAuth imports and CLI provisioning use.
func (r *Repo) CreateProviderConnection(id, provider, authType, name string, apiKey string) error {
	data, err := json.Marshal(map[string]string{"apiKey": apiKey})
	if err != nil {
		return fmt.Errorf("marshal provider connection data: %w", err)
	}
	now := nowUTC()
	if _, err = r.db.Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, isActive, data, createdAt, updatedAt) VALUES (?, ?, ?, ?, 1, ?, ?, ?)`,
		id, provider, authType, name, string(data), now, now,
	); err != nil {
		return fmt.Errorf("create provider connection: %w", err)
	}
	// Seal after the insert: the sealed columns are written with one UPDATE per
	// slot, which needs the row to exist.
	sealed, err := r.sealOnWrite(id, string(data))
	if err != nil {
		return err
	}
	if sealed != string(data) {
		if _, err = r.db.Exec(`UPDATE providerConnections SET data = ? WHERE id = ?`, sealed, id); err != nil {
			return fmt.Errorf("create provider connection: redact data: %w", err)
		}
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
	now := nowUTC()
	_, err := r.db.Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?)`,
		id, provider, authType, name, priorityVal, dataJSON, now, now,
	)
	if err != nil {
		return fmt.Errorf("create provider connection: %w", err)
	}
	// Seal after the insert so the credentials land in their own columns and
	// the stored payload keeps only what the model resolver must read.
	sealed, err := r.sealOnWrite(id, dataJSON)
	if err != nil {
		return err
	}
	if sealed == dataJSON {
		return nil
	}
	if _, err = r.db.Exec(`UPDATE providerConnections SET data = ? WHERE id = ?`, sealed, id); err != nil {
		return fmt.Errorf("create provider connection: redact data: %w", err)
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
		`SELECT `+providerConnectionColumns+` FROM providerConnections
		 WHERE provider = ? AND authType = ? AND name = ? LIMIT 1`,
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
	r.hydrate(&conn)
	return &conn, nil
}

// GetProviderConnectionByID retrieves a single provider connection by primary
// key. Returns nil, nil when no row matches.
func (r *Repo) GetProviderConnectionByID(id string) (*models.ProviderConnection, error) {
	var conn models.ProviderConnection
	err := scanConnection(r.db.QueryRow(
		`SELECT `+providerConnectionColumns+` FROM providerConnections WHERE id = ? LIMIT 1`,
		id,
	).Scan, &conn)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.hydrate(&conn)
	return &conn, nil
}

// ReplaceProviderConnectionPayload rewrites an existing connection's name and
// data in place, keeping its id and position in the rotation. This is what an
// explicit overwrite does: the caller asked to change the key behind a name
// they already own, not to add a second row with the same name.
func (r *Repo) ReplaceProviderConnectionPayload(id, name, dataJSON string) error {
	// Seal first: this is the dashboard's overwrite path, so the new key must
	// never land in `data` in the clear even for the moment between the two
	// statements.
	sealed, err := r.sealOnWrite(id, dataJSON)
	if err != nil {
		return err
	}
	now := nowUTC()
	res, err := r.db.Exec(
		`UPDATE providerConnections SET name = ?, data = ?, updatedAt = ? WHERE id = ?`,
		name, sealed, now, id,
	)
	if err != nil {
		return fmt.Errorf("replace provider connection %s: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("replace provider connection %s: no such connection", id)
	}
	return nil
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
func (r *Repo) GetProviderConnections(provider string, activeOnly bool) ([]*models.ProviderConnection, error) {
	query := "SELECT " + providerConnectionColumns + " FROM providerConnections "
	var args []any

	switch {
	case provider != "" && activeOnly:
		query += "WHERE provider = ? AND isActive = 1 "
		args = append(args, provider)
	case provider != "":
		query += "WHERE provider = ? "
		args = append(args, provider)
	case activeOnly:
		query += "WHERE isActive = 1 "
	}
	query += connectionSortOrder

	rows, err := r.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var connections []*models.ProviderConnection
	for rows.Next() {
		var conn models.ProviderConnection
		if err := scanConnection(rows.Scan, &conn); err != nil {
			return nil, err
		}
		connections = append(connections, &conn)
	}

	if err = rows.Err(); err != nil {
		return nil, err
	}

	return connections, nil
}