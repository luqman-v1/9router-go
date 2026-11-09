package db

import (
	"database/sql"
	"testing"
	"time"
)

// The rotation candidate list belongs to one database. Scoped as package state
// instead, a Repo over a second handle answered with the first one's pools.
func TestActivePoolIDs_AreScopedToTheirRepo(t *testing.T) {
	first, cleanupFirst := setupTestDB(t)
	defer cleanupFirst()
	second, cleanupSecond := setupTestDB(t)
	defer cleanupSecond()

	for _, d := range []*sql.DB{first, second} {
		if _, err := d.Exec(`CREATE TABLE IF NOT EXISTS proxyPools (
			id TEXT PRIMARY KEY,
			isActive INTEGER DEFAULT 1,
			testStatus TEXT,
			data TEXT NOT NULL,
			createdAt TEXT NOT NULL,
			updatedAt TEXT NOT NULL
		);`); err != nil {
			t.Fatalf("create proxyPools: %v", err)
		}
	}

	repoA := NewRepo(first)
	repoB := NewRepo(second)

	poolA, err := repoA.InsertProxyPool(ProxyPoolData{Name: "a", ProxyURL: "http://172.17.0.1:8001", Type: "http"})
	if err != nil {
		t.Fatalf("insert pool a: %v", err)
	}
	idA := poolA["id"].(string)

	if got := repoB.ActivePoolIDs(); len(got) != 0 {
		t.Fatalf("empty database reported pools %v, want none", got)
	}
	if got := repoA.ActivePoolIDs(); len(got) != 1 || got[0] != idA {
		t.Fatalf("repoA pools = %v, want [%s]", got, idA)
	}

	// The second database gains a pool. repoA's answer must not change, and the
	// two must never report the same id.
	poolB, err := repoB.InsertProxyPool(ProxyPoolData{Name: "b", ProxyURL: "http://172.17.0.1:8002", Type: "http"})
	if err != nil {
		t.Fatalf("insert pool b: %v", err)
	}
	idB := poolB["id"].(string)

	if got := repoB.ActivePoolIDs(); len(got) != 1 || got[0] != idB {
		t.Fatalf("repoB pools = %v, want [%s]", got, idB)
	}
	if got := repoA.ActivePoolIDs(); len(got) != 1 || got[0] != idA {
		t.Fatalf("repoA pools = %v after repoB changed, want [%s]", got, idA)
	}
}

// A deactivated or deleted pool must leave the candidate list — rotation picks
// from this, and the resolver refuses a pool that is gone or inactive, so a
// stale entry would hand traffic to a pool that then fails the request.
func TestActivePoolIDs_DropsDeactivatedAndDeletedPools(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS proxyPools (
		id TEXT PRIMARY KEY,
		isActive INTEGER DEFAULT 1,
		testStatus TEXT,
		data TEXT NOT NULL,
		createdAt TEXT NOT NULL,
		updatedAt TEXT NOT NULL
	);`); err != nil {
		t.Fatalf("create proxyPools: %v", err)
	}
	repo := NewRepo(database)

	mk := func(name, url string) string {
		t.Helper()
		pool, err := repo.InsertProxyPool(ProxyPoolData{Name: name, ProxyURL: url, Type: "http"})
		if err != nil {
			t.Fatalf("insert pool %s: %v", name, err)
		}
		return pool["id"].(string)
	}
	idA := mk("a", "http://172.17.0.1:8001")
	idB := mk("b", "http://172.17.0.1:8002")
	idC := mk("c", "http://172.17.0.1:8003")

	if got := len(repo.ActivePoolIDs()); got != 3 {
		t.Fatalf("active pools = %d, want 3", got)
	}

	if err := repo.UpdateProxyPool(idB, map[string]any{"isActive": false}); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	got := repo.ActivePoolIDs()
	if len(got) != 2 {
		t.Fatalf("after deactivating one pool: %v, want 2 pools", got)
	}
	for _, id := range got {
		if id == idB {
			t.Errorf("deactivated pool %s still offered to rotation", idB)
		}
	}

	if err := repo.DeleteProxyPool(idC); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got = repo.ActivePoolIDs()
	if len(got) != 1 || got[0] != idA {
		t.Fatalf("after delete: %v, want only [%s]", got, idA)
	}
}

// A pool with no URL cannot serve a request. Rotation must not select it, or it
// hands a share of the traffic to a pool the resolver then refuses.
func TestActivePoolIDs_SkipPoolsWithoutURL(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS proxyPools (
		id TEXT PRIMARY KEY,
		isActive INTEGER DEFAULT 1,
		testStatus TEXT,
		data TEXT NOT NULL,
		createdAt TEXT NOT NULL,
		updatedAt TEXT NOT NULL
	);`); err != nil {
		t.Fatalf("create proxyPools: %v", err)
	}
	repo := NewRepo(database)

	usable, err := repo.InsertProxyPool(ProxyPoolData{Name: "usable", ProxyURL: "http://172.17.0.1:8001", Type: "http"})
	if err != nil {
		t.Fatalf("insert usable pool: %v", err)
	}

	// Seed a URL-less pool straight into the table: InsertProxyPool requires one.
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := database.Exec(`INSERT INTO proxyPools (id, isActive, testStatus, data, createdAt, updatedAt)
		VALUES ('empty-pool', 1, 'unknown', '{"name":"empty","proxyUrl":""}', ?, ?)`, now, now); err != nil {
		t.Fatalf("insert empty pool: %v", err)
	}

	got := repo.ActivePoolIDs()
	if len(got) != 1 || got[0] != usable["id"].(string) {
		t.Fatalf("active pools = %v, want only the pool carrying a URL", got)
	}
}

// Deactivating every pool leaves rotation with nothing to pick, which the caller
// reads as "no rotation" rather than as a pool that will fail.
func TestActivePoolIDs_EmptyWhenAllInactive(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS proxyPools (
		id TEXT PRIMARY KEY,
		isActive INTEGER DEFAULT 1,
		testStatus TEXT,
		data TEXT NOT NULL,
		createdAt TEXT NOT NULL,
		updatedAt TEXT NOT NULL
	);`); err != nil {
		t.Fatalf("create proxyPools: %v", err)
	}
	repo := NewRepo(database)

	pool, err := repo.InsertProxyPool(ProxyPoolData{Name: "a", ProxyURL: "http://172.17.0.1:8001", Type: "http"})
	if err != nil {
		t.Fatalf("insert pool: %v", err)
	}
	if err := repo.UpdateProxyPool(pool["id"].(string), map[string]any{"isActive": false}); err != nil {
		t.Fatalf("deactivate: %v", err)
	}

	if got := repo.ActivePoolIDs(); len(got) != 0 {
		t.Fatalf("active pools = %v, want none", got)
	}
}
