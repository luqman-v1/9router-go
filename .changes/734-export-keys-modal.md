### Features
- **Export Keys Modal**: Added per-provider Export Keys action and modal in dashboard (upstream `decolua/9router#4889` parity, resolving #733). Exposes `GET /api/providers/{id}/export-keys` behind dashboard authorization to list unmasked credentials for account backup and migration, with per-row copy and Copy All actions.
