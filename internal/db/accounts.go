package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ModelLock represents a lock entry stored in the kv table.
type ModelLock struct {
	LockedUntil  string `json:"lockedUntil"`
	LastError    string `json:"lastError"`
	ErrorCode    int    `json:"errorCode"`
	BackoffLevel int    `json:"backoffLevel,omitempty"`
}

// LockModel inserts or replaces a model lock in the kv table.
// The key is formatted as "PROVIDER/MODEL" (uppercase).
// durationSec controls how long the lock lasts from now.
func (r *Repo) LockModel(provider, model string, durationSec int, lastError string, errorCode int, backoffLevel int) error {
	key := strings.ToUpper(provider + "/" + model)
	lock := ModelLock{
		LockedUntil:  time.Now().UTC().Add(time.Duration(durationSec) * time.Second).Format(time.RFC3339),
		LastError:    lastError,
		ErrorCode:    errorCode,
		BackoffLevel: backoffLevel,
	}
	valueBytes, err := json.Marshal(lock)
	if err != nil {
		return fmt.Errorf("failed to marshal model lock: %w", err)
	}

	_, err = r.db.Exec(
		`INSERT OR REPLACE INTO kv (scope, key, value) VALUES ('modelLock', ?, ?)`,
		key, string(valueBytes),
	)
	if err != nil {
		return fmt.Errorf("lock model %s: %w", key, err)
	}
	return nil
}

// IsModelLocked checks whether a model lock exists and has not expired.
func (r *Repo) IsModelLocked(provider, model string) (bool, error) {
	key := strings.ToUpper(provider + "/" + model)
	var rawValue string
	err := r.db.QueryRow(
		"SELECT value FROM kv WHERE scope = 'modelLock' AND key = ? LIMIT 1",
		key,
	).Scan(&rawValue)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("is model locked %s: %w", key, err)
	}

	var lock ModelLock
	if err := json.Unmarshal([]byte(rawValue), &lock); err != nil {
		return false, nil // malformed lock — treat as unlocked
	}

	parsed, err := time.Parse(time.RFC3339, lock.LockedUntil)
	if err != nil {
		return false, nil // unparseable time — treat as unlocked
	}

	return time.Now().UTC().Before(parsed), nil
}

// UnlockModel removes a model lock from the kv table.
func (r *Repo) UnlockModel(provider, model string) error {
	key := strings.ToUpper(provider + "/" + model)
	_, err := r.db.Exec(
		"DELETE FROM kv WHERE scope = 'modelLock' AND key = ?",
		key,
	)
	if err != nil {
		return fmt.Errorf("unlock model %s: %w", key, err)
	}
	return nil
}

// GetModelLock retrieves the current lock details for a model, if any.
// Returns nil when no lock row exists (expired or missing).
func (r *Repo) GetModelLock(provider, model string) (*ModelLock, error) {
	key := strings.ToUpper(provider + "/" + model)
	var rawValue string
	err := r.db.QueryRow(
		"SELECT value FROM kv WHERE scope = 'modelLock' AND key = ? LIMIT 1",
		key,
	).Scan(&rawValue)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get model lock %s: %w", key, err)
	}

	var lock ModelLock
	if err := json.Unmarshal([]byte(rawValue), &lock); err != nil {
		return nil, nil
	}

	parsed, err := time.Parse(time.RFC3339, lock.LockedUntil)
	if err != nil {
		return nil, nil
	}
	if time.Now().UTC().After(parsed) {
		return nil, nil // expired
	}

	return &lock, nil
}

const modelLockPrefix = "modelLock_"

// modelLockDataKey builds the JSON key used to store a model lock in the
// providerConnections.data blob. Matches Next.js flat field key format
// so dashboard can read modelLock_* fields across shared DB.
func modelLockDataKey(model string) string {
	return "$." + modelLockPrefix + model
}

// LockConnectionModel stores a per-connection model lock in the
// providerConnections.data JSON blob using SQLite json_set().
// Also stores backoffLevel. Matches Next.js markAccountUnavailable.
// The stored data is readable by the shared Next.js dashboard.
func (r *Repo) LockConnectionModel(connID, model string, durationSec int, backoffLevel int) error {
	lockedUntil := time.Now().UTC().Add(time.Duration(durationSec) * time.Second).Format(time.RFC3339)
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := r.db.Exec(
		`UPDATE providerConnections SET data = json_set(data, ?, ?, '$.backoffLevel', ?), updatedAt = ? WHERE id = ?`,
		modelLockDataKey(model), lockedUntil, backoffLevel, now, connID,
	)
	if err != nil {
		return fmt.Errorf("lock connection model %s/%s: %w", connID, model, err)
	}
	return nil
}

// IsConnectionModelLocked checks whether the given connection has an active
// modelLock_<model> field in its data JSON blob. Returns true when the
// timestamp is in the future.
func (r *Repo) IsConnectionModelLocked(connID, model string) (bool, error) {
	var rawData string
	err := r.db.QueryRow("SELECT data FROM providerConnections WHERE id = ?", connID).Scan(&rawData)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get connection data %s: %w", connID, err)
	}

	var raw map[string]any
	if err := json.Unmarshal([]byte(rawData), &raw); err != nil {
		return false, nil // malformed data — treat as unlocked
	}

	key := modelLockPrefix + model
	val, ok := raw[key]
	if !ok {
		return false, nil
	}
	str, ok := val.(string)
	if !ok || str == "" {
		return false, nil
	}
	t, err := time.Parse(time.RFC3339, str)
	if err != nil {
		return false, nil
	}
	return time.Now().UTC().Before(t), nil
}

