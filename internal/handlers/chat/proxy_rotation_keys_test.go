package chat

import (
	"testing"

	"9router/proxy/internal/db"
)

// poolRotationDB builds a repo holding an active pool, plus a settings row with
// the given providerStrategies JSON.
func poolRotationDB(t *testing.T, settingsJSON string) *db.Repo {
	t.Helper()
	database, cleanup := setupChatTestDB(t)
	t.Cleanup(cleanup)

	for _, ddl := range []string{
		`CREATE TABLE IF NOT EXISTS proxyPools (
			id TEXT PRIMARY KEY,
			isActive INTEGER DEFAULT 1,
			testStatus TEXT,
			data TEXT NOT NULL,
			createdAt TEXT NOT NULL,
			updatedAt TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS settings (id INTEGER PRIMARY KEY, data TEXT);`,
	} {
		if _, err := database.Exec(ddl); err != nil {
			t.Fatalf("create table: %v", err)
		}
	}

	repo := db.NewRepo(database)
	if _, err := database.Exec(`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`, settingsJSON); err != nil {
		t.Fatalf("insert settings: %v", err)
	}
	return repo
}

// insertPools adds n active pools and returns their ids.
func insertPools(t *testing.T, repo *db.Repo, n int) []string {
	t.Helper()
	ids := make([]string, 0, n)
	for i := range n {
		pool, err := repo.InsertProxyPool(db.ProxyPoolData{
			Name: "pool-" + string(rune('a'+i)), ProxyURL: "http://172.17.0.1:8001", Type: "http",
		})
		if err != nil {
			t.Fatalf("insert pool %d: %v", i, err)
		}
		ids = append(ids, pool["id"].(string))
	}
	return ids
}

// Pool rotation is offered only inside the provider card's `isNoAuth` block,
// and that is where `rotateStrategy` means "rotate pools". On a keyed provider
// the same key holds account rotation, so honouring it there would send the
// provider's egress through pools its operator never configured.
func TestProxyPoolRotation_OnlyAppliesToNoAuthProviders(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		wantPool bool
	}{
		{"noauth provider rotates", "opencode", true},
		{"keyed provider ignores it", "deepseek", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := poolRotationDB(t, `{"providerStrategies":{
				"opencode": {"rotateStrategy":"round-robin"},
				"deepseek": {"rotateStrategy":"round-robin"}
			}}`)
			ids := insertPools(t, repo, 2)

			got := NewChatHandler(repo).ResolveProviderProxyPoolID(tt.provider)
			if tt.wantPool {
				if got != ids[0] && got != ids[1] {
					t.Fatalf("ResolveProviderProxyPoolID(%q) = %q, want one of the active pools", tt.provider, got)
				}
				return
			}
			if got != "" {
				t.Fatalf("ResolveProviderProxyPoolID(%q) = %q, want empty: a keyed provider's rotateStrategy is account rotation", tt.provider, got)
			}
		})
	}
}

// Rotating pools must not start rotating connections. Both land in the same
// stored entry for a NoAuth provider, so the connection path has to read
// `fallbackStrategy` only — otherwise the operator's pool choice silently
// begins cycling their accounts.
func TestProxyPoolRotation_DoesNotArmConnectionRotation(t *testing.T) {
	tests := []struct {
		name          string
		settings      string
		wantConnRot   string
		wantPoolCount int
	}{
		{
			name:          "pool rotation alone leaves connection rotation off",
			settings:      `{"providerStrategies":{"opencode":{"rotateStrategy":"round-robin"}}}`,
			wantConnRot:   "",
			wantPoolCount: 1,
		},
		{
			name:          "the two rotations are independent",
			settings:      `{"providerStrategies":{"opencode":{"rotateStrategy":"round-robin","fallbackStrategy":"round-robin","stickyRoundRobinLimit":3}}}`,
			wantConnRot:   "round-robin",
			wantPoolCount: 1,
		},
		{
			name:          "connection rotation alone leaves pool rotation off",
			settings:      `{"providerStrategies":{"opencode":{"fallbackStrategy":"round-robin","stickyRoundRobinLimit":3}}}`,
			wantConnRot:   "round-robin",
			wantPoolCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := poolRotationDB(t, tt.settings)
			insertPools(t, repo, 2)

			h := NewChatHandler(repo)
			got := h.ResolveProviderProxyPoolID("opencode")

			// connRotationStrategy is what ApplyConnectionStrategy consumes: a
			// non-empty RotateStrategy there is what makes accounts start cycling.
			s, err := repo.GetSettings()
			if err != nil {
				t.Fatalf("GetSettings: %v", err)
			}
			strat := connRotationStrategy(s, "opencode")
			if strat.RotateStrategy != tt.wantConnRot {
				t.Errorf("connection rotation = %q, want %q", strat.RotateStrategy, tt.wantConnRot)
			}

			poolCount := 0
			if got != "" {
				poolCount = 1
			}
			if poolCount != tt.wantPoolCount {
				t.Errorf("pool rotation selected a pool = %v (id %q), want %v", poolCount, got, tt.wantPoolCount)
			}
		})
	}
}

