package dashboard

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/db"
)

// Rotation routes traffic through every active pool, so each one is in use
// whether or not a connection pins it. The delete guard refused only pools
// someone pinned, which let a pool actively serving rotation traffic be deleted
// underneath the next request.
func TestProxyPoolBindings_RotationUsedPoolIsInUse(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	if _, err := repo.RawDB().Exec(`CREATE TABLE IF NOT EXISTS proxyPools (
		id TEXT PRIMARY KEY,
		isActive INTEGER DEFAULT 1,
		testStatus TEXT,
		data TEXT NOT NULL,
		createdAt TEXT NOT NULL,
		updatedAt TEXT NOT NULL
	);`); err != nil {
		t.Fatalf("create proxyPools: %v", err)
	}

	pinned, err := repo.InsertProxyPool(db.ProxyPoolData{
		Name: "pinned", ProxyURL: "http://172.17.0.1:8001", Type: "http",
	})
	if err != nil {
		t.Fatalf("insert pinned pool: %v", err)
	}
	rotatedOnly, err := repo.InsertProxyPool(db.ProxyPoolData{
		Name: "rotated-only", ProxyURL: "http://172.17.0.1:8002", Type: "http",
	})
	if err != nil {
		t.Fatalf("insert rotated pool: %v", err)
	}

	// Pool rotation is derived from the `rotateStrategy` the provider card
	// writes inside its NoAuth block, so seed that key rather than going
	// through SetProviderStrategy, which persists connection rotation.
	if err := repo.UpdateSettingsRaw(map[string]any{
		"providerStrategies": map[string]any{
			"opencode": map[string]any{
				"rotateStrategy": "round-robin",
				"proxyPoolId":    pinned["id"].(string),
			},
		},
	}); err != nil {
		t.Fatalf("seed provider strategy: %v", err)
	}

	router := setupTestRouter(repo)

	// Both pools serve rotation traffic, so both must refuse deletion.
	for name, id := range map[string]string{
		"pinned":       pinned["id"].(string),
		"rotated-only": rotatedOnly["id"].(string),
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodDelete, "/api/proxy-pools/"+id, nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusConflict {
				t.Fatalf("delete in-use pool = %d, want 409: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// boundConnectionCount feeds a number an operator reads as "how many things use
// this pool". A pool that is both pinned and rotated is one binding, not two.
func TestProxyPoolBindings_PinnedAndRotatedCountsOnce(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	if _, err := repo.RawDB().Exec(`CREATE TABLE IF NOT EXISTS proxyPools (
		id TEXT PRIMARY KEY,
		isActive INTEGER DEFAULT 1,
		testStatus TEXT,
		data TEXT NOT NULL,
		createdAt TEXT NOT NULL,
		updatedAt TEXT NOT NULL
	);`); err != nil {
		t.Fatalf("create proxyPools: %v", err)
	}

	pool, err := repo.InsertProxyPool(db.ProxyPoolData{
		Name: "warp", ProxyURL: "http://172.17.0.1:8001", Type: "http",
	})
	if err != nil {
		t.Fatalf("insert pool: %v", err)
	}
	poolID := pool["id"].(string)

	// Seed the card's own key: rotation is derived from `rotateStrategy`, which
	// SetProviderStrategy does not write as a pool rotation.
	if err := repo.UpdateSettingsRaw(map[string]any{
		"providerStrategies": map[string]any{
			"opencode": map[string]any{
				"rotateStrategy": "round-robin",
				"proxyPoolId":    poolID,
			},
		},
	}); err != nil {
		t.Fatalf("seed provider strategy: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/proxy-pools?includeUsage=true", nil)
	rec := httptest.NewRecorder()
	setupTestRouter(repo).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list pools = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		ProxyPools []map[string]any `json:"proxyPools"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode pools: %v", err)
	}
	if len(body.ProxyPools) != 1 {
		t.Fatalf("want 1 pool, got %d", len(body.ProxyPools))
	}
	if got, _ := body.ProxyPools[0]["boundConnectionCount"].(float64); got != 1 {
		t.Fatalf("boundConnectionCount = %v, want 1: one pool is one binding however it is reached", got)
	}
}

// With rotation off, a pool nobody pins stays deletable — the guard must not
// start refusing every pool once a provider has ever rotated.
func TestProxyPoolBindings_UnboundPoolStillDeletable(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	if _, err := repo.RawDB().Exec(`CREATE TABLE IF NOT EXISTS proxyPools (
		id TEXT PRIMARY KEY,
		isActive INTEGER DEFAULT 1,
		testStatus TEXT,
		data TEXT NOT NULL,
		createdAt TEXT NOT NULL,
		updatedAt TEXT NOT NULL
	);`); err != nil {
		t.Fatalf("create proxyPools: %v", err)
	}

	pool, err := repo.InsertProxyPool(db.ProxyPoolData{
		Name: "spare", ProxyURL: "http://172.17.0.1:8003", Type: "http",
	})
	if err != nil {
		t.Fatalf("insert pool: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/proxy-pools/"+pool["id"].(string), nil)
	rec := httptest.NewRecorder()
	setupTestRouter(repo).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete unbound pool = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}
