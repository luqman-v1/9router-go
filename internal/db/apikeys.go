package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"9router/proxy/internal/models"
)

// errNoSuchApiKey signals an update that matched no row.
var errNoSuchApiKey = errors.New("no such api key")

// ValidateApiKey checks if the given API key exists and is active.
// Legacy plaintext path; hashed rows are resolved through
// FindApiKeyByLookup plus an argon2id verification.
func (r *Repo) ValidateApiKey(key string) (bool, error) {
	var active int
	err := r.db.QueryRow("SELECT isActive FROM apiKeys WHERE key = ? LIMIT 1", key).Scan(&active)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return active == 1, nil
}

// apiKeyColumns is the shared column list for every full-row apiKeys read.
const apiKeyColumns = `SELECT id, key, name, machineId, isActive, createdAt,
	rateLimitRPM, rateLimitTPM, rateLimitConcurrency,
	keyHash, lookupHash, keyDisplay,
	expiresAt, lastUsedAt, usedCount, metadata`

// apiKeyScan returns the Scan destinations for apiKeyColumns plus an applier
// that must run after Scan has filled the nullable columns.
func apiKeyScan(k *models.APIKey) (dest []any, apply func()) {
	var nameVal, machineVal sql.NullString
	var rateRPM, rateTPM, rateConc sql.NullInt64
	var keyHash, lookupHash, keyDisplay sql.NullString
	var expiresAt, lastUsedAt, metadata sql.NullString
	var usedCount sql.NullInt64
	dest = []any{&k.ID, &k.Key, &nameVal, &machineVal, &k.IsActive, &k.CreatedAt,
		&rateRPM, &rateTPM, &rateConc, &keyHash, &lookupHash, &keyDisplay,
		&expiresAt, &lastUsedAt, &usedCount, &metadata}
	apply = func() {
		applyApiKeyNulls(k, nameVal, machineVal, rateRPM, rateTPM, rateConc, usedCount,
			keyHash, lookupHash, keyDisplay, expiresAt, lastUsedAt, metadata)
	}
	return dest, apply
}

// applyApiKeyNulls copies the scanned nullable columns onto the model.
func applyApiKeyNulls(k *models.APIKey, nameVal, machineVal sql.NullString,
	rateRPM, rateTPM, rateConc, usedCount sql.NullInt64,
	keyHash, lookupHash, keyDisplay sql.NullString,
	expiresAt, lastUsedAt, metadata sql.NullString) {
	if nameVal.Valid {
		k.Name = &nameVal.String
	}
	if machineVal.Valid {
		k.MachineID = &machineVal.String
	}
	if rateRPM.Valid {
		v := int(rateRPM.Int64)
		k.RateLimitRPM = &v
	}
	if rateTPM.Valid {
		v := int(rateTPM.Int64)
		k.RateLimitTPM = &v
	}
	if rateConc.Valid {
		v := int(rateConc.Int64)
		k.RateLimitConcurrency = &v
	}
	if keyHash.Valid {
		k.KeyHash = &keyHash.String
	}
	if lookupHash.Valid {
		k.LookupHash = &lookupHash.String
	}
	if keyDisplay.Valid {
		k.KeyDisplay = &keyDisplay.String
	}
	if expiresAt.Valid {
		k.ExpiresAt = &expiresAt.String
	}
	if lastUsedAt.Valid {
		k.LastUsedAt = &lastUsedAt.String
	}
	if usedCount.Valid {
		v := int(usedCount.Int64)
		k.UsedCount = &v
	}
	if metadata.Valid {
		k.Metadata = &metadata.String
	}
}

// GetApiKeyByKey retrieves detailed APIKey information by plaintext key.
// Kept for legacy rows whose `key` column still holds the secret; hashed
// rows have a blank `key` and are found via FindApiKeyByLookup.
func (r *Repo) GetApiKeyByKey(key string) (*models.APIKey, error) {
	return r.queryApiKey("FROM apiKeys WHERE key = ? LIMIT 1", key)
}

// FindApiKeyByLookup retrieves an APIKey by its SHA-256 lookup hash (F-6).
// This is the primary lookup path for hashed rows; it never compares
// plaintext. It returns (nil, nil) when no row matches.
func (r *Repo) FindApiKeyByLookup(lookupHash string) (*models.APIKey, error) {
	if lookupHash == "" {
		return nil, nil
	}
	return r.queryApiKey("FROM apiKeys WHERE lookupHash = ? LIMIT 1", lookupHash)
}

