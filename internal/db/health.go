package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// HealthRecord tracks the health status of a provider/model combination.
type HealthRecord struct {
	LastStatus           int    `json:"lastStatus"`
	LastLatencyMs        int64  `json:"lastLatencyMs"`
	LastChecked          string `json:"lastChecked"`
	ConsecutiveErrors    int    `json:"consecutiveErrors"`
	ConsecutiveSuccesses int    `json:"consecutiveSuccesses"`
}

// unhealthyThreshold is the number of consecutive errors before a provider is considered unhealthy.
const unhealthyThreshold = 5

// RecordProviderHealth inserts or updates a health record for a provider/model pair in the kv table.
// On success (2xx status), consecutiveErrors resets and consecutiveSuccesses increments.
// On error (non-2xx), consecutiveSuccesses resets and consecutiveErrors increments.
func RecordProviderHealth(database *sql.DB, provider, model string, statusCode int, latencyMs int64) error {
	key := fmt.Sprintf("%s/%s", provider, model)
	now := time.Now().UTC().Format(time.RFC3339)

	// Try to read existing record
	existing, _ := GetProviderHealth(database, provider, model)

	record := HealthRecord{
		LastStatus:    statusCode,
		LastLatencyMs: latencyMs,
		LastChecked:   now,
	}

	if existing != nil {
		if statusCode >= 200 && statusCode < 300 {
			record.ConsecutiveErrors = 0
			record.ConsecutiveSuccesses = existing.ConsecutiveSuccesses + 1
		} else {
			record.ConsecutiveSuccesses = 0
			record.ConsecutiveErrors = existing.ConsecutiveErrors + 1
		}
	} else {
		if statusCode >= 200 && statusCode < 300 {
			record.ConsecutiveSuccesses = 1
			record.ConsecutiveErrors = 0
		} else {
			record.ConsecutiveErrors = 1
			record.ConsecutiveSuccesses = 0
		}
	}

	valueBytes, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("failed to marshal health record: %w", err)
	}

	_, err = database.Exec(
		`INSERT INTO kv (scope, key, value) VALUES ('providerHealth', ?, ?)
		 ON CONFLICT(scope, key) DO UPDATE SET value = excluded.value`,
		key, string(valueBytes),
	)
	return err
}

// GetProviderHealth retrieves the health record for a provider/model pair.
// Returns nil, nil when no record exists.
func GetProviderHealth(database *sql.DB, provider, model string) (*HealthRecord, error) {
	key := fmt.Sprintf("%s/%s", provider, model)
	var raw string
	err := database.QueryRow(
		"SELECT value FROM kv WHERE scope = 'providerHealth' AND key = ? LIMIT 1",
		key,
	).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var record HealthRecord
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		return nil, fmt.Errorf("failed to unmarshal health record: %w", err)
	}
	return &record, nil
}

// IsProviderHealthy checks if a provider/model is considered healthy.
// A provider is unhealthy when it has >= unhealthyThreshold consecutive errors.
// Providers with no health record are considered healthy (optimistic default).
func IsProviderHealthy(database *sql.DB, provider, model string) bool {
	record, err := GetProviderHealth(database, provider, model)
	if err != nil || record == nil {
		return true // optimistic: no data means healthy
	}
	return record.ConsecutiveErrors < unhealthyThreshold
}
