package db

import "database/sql"

// IsProviderHealthy checks whether at least one active connection for the given
// provider exists without an active modelLock_<model>. Returns true when no
// connections exist (optimistic default for no-auth providers).
// Delegates to Repo to avoid duplicating model lock logic.
func IsProviderHealthy(database *sql.DB, provider, model string) bool {
	return NewRepo(database).IsProviderAvailable(provider, model)
}

// ResetProviderHealth clears modelLock_* fields on provider connections.
//   - provider="" and model="" → all connections
//   - model="" → all connections for provider
//   - both set → specific model on all connections for provider
func ResetProviderHealth(database *sql.DB, provider, model string) error {
	return NewRepo(database).ResetProviderHealth(provider, model)
}
