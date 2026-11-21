package db

import (
	"context"
	"database/sql"
)

// EnsureSchemaPostgres is the exported wrapper around schemaPostgres so
// callers outside this package (the `9router-go db import` migration
// tooling) can create the PostgreSQL schema without reaching for an
// unexported symbol.
func EnsureSchemaPostgres(pg *sql.DB) error {
	return schemaPostgres(context.Background(), pg)
}
