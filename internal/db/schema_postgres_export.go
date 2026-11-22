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

// EnsureSchemaPostgresWithoutSeed bootstraps the PG schema but skips the
// default settings/_meta rows. Used by `db import` so imported values win;
// callers must seed afterwards (schemaPostgres with seed=true) to fill a
// genuinely empty database.
func EnsureSchemaPostgresWithoutSeed(pg *sql.DB) error {
	return schemaPostgresWithSeed(context.Background(), pg, false)
}
