package app

import (
	"9router/proxy/internal/db"
	"9router/proxy/internal/log"
	"9router/proxy/internal/providers"
)

// installModelCapsOverrideLoader hands the per-model capability overrides to
// internal/providers, which cannot import the DB (it is also bundled into
// places that have no database). This is the same install pattern the synced
// catalog uses, and it is the only place the override source is set.
//
// A nil repo or a read error is fail-open on purpose: the capability path then
// answers with detection alone, which is the pre-feature behaviour, never an
// error a request has to handle.
func installModelCapsOverrideLoader(repo *db.Repo) {
	if repo == nil {
		return
	}
	providers.InstallModelCapsOverrideLoader(func() (map[string]string, error) {
		rows, err := repo.AllModelCapOverrides()
		if err != nil {
			// Not fatal: the loader keeps the previous snapshot and retries on
			// the next TTL expiry.
			log.Warn("caps-override", "load failed, serving detection only", "error", err)
			return nil, err
		}
		return rows, nil
	})
}
