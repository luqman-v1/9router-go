package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"9router/proxy/internal/keikey"
	"9router/proxy/internal/models"
)

// ErrApiKeyImportConflict reports an import that cannot be applied without
// changing stored state: the id or the secret already identifies another row,
// the secret is malformed, or it is the operator's reserved environment key.
// The handler maps it to 409.
var ErrApiKeyImportConflict = errors.New("api key import conflict")

// ErrApiKeyImportInvalid reports a body the importer cannot act on: a malformed
// secret or an id that is not a UUID. The handler maps it to 400.
var ErrApiKeyImportInvalid = errors.New("api key import invalid")

// apiKeyImportPattern is the accepted secret shape: the `sk-` prefix followed
// by 29..253 characters of the key alphabet, so 32..256 characters in total,
// with no whitespace and no newline. Anchored at both ends, so a trailing
// newline pasted with the key is refused rather than silently trimmed into a
// secret the caller does not hold.
var apiKeyImportPattern = regexp.MustCompile(`^sk-[A-Za-z0-9_-]{29,253}$`)

// reservedApiKeyEnv is the operator's own key, injected through the
// environment. It is never a database row, so importing it would create a
// second credential the operator cannot see or revoke.
const reservedApiKeyEnv = "9ROUTER_API_KEY"

// ImportApiKeyParams is one import request. ID is optional: an empty one mints
// a UUID. Supplying the same id with the same key is the idempotent retry
// (200 OK); a duplicate secret without a matching id is always a conflict.
type ImportApiKeyParams struct {
	ID            string
	Key           string
	Name          string
	MachineID     string
	AllowedModels []string
}

// ImportApiKeyResult reports whether this call created the row. Created is
// false when an explicit id resolved to the row holding that same key, which
// is the idempotent-retry answer rather than an error.
type ImportApiKeyResult struct {
	Created bool
	Key     *models.APIKey
}

// ImportApiKey inserts a client API key idempotently.
//
// The contract an importer needs is "apply this key, and applying it twice
// changes nothing": a row that already holds the id is returned untouched when
// the supplied secret is the one it holds, and refused when it is not. A
// duplicate secret without a matching id is always a conflict (409). The
// secret is checked against the row's argon2id verifier, its plaintext column
// and its lookup hash; the stored hash columns are never rewritten.
func (r *Repo) ImportApiKey(ctx context.Context, p ImportApiKeyParams) (ImportApiKeyResult, error) {
	if r == nil || r.db == nil {
		return ImportApiKeyResult{}, fmt.Errorf("import api key: nil repo")
	}
	if !apiKeyImportPattern.MatchString(p.Key) {
		return ImportApiKeyResult{}, fmt.Errorf("import api key: %w: key must be 32..256 characters: sk- plus letters, digits, '_' or '-'", ErrApiKeyImportInvalid)
	}
	id := p.ID
	if id == "" {
		id = uuid.NewString()
	} else if _, err := uuid.Parse(id); err != nil {
		return ImportApiKeyResult{}, fmt.Errorf("import api key: %w: id must be a UUID", ErrApiKeyImportInvalid)
	}
	if reserved := os.Getenv(reservedApiKeyEnv); reserved != "" && reserved == p.Key {
		return ImportApiKeyResult{}, fmt.Errorf("import api key: %w: the key is reserved for %s", ErrApiKeyImportConflict, reservedApiKeyEnv)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return ImportApiKeyResult{}, fmt.Errorf("import api key %s: begin transaction: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }()

	result, stored, err := r.importApiKeyTx(ctx, tx, id, p)
	if err != nil {
		return ImportApiKeyResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ImportApiKeyResult{}, fmt.Errorf("import api key %s: commit: %w", id, err)
	}
	result.Key = stored
	return result, nil
}

// importApiKeyTx runs the whole import inside one transaction: the id lookup,
// the secret lookup, the insert and the allowlist write either all land or none
// do, so a concurrent import can never observe a half-applied key. The stored
// row is read back before the commit and returned to the caller.
func (r *Repo) importApiKeyTx(ctx context.Context, tx *sql.Tx, id string, p ImportApiKeyParams) (ImportApiKeyResult, *models.APIKey, error) {
	// The id decides first: a row already holding it either authenticates the
	// supplied secret, which is the idempotent answer, or it does not, which is
	// a conflict. Nothing is written on either path.
	var existingKey string
	var existingHash, existingLookup sql.NullString
	err := tx.QueryRowContext(ctx,
		`SELECT key, keyHash, lookupHash FROM apiKeys WHERE id = ?`, id,
	).Scan(&existingKey, &existingHash, &existingLookup)
	switch {
	case err == nil:
		if !importRowHoldsKey(existingKey, existingHash, existingLookup, p.Key) {
			return ImportApiKeyResult{}, nil, fmt.Errorf("import api key: %w: id %s already holds a different key", ErrApiKeyImportConflict, id)
		}
		return r.importExisting(ctx, tx, id)
	case !errors.Is(err, sql.ErrNoRows):
		return ImportApiKeyResult{}, nil, fmt.Errorf("import api key %s: read by id: %w", id, err)
	}

	// The secret decides next. A row that already holds the secret is a
	// duplicate: the caller must resolve it, not get a silent redirect to the
	// other row. The only idempotent-replay answer is the id path above, where
	// an explicit id matched the row holding that same secret (OmniRoute
	// apiKeyImport.ts answers 409 for a duplicate secret; Issue #1557).
	secretID, err := importLookupSecret(ctx, tx, p.Key)
	if err != nil {
		return ImportApiKeyResult{}, nil, err
	}
	if secretID != "" {
		return ImportApiKeyResult{}, nil, fmt.Errorf("import api key: %w: key already belongs to %s", ErrApiKeyImportConflict, secretID)
	}

	stored, err := r.importInsertRow(ctx, tx, id, p)
	if err != nil {
		return ImportApiKeyResult{}, nil, err
	}
	return ImportApiKeyResult{Created: true}, stored, nil
}

// importExisting answers the idempotent case: the row is returned exactly as
// stored, so a retry rewrites neither the secret nor a name or allowlist the
// operator may have changed since.
func (r *Repo) importExisting(ctx context.Context, tx *sql.Tx, id string) (ImportApiKeyResult, *models.APIKey, error) {
	stored, err := r.importReadBack(ctx, tx, id)
	if err != nil {
		return ImportApiKeyResult{}, nil, err
	}
	return ImportApiKeyResult{Created: false}, stored, nil
}

// importRowHoldsKey reports whether a stored row authenticates the supplied
// secret. A row carrying an argon2id verifier is confirmed with it; otherwise
// the plaintext column answers, and a row with neither is confirmed only by a
// matching lookup hash. A row that cannot be confirmed is a mismatch, never a
// match.
func importRowHoldsKey(storedKey string, storedHash, storedLookup sql.NullString, key string) bool {
	if storedHash.Valid && storedHash.String != "" {
		return keikey.Verify(key, storedHash.String)
	}
	if storedKey == key {
		return true
	}
	return storedLookup.Valid && storedLookup.String != "" && storedLookup.String == keikey.LookupHash(key)
}

// importLookupSecret returns the id of the row the secret already identifies,
// or "" when it identifies none. The lookup hash is the indexed identity of a
// hashed row and the plaintext column the identity of a legacy one, so both are
// checked in one statement.
func importLookupSecret(ctx context.Context, tx *sql.Tx, key string) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx,
		`SELECT id FROM apiKeys WHERE lookupHash = ? OR key = ? LIMIT 1`,
		keikey.LookupHash(key), key,
	).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("import api key: read by secret: %w", err)
	}
	return "", nil
}

