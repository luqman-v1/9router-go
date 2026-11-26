package db

// PerModelRateLimit returns the stored per-model cap block, or nil when it is
// not configured. It reads the settings cache the way GetSettings does, but
// without the clone: the chat dispatch path consults this on every candidate
// connection, and a clone per read would put an allocation on a path that only
// needs to look. Callers must not write into the returned value.
//
// The cache is already invalidated by construction: it is keyed on the raw row
// content, so a settings write changes the raw string and the next read
// reparses. A dedicated invalidation hook would be a second thing to remember.
func (r *Repo) PerModelRateLimit() *PerModelRateLimitSettings {
	if c := r.settingsCache.Load(); c != nil {
		return c.value.PerModelRateLimit
	}
	// Cache cold: one parse populates it, and the chat path is not the first
	// reader in practice — the boot sequence reads settings before serving.
	s, err := r.GetSettings()
	if err != nil || s == nil {
		return nil
	}
	return s.PerModelRateLimit
}
