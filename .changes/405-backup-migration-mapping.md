### Restore a Node backup without silently opening restricted keys

A backup carries a key's model restriction as `access: {restricted, allow}`
on the Node side and as `allowedModels` on the Go side. The importer only read
`allowedModels`, so restoring a Node backup turned every restricted key into an
unrestricted one — including a restricted-empty key, whose whole purpose is to
allow nothing. Import now reads `access` first and falls back to
`allowedModels`, mapping an empty allow list to the `!*` sentinel that already
means deny-all in the database.

The allowlist INSERT also omitted `created_at`, a NOT NULL column, so restoring
any key that had model restrictions failed outright with a constraint error.
Every restore of a restricted key was rejected.

Export now emits both shapes, so a backup taken here can be read back by the
Node dashboard rather than restoring unrestricted there. The export walk runs
inside one read transaction: reading table by table on a bare pool let a write
land between the `apiKeys` query and the access query, exporting a key with no
allowlist — which restores as unrestricted.

Import accepts `?dryRun=1`. It runs the identical transaction and rolls it
back, so the report describes work that actually executed rather than a
separate estimate that could disagree with it, and a payload that would fail
halfway fails here too. The report carries per-table counts, the access state
of each key, and `incompatible_header_limit` warnings for provider overrides
past the G17 ceiling (20 headers, 8192 bytes per value). Over-limit headers are
reported, never truncated.

`docs/parity/rollout-runbook.md` records the rehearsal order and what still
blocks each later step.
