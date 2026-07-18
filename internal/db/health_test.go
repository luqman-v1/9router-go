package db

import (
	"database/sql"
	"os"
	"testing"
)

func setupHealthTestDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()

	tmpFile, err := os.CreateTemp("", "test_health_*.sqlite")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	tmpFile.Close()

	db, err := OpenDatabase(tmpFile.Name())
	if err != nil {
		os.Remove(tmpFile.Name())
		t.Fatalf("OpenDatabase failed: %v", err)
	}

	cleanup := func() {
		db.Close()
		os.Remove(tmpFile.Name())
	}

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS kv (
		scope TEXT NOT NULL,
		key TEXT NOT NULL,
		value TEXT NOT NULL,
		PRIMARY KEY (scope, key)
	);`)
	if err != nil {
		cleanup()
		t.Fatalf("failed to create kv table: %v", err)
	}

	return db, cleanup
}

func TestRecordProviderHealth_Insert_Success(t *testing.T) {
	db, cleanup := setupHealthTestDB(t)
	defer cleanup()

	err := RecordProviderHealth(db, "openai", "gpt-4", 200, 150)
	if err != nil {
		t.Fatalf("RecordProviderHealth insert failed: %v", err)
	}

	record, err := GetProviderHealth(db, "openai", "gpt-4")
	if err != nil {
		t.Fatalf("GetProviderHealth failed: %v", err)
	}
	if record == nil {
		t.Fatal("expected non-nil record")
	}
	if record.LastStatus != 200 {
		t.Errorf("expected LastStatus 200, got %d", record.LastStatus)
	}
	if record.LastLatencyMs != 150 {
		t.Errorf("expected LastLatencyMs 150, got %d", record.LastLatencyMs)
	}
	if record.ConsecutiveSuccesses != 1 {
		t.Errorf("expected ConsecutiveSuccesses 1, got %d", record.ConsecutiveSuccesses)
	}
	if record.ConsecutiveErrors != 0 {
		t.Errorf("expected ConsecutiveErrors 0, got %d", record.ConsecutiveErrors)
	}
	if record.LastChecked == "" {
		t.Error("expected non-empty LastChecked")
	}
}

func TestRecordProviderHealth_Insert_Error(t *testing.T) {
	db, cleanup := setupHealthTestDB(t)
	defer cleanup()

	err := RecordProviderHealth(db, "openai", "gpt-4", 500, 0)
	if err != nil {
		t.Fatalf("RecordProviderHealth insert failed: %v", err)
	}

	record, err := GetProviderHealth(db, "openai", "gpt-4")
	if err != nil {
		t.Fatalf("GetProviderHealth failed: %v", err)
	}
	if record == nil {
		t.Fatal("expected non-nil record")
	}
	if record.ConsecutiveErrors != 1 {
		t.Errorf("expected ConsecutiveErrors 1, got %d", record.ConsecutiveErrors)
	}
	if record.ConsecutiveSuccesses != 0 {
		t.Errorf("expected ConsecutiveSuccesses 0, got %d", record.ConsecutiveSuccesses)
	}
}

func TestRecordProviderHealth_Update_Success(t *testing.T) {
	db, cleanup := setupHealthTestDB(t)
	defer cleanup()

	err := RecordProviderHealth(db, "openai", "gpt-4", 200, 100)
	if err != nil {
		t.Fatalf("first insert failed: %v", err)
	}

	err = RecordProviderHealth(db, "openai", "gpt-4", 200, 200)
	if err != nil {
		t.Fatalf("update failed: %v", err)
	}

	record, err := GetProviderHealth(db, "openai", "gpt-4")
	if err != nil {
		t.Fatalf("GetProviderHealth failed: %v", err)
	}
	if record.ConsecutiveSuccesses != 2 {
		t.Errorf("expected ConsecutiveSuccesses 2, got %d", record.ConsecutiveSuccesses)
	}
	if record.ConsecutiveErrors != 0 {
		t.Errorf("expected ConsecutiveErrors 0, got %d", record.ConsecutiveErrors)
	}
	if record.LastLatencyMs != 200 {
		t.Errorf("expected LastLatencyMs 200, got %d", record.LastLatencyMs)
	}
}

func TestRecordProviderHealth_Update_SuccessToError(t *testing.T) {
	db, cleanup := setupHealthTestDB(t)
	defer cleanup()

	err := RecordProviderHealth(db, "openai", "gpt-4", 200, 100)
	if err != nil {
		t.Fatalf("first insert failed: %v", err)
	}

	err = RecordProviderHealth(db, "openai", "gpt-4", 500, 0)
	if err != nil {
		t.Fatalf("error update failed: %v", err)
	}

	record, err := GetProviderHealth(db, "openai", "gpt-4")
	if err != nil {
		t.Fatalf("GetProviderHealth failed: %v", err)
	}
	if record.ConsecutiveSuccesses != 0 {
		t.Errorf("expected ConsecutiveSuccesses 0, got %d", record.ConsecutiveSuccesses)
	}
	if record.ConsecutiveErrors != 1 {
		t.Errorf("expected ConsecutiveErrors 1, got %d", record.ConsecutiveErrors)
	}
	if record.LastStatus != 500 {
		t.Errorf("expected LastStatus 500, got %d", record.LastStatus)
	}
}

func TestRecordProviderHealth_Update_ErrorToSuccess(t *testing.T) {
	db, cleanup := setupHealthTestDB(t)
	defer cleanup()

	err := RecordProviderHealth(db, "openai", "gpt-4", 500, 0)
	if err != nil {
		t.Fatalf("first insert failed: %v", err)
	}

	err = RecordProviderHealth(db, "openai", "gpt-4", 200, 100)
	if err != nil {
		t.Fatalf("success update failed: %v", err)
	}

	record, err := GetProviderHealth(db, "openai", "gpt-4")
	if err != nil {
		t.Fatalf("GetProviderHealth failed: %v", err)
	}
	if record.ConsecutiveErrors != 0 {
		t.Errorf("expected ConsecutiveErrors 0, got %d", record.ConsecutiveErrors)
	}
	if record.ConsecutiveSuccesses != 1 {
		t.Errorf("expected ConsecutiveSuccesses 1, got %d", record.ConsecutiveSuccesses)
	}
}

func TestRecordProviderHealth_Update_IncrementErrors(t *testing.T) {
	db, cleanup := setupHealthTestDB(t)
	defer cleanup()

	for i := 0; i < 3; i++ {
		err := RecordProviderHealth(db, "openai", "gpt-4", 500, 0)
		if err != nil {
			t.Fatalf("error record %d failed: %v", i, err)
		}
	}

	record, err := GetProviderHealth(db, "openai", "gpt-4")
	if err != nil {
		t.Fatalf("GetProviderHealth failed: %v", err)
	}
	if record.ConsecutiveErrors != 3 {
		t.Errorf("expected ConsecutiveErrors 3, got %d", record.ConsecutiveErrors)
	}
}

func TestGetProviderHealth_NotFound(t *testing.T) {
	db, cleanup := setupHealthTestDB(t)
	defer cleanup()

	record, err := GetProviderHealth(db, "nonexistent", "model")
	if err != nil {
		t.Fatalf("GetProviderHealth failed: %v", err)
	}
	if record != nil {
		t.Error("expected nil record for non-existent provider")
	}
}

func TestGetProviderHealth_Exists(t *testing.T) {
	db, cleanup := setupHealthTestDB(t)
	defer cleanup()

	err := RecordProviderHealth(db, "anthropic", "claude-3", 200, 50)
	if err != nil {
		t.Fatalf("RecordProviderHealth failed: %v", err)
	}

	record, err := GetProviderHealth(db, "anthropic", "claude-3")
	if err != nil {
		t.Fatalf("GetProviderHealth failed: %v", err)
	}
	if record == nil {
		t.Fatal("expected non-nil record")
	}
	if record.LastLatencyMs != 50 {
		t.Errorf("expected LastLatencyMs 50, got %d", record.LastLatencyMs)
	}
}

func TestIsProviderHealthy_NoRecord(t *testing.T) {
	db, cleanup := setupHealthTestDB(t)
	defer cleanup()

	healthy := IsProviderHealthy(db, "unknown", "model")
	if !healthy {
		t.Error("expected healthy=true when no record (optimistic)")
	}
}

func TestIsProviderHealthy_BelowThreshold(t *testing.T) {
	db, cleanup := setupHealthTestDB(t)
	defer cleanup()

	for i := 0; i < 4; i++ {
		RecordProviderHealth(db, "openai", "gpt-4", 500, 0)
	}

	healthy := IsProviderHealthy(db, "openai", "gpt-4")
	if !healthy {
		t.Error("expected healthy=true when consecutiveErrors=4 (threshold=5)")
	}
}

func TestIsProviderHealthy_AtThreshold(t *testing.T) {
	db, cleanup := setupHealthTestDB(t)
	defer cleanup()

	for i := 0; i < 5; i++ {
		RecordProviderHealth(db, "openai", "gpt-4", 500, 0)
	}

	healthy := IsProviderHealthy(db, "openai", "gpt-4")
	if healthy {
		t.Error("expected healthy=false when consecutiveErrors=5 (threshold=5)")
	}
}

func TestIsProviderHealthy_AboveThreshold(t *testing.T) {
	db, cleanup := setupHealthTestDB(t)
	defer cleanup()

	for i := 0; i < 6; i++ {
		RecordProviderHealth(db, "openai", "gpt-4", 500, 0)
	}

	healthy := IsProviderHealthy(db, "openai", "gpt-4")
	if healthy {
		t.Error("expected healthy=false when consecutiveErrors=6 > threshold=5")
	}
}

func TestIsProviderHealthy_AfterRecovery(t *testing.T) {
	db, cleanup := setupHealthTestDB(t)
	defer cleanup()

	// 5 errors -> unhealthy
	for i := 0; i < 5; i++ {
		RecordProviderHealth(db, "openai", "gpt-4", 500, 0)
	}
	if IsProviderHealthy(db, "openai", "gpt-4") {
		t.Fatal("expected unhealthy after 5 errors")
	}

	// 1 success -> back to healthy, errors reset
	RecordProviderHealth(db, "openai", "gpt-4", 200, 100)
	healthy := IsProviderHealthy(db, "openai", "gpt-4")
	if !healthy {
		t.Error("expected healthy=true after success following errors")
	}
}
