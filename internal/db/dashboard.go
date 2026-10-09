package db

import (
	"database/sql"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"time"

	"9router/proxy/internal/models"
)

// UpdateProviderConnection updates a provider connection's name, priority, isActive, data, and updatedAt.
// priority is a pointer so a NULL priority stays NULL: coercing it to 0 would
// sort the row ahead of every other account (priority 0 beats every positive
// rank) and silently promote it to the top of the rotation.
func (r *Repo) UpdateProviderConnection(id string, name string, priority *int, isActive bool, data string) error {
	activeInt := 0
	if isActive {
		activeInt = 1
	}
	now := time.Now().UTC().Format(time.RFC3339)
	var nameVal any
	if name != "" {
		nameVal = name
	}
	_, err := r.db.Exec(
		`UPDATE providerConnections SET name = ?, priority = ?, isActive = ?, data = ?, updatedAt = ? WHERE id = ?`,
		nameVal, priority, activeInt, data, now, id,
	)
	if err != nil {
		return fmt.Errorf("update provider connection %s: %w", id, err)
	}
	return nil
}

// UpdateConnectionData updates only the data payload and updatedAt.
// Background writers (token refresh, node sync, probe persist) must use this:
// a full-row update writes back the name/isActive/priority they read before the
// write, which reverts a reorder or a toggle the user just performed.
func (r *Repo) UpdateConnectionData(id string, data string) error {
	// Seal before the data write so a credential is never briefly readable in
	// `data` after this call returns. A payload with no whitelisted credential
	// (a background writer touching only expiresAt) is stored verbatim and the
	// existing sealed slots are left alone.
	sealed, err := r.sealOnWrite(id, data)
	if err != nil {
		return err
	}
	now := nowUTC()
	if _, err = r.db.Exec(
		`UPDATE providerConnections SET data = ?, updatedAt = ? WHERE id = ?`,
		sealed, now, id,
	); err != nil {
		return fmt.Errorf("update connection data %s: %w", id, err)
	}
	return nil
}