// queryApiKey runs a single-row apiKeys read and maps the scanned columns.
func (r *Repo) queryApiKey(whereClause string, arg string) (*models.APIKey, error) {
	var apiKey models.APIKey
	dest, apply := apiKeyScan(&apiKey)
	err := r.db.QueryRow(apiKeyColumns+" "+whereClause, arg).Scan(dest...)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get api key: %w", err)
	}
	apply()
	return &apiKey, nil
}

// hashedKeyPrefix marks a `key` column as a hashed row standing in for the
// original secret. `apiKeys.key` is TEXT UNIQUE NOT NULL, so every row cannot
// simply be blanked to the same value — the second hashed key would collide
// with the first. The sentinel is derived from that row's lookup hash, which
// makes it unique per row, and carries a prefix no client ever sends, so a
// legacy `WHERE key = ?` lookup can never authenticate against it.
const hashedKeyPrefix = "\x00argon2id:"

func hashedKeySentinel(lookupHash string) string {
	return hashedKeyPrefix + lookupHash
}

// IsHashedKeyRow reports whether a row is a pre-#199 hashed row: its `key`
// column holds the sentinel rather than a secret, so the plaintext is
// unrecoverable. Such a row still authenticates through its argon2id verifier
// (see middleware.resolveApiKey) but cannot be revealed in the dashboard.
func IsHashedKeyRow(k *models.APIKey) bool {
	return k != nil && strings.HasPrefix(k.Key, hashedKeyPrefix)
}

// HashApiKeyRow fills the F-6 hash columns for a row and replaces the
// plaintext `key` column with the hashed-row sentinel. A sentinel `key` is how
// a hashed row is recognised; legacy rows keep their plaintext and
// authenticate through GetApiKeyByKey until this runs. After it runs the secret
// exists only as an argon2id verifier and a masked display value.
func (r *Repo) HashApiKeyRow(id, keyHash, lookupHash, keyDisplay string) error {
	res, err := r.db.Exec(
		`UPDATE apiKeys SET key = ?, keyHash = ?, lookupHash = ?, keyDisplay = ? WHERE id = ?`,
		hashedKeySentinel(lookupHash), keyHash, lookupHash, keyDisplay, id,
	)
	if err != nil {
		return fmt.Errorf("hash api key row %s: %w", id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("hash api key row %s: %w", id, errNoSuchApiKey)
	}
	return nil
}

// GetApiKeyByID retrieves one key by its row id rather than by its secret.
// The dashboard policy editor uses it so a partial update can read the current
// values without ever touching the credential columns.
func (r *Repo) GetApiKeyByID(id string) (*models.APIKey, error) {
	return r.queryApiKey("FROM apiKeys WHERE id = ? LIMIT 1", id)
}

// RotateApiKeySecret replaces a row's secret with a new plaintext value and
// refreshes its masked display.
//
// The hash columns are cleared rather than left stale: a row that still carried
// a verifier from before issue #199 would keep being resolved through the
// argon2id path, so its new secret would never be found and the key would
// appear dead to every client holding it.
func (r *Repo) RotateApiKeySecret(id, plaintext, keyDisplay string) error {
	res, err := r.db.Exec(
		`UPDATE apiKeys SET key = ?, keyDisplay = ?, keyHash = '', lookupHash = '' WHERE id = ?`,
		plaintext, keyDisplay, id,
	)
	if err != nil {
		return fmt.Errorf("rotate api key secret %s: %w", id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("rotate api key secret %s: %w", id, errNoSuchApiKey)
	}
	return nil
}

// SetApiKeyDisplay records the masked form of a row's secret without touching
// the secret itself.
func (r *Repo) SetApiKeyDisplay(id, keyDisplay string) error {
	res, err := r.db.Exec(`UPDATE apiKeys SET keyDisplay = ? WHERE id = ?`, keyDisplay, id)
	if err != nil {
		return fmt.Errorf("set api key display %s: %w", id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("set api key display %s: %w", id, errNoSuchApiKey)
	}
	return nil
}