// `sticky` is a connection-rotation value. Pool rotation used to accept it and
// serve round-robin, promising an affinity this resolver does not keep.
func TestIsProxyPoolRotation_RejectsSticky(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{"round-robin", true},
		{"roundrobin", true},
		{"random", true},
		{" Round-Robin ", true},
		{"sticky", false},
		{"Sticky", false},
		{"none", false},
		{"", false},
		{"fill-first", false},
	}

	for _, tt := range tests {
		if got := db.IsProxyPoolRotation(tt.value); got != tt.want {
			t.Errorf("IsProxyPoolRotation(%q) = %v, want %v", tt.value, got, tt.want)
		}
	}
}

// One shared cursor let one provider's traffic advance another's, so a provider
// rotating over a different number of pools skipped positions.
//
// This drives rotatedActiveProxyPool directly: opencode is currently the only
// NoAuth chat provider, so the NoAuth gate would leave nothing to compare, and
// the cursor is the unit under test here anyway.
func TestPoolRotation_CursorsAreIndependentPerProvider(t *testing.T) {
	repo := poolRotationDB(t, `{}`)
	ids := insertPools(t, repo, 3)

	pos := func(id string) int {
		for i, v := range ids {
			if v == id {
				return i
			}
		}
		return -1
	}

	h := NewChatHandler(repo)
	// A full cycle must reach every pool. Which pool comes first depends on the
	// query's ordering, so this asserts coverage rather than a fixed start.
	for _, provider := range []string{"opencode", "blackbox"} {
		seen := map[int]bool{}
		var order []int
		for range 3 {
			order = append(order, pos(h.rotatedActiveProxyPool(provider, "round-robin")))
		}
		for _, p := range order {
			seen[p] = true
		}
		if len(seen) != 3 {
			t.Errorf("%s rotation visited %d of 3 pools (%v), want all of them", provider, len(seen), order)
		}
	}

	// Interleaving another provider's traffic must not advance this one's cursor.
	// One blackbox request is enough to tell the two apart: a shared counter
	// would shift opencode's next pick by one.
	pick := func(provider string, n int) []string {
		out := make([]string, 0, n)
		for range n {
			out = append(out, h.rotatedActiveProxyPool(provider, "round-robin"))
		}
		return out
	}
	before := pick("opencode", 3)
	_ = pick("blackbox", 1)
	after := pick("opencode", 3)
	for i := range before {
		if after[i] != before[i] {
			t.Fatalf("blackbox's traffic moved opencode's cursor: %v became %v", before, after)
		}
	}
}

// `oc` and `opencode` are one provider behind two names. They must share a
// cursor, or a request arriving under either alias rotates independently and
// the two skip each other.
func TestPoolRotation_AliasesShareOneCursor(t *testing.T) {
	repo := poolRotationDB(t, `{"providerStrategies":{"opencode":{"rotateStrategy":"round-robin"}}}`)
	insertPools(t, repo, 3)

	h := NewChatHandler(repo)
	var order []string
	for range 3 {
		order = append(order, h.ResolveProviderProxyPoolID("oc"))
	}

	seen := map[string]bool{}
	for _, id := range order {
		seen[id] = true
	}
	if len(seen) != 3 {
		t.Fatalf("alias rotation visited %d of 3 pools (%v), want all of them", len(seen), order)
	}
}

// A keyed provider rotates pools only under a live free-tier exclusion set;
// even then the excluded pool is never a candidate.
func TestResolveProviderProxyPoolID_KeyedExclusionNeverSelectsExcludedPool(t *testing.T) {
	repo := poolRotationDB(t, `{"providerStrategies":{"opencode-zen":{"rotateStrategy":"round-robin"}}}`)
	ids := insertPools(t, repo, 2)
	h := NewChatHandler(repo)

	excluded := map[string]bool{ids[1]: true}
	for i := 0; i < 6; i++ {
		if got := h.ResolveProviderProxyPoolID("opencode-zen", excluded); got != ids[0] {
			t.Fatalf("call %d: pool = %q, want %q (excluded pool was selected)", i, got, ids[0])
		}
	}
}
