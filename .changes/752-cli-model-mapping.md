### feat(cli-tools): add CLI model mapping (slot → model per key+tool)

Port of Cartethyia's CLI model mapping feature (risunCodex/Cartethyia,
`documentation/features/01-cli-model-mapping.md`).

A CLI coding agent asks for a *slot* (`opus`, `sonnet`, `haiku`), not a model
id. An operator can remap those slot names to any routed model per API key,
scoped to one (tool, apiKey, slot) triple, consumed only when both gates pass:

1. **Scope gate** — the key carries the flat `routing:cli_mapping` scope
   (never implied by other scopes).
2. **User-Agent gate** — the request's UA matches a known CLI tool
   (`claude-cli/`, `claude-code/` → `claude`).

CLI aliases merge after ordinary aliases, so a mapped slot wins for the names
it covers. `enforceModelAccess` judges the *mapped* target, so a CLI remap
cannot bypass the key's model policy.

Backend:
- `internal/db/cli_tool_mappings.go` — `cliToolMappings` +
  `cliToolSettings` tables (unique `(toolId, apiKeyId, slotKey)`), CRUD,
  `ResolveCliModel` (settings-gated), `DetectCliToolID` UA matcher with
  boundary checks, `HasCliMappingScope` flat-scope check.
- `internal/db/schema.go` — table DDL + `apiKeys.scopes` column
  (JSON array of flat grants) + `EnsureCliToolTables` in `EnsureCoreSchema`.
- `internal/db/apikeys.go` — scopes column read/scan (JSON array).
- `internal/db/dashboard.go` — `SetApiKeyScopes` writer.
- `internal/handlers/chat/cli_model_mapping.go` — `applyCliModelMapping`
  (both gates in order, runs before `enforceModelAccess` and
  `resolveModel`), `cliMappingSourceKeys` slot expansion,
  `normalizeCliSlot` variant stripping.
- Wired into `HandleChatCompletions`, `HandleMessages`, `HandleResponses`.
- `internal/handlers/dashboard/cli_tool_mapping_handler.go` — CRUD for
  mappings + settings + the `/keys/{id}/cli-scope` grant/revocation PUT.
- Routes: `/api/cli-tools*`, `/api/keys/{id}/cli-scope`.

Frontend:
- `web/src/components/cli-tools/CliModelMappingCard.svelte` — per-key
  mapping editor inside the Claude Code tool card (scope toggle, master
  switch, slot/target rows, add/delete).
- `web/src/components/CliToolsView.svelte` — mounts the card.
- `web/src/api/client.ts` — `CliToolInfo`, `CliToolMapping`,
  `CliToolSettings` types + API methods + `scopes` on `APIKey`.

Tests (all pass):
- `internal/db/cli_tool_mappings_test.go` — round-trip, slot isolation,
  settings gating, delete.
- `internal/handlers/chat/cli_model_mapping_test.go` — gate ordering,
  UA detection boundaries, flat-scope rule, slot normalization.
- `internal/handlers/chat/cli_model_mapping_e2e_test.go` — full happy path
  through the handler with a seeded SQLite database.