// DeleteProviderConnection deletes a provider connection by its ID.
func (r *Repo) DeleteProviderConnection(id string) error {
	_, err := r.db.Exec(`DELETE FROM providerConnections WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete provider connection %s: %w", id, err)
	}
	return nil
}

// SetConnectionStatus updates the isActive status and updatedAt of a provider connection.
func (r *Repo) SetConnectionStatus(id string, isActive bool) error {
	activeInt := 0
	if isActive {
		activeInt = 1
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := r.db.Exec(
		`UPDATE providerConnections SET isActive = ?, updatedAt = ? WHERE id = ?`,
		activeInt, now, id,
	)
	if err != nil {
		return fmt.Errorf("set connection status %s: %w", id, err)
	}
	return nil
}

// SetConnectionPriority updates the priority and updatedAt of a provider connection.
func (r *Repo) SetConnectionPriority(id string, priority int) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := r.db.Exec(
		`UPDATE providerConnections SET priority = ?, updatedAt = ? WHERE id = ?`,
		priority, now, id,
	)
	if err != nil {
		return fmt.Errorf("set connection priority %s: %w", id, err)
	}
	return nil
}

// ReorderProviderConnections atomically moves one connection within its
// provider's pool and renumbers the whole pool to a contiguous 1..N sequence.
//
// The dashboard previously reordered with two independent full-row PUTs. That
// has no cross-row transaction, so a partial failure left two rows sharing a
// priority — and a stable sort over tied priorities then made every later swap
// a literal no-op, permanently bricking the pair. Doing the swap plus the
// renumber in one SQLite transaction makes partial failure impossible, and
// normalising to 1..N also repairs any duplicates left behind by older data.
// direction is -1 to move the row up and +1 to move it down.
func (r *Repo) ReorderProviderConnections(provider, id string, direction int) error {
	tx, err := r.db.Begin()
	if err != nil {
		return fmt.Errorf("reorder %s/%s: begin transaction: %w", provider, id, err)
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.Query(
		`SELECT id FROM providerConnections WHERE provider = ?
		 ORDER BY CASE WHEN priority IS NULL THEN 999999 ELSE priority END ASC, updatedAt DESC, id ASC`,
		provider,
	)
	if err != nil {
		return fmt.Errorf("reorder %s/%s: read pool: %w", provider, id, err)
	}
	var order []string
	for rows.Next() {
		var rowID string
		if scanErr := rows.Scan(&rowID); scanErr != nil {
			_ = rows.Close()
			return fmt.Errorf("reorder %s/%s: scan pool: %w", provider, id, scanErr)
		}
		order = append(order, rowID)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("reorder %s/%s: read pool: %w", provider, id, err)
	}
	_ = rows.Close()

	idx := slices.Index(order, id)
	if idx < 0 {
		return fmt.Errorf("reorder %s/%s: connection not found in provider pool", provider, id)
	}
	target := idx + direction
	if target < 0 || target >= len(order) {
		// Already at the edge: still renumber, so a pool carrying stale or
		// duplicate priorities is normalised even on a no-move request.
		target = idx
	} else {
		order[idx], order[target] = order[target], order[idx]
	}

	now := time.Now().UTC().Format(time.RFC3339)
	for i, rowID := range order {
		if _, err = tx.Exec(
			`UPDATE providerConnections SET priority = ?, updatedAt = ? WHERE id = ?`,
			i+1, now, rowID,
		); err != nil {
			return fmt.Errorf("reorder %s/%s: write rank %d: %w", provider, id, i+1, err)
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("reorder %s/%s: commit: %w", provider, id, err)
	}
	return nil
}

// CreateCombo inserts a new combo routing configuration.
func (r *Repo) CreateCombo(id, name, kind, modelsJSON, strategy string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	var kindVal any
	if kind != "" {
		kindVal = kind
	}
	_, err := r.db.Exec(
		`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES (?, ?, ?, ?, ?, ?)`,
		id, name, kindVal, modelsJSON, now, now,
	)
	if err != nil {
		return fmt.Errorf("create combo %s: %w", id, err)
	}
	if strategy != "" {
		// Update strategy if column exists in the schema
		_, _ = r.db.Exec(`UPDATE combos SET strategy = ? WHERE id = ?`, strategy, id)
	}
	return nil
}

// UpdateCombo updates an existing combo routing configuration.
func (r *Repo) UpdateCombo(id, name, kind, modelsJSON, strategy string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	var kindVal any
	if kind != "" {
		kindVal = kind
	}
	_, err := r.db.Exec(
		`UPDATE combos SET name = ?, kind = ?, models = ?, updatedAt = ? WHERE id = ?`,
		name, kindVal, modelsJSON, now, id,
	)
	if err != nil {
		return fmt.Errorf("update combo %s: %w", id, err)
	}
	if strategy != "" {
		// Update strategy if column exists in the schema
		_, _ = r.db.Exec(`UPDATE combos SET strategy = ? WHERE id = ?`, strategy, id)
	}
	return nil
}

// DeleteCombo deletes a combo configuration by its ID.
func (r *Repo) DeleteCombo(id string) error {
	_, err := r.db.Exec(`DELETE FROM combos WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete combo %s: %w", id, err)
	}
	return nil
}

