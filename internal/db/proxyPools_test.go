package db

import (
	json "encoding/json/v2"
	"testing"
)

// TestInsertProxyPool_DataShape verifies InsertProxyPool writes the proxyPools
// row with the same data JSON shape and values the Next.js dashboard stores, so
// the shared DB stays compatible.
func TestInsertProxyPool_DataShape(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS proxyPools (
		id TEXT PRIMARY KEY,
		isActive INTEGER DEFAULT 1,
		testStatus TEXT,
		data TEXT NOT NULL,
		createdAt TEXT NOT NULL,
		updatedAt TEXT NOT NULL
	);`); err != nil {
		t.Fatalf("failed to create proxyPools table: %v", err)
	}

	repo := NewRepo(db)
	pool, err := repo.InsertProxyPool(ProxyPoolData{
		Name: "relay-x", ProxyURL: "https://relay-x.example.com", NoProxy: "", Type: "vercel", StrictProxy: false,
	})
	if err != nil {
		t.Fatalf("InsertProxyPool failed: %v", err)
	}
	if pool["id"].(string) == "" {
		t.Fatal("expected non-empty generated id")
	}
	if pool["isActive"] != true {
		t.Errorf("expected isActive=true, got %v", pool["isActive"])
	}

	var row struct {
		IsActive   int
		TestStatus string
		Data       string
		CreatedAt  string
		UpdatedAt  string
	}
	err = db.QueryRow(`SELECT isActive, testStatus, data, createdAt, updatedAt FROM proxyPools WHERE id = ?`, pool["id"]).
		Scan(&row.IsActive, &row.TestStatus, &row.Data, &row.CreatedAt, &row.UpdatedAt)
	if err != nil {
		t.Fatalf("read back row: %v", err)
	}
	if row.IsActive != 1 {
		t.Errorf("expected isActive column 1, got %d", row.IsActive)
	}
	if row.TestStatus != "unknown" {
		t.Errorf("expected testStatus 'unknown', got %q", row.TestStatus)
	}
	if row.CreatedAt != row.UpdatedAt {
		t.Errorf("expected createdAt == updatedAt, got %q vs %q", row.CreatedAt, row.UpdatedAt)
	}

	// Parse the stored data JSON and assert it matches Next's createProxyPool shape.
	var data map[string]any
	if err := json.Unmarshal([]byte(row.Data), &data); err != nil {
		t.Fatalf("parse stored data: %v", err)
	}
	want := map[string]any{
		"name":         "relay-x",
		"proxyUrl":     "https://relay-x.example.com",
		"noProxy":      "",
		"type":         "vercel",
		"strictProxy":  false,
		"lastTestedAt": nil,
		"lastError":    nil,
	}
	if len(data) != len(want) {
		t.Fatalf("expected %d keys in data, got %d: %v", len(want), len(data), data)
	}
	for k, v := range want {
		if got, ok := data[k]; !ok || got != v {
			t.Errorf("data[%q] = %v (present=%v), want %v", k, got, ok, v)
		}
	}
}

func TestGetProxyPool_SingleProxyUrl_AndMetadata(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS proxyPools (
		id TEXT PRIMARY KEY,
		data TEXT,
		isActive INTEGER DEFAULT 1,
		testStatus TEXT,
		createdAt TEXT,
		updatedAt TEXT
	);`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	repo := NewRepo(db)

	// Single proxyUrl string (Next.js format)
	poolData := `{"name":"relay-pool","proxyUrl":"https://relay.example.com","type":"vercel","noProxy":"localhost,*.internal","strictProxy":true}`
	if _, err := db.Exec(`INSERT INTO proxyPools (id, data, isActive) VALUES (?, ?, ?)`, "pool-single", poolData, 1); err != nil {
		t.Fatalf("insert: %v", err)
	}

	pool, err := repo.GetProxyPool("pool-single")
	if err != nil {
		t.Fatalf("GetProxyPool failed: %v", err)
	}
	if pool == nil {
		t.Fatal("expected pool, got nil")
	}
	if len(pool.URLs) != 1 || pool.URLs[0] != "https://relay.example.com" {
		t.Errorf("expected URLs [https://relay.example.com], got %v", pool.URLs)
	}
	if pool.Type != "vercel" {
		t.Errorf("expected Type vercel, got %s", pool.Type)
	}
	if pool.NoProxy != "localhost,*.internal" {
		t.Errorf("expected NoProxy localhost,*.internal, got %s", pool.NoProxy)
	}
	if !pool.StrictProxy {
		t.Errorf("expected StrictProxy true, got %v", pool.StrictProxy)
	}
	if next := pool.NextURL(); next != "https://relay.example.com" {
		t.Errorf("expected NextURL https://relay.example.com, got %s", next)
	}
}