// importInsertRow writes the new row with the secret hashed, the plaintext
// column holding the hashed-row sentinel, and the allowlist in the same
// transaction.
func (r *Repo) importInsertRow(ctx context.Context, tx *sql.Tx, id string, p ImportApiKeyParams) (*models.APIKey, error) {
	verifier, err := keikey.Hash(p.Key)
	if err != nil {
		return nil, fmt.Errorf("import api key %s: hash secret: %w", id, err)
	}
	lookup := keikey.LookupHash(p.Key)
	now := time.Now().UTC().Format(time.RFC3339)

	var nameVal, machineVal any
	if p.Name != "" {
		nameVal = p.Name
	}
	if p.MachineID != "" {
		machineVal = p.MachineID
	}
	// isActive is a `?` bound to true rather than a literal 1: apiKeys.isActive
	// is INTEGER on SQLite and BOOLEAN on PostgreSQL, and the literal 1 is an
	// integer expression PostgreSQL refuses to coerce to boolean. The binding
	// stores 1 on SQLite and TRUE on PostgreSQL.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO apiKeys (id, key, name, machineId, isActive, createdAt, keyHash, lookupHash, keyDisplay)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, hashedKeySentinel(lookup), nameVal, machineVal, true, now, verifier, lookup, keikey.Mask(p.Key),
	); err != nil {
		return nil, fmt.Errorf("import api key %s: insert: %w", id, err)
	}
	if err := importInsertAllowlist(ctx, tx, id, p.AllowedModels, now); err != nil {
		return nil, err
	}
	return r.importReadBack(ctx, tx, id)
}

// importInsertAllowlist writes the optional per-key allowlist, trimmed and
// deduplicated the way SetAllowedModels does it. An empty list writes nothing,
// which leaves the allow-everything default in place.
func importInsertAllowlist(ctx context.Context, tx *sql.Tx, id string, allowed []string, now string) error {
	seen := make(map[string]bool, len(allowed))
	for _, m := range allowed {
		m = strings.TrimSpace(m)
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO `+modelAccessTable+` (api_key_id, model, created_at) VALUES (?, ?, ?)`,
			id, m, now,
		); err != nil {
			return fmt.Errorf("import api key %s: insert allowlist %q: %w", id, m, err)
		}
	}
	return nil
}

// importReadBack loads the stored row through the shared column list, so the
// caller sees exactly what a later GET would report.
func (r *Repo) importReadBack(ctx context.Context, tx *sql.Tx, id string) (*models.APIKey, error) {
	var k models.APIKey
	withAdditive := r.additiveColumnsPresent()
	dest, apply := apiKeyScan(&k, withAdditive)
	err := tx.QueryRowContext(ctx, r.apiKeyColumnsFor()+` FROM apiKeys WHERE id = ? LIMIT 1`, id).Scan(dest...)
	if err != nil {
		return nil, fmt.Errorf("import api key %s: read back: %w", id, err)
	}
	apply()
	return &k, nil
}
