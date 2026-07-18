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
	LockedUntil string `json:"lockedUntil"`
	LastError   string `json:"lastError"`
	ErrorCode   int    `json:"errorCode"`
}

// LockModel inserts or replaces a model lock in the kv table.
// The key is formatted as "PROVIDER/MODEL" (uppercase).
// durationSec controls how long the lock lasts from now.
func (r *Repo) LockModel(provider, model string, durationSec int, lastError string, errorCode int) error {
	key := strings.ToUpper(provider + "/" + model)
	lock := ModelLock{
		LockedUntil: time.Now().UTC().Add(time.Duration(durationSec) * time.Second).Format(time.RFC3339),
		LastError:   lastError,
		ErrorCode:   errorCode,
	}
	valueBytes, err := json.Marshal(lock)
	if err != nil {
		return fmt.Errorf("failed to marshal model lock: %w", err)
	}

	_, err = r.db.Exec(
		`INSERT OR REPLACE INTO kv (scope, key, value) VALUES ('modelLock', ?, ?)`,
		key, string(valueBytes),
	)
	return err
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
		return false, err
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
	return err
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
		return nil, err
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