// A relay URL pasted into a pool declared "http" must still read as a relay, or
// the request path dials it with http.ProxyURL and answers "malformed HTTP
// status code" — the failure the predicate exists to prevent.
func TestProxyPool_IsEdgeRelay(t *testing.T) {
	tests := []struct {
		name string
		pool *ProxyPool
		want bool
	}{
		{name: "nil", pool: nil, want: false},
		{name: "declared vercel", pool: &ProxyPool{Type: "vercel", URLs: []string{"https://x.example.com"}}, want: true},
		{name: "declared cloudflare", pool: &ProxyPool{Type: "cloudflare", URLs: []string{"https://x.example.com"}}, want: true},
		{name: "declared deno", pool: &ProxyPool{Type: "deno", URLs: []string{"https://x.example.com"}}, want: true},
		{name: "declared netlify", pool: &ProxyPool{Type: "netlify", URLs: []string{"https://x.example.com"}}, want: true},
		{name: "http typed but vercel host", pool: &ProxyPool{Type: "http", URLs: []string{"https://relay-deploy.vercel.app"}}, want: true},
		{name: "http typed but workers host", pool: &ProxyPool{Type: "http", URLs: []string{"https://relay.user.workers.dev"}}, want: true},
		{name: "untyped deno host", pool: &ProxyPool{Type: "", URLs: []string{"https://relay.deno.dev"}}, want: true},
		{name: "netlify function path", pool: &ProxyPool{Type: "http", URLs: []string{"https://site.example.com/.netlify/functions/relay"}}, want: true},
		{name: "http typed real proxy stays a proxy", pool: &ProxyPool{Type: "http", URLs: []string{"http://user:pass@proxy.example.com:8080"}}, want: false},
		{name: "http typed loopback stays a proxy", pool: &ProxyPool{Type: "http", URLs: []string{"http://127.0.0.1:8888"}}, want: false},
		{name: "socks5 is not inferred as relay", pool: &ProxyPool{Type: "socks5", URLs: []string{"https://relay.vercel.app"}}, want: false},
		{name: "lookalike host is not a relay", pool: &ProxyPool{Type: "http", URLs: []string{"https://vercel.app.evil.com"}}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.pool.IsEdgeRelay(); got != tt.want {
				t.Errorf("IsEdgeRelay() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestInsertProxyPool_RespectsExplicitIsActiveFalse(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS proxyPools (
		id TEXT PRIMARY KEY,
		isActive INTEGER DEFAULT 1,
		testStatus TEXT,
		data TEXT NOT NULL,
		createdAt TEXT NOT NULL,
		updatedAt TEXT NOT NULL
	);`); err != nil {
		t.Fatalf("failed to create proxyPools table: %v", err)
	}

	repo := NewRepo(db)
	inactive := false
	pool, err := repo.InsertProxyPool(ProxyPoolData{
		Name:        "inactive-warp",
		ProxyURL:    "socks5://127.0.0.1:40000",
		Type:        "socks5",
		StrictProxy: true,
		IsActive:    &inactive,
	})
	if err != nil {
		t.Fatalf("InsertProxyPool failed: %v", err)
	}
	if pool["isActive"] != false {
		t.Fatalf("returned isActive = %v, want false", pool["isActive"])
	}

	var isActive int
	var dataStr string
	if err := db.QueryRow(`SELECT isActive, data FROM proxyPools WHERE id = ?`, pool["id"]).Scan(&isActive, &dataStr); err != nil {
		t.Fatalf("read back row: %v", err)
	}
	if isActive != 0 {
		t.Errorf("stored isActive column = %d, want 0", isActive)
	}

	var data map[string]any
	if err := json.Unmarshal([]byte(dataStr), &data); err != nil {
		t.Fatalf("unmarshal stored data: %v", err)
	}
	if len(data) != 7 {
		t.Fatalf("expected exact 7 JSON keys in data, got %d: %v", len(data), data)
	}
	if _, leaked := data["isActive"]; leaked {
		t.Errorf("isActive must not leak into data JSON blob: %v", data)
	}
}