// GetApiKeys retrieves all client API keys ordered by createdAt DESC.
func (r *Repo) GetApiKeys() ([]*models.APIKey, error) {
	rows, err := r.db.Query(`SELECT id, key, name, machineId, isActive, createdAt,
		rateLimitRPM, rateLimitTPM, rateLimitConcurrency,
		keyHash, lookupHash, keyDisplay,
		expiresAt, lastUsedAt, usedCount, metadata
		FROM apiKeys ORDER BY createdAt DESC`)
	if err != nil {
		return nil, fmt.Errorf("get api keys: %w", err)
	}
	defer rows.Close()

	keys := make([]*models.APIKey, 0)
	for rows.Next() {
		var k models.APIKey
		var nameVal, machineVal sql.NullString
		var rateRPM, rateTPM, rateConc sql.NullInt64
		var keyHash, lookupHash, keyDisplay sql.NullString
		var expiresAt, lastUsedAt, metadata sql.NullString
		var usedCount sql.NullInt64
		if err := rows.Scan(&k.ID, &k.Key, &nameVal, &machineVal, &k.IsActive, &k.CreatedAt,
			&rateRPM, &rateTPM, &rateConc,
			&keyHash, &lookupHash, &keyDisplay,
			&expiresAt, &lastUsedAt, &usedCount, &metadata); err != nil {
			return nil, fmt.Errorf("scan api key: %w", err)
		}
		if nameVal.Valid {
			k.Name = &nameVal.String
		}
		if machineVal.Valid {
			k.MachineID = &machineVal.String
		}
		if rateRPM.Valid {
			v := int(rateRPM.Int64)
			k.RateLimitRPM = &v
		}
		if rateTPM.Valid {
			v := int(rateTPM.Int64)
			k.RateLimitTPM = &v
		}
		if rateConc.Valid {
			v := int(rateConc.Int64)
			k.RateLimitConcurrency = &v
		}
		if keyHash.Valid {
			k.KeyHash = &keyHash.String
		}
		if lookupHash.Valid {
			k.LookupHash = &lookupHash.String
		}
		if keyDisplay.Valid {
			k.KeyDisplay = &keyDisplay.String
		}
		if expiresAt.Valid {
			k.ExpiresAt = &expiresAt.String
		}
		if lastUsedAt.Valid {
			k.LastUsedAt = &lastUsedAt.String
		}
		if usedCount.Valid {
			v := int(usedCount.Int64)
			k.UsedCount = &v
		}
		if metadata.Valid {
			k.Metadata = &metadata.String
		}
		keys = append(keys, &k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate api keys: %w", err)
	}
	return keys, nil
}

// CreateApiKey inserts a new client API key with isActive set to 1.
func (r *Repo) CreateApiKey(id, key, name, machineID string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	var nameVal, machineVal any
	if name != "" {
		nameVal = name
	}
	if machineID != "" {
		machineVal = machineID
	}
	_, err := r.db.Exec(
		`INSERT INTO apiKeys (id, key, name, machineId, isActive, createdAt) VALUES (?, ?, ?, ?, 1, ?)`,
		id, key, nameVal, machineVal, now,
	)
	if err != nil {
		return fmt.Errorf("create api key %s: %w", id, err)
	}
	return nil
}

// DeleteApiKey deletes a client API key by its ID.
func (r *Repo) DeleteApiKey(id string) error {
	_, err := r.db.Exec(`DELETE FROM apiKeys WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete api key %s: %w", id, err)
	}
	return nil
}

// SetApiKeyStatus updates the isActive status of a client API key.
func (r *Repo) SetApiKeyStatus(id string, isActive bool) error {
	activeInt := 0
	if isActive {
		activeInt = 1
	}
	_, err := r.db.Exec(`UPDATE apiKeys SET isActive = ? WHERE id = ?`, activeInt, id)
	if err != nil {
		return fmt.Errorf("set api key status %s: %w", id, err)
	}
	return nil
}

// UpdateApiKeyUsage increments usedCount and sets lastUsedAt.
func (r *Repo) UpdateApiKeyUsage(keyID string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := r.db.Exec(
		`UPDATE apiKeys SET lastUsedAt = ?, usedCount = usedCount + 1 WHERE id = ?`,
		now, keyID,
	)
	if err != nil {
		return fmt.Errorf("update api key usage %s: %w", keyID, err)
	}
	return nil
}

// SetApiKeyRateLimit updates rate limit columns for an API key.
func (r *Repo) SetApiKeyRateLimit(keyID string, rpm, tpm, concurrency int) error {
	_, err := r.db.Exec(
		`UPDATE apiKeys SET rateLimitRPM = ?, rateLimitTPM = ?, rateLimitConcurrency = ? WHERE id = ?`,
		rpm, tpm, concurrency, keyID,
	)
	if err != nil {
		return fmt.Errorf("set api key rate limit %s: %w", keyID, err)
	}
	return nil
}

// SetApiKeyExpiry sets expiresAt for an API key.
func (r *Repo) SetApiKeyExpiry(keyID, expiresAt string) error {
	_, err := r.db.Exec(
		`UPDATE apiKeys SET expiresAt = ? WHERE id = ?`,
		expiresAt, keyID,
	)
	if err != nil {
		return fmt.Errorf("set api key expiry %s: %w", keyID, err)
	}
	return nil
}

// SetApiKeyMetadata sets metadata JSON for an API key.
func (r *Repo) SetApiKeyMetadata(keyID, metadata string) error {
	_, err := r.db.Exec(
		`UPDATE apiKeys SET metadata = ? WHERE id = ?`,
		metadata, keyID,
	)
	if err != nil {
		return fmt.Errorf("set api key metadata %s: %w", keyID, err)
	}
	return nil
}

// SetApiKeyName renames an API key. The name is operator-facing only — it never
// reaches the wire — so an empty value clears it and the UI falls back to the
// masked display.
func (r *Repo) SetApiKeyName(keyID, name string) error {
	_, err := r.db.Exec(
		`UPDATE apiKeys SET name = ? WHERE id = ?`,
		name, keyID,
	)
	if err != nil {
		return fmt.Errorf("set api key name %s: %w", keyID, err)
	}
	return nil
}

// GetKVScope retrieves all key-value pairs for a given scope.
func (r *Repo) GetKVScope(scope string) (map[string]string, error) {
	rows, err := r.db.Query(`SELECT key, value FROM kv WHERE scope = ?`, scope)
	if err != nil {
		return nil, fmt.Errorf("get kv scope %s: %w", scope, err)
	}
	defer rows.Close()

	result := make(map[string]string)
	for rows.Next() {
		var key, val string
		if err := rows.Scan(&key, &val); err != nil {
			return nil, fmt.Errorf("scan kv scope %s: %w", scope, err)
		}
		result[key] = val
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate kv scope %s: %w", scope, err)
	}
	return result, nil
}

// SetKV inserts or updates a key-value pair for a given scope.
func (r *Repo) SetKV(scope, key, value string) error {
	_, err := r.db.Exec(
		`INSERT INTO kv (scope, key, value) VALUES (?, ?, ?) ON CONFLICT(scope, key) DO UPDATE SET value = excluded.value`,
		scope, key, value,
	)
	if err != nil {
		return fmt.Errorf("set kv [%s] %s: %w", scope, key, err)
	}
	return nil
}

// DeleteKV removes a key-value pair for a given scope.
func (r *Repo) DeleteKV(scope, key string) error {
	_, err := r.db.Exec(`DELETE FROM kv WHERE scope = ? AND key = ?`, scope, key)
	if err != nil {
		return fmt.Errorf("delete kv [%s] %s: %w", scope, key, err)
	}
	return nil
}

// GetSettingsRaw reads settings row id=1 data JSON into map[string]any.
// Returns an empty map if no settings row exists.
func (r *Repo) GetSettingsRaw() (map[string]any, error) {
	var rawData string
	err := r.db.QueryRow(`SELECT data FROM settings WHERE id = 1`).Scan(&rawData)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return make(map[string]any), nil
		}
		return nil, fmt.Errorf("get settings raw: %w", err)
	}
	if rawData == "" {
		return make(map[string]any), nil
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(rawData), &raw); err != nil {
		return nil, fmt.Errorf("unmarshal settings data: %w", err)
	}
	if raw == nil {
		raw = make(map[string]any)
	}
	return raw, nil
}

// UpdateSettingsRaw reads settings row id=1 data JSON into map[string]any,
// merges updates into it, re-marshals, and updates settings row id=1.
func (r *Repo) UpdateSettingsRaw(updates map[string]any) error {
	existing, err := r.GetSettingsRaw()
	if err != nil {
		existing = make(map[string]any)
	}
	for k, v := range updates {
		existing[k] = v
	}
	b, err := json.Marshal(existing)
	if err != nil {
		return fmt.Errorf("marshal settings data: %w", err)
	}
	_, err = r.db.Exec(
		`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`,
		string(b),
	)
	if err != nil {
		return fmt.Errorf("update settings raw: %w", err)
	}
	return nil
}
