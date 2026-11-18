### Fixed

- `agy-acp` models now appear in the dashboard's provider detail, model picker, and combo catalog. The frontend `web/src/lib/models.ts` static catalog had no `agy-acp`/`agcpy` entry, so `getModelsByProviderId()` returned an empty list even though the Go registry and `/v1/models` already served the models.
- Added the explicit agy 1.5.9 roster (`gemini-3.8-flash-*`, `claude-sonnet-4-6`, `claude-opus-4-6-thinking`, `gpt-oss-120b-medium`) under both the canonical id and the `agcpy` alias, mirroring `internal/providers/registry_models.go`.

### Verification

- `bun test src scripts` — 845 pass, 0 fail.
- `bun run ratchet:svelte` — 0 unresolved identifiers, 74 errors (baseline unchanged).
- `bun run build` succeeds; `dist/` 4.8 MB.
