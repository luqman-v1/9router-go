package chat

import (
	"errors"
	"net/http"
	"testing"

	"9router/proxy/internal/db"
)

func TestResolveProviderProxyPoolID_RotatesAcrossActivePools(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
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
	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS settings (id INTEGER PRIMARY KEY, data TEXT);`); err != nil {
		t.Fatalf("create settings: %v", err)
	}

	repo := db.NewRepo(database)
	mk := func(name string) string {
		pool, err := repo.InsertProxyPool(db.ProxyPoolData{
			Name: name, ProxyURL: "http://172.17.0.1:8001", Type: "http",
		})
		if err != nil {
			t.Fatalf("insert pool %s: %v", name, err)
		}
		return pool["id"].(string)
	}
	idA := mk("warp")
	idB := mk("warp 2")
	idOff := mk("vercel-relay")
	if err := repo.UpdateProxyPool(idOff, map[string]any{"isActive": false}); err != nil {
		t.Fatalf("deactivate pool: %v", err)
	}

	settingsJSON := `{"providerStrategies": {"opencode": {"rotateStrategy": "round-robin"}}}`
	if _, err := database.Exec(`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`, settingsJSON); err != nil {
		t.Fatalf("insert settings: %v", err)
	}

	h := NewChatHandler(repo)
	seen := map[string]int{}
	prev := ""
	for i := 0; i < 4; i++ {
		got := h.ResolveProviderProxyPoolID("opencode")
		if got != idA && got != idB {
			t.Fatalf("call %d: got %q, want one of the two active pools", i, got)
		}
		if got == prev {
			t.Fatalf("call %d: round-robin repeated %q, want alternation", i, got)
		}
		prev = got
		seen[got]++
	}
	if len(seen) != 2 {
		t.Fatalf("want both active pools used, got %v", seen)
	}
}

func TestResolveProviderProxyPoolID_RandomStaysInActiveSet(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
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
	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS settings (id INTEGER PRIMARY KEY, data TEXT);`); err != nil {
		t.Fatalf("create settings: %v", err)
	}

	repo := db.NewRepo(database)
	var active []string
	for _, name := range []string{"warp", "warp 2"} {
		pool, err := repo.InsertProxyPool(db.ProxyPoolData{
			Name: name, ProxyURL: "http://172.17.0.1:8001", Type: "http",
		})
		if err != nil {
			t.Fatalf("insert pool: %v", err)
		}
		active = append(active, pool["id"].(string))
	}

	settingsJSON := `{"providerStrategies": {"opencode": {"rotateStrategy": "random"}}}`
	if _, err := database.Exec(`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`, settingsJSON); err != nil {
		t.Fatalf("insert settings: %v", err)
	}

	h := NewChatHandler(repo)
	allowed := map[string]bool{active[0]: true, active[1]: true}
	for i := 0; i < 20; i++ {
		if got := h.ResolveProviderProxyPoolID("opencode"); !allowed[got] {
			t.Fatalf("call %d: got %q, want one of %v", i, got, active)
		}
	}
}

func TestResolveProviderProxyPoolID_FallbackStrategyIsNotPoolRotation(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
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
	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS settings (id INTEGER PRIMARY KEY, data TEXT);`); err != nil {
		t.Fatalf("create settings: %v", err)
	}

	repo := db.NewRepo(database)
	if _, err := repo.InsertProxyPool(db.ProxyPoolData{
		Name: "warp", ProxyURL: "http://172.17.0.1:8001", Type: "http",
	}); err != nil {
		t.Fatalf("insert pool: %v", err)
	}

	settingsJSON := `{"providerStrategies": {"deepseek": {"fallbackStrategy": "round-robin", "stickyRoundRobinLimit": 1}}}`
	if _, err := database.Exec(`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`, settingsJSON); err != nil {
		t.Fatalf("insert settings: %v", err)
	}

	h := NewChatHandler(repo)
	if got := h.ResolveProviderProxyPoolID("deepseek"); got != "" {
		t.Fatalf("got %q, want empty: fallbackStrategy must not trigger pool rotation", got)
	}
}

func TestResolveProviderProxyPoolID_SinglePoolUnaffected(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS settings (id INTEGER PRIMARY KEY, data TEXT);`); err != nil {
		t.Fatalf("create settings: %v", err)
	}
	settingsJSON := `{"providerStrategies": {"opencode": {"proxyPoolId": "pool-single"}}}`
	if _, err := database.Exec(`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`, settingsJSON); err != nil {
		t.Fatalf("insert settings: %v", err)
	}

	h := NewChatHandler(db.NewRepo(database))
	for i := 0; i < 3; i++ {
		if got := h.ResolveProviderProxyPoolID("opencode"); got != "pool-single" {
			t.Fatalf("call %d: got %q, want pool-single", i, got)
		}
	}
}

