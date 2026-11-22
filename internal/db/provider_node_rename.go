package db

import (
	"database/sql"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"strings"

	"9router/proxy/internal/models"
)

// Provider node ids are not opaque to the operator: they are the storage key
// for everything the node owns — providerConnections.provider, the customModels
// / disabledModels / modelAliases kv rows keyed or valued by that id, the
// provider column of usageHistory and requestDetails, the provider-keyed
// settings maps, and the model id a combo member spells out. A custom URL suffix
// that renames a node therefore has to carry those references with it, or the
// node silently loses its credentials, its hand-added models, its per-provider
// settings and its usage history the moment the user saves the edit.
//
// Every rewrite runs inside one transaction. A rename that moved the node but
// not its custom models would leave the node advertising models whose
// providerAlias resolves to nothing, which is exactly the kind of half-state
// the user cannot see and cannot undo.

// ErrProviderNodeNotFound is returned when a rename targets an id that has no
// row, so the handler can answer 404 instead of a generic 500.
var ErrProviderNodeNotFound = errors.New("provider node not found")

// ErrProviderNodeIDTaken is returned when the requested new id already belongs
// to another node. It is a conflict, not a failure: the user picks another
// suffix.
var ErrProviderNodeIDTaken = errors.New("provider node id already in use")

// tableExists reports whether a table is present in this database. The rename
// probes the schema instead of inspecting a driver error, so "nothing to move"
// does not depend on a message string the driver is free to reword.
func tableExists(tx *sql.Tx, name string) bool {
	var count int
	if err := tx.QueryRow(
		`SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = ?`, name,
	).Scan(&count); err != nil {
		return false
	}
	return count > 0
}

// renamedCustomModel is one kv row before and after the alias rewrite. Both are
// kept so the caller can rebuild a combo member from the pair, because a combo
// stores "<alias>/<model>" and only the old half of that pair is searchable.
type renamedCustomModel struct {
	oldKey string
	newKey string
	alias  string
	model  string
}

// RenameProviderNode moves a provider node from one id to another and rewrites
// every row that refers to it. An equal old and new id reloads the row and
// changes nothing, so a caller can hand the current id back unchanged.
func (r *Repo) RenameProviderNode(oldID, newID string) (*models.ProviderNode, error) {
	if oldID == newID {
		node, _, err := r.GetProviderNodeByID(oldID)
		if err != nil {
			return nil, err
		}
		if node == nil {
			return nil, ErrProviderNodeNotFound
		}
		return node, nil
	}

	tx, err := r.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("rename provider node %s: begin transaction: %w", oldID, err)
	}
	defer func() { _ = tx.Rollback() }()

	var taken int
	if err := tx.QueryRow(`SELECT COUNT(1) FROM providerNodes WHERE id = ?`, newID).Scan(&taken); err != nil {
		return nil, fmt.Errorf("rename provider node %s: check target id: %w", oldID, err)
	}
	if taken > 0 {
		return nil, ErrProviderNodeIDTaken
	}
	// Connections move first: nothing after this point can fail for a
	// connection-shaped reason, and an untouched connection column would leave
	// the node with no reachable credentials.
	var source int
	if err := tx.QueryRow(`SELECT COUNT(1) FROM providerNodes WHERE id = ?`, oldID).Scan(&source); err != nil {
		return nil, fmt.Errorf("rename provider node %s: count source row: %w", oldID, err)
	}
	if source == 0 {
		return nil, ErrProviderNodeNotFound
	}
	if _, err := tx.Exec(`UPDATE providerConnections SET provider = ? WHERE provider = ?`, newID, oldID); err != nil {
		return nil, fmt.Errorf("rename provider node %s: move connections: %w", oldID, err)
	}

	moved, err := renameCustomModels(tx, oldID, newID)
	if err != nil {
		return nil, fmt.Errorf("rename provider node %s: move custom models: %w", oldID, err)
	}
	if err := renameDisabledModels(tx, oldID, newID); err != nil {
		return nil, fmt.Errorf("rename provider node %s: move disabled models: %w", oldID, err)
	}
	if err := renameModelAliases(tx, oldID, newID); err != nil {
		return nil, fmt.Errorf("rename provider node %s: move model aliases: %w", oldID, err)
	}
	if err := renameComboMembers(tx, oldID, newID, moved); err != nil {
		return nil, fmt.Errorf("rename provider node %s: move combo members: %w", oldID, err)
	}

	// History rows are append-only analytics; moving them keeps the renamed
	// node's past traffic attached to it instead of stranding it under an id
	// that no longer exists. Both tables can be absent from a database that was
	// never fully bootstrapped, and losing analytics is a far better outcome
	// than refusing the edit.
	for _, table := range []string{"usageHistory", "requestDetails"} {
		if !tableExists(tx, table) {
			continue
		}
		if _, err := tx.Exec(`UPDATE `+table+` SET provider = ? WHERE provider = ?`, newID, oldID); err != nil {
			return nil, fmt.Errorf("rename provider node %s: move %s: %w", oldID, table, err)
		}
	}

	if err := renameSettingsProviderKeys(tx, oldID, newID); err != nil {
		return nil, fmt.Errorf("rename provider node %s: move settings: %w", oldID, err)
	}

	if _, err := tx.Exec(`UPDATE providerNodes SET id = ? WHERE id = ?`, newID, oldID); err != nil {
		return nil, fmt.Errorf("rename provider node %s: move node row: %w", oldID, err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("rename provider node %s: commit: %w", oldID, err)
	}
	r.InvalidateCustomModelExtraBody()

	node, _, err := r.GetProviderNodeByID(newID)
	if err != nil {
		return nil, err
	}
	if node == nil {
		return nil, ErrProviderNodeNotFound
	}
	return node, nil
}

