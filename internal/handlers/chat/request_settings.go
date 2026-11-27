package chat

import (
	"context"

	"9router/proxy/internal/db"
)

// withRequestSettingsHolder installs the request's settings holder: every
// later helper that needs the settings row takes it from this holder instead
// of querying SQLite again. Both request entry points (Chat Completions,
// Messages) install it once, before the first helper runs.
func withRequestSettingsHolder(ctx context.Context) context.Context {
	return db.WithRequestSettings(ctx)
}

// settingsForRequest returns the settings row this request resolved, reading
// the row at most once. A nil repo — the failure-closed shape several tests
// and probe paths use — resolves to nil without a query.
func (h *ChatHandler) settingsForRequest(ctx context.Context) *db.SettingsData {
	if h == nil || h.Repo == nil {
		return nil
	}
	return h.Repo.SettingsForRequest(ctx)
}
