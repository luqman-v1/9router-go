package db

import (
	"testing"
)

// The usage readers used to swallow every row-level failure and return the
// surviving rows with a nil error, so a mid-iteration scan failure or a
// truncated cursor reached the dashboard as a shorter-but-successful list. The
// dashboard aggregated whatever arrived, so a broken read was indistinguishable
// from "this provider had no traffic". Each case below plants a row that cannot
// be scanned and asserts the failure reaches the caller.

func TestGetUsageDailyRecent_ScanFailureIsNotSilent(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	if _, err := database.Exec(`CREATE TABLE usageDaily (
		dateKey TEXT PRIMARY KEY,
		data TEXT
	);`); err != nil {
		t.Fatalf("create usageDaily: %v", err)
	}
	// dateKey DESC ordering puts 03-02 first, so the good row is read before the
	// NULL row fails to scan.
	if _, err := database.Exec(`INSERT INTO usageDaily (dateKey, data) VALUES
		('2026-03-01', '{"requests":1}'),
		('2026-03-02', '{"requests":2}'),
		('2026-03-03', NULL);`); err != nil {
		t.Fatalf("seed usageDaily: %v", err)
	}

	rows, err := NewRepo(database).GetUsageDailyRecent(30)
	if err == nil {
		t.Fatalf("GetUsageDailyRecent returned %d rows and no error; a NULL data column must surface as an error, not a truncated list", len(rows))
	}
	if rows != nil {
		t.Errorf("rows = %v, want nil alongside the error", rows)
	}
}

func TestGetRecentUsageHistory_ScanFailureIsNotSilent(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	if _, err := database.Exec(`CREATE TABLE usageHistory (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		timestamp TEXT NOT NULL,
		provider TEXT,
		model TEXT,
		connectionId TEXT,
		apiKey TEXT,
		endpoint TEXT,
		promptTokens INTEGER,
		completionTokens INTEGER,
		cost REAL,
		status TEXT,
		tokens TEXT,
		meta TEXT
	);`); err != nil {
		t.Fatalf("create usageHistory: %v", err)
	}
	// INTEGER affinity keeps a non-numeric literal as TEXT, which cannot be
	// scanned into the reader's int destination. rowid DESC reads the good row
	// first, so the failure happens after iteration has begun.
	if _, err := database.Exec(`INSERT INTO usageHistory
		(timestamp, provider, model, promptTokens, completionTokens, cost, status, tokens) VALUES
		('2026-03-03T00:00:00Z', 'openai', 'gpt-5.5', 'not-a-number', 5, 0.1, 'success', '{}'),
		('2026-03-02T00:00:00Z', 'openai', 'gpt-5.5', 10, 5, 0.1, 'success', '{}');`); err != nil {
		t.Fatalf("seed usageHistory: %v", err)
	}

	rows, err := NewRepo(database).GetRecentUsageHistory(60)
	if err == nil {
		t.Fatalf("GetRecentUsageHistory returned %d rows and no error; an unscanable promptTokens must surface as an error", len(rows))
	}
	if rows != nil {
		t.Errorf("rows = %v, want nil alongside the error", rows)
	}
}