// UnlockConnectionModel removes a per-connection model lock by setting
// modelLock_<model> to null and resetting backoffLevel to 0.
// Matches Next.js clearAccountError behavior.
func (r *Repo) UnlockConnectionModel(connID, model string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := r.db.Exec(
		`UPDATE providerConnections SET data = json_set(data, ?, json('null'), '$.backoffLevel', 0), updatedAt = ? WHERE id = ?`,
		modelLockDataKey(model), now, connID,
	)
	if err != nil {
		return fmt.Errorf("unlock connection model %s/%s: %w", connID, model, err)
	}
	return nil
}

// GetConnectionBackoffLevel reads the backoffLevel from a connection's data.
// Returns 0 when not set or on error.
func (r *Repo) GetConnectionBackoffLevel(connID string) int {
	var rawData string
	err := r.db.QueryRow("SELECT data FROM providerConnections WHERE id = ?", connID).Scan(&rawData)
	if err != nil {
		return 0
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(rawData), &raw); err != nil {
		return 0
	}
	level, ok := raw["backoffLevel"].(float64)
	if !ok {
		return 0
	}
	return int(level)
}

// IsProviderAvailable returns true if at least one active connection for the
// given provider has no active modelLock_<model>. This is the connection-based
// replacement for the old kv-based IsProviderHealthy, matching Next.js's
// isModelLockActive + filterAvailableAccounts flow.
// Returns true when no connections exist (optimistic default for no-auth providers).
func (r *Repo) IsProviderAvailable(provider, model string) bool {
	if provider == "" {
		return true
	}
	conns, err := r.GetProviderConnections(provider, true)
	if err != nil || len(conns) == 0 {
		return true
	}
	for _, c := range conns {
		locked, err := r.IsConnectionModelLocked(c.ID, model)
		if err == nil && !locked {
			return true
		}
	}
	return false
}

// ResetProviderHealth clears modelLock_* fields on connections, matching
// Next.js clearAccountError semantics.
//   - provider="" and model="" → all connections
//   - model="" → all connections for provider
//   - both set → specific provider + model on all its connections
func (r *Repo) ResetProviderHealth(provider, model string) error {
	if provider == "" && model == "" {
		return r.clearAllModelLocks()
	}
	if model == "" {
		return r.clearProviderModelLocks(provider)
	}
	return r.clearSpecificModelLock(provider, model)
}

func (r *Repo) clearAllModelLocks() error {
	rows, err := r.db.Query("SELECT id, data FROM providerConnections")
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var id, data string
		if err := rows.Scan(&id, &data); err != nil {
			return err
		}
		if err := r.clearConnectionModelLocks(id, data); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (r *Repo) clearProviderModelLocks(provider string) error {
	rows, err := r.db.Query("SELECT id, data FROM providerConnections WHERE provider = ?", provider)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var id, data string
		if err := rows.Scan(&id, &data); err != nil {
			return err
		}
		if err := r.clearConnectionModelLocks(id, data); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (r *Repo) clearSpecificModelLock(provider, model string) error {
	key := "$." + modelLockPrefix + model
	rows, err := r.db.Query("SELECT id, data FROM providerConnections WHERE provider = ?", provider)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var id, data string
		if err := rows.Scan(&id, &data); err != nil {
			return err
		}
		locked := checkConnectionModelLock(data, model)
		if !locked {
			continue
		}
		if _, err := r.db.Exec(
			`UPDATE providerConnections SET data = json_set(data, ?, json('null'), '$.backoffLevel', 0), updatedAt = datetime('now') WHERE id = ?`,
			key, id,
		); err != nil {
			return fmt.Errorf("clear lock %s on %s: %w", key, id, err)
		}
	}
	return rows.Err()
}

func (r *Repo) clearConnectionModelLocks(id, data string) error {
	fields := listModelLockJSONKeys(data)
	if len(fields) == 0 {
		return nil
	}
	q := "UPDATE providerConnections SET data = json_set(data"
	args := make([]any, 0, len(fields)*2+2)
	for _, f := range fields {
		q += ", ?, json('null')"
		args = append(args, "$."+f)
	}
	q += ", '$.backoffLevel', 0), updatedAt = datetime('now') WHERE id = ?"
	args = append(args, id)
	_, err := r.db.Exec(q, args...)
	return err
}

// checkConnectionModelLock parses connection data to check if modelLock_<model> is active.
func checkConnectionModelLock(data string, model string) bool {
	var raw map[string]any
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		return false
	}
	key := modelLockPrefix + model
	val, ok := raw[key]
	if !ok {
		return false
	}
	str, ok := val.(string)
	if !ok || str == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, str)
	if err != nil {
		return false
	}
	return time.Now().UTC().Before(t)
}

// listModelLockJSONKeys returns all top-level JSON keys starting with modelLockPrefix.
func listModelLockJSONKeys(data string) []string {
	var raw map[string]any
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		return nil
	}
	var keys []string
	for k := range raw {
		if len(k) > len(modelLockPrefix) && k[:len(modelLockPrefix)] == modelLockPrefix {
			keys = append(keys, k)
		}
	}
	return keys
}
