package chat

import (
	"database/sql"
	"strings"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
)

// TestHandleModels_HiddenProviderIsNotAdvertised pins the routing half of the
// Hidden flag on the one path that would otherwise publish it. A no-auth
// registry provider with no connection row is added to /v1/models so a caller
// can use the free tier without connecting anything; that rule is right for
// providers a person picks between and wrong for one the platform runs on its
// own. Hidden removes it from the catalog without unregistering it: a stored
// connection pointing at mimo-free must still resolve and route.
func TestHandleModels_HiddenProviderIsNotAdvertised(t *testing.T) {
	cfg, ok := providers.KnownProviders["mimo-free"]
	if !ok || !cfg.Hidden || !cfg.NoAuth {
		t.Skip("fixture assumes mimo-free is a hidden no-auth provider")
	}

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	h := NewChatHandler(db.NewRepo(database))

	// Connected mode is where the no-auth catalog is published at all: the
	// default listing only dumps it when there are no connections, and the
	// fixture seeds one. Asking for the mode that reaches the code under test
	// is what makes the assertions below mean anything.
	ids := modelsIDsQuery(t, h, "?connected=1")

	// hiddenPrefixes is decided from the registry, not from the two names this
	// fixture happens to know: whatever is marked Hidden must be absent from the
	// listing, under every spelling that resolves to it.
	hiddenPrefixes := make(map[string]bool, 2)
	for id := range providers.KnownProviders {
		if providers.KnownProviders[id].Hidden {
			hiddenPrefixes[id] = true
		}
	}
	for alias := range providers.ProviderModels {
		if canon := providers.ResolveAlias(alias); hiddenPrefixes[canon] {
			hiddenPrefixes[alias] = true
		}
	}
	for _, id := range ids {
		prefix, _, ok := strings.Cut(id, "/")
		if ok && hiddenPrefixes[prefix] {
			t.Errorf("hidden provider reached /v1/models: %q", id)
		}
	}
	// The control. The absence above is only meaningful if the no-auth catalog
	// pass ran and published its visible members — otherwise a listing that
	// never reached appendNoAuthCatalogModels would pass the assertion for the
	// wrong reason. The fixture seeds connections, so the visible no-auth
	// providers in the result must come from that pass and nowhere else.
	visiblePresent := false
	for _, id := range ids {
		prefix, _, ok := strings.Cut(id, "/")
		if !ok || hiddenPrefixes[prefix] || prefix == "ds" || prefix == "groq" {
			continue
		}
		if providers.IsNoAuthProvider(prefix) {
			visiblePresent = true
			break
		}
	}
	if !visiblePresent {
		t.Fatalf("no visible no-auth provider in /v1/models (%d ids); the no-auth catalog pass did not run, so the absence of %v proves nothing", len(ids), hiddenPrefixes)
	}

	// Hidden is not disabled: the resolver still knows the provider, which is
	// what routing uses for a connection that already points at it.
	if got := providers.ResolveAlias("mmf"); got != "mimo-free" {
		t.Errorf("ResolveAlias(mmf) = %q, want mimo-free: hidden must not unregister the provider", got)
	}
	if providers.GetProviderAlias("mimo-free") == "" {
		t.Error("mimo-free lost its published alias; hidden is about listing, not identity")
	}
}

// TestHandleModels_HiddenProviderAbsentFromEveryMode pins all three listing
// paths against every configuration: the static catalog dump (fresh install and
// ?all=1), the per-connection loop (active connections, where a stored
// connection would otherwise publish its models), the loose custom-model pass,
// and the all-inactive fallback.
//
// In every mode and under every configuration, a provider marked Hidden must
// never appear in /v1/models — under its canonical name or under any alias that
// resolves to it.
func TestHandleModels_HiddenProviderAbsentFromEveryMode(t *testing.T) {
	hiddenPrefixes := hiddenPrefixSpellings()

	configs := map[string]func(t *testing.T, database *sql.DB){
		"fresh_install_no_rows": func(t *testing.T, database *sql.DB) {
			t.Helper()
			mustExec(t, database, `DELETE FROM providerConnections`)
		},
		"rows_but_all_inactive": func(t *testing.T, database *sql.DB) {
			t.Helper()
			mustExec(t, database, `DELETE FROM providerConnections`)
			// One row per spelling of the hidden family, all switched off.
			mustExec(t, database, `INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
				('conn-mmf-off', 'mmf', 'none', 'MMF off', 1, 0, '{}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`)
			mustExec(t, database, `INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
				('conn-ds-off', 'deepseek', 'apikey', 'DeepSeek off', 1, 0, '{"apiKey":"sk-off"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`)
			// A custom model pinned to the hidden provider. appendLooseCustomModels
			// must drop this even when ?all=1 leaves the listing unfiltered.
			mustExec(t, database, `INSERT INTO kv (scope, key, value) VALUES ('customModels', 'mimo-free|probe-model|llm', '{"providerAlias":"mimo-free","id":"probe-model","type":"llm","name":"probe-model"}')`)
		},
		"connected_and_active": func(t *testing.T, database *sql.DB) {
			t.Helper()
			// An active connection to the hidden provider. The per-connection loop
			// must not publish its models.
			mustExec(t, database, `INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
				('conn-mmf-on', 'mmf', 'none', 'MMF on', 1, 1, '{}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`)
		},
	}

	for cfgName, setup := range configs {
		t.Run(cfgName, func(t *testing.T) {
			database, cleanup := setupChatTestDB(t)
			defer cleanup()
			setup(t, database)
			h := NewChatHandler(db.NewRepo(database))

			for _, query := range []string{"", "?connected=1", "?all=1"} {
				ids := modelsIDsQuery(t, h, query)
				if len(ids) == 0 {
					t.Fatalf("%s %q published no models; the absence assertions below would pass vacuously", cfgName, query)
				}
				for _, id := range ids {
					prefix, _, ok := strings.Cut(id, "/")
					if ok && hiddenPrefixes[prefix] {
						t.Errorf("%s %q published hidden provider entry %q", cfgName, query, id)
					}
				}
			}
		})
	}
}

// hiddenPrefixSpellings is every ProviderModels key that resolves to a Hidden
// provider, taken from the registry so the test keeps covering the whole set
// rather than the names it happens to mention.
func hiddenPrefixSpellings() map[string]bool {
	out := make(map[string]bool, 4)
	for id, cfg := range providers.KnownProviders {
		if cfg.Hidden {
			out[id] = true
		}
	}
	for alias := range providers.ProviderModels {
		if out[providers.ResolveAlias(alias)] {
			out[alias] = true
		}
	}
	return out
}

func mustExec(t *testing.T, database *sql.DB, query string) {
	t.Helper()
	if _, err := database.Exec(query); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}
