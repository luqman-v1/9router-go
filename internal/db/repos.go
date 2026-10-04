package db

import (
	"database/sql"
	"sync/atomic"
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
}

// NewRepo creates a new repository instance using the provided SQL database connection.
func NewRepo(db *sql.DB) *Repo {
	return &Repo{db: db}
}

// RawDB returns the underlying *sql.DB connection for direct queries.
func (r *Repo) RawDB() *sql.DB {
	return r.db
}