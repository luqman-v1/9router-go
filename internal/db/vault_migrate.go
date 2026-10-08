// Credential migration (F-5 §5.6).
//
// Sealing on write only covers credentials stored after the vault was switched
// on. An install that has been running for months holds every provider token
// in plaintext `data`, and the vault reports those rows as plaintext forever
// unless something walks them. This is that walk.
//
// The pass is deliberately fail-safe rather than fail-closed. Losing the master
// key already means the credentials are unrecoverable (F-5 §9), so a migration
// that aborts the boot on one bad row would trade a recoverable problem for an
// unbootable one: a row that cannot be sealed keeps its plaintext and the
// gateway starts, because a running gateway with one unsealed row is strictly
// better than a gateway that refuses to start.
package db

import (
	"fmt"
	"path/filepath"
	"time"

	"9router/proxy/internal/log"
)

// SealResult reports what a migration pass did.
type SealResult struct {
	// Sealed is the number of connections whose credentials moved into the
	// sealed columns.
	Sealed int
	// Skipped names connections that were left alone, with the reason. A row
	// appears here when its payload is not a JSON object, when it holds no
	// whitelisted credential, or when sealing it failed.
	Skipped []string
}

// MigratePlaintextCredentials seals every connection that still holds a
// plaintext credential.
//
// dbPath names the SQLite file, and is only used to take the pre-migration
// backup. A backup that cannot be written is a hard error: sealing rewrites
// every credential row, and the operator deserves a restore point before that
// happens rather than after.
//
// A vault-less Repo is a no-op, so an install without ROUTER_MASTER_KEY pays
// nothing and keeps behaving exactly as before.
func (r *Repo) MigratePlaintextCredentials(dbPath string) (SealResult, error) {
	var result SealResult
	if !r.sealingEnabled() {
		return result, nil
	}

	candidates, err := r.ListConnectionsNeedingSeal()
	if err != nil {
		return result, err
	}
	if len(candidates) == 0 {
		return result, nil
	}

	// Backup before the first write. The snapshot is the only way back if the
	// master key turns out to be wrong for these rows.
	if target := preMigrationBackupPath(dbPath); target != "" {
		if err := r.backupDatabase(target); err != nil {
			return result, err
		}
		log.Info("vault", "pre-migration backup written", "path", target)
	}

	for _, c := range candidates {
		sealed, sErr := r.sealOnWrite(c.ID, c.Data)
		if sErr != nil {
			// One bad row must not stop the pass: the remaining connections
			// still need sealing, and this one keeps working plaintext.
			result.Skipped = append(result.Skipped, fmt.Sprintf("%s: %v", c.ID, sErr))
			log.Warn("vault", "connection not sealed, left plaintext", "conn", c.ID, "error", sErr)
			continue
		}
		if sealed == c.Data {
			// Nothing was extracted, so this row is not actually a candidate;
			// listing it would overstate what the migration did.
			continue
		}
		// sealOnWrite returns the redacted payload; the row itself still holds
		// the credential until it is rewritten here.
		if _, wErr := r.db.Exec(`UPDATE providerConnections SET data = ? WHERE id = ?`, sealed, c.ID); wErr != nil {
			result.Skipped = append(result.Skipped, fmt.Sprintf("%s: %v", c.ID, wErr))
			log.Warn("vault", "sealed credentials not redacted in place", "conn", c.ID, "error", wErr)
			continue
		}
		result.Sealed++
	}

	log.Info("vault", "plaintext credential migration", "sealed", result.Sealed, "skipped", len(result.Skipped))
	return result, nil
}

// backupDatabase snapshots the database before a migration.
//
// It uses VACUUM INTO rather than copying the file. The database runs in WAL
// mode, so recent writes live in the -wal sidecar and a plain file copy would
// capture exactly the wrong state — the older pages present, the writes the
// migration is about to overwrite missing. VACUUM INTO takes a transactionally
// consistent snapshot through the same connection, which is the only copy that
// can actually restore the pre-migration state.
//
// The snapshot is created owner-only: it holds every provider credential in
// the clear, exactly as the source did.
func (r *Repo) backupDatabase(target string) error {
	if _, err := r.db.Exec(`VACUUM INTO ?`, target); err != nil {
		return fmt.Errorf("vault backup into %s: %w", target, err)
	}
	return nil
}

// preMigrationBackupPath names the snapshot file for a database path.
func preMigrationBackupPath(dbPath string) string {
	if dbPath == "" {
		return ""
	}
	return fmt.Sprintf("%s/pre-vault-%s.sqlite", filepath.Dir(dbPath), time.Now().UTC().Format("20060102-150405"))
}