func TestResolveEgress_HTTPPoolIsNotDirect(t *testing.T) {
	e := resolveEgress(&ConnectionData{
		ProxyPoolID:       "589e8c9bc9a3d77677894c03bddbef6a",
		ResolvedProxyPool: "warp",
	}, nil)
	if e.Kind != "http" {
		t.Fatalf("Kind = %q, want http", e.Kind)
	}
	if e.LogValue() != "warp" {
		t.Fatalf("LogValue() = %q, want warp", e.LogValue())
	}
}

// A pool that already refused the turn is never a rotation candidate: the next
// attempt must land on the egress that has not been tried yet.
func TestResolveProviderProxyPoolID_NeverSelectsExcludedPool(t *testing.T) {
	repo := poolRotationDB(t, `{"providerStrategies":{"opencode":{"rotateStrategy":"round-robin"}}}`)
	ids := insertPools(t, repo, 2)
	excluded := map[string]bool{ids[0]: true}
	h := NewChatHandler(repo)

	for i := 0; i < 8; i++ {
		if got := h.ResolveProviderProxyPoolID("opencode", excluded); got != ids[1] {
			t.Fatalf("call %d: pool = %q, want %q (an excluded pool must never be selected)", i, got, ids[1])
		}
	}
}

// With every pool excluded the pool binding is dropped so the attempt can go
// out direct — errNoUsablePool is the sentinel, not a client-visible failure.
func TestResolveProviderProxyPoolID_AllPoolsExcludedFallsThrough(t *testing.T) {
	repo := poolRotationDB(t, `{"providerStrategies":{"opencode":{"rotateStrategy":"round-robin"}}}`)
	ids := insertPools(t, repo, 2)
	excluded := map[string]bool{ids[0]: true, ids[1]: true}
	h := NewChatHandler(repo)

	if got := h.ResolveProviderProxyPoolID("opencode", excluded); got != "" {
		t.Fatalf("pool = %q, want empty (direct attempt instead of a hard failure)", got)
	}
	if _, err := h.resolveProviderProxyPoolID("opencode", excluded); !errors.Is(err, errNoUsablePool) {
		t.Fatalf("err = %v, want errNoUsablePool", err)
	}
	connData := &ConnectionData{}
	h.applyProviderProxyPool(connData, "opencode", excluded)
	if connData.ProxyPoolID != "" {
		t.Fatalf("bound pool = %q, want empty once every pool is excluded", connData.ProxyPoolID)
	}
}

// A keyed provider's rotateStrategy means ACCOUNT rotation, so it must not
// rotate pools on its own. The exclusion set is the free-tier-refusal signal
// (the failover loop fills it), and only then does the next pool get picked.
func TestResolveProviderProxyPoolID_KeyedProviderRotatesAfterFreeTierExclusion(t *testing.T) {
	repo := poolRotationDB(t, `{"providerStrategies":{"opencode-zen":{"rotateStrategy":"round-robin"}}}`)
	ids := insertPools(t, repo, 2)
	h := NewChatHandler(repo)

	if got := h.ResolveProviderProxyPoolID("opencode-zen"); got != "" {
		t.Fatalf("pool = %q, want empty before any refusal: a keyed provider's rotation is account rotation", got)
	}
	if got := h.ResolveProviderProxyPoolID("opencode-zen", map[string]bool{ids[0]: true}); got != ids[1] {
		t.Fatalf("pool = %q, want %q (a free-tier exclusion must rotate the egress)", got, ids[1])
	}
}

// A free-tier 403 is an egress verdict: it must not be recorded as a
// model-scoped lock. Any other 403 keeps the pre-existing handling.
func TestModelScopedError_FreeTierForbiddenIsNotModelScoped(t *testing.T) {
	body := []byte(`{"error":{"type":"FreeTierError","message":"free tier can only be used from within OpenCode"}}`)
	if isModelScopedError(http.StatusForbidden, extractErrorText(body), "jev-1.13-free", body) {
		t.Fatal("a FreeTierError 403 must not be model-scoped; the failover loop rotates the pool instead")
	}
}
