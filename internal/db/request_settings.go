package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// requestSettingsKey types the context key that carries this request's settings
// holder.
type requestSettingsKey struct{}

// requestSettingsHolder carries what one request read from the settings row:
// the parsed value it resolved. It is stored as a pointer so the first reader
// fills it for everyone downstream in the same request.
type requestSettingsHolder struct {
	settings *SettingsData
	// failed records a read that could not answer, so a broken row is not
	// retried once per helper inside one request.
	failed bool
}

// WithRequestSettings returns ctx carrying an empty settings holder: the one
// read every request-path settings lookup goes through.
//
// The request entry points install it once. A context that never went through
// one still answers correctly — the lookup just pays a row read per call,
// exactly as it did before this holder existed.
func WithRequestSettings(ctx context.Context) context.Context {
	return context.WithValue(ctx, requestSettingsKey{}, &requestSettingsHolder{})
}

// SettingsForRequest returns the typed settings row for this request, reading
// the row at most once.
//
// GetSettings is deliberately cheap already — it parses the row once and
// reuses the parse — but the row read itself is not free, and the request path
// used to pay it once per helper. A measured profile of one streamed request
// at concurrency 50 showed six of them per request (9.7% of total CPU) for a
// row whose content cannot change inside one request.
//
// The answer is cloned per caller, for the same reason GetSettings clones: a
// caller that replaces a field must not change what the next reader sees. The
// row is still read fresh on the next request, so a settings change the
// dashboard writes between two requests is never served stale.
func (r *Repo) SettingsForRequest(ctx context.Context) *SettingsData {
	holder, ok := ctx.Value(requestSettingsKey{}).(*requestSettingsHolder)
	if !ok || holder == nil {
		// No holder installed: behave like GetSettings for this one call.
		if settings, err := r.readSettingsRow(); err == nil {
			return settings
		}
		return nil
	}
	if holder.settings == nil && !holder.failed {
		if settings, err := r.readSettingsRow(); err == nil {
			holder.settings = settings
		} else {
			holder.failed = true
		}
	}
	if holder.failed {
		// A row that cannot be read is not a row of defaults: a transient
		// database failure must not silently switch the operator's settings
		// off for the rest of this request. Callers already treat nil as
		// "unset", and one failed read costs one query, not six.
		return nil
	}
	return cloneSettings(holder.settings)
}

// readSettingsRow is GetSettings' body: read row 1, parse it, and hand back a
// value that shares nothing with the cached parse.
func (r *Repo) readSettingsRow() (*SettingsData, error) {
	var rawData string
	err := r.db.QueryRowContext(context.Background(), `SELECT data FROM settings WHERE id = 1`).Scan(&rawData)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// A row that does not exist yet is a fresh install, not a failure.
			return DefaultSettings(), nil
		}
		return nil, fmt.Errorf("get settings: %w", err)
	}
	if c := r.settingsCache.Load(); c != nil && c.raw == rawData {
		return cloneSettings(c.value), nil
	}
	settings, ok := parseSettings(rawData)
	if !ok {
		return nil, errors.New("parse settings: the stored settings row is not readable")
	}
	r.settingsCache.Store(&cachedSettings{raw: rawData, value: settings})
	return cloneSettings(settings), nil
}