// renameCustomModels rewrites the customModels kv rows that name oldID.
//
// The scope's primary key is (scope, key) and the key carries the alias in its
// first segment, so a rewrite is delete-then-insert rather than UPDATE. Every
// affected row is collected before anything is deleted: a row whose key moves
// and whose value stays is indistinguishable from one that was never touched
// once the old key is gone.
func renameCustomModels(tx *sql.Tx, oldID, newID string) ([]renamedCustomModel, error) {
	rows, err := tx.Query(`SELECT key, value FROM kv WHERE scope = 'customModels'`)
	if err != nil {
		return nil, err
	}
	type row struct{ key, value string }
	var inserts, deletes []row
	var moved []renamedCustomModel
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			_ = rows.Close()
			return nil, err
		}
		alias, rest, ok := splitCustomModelKey(key)
		if !ok {
			continue
		}
		rewritten, valueChanged := rewriteCustomModelValue(value, oldID, newID)
		if alias != oldID && !valueChanged {
			continue
		}
		newKey := key
		if alias == oldID {
			newKey = newID + rest
			moved = append(moved, renamedCustomModel{
				oldKey: key,
				newKey: newKey,
				alias:  alias,
				model:  customModelKeyModel(rest),
			})
		}
		deletes = append(deletes, row{key: key})
		inserts = append(inserts, row{key: newKey, value: rewritten})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	rows.Close()

	for _, d := range deletes {
		if _, err := tx.Exec(`DELETE FROM kv WHERE scope = 'customModels' AND key = ?`, d.key); err != nil {
			return nil, err
		}
	}
	for _, ins := range inserts {
		if _, err := tx.Exec(
			`INSERT INTO kv (scope, key, value) VALUES ('customModels', ?, ?)`,
			ins.key, ins.value,
		); err != nil {
			return nil, err
		}
	}
	return moved, nil
}

// splitCustomModelKey splits the "<alias>|<modelId>|<kind>" kv key. Rows the
// upstream dashboard wrote with "/" separators keep the remainder in rest, so
// the reassembled key is byte-identical apart from its alias.
func splitCustomModelKey(key string) (alias, rest string, ok bool) {
	if i := strings.Index(key, "|"); i > 0 {
		return key[:i], key[i:], true
	}
	if i := strings.Index(key, "/"); i > 0 {
		return key[:i], key[i:], true
	}
	return "", "", false
}

// customModelKeyModel returns the model id inside the remainder of a split key,
// i.e. everything between the alias and the trailing kind segment.
func customModelKeyModel(rest string) string {
	segments := strings.Split(rest, "|")
	if len(segments) < 2 {
		// A "/"-separated key can carry a slash inside the model id, so only
		// the first segment is the model when there is no kind segment.
		return strings.TrimPrefix(rest, "/")
	}
	return segments[1]
}

// rewriteCustomModelValue points the providerAlias field at newID. The field is
// authoritative when present — GetCustomModels only falls back to the key — so
// a row whose value disagrees with its key is not left half-moved.
func rewriteCustomModelValue(value, oldID, newID string) (string, bool) {
	var decoded map[string]any
	if err := json.Unmarshal([]byte(value), &decoded); err != nil {
		return value, false
	}
	alias, _ := decoded["providerAlias"].(string)
	if alias != oldID {
		return value, false
	}
	decoded["providerAlias"] = newID
	b, err := json.Marshal(decoded)
	if err != nil {
		return value, false
	}
	return string(b), true
}

