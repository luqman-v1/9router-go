package chat

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
)

// resetModelScopeDB drops connection rows and the disable list so a test starts
// from a known install state.
func resetModelScopeDB(t *testing.T, database *sql.DB) {
	t.Helper()
	if _, err := database.Exec(`DELETE FROM providerConnections`); err != nil {
		t.Fatalf("delete connections: %v", err)
	}
	if _, err := database.Exec(`DELETE FROM kv WHERE scope='disabledModels'`); err != nil {
		t.Fatalf("delete disabled: %v", err)
	}
}

// seedActiveKiro inserts one active connection row, which is what makes the
// default list connection-scoped instead of a full catalog dump.
func seedActiveKiro(t *testing.T, database *sql.DB) {
	t.Helper()
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-kiro-1', 'kiro', 'oauth', 'Kiro Account', 1, 1, '{"apiKey":"tok"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`); err != nil {
		t.Fatalf("seed kiro: %v", err)
	}
}

// TestModelLookup_AgreesWithConnectedListing covers the lookup route
// disagreeing with the listing endpoint. Once a connection row exists the
// default list is connection-scoped, so a registry noAuth model that
// /v1/models?connected=1 advertises was 404 on /v1/models/<id>.
func TestModelLookup_AgreesWithConnectedListing(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	resetModelScopeDB(t, database)
	seedActiveKiro(t, database)

	h := NewChatHandler(db.NewRepo(database))
	// The model has to be advertised before the lookup can be said to agree
	// with it, otherwise this asserts nothing.
	if !fetchModels(t, h, "?connected=1").idSet()["oc/jev-1.13-free"] {
		t.Fatal("?connected=1 did not advertise oc/jev-1.13-free")
	}

	w := httptest.NewRecorder()
	h.HandleModelLookup(w, httptest.NewRequest("GET", "/v1/models/oc/jev-1.13-free", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("lookup of a model advertised by ?connected=1 = %d, want 200: %s",
			w.Code, w.Body.String())
	}
}

// TestModelLookup_FreshInstallStaysPermissive is the regression catcher for the
// obvious but wrong fix. On a fresh install the default list is the full
// catalog, which is a superset of connected mode, so resolving against
// connected mode alone would turn the credentialed-provider lookups that work
// today into 404s.
func TestModelLookup_FreshInstallStaysPermissive(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	resetModelScopeDB(t, database)

	h := NewChatHandler(db.NewRepo(database))
	catalog := fetchModels(t, h, "").idSet()

	// Pick a catalog model from a provider that is not noAuth, so it only
	// resolves through the default list.
	var probe string
	for id := range catalog {
		if prefix, _, ok := strings.Cut(id, "/"); ok && !providers.IsNoAuthProvider(prefix) {
			probe = id
			break
		}
	}
	if probe == "" {
		t.Skip("no credentialed catalog model available to probe")
	}
	if fetchModels(t, h, "?connected=1").idSet()[probe] {
		t.Fatalf("probe %q turned out to be noAuth, the test would not discriminate", probe)
	}

	w := httptest.NewRecorder()
	h.HandleModelLookup(w, httptest.NewRequest("GET", "/v1/models/"+probe, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("fresh-install lookup of catalog model %q = %d, want 200: %s",
			probe, w.Code, w.Body.String())
	}
}

// TestModelLookup_ConnectedModelStillResolves proves the fallback did not
// displace the connection-scoped path it is layered on top of.
func TestModelLookup_ConnectedModelStillResolves(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	resetModelScopeDB(t, database)
	seedActiveKiro(t, database)

	h := NewChatHandler(db.NewRepo(database))
	// Read a real published id rather than hardcoding one: kiro serves a live
	// catalog when its credentials work and the static registry when they do
	// not, so a fixed model id is not stable across environments.
	published := fetchModels(t, h, "").idSet()
	var probe string
	for id := range published {
		if strings.HasPrefix(id, "kr/") {
			probe = id
			break
		}
	}
	if probe == "" {
		t.Skip("kiro published no models in this environment")
	}
	w := httptest.NewRecorder()
	h.HandleModelLookup(w, httptest.NewRequest("GET", "/v1/models/"+probe, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("lookup of a connected provider model = %d, want 200: %s",
			w.Code, w.Body.String())
	}
}

// TestModelLookup_UnknownModelStill404 keeps the fallback from turning the
// route into a catalogue of everything that ever existed.
func TestModelLookup_UnknownModelStill404(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	resetModelScopeDB(t, database)
	seedActiveKiro(t, database)

	h := NewChatHandler(db.NewRepo(database))
	w := httptest.NewRecorder()
	h.HandleModelLookup(w, httptest.NewRequest("GET", "/v1/models/oc/does-not-exist-xyz", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown model = %d, want 404: %s", w.Code, w.Body.String())
	}
}
