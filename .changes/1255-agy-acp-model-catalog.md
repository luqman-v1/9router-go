### Fixed

- `agy-acp` models now appear in the dashboard's provider detail, model picker, and combo catalog. The frontend `web/src/lib/models.ts` static catalog had no `agy-acp`/`agcpy` entry, so `getModelsByProviderId()` returned an empty list even though the Go registry and `/v1/models` already served the models.

### Changed

- Synced the full Antigravity roster to `agy-acp` (37 ids incl. gemini-4 carbon/argon/hydrogen, gemini-3.8/3.7 flash, gemini-3.1-pro, claude-sonnet/opus 5-5 and 4-6, gpt-oss-120b), across `internal/providers/registry_models.go`, `internal/providers/capabilities.go`, `internal/acp/agy/models.go`, and `web/src/lib/models.ts`. Models an account cannot select still list and fail clearly at `session/new` — no silent fallback.
- Removed the now-redundant short-id alias map in `internal/acp/agy/models.go` (those ids are now catalogue entries themselves).

### Verification

- `go test ./internal/acp/agy/ ./internal/providers/ ./internal/proxy/executor/` pass.
- `bun test src scripts` — 845 pass, 0 fail.
- `bun run ratchet:svelte` — 0 unresolved identifiers, 74 errors (baseline unchanged).
- `bun run build` succeeds; `dist/` 4.8 MB.
