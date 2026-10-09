package db

import (
	json "encoding/json/v2"
	"testing"
	"time"
)

func readConnData(t *testing.T, repo *Repo, connID string) string {
	t.Helper()
	var raw string
	if err := repo.RawDB().QueryRow(`SELECT data FROM providerConnections WHERE id = ?`, connID).Scan(&raw); err != nil {
		t.Fatalf("read connection data: %v", err)
	}
	return raw
}

// A written cooldown must be readable by the selector, and clearing it must
// put the account back in rotation. Before this the dashboard read
// rateLimitedUntil but nothing in the Go port ever wrote it, so the field was
// always empty and the selector had no account-scoped signal to skip on.
func TestLockConnectionRateLimit_RoundTripsThroughCooldownUntil(t *testing.T) {
	repo, cleanup := setupHealthConnTestDB(t)
	defer cleanup()
	insertTestConn(t, repo)

	until := time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second)
	if err := repo.LockConnectionRateLimit("conn-1", until, 2, 429, "quota exhausted"); err != nil {
		t.Fatalf("LockConnectionRateLimit: %v", err)
	}

	got, ok := ConnectionCooldownUntil(readConnData(t, repo, "conn-1"))
	if !ok {
		t.Fatal("expected the connection to carry a cooldown")
	}
	if !got.Equal(until) {
		t.Errorf("cooldown until = %s, want %s", got.Format(time.RFC3339), until.Format(time.RFC3339))
	}
}

func TestLockConnectionRateLimit_RecordsTheError(t *testing.T) {
	repo, cleanup := setupHealthConnTestDB(t)
	defer cleanup()
	insertTestConn(t, repo)

	if err := repo.LockConnectionRateLimit("conn-1", time.Now().UTC().Add(time.Minute), 1, 401, "authentication expired"); err != nil {
		t.Fatalf("LockConnectionRateLimit: %v", err)
	}

	raw := readConnData(t, repo, "conn-1")
	var data map[string]any
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	if data["status"] != "error" {
		t.Errorf("status = %v, want error", data["status"])
	}
	lastError, ok := data["lastError"].(map[string]any)
	if !ok {
		t.Fatalf("lastError missing from the connection data: %s", raw)
	}
	if lastError["message"] != "authentication expired" {
		t.Errorf("lastError.message = %v, want %q", lastError["message"], "authentication expired")
	}
}

func TestClearConnectionRateLimit_PutsAccountBackInRotation(t *testing.T) {
	repo, cleanup := setupHealthConnTestDB(t)
	defer cleanup()
	insertTestConn(t, repo)

	if err := repo.LockConnectionRateLimit("conn-1", time.Now().UTC().Add(5*time.Minute), 2, 429, "quota exhausted"); err != nil {
		t.Fatalf("LockConnectionRateLimit: %v", err)
	}
	if err := repo.ClearConnectionRateLimit("conn-1"); err != nil {
		t.Fatalf("ClearConnectionRateLimit: %v", err)
	}

	if _, ok := ConnectionCooldownUntil(readConnData(t, repo, "conn-1")); ok {
		t.Error("expected the cooldown to be cleared so the account is selectable again")
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(readConnData(t, repo, "conn-1")), &data); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	if data["status"] != "active" {
		t.Errorf("status = %v, want active", data["status"])
	}
	if data["lastError"] != nil {
		t.Errorf("lastError = %v, want nil", data["lastError"])
	}
}

// Clearing the account cooldown must not unlock a per-model lock, or a success
// on one model would release a model that is still in cooldown.
func TestClearConnectionRateLimit_KeepsModelLocks(t *testing.T) {
	repo, cleanup := setupHealthConnTestDB(t)
	defer cleanup()
	insertTestConn(t, repo)

	if err := repo.LockConnectionModel("conn-1", "gpt-4", 600, 1); err != nil {
		t.Fatalf("LockConnectionModel: %v", err)
	}
	if err := repo.ClearConnectionRateLimit("conn-1"); err != nil {
		t.Fatalf("ClearConnectionRateLimit: %v", err)
	}
	if locked, err := repo.IsConnectionModelLocked("conn-1", "gpt-4"); err != nil || !locked {
		t.Errorf("model lock was dropped by the account-cooldown clear (locked=%v err=%v)", locked, err)
	}
}

// A malformed or absent field must never take routing down: the selector
// treats "cannot tell" as "not in cooldown".
func TestConnectionCooldownUntil_FailsOpen(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{"empty data", "", false},
		{"invalid json", "not json at all", false},
		{"key missing", `{"apiKey":"sk-x"}`, false},
		{"key null", `{"rateLimitedUntil":null}`, false},
		{"key empty string", `{"rateLimitedUntil":""}`, false},
		{"unparseable timestamp", `{"rateLimitedUntil":"soon"}`, false},
		{"wrong type", `{"rateLimitedUntil":12345}`, false},
		{"valid timestamp", `{"rateLimitedUntil":"2030-01-01T00:00:00Z"}`, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := ConnectionCooldownUntil(tc.raw); ok != tc.want {
				t.Errorf("ConnectionCooldownUntil(%s) ok = %v, want %v", tc.raw, ok, tc.want)
			}
		})
	}
}

func TestRecordConnectionScopedError_StoresModelAndSource(t *testing.T) {
	repo, cleanup := setupHealthConnTestDB(t)
	defer cleanup()
	insertTestConn(t, repo)

	if err := repo.RecordConnectionScopedError("conn-1", "mimo-v2.5-free", "chat", 401, "Model is not supported", 1); err != nil {
		t.Fatalf("RecordConnectionScopedError: %v", err)
	}

	raw := readConnData(t, repo, "conn-1")
	var data map[string]any
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	lastError, ok := data["lastError"].(map[string]any)
	if !ok {
		t.Fatalf("lastError missing from connection data: %s", raw)
	}
	if lastError["model"] != "mimo-v2.5-free" {
		t.Errorf("lastError.model = %v, want mimo-v2.5-free", lastError["model"])
	}
	if lastError["source"] != "chat" {
		t.Errorf("lastError.source = %v, want chat", lastError["source"])
	}
	if lastError["message"] != "Model is not supported" {
		t.Errorf("lastError.message = %v, want Model is not supported", lastError["message"])
	}
	// Test that empty model and source are omitted from lastError map
	if err := repo.RecordConnectionScopedError("conn-1", "", "", 401, "Invalid API key", 1); err != nil {
		t.Fatalf("RecordConnectionScopedError with empty model: %v", err)
	}
	rawEmpty := readConnData(t, repo, "conn-1")
	var dataEmpty map[string]any
	if err := json.Unmarshal([]byte(rawEmpty), &dataEmpty); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	lastErrEmpty, ok := dataEmpty["lastError"].(map[string]any)
	if !ok {
		t.Fatalf("lastError missing from connection data: %s", rawEmpty)
	}
	if _, present := lastErrEmpty["model"]; present {
		t.Errorf("lastError.model must be absent when empty, got %v", lastErrEmpty["model"])
	}
	if _, present := lastErrEmpty["source"]; present {
		t.Errorf("lastError.source must be absent when empty, got %v", lastErrEmpty["source"])
	}
	if lastErrEmpty["message"] != "Invalid API key" {
		t.Errorf("lastError.message = %v, want Invalid API key", lastErrEmpty["message"])
	}

}
