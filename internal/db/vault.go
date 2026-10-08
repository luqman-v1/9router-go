// Credential sealing (F-5).
//
// A connection's secrets live in two places at once: `data` holds the JSON
// config the model resolver and dashboard read, and three sealed column pairs
// hold the credentials under envelope encryption. Only the five whitelisted
// fields below are ever sealed. Everything else — enabledModels, baseUrl,
// assignedModel, cooldown bookkeeping — stays plaintext on purpose: the model
// resolver walks providerConnections on every listing and must never depend on
// a decrypt.
//
// The vault is optional. With no master key configured the Repo seals nothing
// and every reader passes rows through untouched, so an existing install keeps
// behaving exactly as before.
package db

import (
	"database/sql"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"slices"

	"9router/proxy/internal/log"
	"9router/proxy/internal/models"
	"9router/proxy/internal/vault"
)

// The three sealed column pairs on providerConnections.
const (
	SlotSecret  = "secret"
	SlotToken   = "token"
	SlotRefresh = "refresh"
)

// slotFields maps each sealed slot to the whitelisted data fields it holds.
// The list is exhaustive on purpose: an unlisted field is config, not a
// credential, and sealing one would make a hot read path depend on the master
// key.
var slotFields = map[string][]string{
	SlotSecret:  {"apiKey", "clientSecret"},
	SlotToken:   {"accessToken", "authToken"},
	SlotRefresh: {"refreshToken"},
}

// slotColumns maps a slot to its (wrappedDEK, ciphertext) column pair. The
// column names are literals rather than derived so a rename cannot silently
// retarget an UPDATE at the wrong pair.
var slotColumns = map[string][2]string{
	SlotSecret:  {"secretWrappedDEK", "secretCiphertext"},
	SlotToken:   {"tokenWrappedDEK", "tokenCiphertext"},
	SlotRefresh: {"refreshWrappedDEK", "refreshCiphertext"},
}

// SealedSlots is the slot order every fixed-width sealed read and write uses.
var SealedSlots = []string{SlotSecret, SlotToken, SlotRefresh}

// sealedColumns is the fixed SELECT list every vault read needs, in slot order.
const sealedColumns = `secretWrappedDEK, secretCiphertext, tokenWrappedDEK, tokenCiphertext, refreshWrappedDEK, refreshCiphertext`

// SetVault attaches a vault to the Repo. A nil vault disables sealing.
func (r *Repo) SetVault(v *vault.Vault) { r.vault.Store(v) }

// Vault returns the attached vault, or nil when none is configured.
func (r *Repo) Vault() *vault.Vault {
	v, _ := r.vault.Load().(*vault.Vault)
	if v == nil {
		return nil
	}
	return v
}

// sealingEnabled reports whether this Repo encrypts credentials.
func (r *Repo) sealingEnabled() bool {
	v := r.Vault()
	return v != nil && v.Enabled()
}

// GetConnectionSealed reads one slot's sealed pair. ok is false when the row
// is missing or the slot was never sealed.
func (r *Repo) GetConnectionSealed(id, field string) (wrappedDEK, ciphertext string, ok bool, err error) {
	cols, known := slotColumns[field]
	if !known {
		return "", "", false, fmt.Errorf("get connection sealed %s: unknown slot %q", id, field)
	}
	row := r.db.QueryRow(
		`SELECT `+cols[0]+`, `+cols[1]+` FROM providerConnections WHERE id = ? LIMIT 1`, id)
	if err = row.Scan(&wrappedDEK, &ciphertext); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", false, nil
		}
		return "", "", false, fmt.Errorf("get connection sealed %s: %w", id, err)
	}
	return wrappedDEK, ciphertext, wrappedDEK != "" || ciphertext != "", nil
}

