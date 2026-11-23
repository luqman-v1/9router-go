### Added

- PostgreSQL is documented as a first-class engine: `wiki/database.md` gains the Engines table and a SQLite-to-PostgreSQL migration guide built on `db export` / `db import`, `DATABASE.md` gains the `DATABASE_URL` engine-selection contract and the pool tuning, and the README's Environment table lists `DATABASE_URL`. Persona gains its two new surfaces (per-key scope, GitHub auto-sync with the injection tester), `wiki/installation.md` lists every release asset including the Android gateway, and the Windows autostart section names both backends. The Quota Tracker page documents the Cloudflare Workers AI tracker and the Qoder/MiniMax daily check-ins; the combos, custom-provider, and white-label pages document the features that landed after the wiki was written.

### Changed

- The README's "40+ AI providers & 100+ models" is now "150+ AI providers & 1000+ models", matching the 153 visible providers in the registry, and the CHANGELOG reference names the current release line instead of v1.9.15. `wiki/index.md` carries the same 150+ figure in its Providers row — the one cell the previous wiki update missed.
