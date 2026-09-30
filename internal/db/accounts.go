package db

import (
	"database/sql"
	json "encoding/json/v2"
	"fmt"
	"strings"
	"time"
)

const modelLockPrefix = "modelLock_"

// modelLockDataKey builds the JSON key used to store a model lock in the
// providerConnections.data blob. Matches Next.js flat field key format
// so dashboard can read modelLock_* fields across shared DB.
func modelLockDataKey(model string) string {
	// Quote the path segment so a model containing '.', '"', '[', ']' etc.
	// is treated as a literal key name, not JSON path syntax.
	return "$.\"" + modelLockPrefix + model + "\""
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

// ConnectionCooldownUntil reads the account-scoped quota cooldown a connection
// carries in its data blob, mirroring upstream filterAvailableAccounts
// (open-sse/services/accountFallback.js:180), which skips any account whose
// rateLimitedUntil is still in the future.
//
// Unlike the model lock this cooldown is not keyed by model, which is what
// catches a quota spent account-wide, and it is the only quota cooldown the
// selector can consult when the request carries no model at all. An account
// parked for a rejected OAuth grant carries its own timestamp instead; read
// both through ConnectionBlockedUntil.
//
// A missing, empty or unparseable value reports "not in cooldown" so a
// malformed field can never take routing down.
func ConnectionCooldownUntil(rawData string) (time.Time, bool) {
	raw, ok := parseConnData(rawData)
	if !ok {
		return time.Time{}, false
	}
	return readTimestampField(raw, "rateLimitedUntil")
}

// LockConnectionRateLimit stores the account-scoped cooldown and the error
// that caused it, mirroring upstream applyErrorState
// (open-sse/services/accountFallback.js:216). The dashboard already reads
// rateLimitedUntil to render "reset at", but nothing in the Go port ever
// wrote it, so that field was always empty and the selector had no
// account-scoped signal to skip on.
func (r *Repo) LockConnectionRateLimit(connID string, until time.Time, backoffLevel, status int, errText string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	lastError := map[string]any{
		"status":    status,
		"message":   errText,
		"timestamp": now,
	}
	payload, err := json.Marshal(lastError)
	if err != nil {
		return fmt.Errorf("marshal last error for %s: %w", connID, err)
	}
	_, err = r.db.Exec(
		`UPDATE providerConnections
		 SET data = json_set(data,
		   '$.rateLimitedUntil', ?,
		   '$.backoffLevel', ?,
		   '$.lastError', json(?),
		   '$.status', 'error'),
		     updatedAt = ?
		 WHERE id = ?`,
		until.UTC().Format(time.RFC3339), backoffLevel, string(payload), now, connID,
	)
	if err != nil {
		return fmt.Errorf("lock connection rate limit %s: %w", connID, err)
	}
	return nil
}

// ClearConnectionRateLimit drops the account-scoped cooldown after a request
// was served, mirroring upstream resetAccountState
// (open-sse/services/accountFallback.js:198). Without it a recovered account
// would stay skipped until the cooldown expired on its own. The per-model
// locks are deliberately left alone: a success on one model must not unlock a
// model that is still in cooldown.
func (r *Repo) ClearConnectionRateLimit(connID string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := r.db.Exec(
		`UPDATE providerConnections
		 SET data = json_set(data,
		   '$.rateLimitedUntil', NULL,
		   '$.lastError', NULL,
		   '$.status', 'active',
		   '$.backoffLevel', 0),
		     updatedAt = ?
		 WHERE id = ?`,
		now, connID,
	)
	if err != nil {
		return fmt.Errorf("clear connection rate limit %s: %w", connID, err)
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

// ResetConnectionHealthState clears model locks, error codes, rate limits, and resets backoff
// on a connection when activated or re-validated (parity with #3830 / #7fee56ba).
func (r *Repo) ResetConnectionHealthState(connID string) error {
	var rawData string
	err := r.db.QueryRow("SELECT data FROM providerConnections WHERE id = ?", connID).Scan(&rawData)
	if err != nil {
		return err
	}
	var dataMap map[string]any
	if err := json.Unmarshal([]byte(rawData), &dataMap); err != nil {
		return err
	}

	dataMap["errorCode"] = nil
	dataMap["rateLimitedUntil"] = nil
	dataMap["oauthLockedUntil"] = nil
	dataMap["oauthFailureCount"] = 0
	dataMap["backoffLevel"] = 0

	for k := range dataMap {
		if strings.HasPrefix(k, modelLockPrefix) {
			dataMap[k] = nil
		}
	}

	newBytes, err := json.Marshal(dataMap)
	if err != nil {
		return err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	_, err = r.db.Exec("UPDATE providerConnections SET data = ?, updatedAt = ? WHERE id = ?", string(newBytes), now, connID)
	return err
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
