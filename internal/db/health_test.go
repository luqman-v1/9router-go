package db

import (
	"testing"
)

func setupHealthConnTestDB(t *testing.T) (*Repo, func()) {
	t.Helper()

	database, cleanup := setupTestDB(t)

	_, err := database.Exec(`CREATE TABLE IF NOT EXISTS providerConnections (
		id TEXT PRIMARY KEY,
		provider TEXT NOT NULL,
		authType TEXT,
		name TEXT,
		email TEXT,
		priority INTEGER,
		isActive INTEGER DEFAULT 1,
		data TEXT NOT NULL DEFAULT '{}',
		createdAt TEXT NOT NULL DEFAULT (datetime('now')),
		updatedAt TEXT NOT NULL DEFAULT (datetime('now'))
	)`)
	if err != nil {
		cleanup()
		t.Fatalf("create providerConnections: %v", err)
	}

	repo := NewRepo(database)
	return repo, cleanup
}

func insertTestConn(t *testing.T, repo *Repo) {
	t.Helper()
	_, err := repo.RawDB().Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, isActive, data, createdAt, updatedAt) VALUES (?, ?, 'api_key', ?, 1, '{}', datetime('now'), datetime('now'))`,
		"conn-1", "openai", "test-conn",
	)
	if err != nil {
		t.Fatalf("insert connection: %v", err)
	}
}

func insertTestConns(t *testing.T, repo *Repo, conns ...string) {
	t.Helper()
	for i, id := range conns {
		_, err := repo.RawDB().Exec(
			`INSERT INTO providerConnections (id, provider, authType, name, isActive, data, createdAt, updatedAt) VALUES (?, ?, 'api_key', ?, 1, '{}', datetime('now'), datetime('now'))`,
			id, "openai", id,
		)
		if err != nil {
			t.Fatalf("insert connection %d: %v", i, err)
		}
	}
}

func TestIsProviderHealthy_NoConnections(t *testing.T) {
	repo, cleanup := setupHealthConnTestDB(t)
	defer cleanup()

	if !IsProviderHealthy(repo.RawDB(), "openai", "gpt-4") {
		t.Error("expected healthy=true when no connections exist")
	}
}

func TestIsProviderHealthy_NoModelLock(t *testing.T) {
	repo, cleanup := setupHealthConnTestDB(t)
	defer cleanup()

	insertTestConn(t, repo)

	if !IsProviderHealthy(repo.RawDB(), "openai", "gpt-4") {
		t.Error("expected healthy=true when no model lock exists")
	}
}

func TestIsProviderHealthy_WithModelLock(t *testing.T) {
	repo, cleanup := setupHealthConnTestDB(t)
	defer cleanup()

	insertTestConn(t, repo)

	err := repo.LockConnectionModel("conn-1", "gpt-4", 3600, 1)
	if err != nil {
		t.Fatalf("LockConnectionModel: %v", err)
	}

	if IsProviderHealthy(repo.RawDB(), "openai", "gpt-4") {
		t.Error("expected healthy=false when all connections have active model lock")
	}
}

func TestIsProviderHealthy_WithUnlockedConnection(t *testing.T) {
	repo, cleanup := setupHealthConnTestDB(t)
	defer cleanup()

	insertTestConns(t, repo, "conn-1", "conn-2")

	err := repo.LockConnectionModel("conn-1", "gpt-4", 3600, 1)
	if err != nil {
		t.Fatalf("LockConnectionModel: %v", err)
	}

	if !IsProviderHealthy(repo.RawDB(), "openai", "gpt-4") {
		t.Error("expected healthy=true when at least one connection is unlocked")
	}
}

func TestResetProviderHealth_ClearsModelLocks(t *testing.T) {
	repo, cleanup := setupHealthConnTestDB(t)
	defer cleanup()

	insertTestConn(t, repo)

	err := repo.LockConnectionModel("conn-1", "gpt-4", 3600, 1)
	if err != nil {
		t.Fatalf("LockConnectionModel: %v", err)
	}

	err = repo.ResetProviderHealth("openai", "gpt-4")
	if err != nil {
		t.Fatalf("ResetProviderHealth: %v", err)
	}

	if !IsProviderHealthy(repo.RawDB(), "openai", "gpt-4") {
		t.Error("expected healthy=true after resetting health")
	}
}

func TestIsProviderAvailable_Repo(t *testing.T) {
	repo, cleanup := setupHealthConnTestDB(t)
	defer cleanup()

	insertTestConn(t, repo)

	if !repo.IsProviderAvailable("openai", "gpt-4") {
		t.Error("expected available=true when no lock")
	}

	repo.LockConnectionModel("conn-1", "gpt-4", 3600, 1)

	if repo.IsProviderAvailable("openai", "gpt-4") {
		t.Error("expected available=false when locked")
	}
}
