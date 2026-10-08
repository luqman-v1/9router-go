package semanticcache

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func setupTestSQLite(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}

	createSQL := `
CREATE TABLE IF NOT EXISTS semanticCacheEntries (
	key TEXT PRIMARY KEY,
	model TEXT NOT NULL,
	responseBody BLOB NOT NULL,
	contentType TEXT,
	storedAt TEXT NOT NULL,
	hitCount INTEGER DEFAULT 0,
	tokensSaved INTEGER DEFAULT 0
);`
	if _, err := db.Exec(createSQL); err != nil {
		db.Close()
		t.Fatalf("create table: %v", err)
	}

	return db, func() { db.Close() }
}

func TestPersistentLRUStore_WriteThroughAndHydrate(t *testing.T) {
	db, cleanup := setupTestSQLite(t)
	defer cleanup()

	ctx := context.Background()
	store1 := NewPersistentStore(db, 10, 1*time.Hour)

	entry1 := Entry{
		Key:          "key-1",
		Model:        "gpt-4o",
		ResponseBody: []byte(`{"id":"chatcmpl-1"}`),
		ContentType:  "application/json",
		StoredAt:     time.Now(),
	}
	entry2 := Entry{
		Key:          "key-2",
		Model:        "claude-3-5",
		ResponseBody: []byte(`{"id":"chatcmpl-2"}`),
		ContentType:  "application/json",
		StoredAt:     time.Now(),
	}

	if err := store1.Put(ctx, "key-1", entry1); err != nil {
		t.Fatalf("put entry1: %v", err)
	}
	if err := store1.Put(ctx, "key-2", entry2); err != nil {
		t.Fatalf("put entry2: %v", err)
	}

	if store1.Len() != 2 {
		t.Errorf("store1.Len() = %d, want 2", store1.Len())
	}
	if store1.DBLen() != 2 {
		t.Errorf("store1.DBLen() = %d, want 2", store1.DBLen())
	}

	// Record hit
	store1.RecordHit(ctx, "key-1", 50)
	got, ok := store1.Get(ctx, "key-1")
	if !ok || got.HitCount != 1 || got.TokensSaved != 50 {
		t.Errorf("expected hit updated in RAM: %+v", got)
	}

	// Verify hit persisted to SQLite
	var dbHits, dbSaved int64
	_ = db.QueryRow(`SELECT hitCount, tokensSaved FROM semanticCacheEntries WHERE key = 'key-1'`).Scan(&dbHits, &dbSaved)
	if dbHits != 1 || dbSaved != 50 {
		t.Errorf("expected hit in DB: hits=%d, saved=%d", dbHits, dbSaved)
	}

	// Create new store instance on same DB -> must hydrate from disk
	store2 := NewPersistentStore(db, 10, 1*time.Hour)
	if store2.Len() != 2 {
		t.Errorf("hydrated store2.Len() = %d, want 2", store2.Len())
	}
	hydratedEntry, ok := store2.Get(ctx, "key-1")
	if !ok {
		t.Fatal("expected key-1 present in store2 after hydration")
	}
	if hydratedEntry.Model != "gpt-4o" || hydratedEntry.HitCount != 1 {
		t.Errorf("hydratedEntry = %+v, want gpt-4o with hitCount 1", hydratedEntry)
	}

	// Delete from store2 -> must delete in DB
	deleted := store2.Delete(ctx, "key-1")
	if !deleted {
		t.Fatal("expected Delete to return true")
	}
	if store2.DBLen() != 1 {
		t.Errorf("store2.DBLen() after delete = %d, want 1", store2.DBLen())
	}

	// Clear -> must clear DB
	store2.Clear()
	if store2.Len() != 0 || store2.DBLen() != 0 {
		t.Errorf("after Clear(): len=%d, dbLen=%d, want 0", store2.Len(), store2.DBLen())
	}
}

func TestPersistentLRUStore_Janitor(t *testing.T) {
	db, cleanup := setupTestSQLite(t)
	defer cleanup()

	ctx := context.Background()
	ttl := 20 * time.Millisecond
	store := &PersistentLRUStore{
		lru:         NewLRUStore(10, ttl),
		db:          db,
		ttl:         ttl,
		stopJanitor: make(chan struct{}),
	}

	// Insert entry that is already expired
	oldEntry := Entry{
		Key:          "expired-key",
		Model:        "gpt-4o",
		ResponseBody: []byte(`{}`),
		ContentType:  "application/json",
		StoredAt:     time.Now().Add(-50 * time.Millisecond),
	}
	if err := store.Put(ctx, "expired-key", oldEntry); err != nil {
		t.Fatalf("put oldEntry: %v", err)
	}

	// Start janitor with 10ms interval
	store.startJanitor(10 * time.Millisecond)
	defer store.Close()

	// Wait for janitor tick
	time.Sleep(30 * time.Millisecond)

	if store.DBLen() != 0 {
		t.Errorf("expected janitor to prune expired DB entries, got %d", store.DBLen())
	}
}