// renameDisabledModels re-keys the disabledModels list, whose key is the
// provider alias the dashboard toggled models under.
func renameDisabledModels(tx *sql.Tx, oldID, newID string) error {
	var raw string
	err := tx.QueryRow(`SELECT value FROM kv WHERE scope = 'disabledModels' AND key = ?`, oldID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM kv WHERE scope = 'disabledModels' AND key = ?`, oldID); err != nil {
		return err
	}
	_, err = tx.Exec(
		`INSERT INTO kv (scope, key, value) VALUES ('disabledModels', ?, ?)`,
		newID, raw,
	)
	return err
}

// renameModelAliases rewrites alias targets of the form "<oldID>/<model>".
//
// A key that is itself oldID moves too: a user who aliased a model onto the
// node id would otherwise keep an alias resolving to a provider that no longer
// exists.
func renameModelAliases(tx *sql.Tx, oldID, newID string) error {
	rows, err := tx.Query(`SELECT key, value FROM kv WHERE scope = 'modelAliases'`)
	if err != nil {
		return err
	}
	type row struct{ key, value string }
	var inserts, deletes []row
	for rows.Next() {
		var key, raw string
		if err := rows.Scan(&key, &raw); err != nil {
			_ = rows.Close()
			return err
		}
		rewritten, ok := replaceModelPrefix(parseJSONString(raw), oldID, newID)
		if !ok {
			continue
		}
		value, err := json.Marshal(rewritten)
		if err != nil {
			_ = rows.Close()
			return err
		}
		if key == oldID {
			deletes = append(deletes, row{key: key})
			inserts = append(inserts, row{key: newID, value: string(value)})
			continue
		}
		inserts = append(inserts, row{key: key, value: string(value)})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	rows.Close()

	for _, d := range deletes {
		if _, err := tx.Exec(`DELETE FROM kv WHERE scope = 'modelAliases' AND key = ?`, d.key); err != nil {
			return err
		}
	}
	for _, ins := range inserts {
		if _, err := tx.Exec(
			`INSERT INTO kv (scope, key, value) VALUES ('modelAliases', ?, ?) ON CONFLICT(scope, key) DO UPDATE SET value = excluded.value`,
			ins.key, ins.value,
		); err != nil {
			return err
		}
	}
	return nil
}

// replaceModelPrefix rewrites "<provider>/<model>" when provider matches, and
// reports whether anything changed.
func replaceModelPrefix(model, oldID, newID string) (string, bool) {
	if !strings.HasPrefix(model, oldID+"/") {
		return model, false
	}
	return newID + "/" + strings.TrimPrefix(model, oldID+"/"), true
}

// renameComboMembers rewrites every combo member that spelled out a model of
// the renamed node. A combo stores the address a client asked for, so a member
// left pointing at the old id 404s the moment the node is renamed.
func renameComboMembers(tx *sql.Tx, oldID, newID string, moved []renamedCustomModel) error {
	if len(moved) == 0 {
		return nil
	}
	byModel := make(map[string]string, len(moved))
	for _, m := range moved {
		byModel[oldID+"/"+m.model] = newID + "/" + m.model
	}
	// A node can also have been spelled out through a bare model id when the
	// combo was authored before the node existed; only the fully qualified
	// forms are rewritten, since the bare form is ambiguous across nodes.
	rows, err := tx.Query(`SELECT id, models FROM combos`)
	if err != nil {
		return err
	}
	type update struct{ id, models string }
	var updates []update
	for rows.Next() {
		var id, models string
		if err := rows.Scan(&id, &models); err != nil {
			_ = rows.Close()
			return err
		}
		rewritten := models
		changed := false
		for from, to := range byModel {
			if strings.Contains(rewritten, from) {
				rewritten = strings.ReplaceAll(rewritten, from, to)
				changed = true
			}
		}
		if changed {
			updates = append(updates, update{id: id, models: rewritten})
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	rows.Close()

	for _, u := range updates {
		if _, err := tx.Exec(`UPDATE combos SET models = ? WHERE id = ?`, u.models, u.id); err != nil {
			return err
		}
	}
	return nil
}

// renameSettingsProviderKeys re-keys the provider-keyed maps inside the
// settings blob. These hold a proxy pool binding, a rotation strategy and the
// operator's outbound header overrides for one provider; keying them by a node
// id the rename just retired would silently drop all three.
func renameSettingsProviderKeys(tx *sql.Tx, oldID, newID string) error {
	var rawData string
	err := tx.QueryRow(`SELECT data FROM settings WHERE id = 1`).Scan(&rawData)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var settings map[string]any
	if err := json.Unmarshal([]byte(rawData), &settings); err != nil || settings == nil {
		return nil
	}
	changed := false
	for _, key := range []string{"providerStrategies", "providerOverrides"} {
		entries, ok := settings[key].(map[string]any)
		if !ok {
			continue
		}
		entry, ok := entries[oldID]
		if !ok {
			continue
		}
		delete(entries, oldID)
		if _, taken := entries[newID]; !taken {
			entries[newID] = entry
		}
		changed = true
	}
	if !changed {
		return nil
	}
	b, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	_, err = tx.Exec(
		`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`,
		string(b),
	)
	return err
}
