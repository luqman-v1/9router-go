package app

import (
	"context"
	"database/sql"
	"fmt"

	"go.uber.org/fx"

	"9router/proxy/internal/config"
	"9router/proxy/internal/db"
	"9router/proxy/internal/log"
	"9router/proxy/internal/vault"
)

// DatabaseModule handles database initialization and provides *sql.DB and *db.Repo.
var DatabaseModule = fx.Module("database",
	fx.Provide(
		ProvideDatabase,
		ProvideRepo,
	),
	// An Invoke, not a Provide: the migration produces no value, it is a
	// startup side effect. As a Provide it would be rejected for returning
	// only an error.
	fx.Invoke(MigrateVault),
)

// ProvideDatabase initializes the global SQLite database and registers an OnStop lifecycle hook to close it cleanly.
func ProvideDatabase(lc fx.Lifecycle, cfg *config.Config) (*sql.DB, error) {
	if err := db.InitGlobalDatabase(cfg.DatabasePath); err != nil {
		return nil, fmt.Errorf("database init: %w", err)
	}

	conn, err := db.GetConnection()
	if err != nil {
		return nil, fmt.Errorf("database connect: %w", err)
	}

	// Upstream core schema bootstrap (fresh .9router): create the shared
	// tables/indexes when absent, backfill missing columns, and seed the
	// minimal rows. Idempotent — existing user data is never touched.
	// Best-effort like leases below: a shared test binary may hand us a
	// connection bound to a removed temp file (global singleton); a dead
	// connection is a test artifact, not a production schema failure.
	if err := db.EnsureCoreSchema(conn); err != nil {
		_, statErr := conn.Exec("SELECT 1")
		if statErr == nil {
			return nil, fmt.Errorf("database schema: %w", err)
		}
	}

	// Cross-process lease table for upstream coordination (Freebuff
	// sessions, future scopes). Idempotent: no-op when already present,
	// invisible to dashboards that do not know the table. Best-effort:
	// a shared test binary may hand us a connection bound to a removed
	// temp file (global singleton); leases then simply stay unavailable.
	if err := db.EnsureUpstreamLeases(conn); err != nil {
		_, statErr := conn.Exec("SELECT 1")
		if statErr == nil {
			return nil, fmt.Errorf("database leases: %w", err)
		}
	}

	lc.Append(fx.Hook{
		OnStop: func(ctx context.Context) error {
			return conn.Close()
		},
	})

	return conn, nil
}

// ProvideRepo provides *db.Repo using the database connection.
func ProvideRepo(conn *sql.DB) *db.Repo {
	repo := db.NewRepo(conn)
	repo.SetVault(vault.NewFromEnv())
	return repo
}

// MigrateVault seals any credential still stored in plaintext.
//
// It is a separate provider so it runs after ProvideRepo — the Repo is what
// carries the vault — and before the server starts accepting traffic, so a
// connection is never dispatched with a half-migrated credential.
//
// Failure is not fatal. The migration takes its own backup first, and a row it
// cannot seal keeps working in plaintext, which is strictly better than a
// gateway that refuses to start.
func MigrateVault(repo *db.Repo, cfg *config.Config) error {
	result, err := repo.MigratePlaintextCredentials(cfg.DatabasePath)
	if err != nil {
		log.Warn("vault", "plaintext credential migration skipped", "error", err)
		return nil
	}
	if result.Sealed > 0 || len(result.Skipped) > 0 {
		log.Info("vault", "credential migration complete", "sealed", result.Sealed, "skipped", len(result.Skipped))
	}
	return nil
}