func TestGetUsageHistorySince_ScanFailureIsNotSilent(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	if _, err := database.Exec(`CREATE TABLE usageHistory (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		timestamp TEXT NOT NULL,
		provider TEXT,
		model TEXT,
		connectionId TEXT,
		apiKey TEXT,
		endpoint TEXT,
		promptTokens INTEGER,
		completionTokens INTEGER,
		cost REAL,
		status TEXT,
		tokens TEXT,
		meta TEXT
	);`); err != nil {
		t.Fatalf("create usageHistory: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO usageHistory
		(timestamp, provider, model, promptTokens, completionTokens, cost, status, tokens) VALUES
		('2026-03-03T00:00:00Z', 'openai', 'gpt-5.5', 'not-a-number', 5, 0.1, 'success', '{}'),
		('2026-03-02T00:00:00Z', 'openai', 'gpt-5.5', 10, 5, 0.1, 'success', '{}');`); err != nil {
		t.Fatalf("seed usageHistory: %v", err)
	}

	rows, err := NewRepo(database).GetUsageHistorySince("2026-03-01T00:00:00Z")
	if err == nil {
		t.Fatalf("GetUsageHistorySince returned %d rows and no error; an unscanable promptTokens must surface as an error", len(rows))
	}
	if rows != nil {
		t.Errorf("rows = %v, want nil alongside the error", rows)
	}
}

func TestGetRequestDetailsPaged_ScanFailureIsNotSilent(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	if _, err := database.Exec(`CREATE TABLE requestDetails (
		id TEXT PRIMARY KEY,
		timestamp TEXT NOT NULL,
		provider TEXT,
		model TEXT,
		connectionId TEXT,
		status TEXT,
		data TEXT
	);`); err != nil {
		t.Fatalf("create requestDetails: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO requestDetails (id, timestamp, provider, model, status, data) VALUES
		('req-3', '2026-03-03T00:00:00Z', 'openai', 'gpt-5.5', 'success', NULL),
		('req-2', '2026-03-02T00:00:00Z', 'openai', 'gpt-5.5', 'success', '{"ok":1}');`); err != nil {
		t.Fatalf("seed requestDetails: %v", err)
	}

	rows, total, err := NewRepo(database).GetRequestDetailsPaged(50, 0)
	if err == nil {
		t.Fatalf("GetRequestDetailsPaged returned %d rows (total %d) and no error; a NULL data column must surface as an error", len(rows), total)
	}
	if rows != nil {
		t.Errorf("rows = %v, want nil alongside the error", rows)
	}
}

// A NULL testStatus is not a broken read: the column is nullable in the shared
// schema and both dashboards write this database, so a pool that was never
// probed must still be listed. A column that cannot be decoded at all is a
// broken read and must surface.
func TestListProxyPools_NullTestStatusStillListsThePool(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	if _, err := database.Exec(`CREATE TABLE proxyPools (
		id TEXT PRIMARY KEY,
		isActive INTEGER DEFAULT 1,
		testStatus TEXT,
		data TEXT NOT NULL,
		createdAt TEXT,
		updatedAt TEXT
	);`); err != nil {
		t.Fatalf("create proxyPools: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO proxyPools (id, isActive, testStatus, data, createdAt, updatedAt) VALUES
		('pool-a', 1, 'ok', '{"proxyUrl":"http://127.0.0.1:1"}', '2026-03-03T00:00:00Z', '2026-03-03T00:00:00Z'),
		('pool-b', 1, NULL, '{"proxyUrl":"http://127.0.0.1:2"}', '2026-03-02T00:00:00Z', '2026-03-02T00:00:00Z');`); err != nil {
		t.Fatalf("seed proxyPools: %v", err)
	}

	pools, err := NewRepo(database).ListProxyPools()
	if err != nil {
		t.Fatalf("ListProxyPools on a NULL testStatus = %v; an unprobed pool is an absent value, not a failed read", err)
	}
	if len(pools) != 2 {
		t.Fatalf("pools = %d, want 2; the unprobed pool must not drop out of the list", len(pools))
	}
	// createdAt DESC puts pool-a first, so pool-b is the unprobed one.
	if got := pools[1]["testStatus"]; got != "" {
		t.Errorf("testStatus = %v, want \"\" for a NULL column", got)
	}
	if got := pools[1]["id"]; got != "pool-b" {
		t.Errorf("id = %v, want pool-b", got)
	}
}

func TestListProxyPools_ScanFailureIsNotSilent(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	// isActive is scanned into an int. INTEGER affinity keeps a non-numeric
	// literal as TEXT, which cannot be decoded. createdAt DESC reads the
	// healthy pool first, so the failure happens once iteration is under way.
	if _, err := database.Exec(`CREATE TABLE proxyPools (
		id TEXT PRIMARY KEY,
		isActive INTEGER DEFAULT 1,
		testStatus TEXT,
		data TEXT NOT NULL,
		createdAt TEXT,
		updatedAt TEXT
	);`); err != nil {
		t.Fatalf("create proxyPools: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO proxyPools (id, isActive, testStatus, data, createdAt, updatedAt) VALUES
		('pool-a', 1, 'ok', '{"proxyUrl":"http://127.0.0.1:1"}', '2026-03-03T00:00:00Z', '2026-03-03T00:00:00Z'),
		('pool-b', 'not-a-number', 'ok', '{"proxyUrl":"http://127.0.0.1:2"}', '2026-03-02T00:00:00Z', '2026-03-02T00:00:00Z');`); err != nil {
		t.Fatalf("seed proxyPools: %v", err)
	}

	pools, err := NewRepo(database).ListProxyPools()
	if err == nil {
		t.Fatalf("ListProxyPools returned %d pools and no error; an undecodable isActive must surface as an error", len(pools))
	}
	if pools != nil {
		t.Errorf("pools = %v, want nil alongside the error", pools)
	}
}