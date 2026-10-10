package db

import (
	"database/sql"
	"fmt"
)

// legacyProviderIDs maps a stored providerConnections.provider value that no
// longer exists in the catalog to its current canonical id. "ollama-cloud"
// was never a 9router-go provider id (the catalog has only ever shipped
// "ollama"); rows with that value were synced in from a sibling project that
// uses a different naming scheme (see AGENTS.md §1 "OmniRoute"). Left
// unmapped, getProviderConfig has no KnownProviders entry and no stored
// baseUrl to fall back on, so chat requests on those connections fail with
// "has no baseUrl in connection data and is not in KnownProviders", and the
// dashboard topology falls through to the raw connection name (the account
// email) as the node label — see #<issue>.
var legacyProviderIDs = map[string]string{
	"ollama-cloud": "ollama",
}

// MigrateLegacyProviderIDs rewrites providerConnections rows stuck on a
// provider id the catalog no longer recognizes. It is idempotent — rows
// already on the canonical id are untouched — and is safe to run on every
// boot. Failure is logged by the caller and does not block startup: a row
// left on the legacy id just keeps failing the way it already was.
func MigrateLegacyProviderIDs(db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("migrate legacy provider ids: nil db")
	}
	for legacy, canonical := range legacyProviderIDs {
		if _, err := db.Exec(
			`UPDATE providerConnections SET provider = ? WHERE provider = ?`,
			canonical, legacy,
		); err != nil {
			return fmt.Errorf("migrate legacy provider id %q -> %q: %w", legacy, canonical, err)
		}
	}
	return nil
}
