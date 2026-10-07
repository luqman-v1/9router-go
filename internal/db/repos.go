package db

import (
	"database/sql"
	"sync/atomic"
	"time"
)

// Repo wraps the SQLite handle and groups all persistence queries.
//
// The queries live in files named after the table they touch, so this file
// holds only the type every reader hangs off:
//
//	apikeys.go        apiKeys
//	connections.go    providerConnections
//	providernodes.go  providerNodes
//	combos.go         combos
//	aliases.go        kv scopes: modelAliases, customModels
type Repo struct {
	db *sql.DB

	// activePoolIDs caches ActivePoolIDs. It lives on the Repo rather than in
	// package state because the answer belongs to one database: two Repos over
	// different handles would otherwise read each other's pool list.
	activePoolIDs atomic.Value // *[]string
	// vault is the optional credential vault. It is stored rather than held by
	// value so a key rotation can swap the live vault without touching any
	// reader that already captured a *Repo. A nil value means "no vault", which
	// is every install that never set ROUTER_MASTER_KEY.
	vault atomic.Value // *vault.Vault
}

// nowUTC is the timestamp format every connection write stamps.
func nowUTC() string { return time.Now().UTC().Format(time.RFC3339) }

// NewRepo creates a new repository instance using the provided SQL database connection.
func NewRepo(db *sql.DB) *Repo {
	return &Repo{db: db}
}

// RawDB returns the underlying *sql.DB connection for direct queries.
func (r *Repo) RawDB() *sql.DB {
	return r.db
}