// SetConnectionSealed writes one slot's sealed pair with a single UPDATE. It
// never reads the row first: two background writers race on this table, and a
// read-modify-write here would let one clobber the other's column.
func (r *Repo) SetConnectionSealed(id, field, wrappedDEK, ciphertext string) error {
	cols, known := slotColumns[field]
	if !known {
		return fmt.Errorf("set connection sealed %s: unknown slot %q", id, field)
	}
	res, err := r.db.Exec(
		`UPDATE providerConnections SET `+cols[0]+` = ?, `+cols[1]+` = ?, updatedAt = ? WHERE id = ?`,
		wrappedDEK, ciphertext, nowUTC(), id)
	if err != nil {
		return fmt.Errorf("set connection sealed %s/%s: %w", id, field, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("set connection sealed %s/%s: no such connection", id, field)
	}
	return nil
}

// ConnectionSealCandidate is a row still carrying a plaintext credential with
// no sealed slot written yet.
type ConnectionSealCandidate struct {
	ID   string
	Data string
}

// ListConnectionsNeedingSeal returns connections holding a whitelisted secret
// in `data` with no sealed slot yet. The sealed columns being empty is what
// keeps an already-sealed row from being re-sealed on every scan.
func (r *Repo) ListConnectionsNeedingSeal() ([]ConnectionSealCandidate, error) {
	rows, err := r.db.Query(
		`SELECT id, data FROM providerConnections
		 WHERE secretCiphertext = '' AND tokenCiphertext = '' AND refreshCiphertext = ''`)
	if err != nil {
		return nil, fmt.Errorf("list connections needing seal: %w", err)
	}
	defer rows.Close()

	var out []ConnectionSealCandidate
	for rows.Next() {
		var c ConnectionSealCandidate
		if err = rows.Scan(&c.ID, &c.Data); err != nil {
			return nil, fmt.Errorf("list connections needing seal: scan: %w", err)
		}
		if len(connSecretFields(c.Data)) > 0 {
			out = append(out, c)
		}
	}
	return out, rows.Err()
}

// VaultCounts reports how many connections are sealed and how many still hold a
// plaintext credential, for the dashboard status route.
func (r *Repo) VaultCounts() (sealed, plaintext int, err error) {
	rows, err := r.db.Query(
		`SELECT
		   CASE WHEN secretCiphertext <> '' OR tokenCiphertext <> '' OR refreshCiphertext <> '' THEN 1 ELSE 0 END,
		   CASE WHEN secretCiphertext = '' AND tokenCiphertext = '' AND refreshCiphertext = '' THEN 1 ELSE 0 END
		 FROM providerConnections`)
	if err != nil {
		return 0, 0, fmt.Errorf("vault counts: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var isSealed, isPlain int
		if err = rows.Scan(&isSealed, &isPlain); err != nil {
			return 0, 0, fmt.Errorf("vault counts: scan: %w", err)
		}
		sealed += isSealed
		plaintext += isPlain
	}
	if err = rows.Err(); err != nil {
		return 0, 0, fmt.Errorf("vault counts: %w", err)
	}
	return sealed, plaintext, nil
}

// RewrapConnectionDEKs re-wraps every stored DEK under newMasterKey, leaving
// ciphertext untouched. Rows whose DEK cannot be unwrapped (wrong current key,
// tampered) are skipped and named in failures, never rewritten: a rotation
// must not destroy the rows it cannot read.
func (r *Repo) RewrapConnectionDEKs(v *vault.Vault, newMasterKey []byte) (rewrapped int, failures []string, err error) {
	if v == nil || !v.Enabled() {
		return 0, nil, vault.ErrDisabled
	}
	rows, err := r.db.Query(`SELECT id, ` + sealedColumns + ` FROM providerConnections
		WHERE secretWrappedDEK <> '' OR tokenWrappedDEK <> '' OR refreshWrappedDEK <> ''`)
	if err != nil {
		return 0, nil, fmt.Errorf("rewrap connection DEKs: %w", err)
	}
	type target struct{ id, slot, wrapped string }
	var targets []target
	for rows.Next() {
		var (
			id                                string
			secretDEK, secretCT, tokenDEK     string
			tokenCT, refreshDEK, refreshCT   string
		)
		if err = rows.Scan(&id, &secretDEK, &secretCT, &tokenDEK, &tokenCT, &refreshDEK, &refreshCT); err != nil {
			rows.Close()
			return 0, nil, fmt.Errorf("rewrap connection DEKs: scan: %w", err)
		}
		for i, wrapped := range []string{secretDEK, tokenDEK, refreshDEK} {
			if wrapped != "" {
				targets = append(targets, target{id: id, slot: SealedSlots[i], wrapped: wrapped})
			}
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return 0, nil, fmt.Errorf("rewrap connection DEKs: %w", err)
	}
	rows.Close()

	for _, t := range targets {
		wrapped, rewrapErr := v.Rewrap(t.wrapped, newMasterKey)
		if rewrapErr != nil {
			failures = append(failures, t.id+"/"+t.slot)
			continue
		}
		_, ciphertext, _, getErr := r.GetConnectionSealed(t.id, t.slot)
		if getErr != nil {
			failures = append(failures, t.id+"/"+t.slot)
			continue
		}
		if setErr := r.SetConnectionSealed(t.id, t.slot, wrapped, ciphertext); setErr != nil {
			failures = append(failures, t.id+"/"+t.slot)
			continue
		}
		rewrapped++
	}
	return rewrapped, failures, nil
}

// sealConnectionData moves the whitelisted credentials out of a data payload
// and into the sealed columns, returning the redacted payload to store. It
// returns data unchanged when the vault is disabled or the payload is not a
// JSON object, so every writer keeps one code path.
func (r *Repo) sealConnectionData(id, data string) (string, error) {
	if !r.sealingEnabled() || data == "" {
		return data, nil
	}
	fields, ok := decodeObject(data)
	if !ok {
		return data, nil
	}
	sealedAny := false
	for _, slot := range SealedSlots {
		payload, taken := takeSlotFields(fields, slot)
		if !taken {
			continue
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			return data, fmt.Errorf("seal connection %s/%s: marshal: %w", id, slot, err)
		}
		wrapped, ciphertext, err := r.Vault().Seal(string(encoded))
		if err != nil {
			return data, fmt.Errorf("seal connection %s/%s: %w", id, slot, err)
		}
		if err = r.SetConnectionSealed(id, slot, wrapped, ciphertext); err != nil {
			return data, err
		}
		// Redact exactly the sealed fields; `remaining` was only used to
		// decide that this slot carries a credential at all.
		for name := range payload {
			delete(fields, name)
		}
		sealedAny = true
	}
	if !sealedAny {
		return data, nil
	}
	redacted, err := json.Marshal(fields)
	if err != nil {
		return data, fmt.Errorf("seal connection %s: marshal redacted: %w", id, err)
	}
	return string(redacted), nil
}

// openConnectionData re-injects the sealed credentials into a data payload. A
// slot that fails to decrypt is left sealed and logged, and the payload is
// still returned with whatever did open — one bad row must never take the other
// connections down with it, and the row keeps its ciphertext for a later retry
// once the right key is back.
func (r *Repo) openConnectionData(id, data string, sealed [6]string) string {
	v := r.Vault()
	if v == nil {
		return data
	}
	fields, ok := decodeObject(data)
	if !ok {
		return data
	}
	opened := false
	for i, slot := range SealedSlots {
		wrapped, ciphertext := sealed[i*2], sealed[i*2+1]
		if wrapped == "" || ciphertext == "" {
			continue
		}
		plain, err := v.Open(wrapped, ciphertext)
		if err != nil {
			log.Warn("vault", "sealed credential could not be opened; row left untouched",
				"conn", id, "slot", slot, "error", err)
			continue
		}
		decoded, ok := decodeObject(plain)
		if !ok {
			log.Warn("vault", "sealed payload is not an object; row left untouched",
				"conn", id, "slot", slot)
			continue
		}
		maps.Copy(fields, decoded)
		opened = true
	}
	if !opened {
		return data
	}
	merged, err := json.Marshal(fields)
	if err != nil {
		return data
	}
	return string(merged)
}

// connSecretFields returns the whitelisted credential names present in a data
// payload with a non-empty string value.
func connSecretFields(data string) []string {
	fields, ok := decodeObject(data)
	if !ok {
		return nil
	}
	var found []string
	for _, slot := range SealedSlots {
		for _, name := range slotFields[slot] {
			if s, ok := fields[name].(string); ok && s != "" {
				found = append(found, name)
			}
		}
	}
	return found
}

// takeSlotFields reports the non-empty string credentials for one slot.
func takeSlotFields(fields map[string]any, slot string) (payload map[string]any, taken bool) {
	for name, value := range fields {
		s, isString := value.(string)
		if !isString || s == "" || !slices.Contains(slotFields[slot], name) {
			continue
		}
		if payload == nil {
			payload = make(map[string]any, len(slotFields[slot]))
		}
		payload[name] = value
	}
	return payload, payload != nil
}

// decodeObject parses a JSON object payload. Anything else (empty, a scalar, a
// list, malformed) returns ok=false so the caller leaves the row alone rather
// than mangling a shape it does not understand.
func decodeObject(raw string) (map[string]any, bool) {
	if raw == "" {
		return nil, false
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(raw), &fields); err != nil || fields == nil {
		return nil, false
	}
	return fields, true
}


// hydrate re-injects sealed credentials into a connection's data payload.
//
// It costs one extra SELECT per row and only when the vault is enabled; a
// plaintext install reads exactly as before. The sealed columns may be absent
// on a database built by an older fixture, which is why a failed query
// degrades to "no sealed credentials" instead of failing the read.
func (r *Repo) hydrate(conn *models.ProviderConnection) {
	if conn == nil || !r.sealingEnabled() {
		return
	}
	conn.Data = r.openConnectionData(conn.ID, conn.Data, r.sealedPairFor(conn.ID))
}

// sealedPairFor reads all three sealed column pairs for one row.
func (r *Repo) sealedPairFor(id string) (sealed [6]string) {
	row := r.db.QueryRow(`SELECT `+sealedColumns+` FROM providerConnections WHERE id = ? LIMIT 1`, id)
	if err := row.Scan(&sealed[0], &sealed[1], &sealed[2], &sealed[3], &sealed[4], &sealed[5]); err != nil {
		return [6]string{}
	}
	return sealed
}

// sealOnWrite moves any plaintext credential in data into the sealed columns
// and returns the payload to store. Writers call it once the row exists, so a
// create is insert-then-seal and an update is seal-then-write. A vault-less
// install gets data back unchanged and issues no statement at all.
func (r *Repo) sealOnWrite(id, data string) (string, error) {
	return r.sealConnectionData(id, data)
}
