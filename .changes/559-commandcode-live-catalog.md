### fix(dashboard): fetch Command Code's live catalogue instead of a 22-model snapshot

Command Code's registry catalog is a fixed 22-model list, and nothing in the
gateway could reach the Provider API that actually publishes its catalogue. The
dashboard therefore had no path to any model released after that snapshot:
`GET /api/providers/{id}/models` fell through to the
`openai-compatible-*` branch and answered `400 provider commandcode does not
support models listing`, the provider carried no `modelsFetcher` so the
"Suggested free models" list was always empty, and `ModelsListURLs` omitted it
so "Check Latest Models" never rendered.

Command Code publishes its whole catalogue on the standard OpenAI `/v1/models`
shape at `https://api.commandcode.ai/provider/v1/models`, keyless, with 92
models today — including the free tiers the operator was looking for
(`reflection/beam:free` at 1,048,576 context, `stealth/glyph-cluster:free`,
`poolside/laguna-s-2.1-free`) and the paid ones added since the snapshot
(`minimax/minimax-m3.1-flash`, `z-ai/glm-5.3-flash`, `moonshotai/Kimi-K3`).

**What changed**

- `internal/providers/providers.go`: registered the Provider API catalogue in
  `modelsListURL`, which is the single switch `writeConnectionModels` and
  `fetchLiveModelIDs` both read — so "Check Latest Models", "Import from
  /models" and "Sync Models" all start working at once, and the deprecation
  revive path can now clear a badge for a model the provider still serves.
- `internal/handlers/suggestedmodels.go`: added the `commandcode-free` filter.
  The catalogue carries no pricing block, so a zero-credit model is signalled
  only by a trailing `:free`/`-free` on the id — the same convention
  `filterOpencodeFree` reads, without its dead-id table.
- `web/src/lib/providers.ts`: wired the `modelsFetcher` for `commandcode`.
- `web/src/components/connections/ProviderDetailView.svelte`: added
  `commandcode` to `ModelsListURLs` and widened the "Import from /models"
  gate, which was hardcoded to cline/clinepass/qoder.

**Verification**

- `go test ./internal/providers/ -run TestCommandCodeLiveCatalog` and
  `go test ./internal/handlers/ -run TestSuggestedModels` pass; the new filter
  test uses a local `httptest` feed in the real `/provider/v1/models` shape.
- Driven against the live endpoint: the filter returns exactly
  `[poolside/laguna-s-2.1-free, reflection/beam:free, stealth/glyph-cluster:free]`,
  and a seeded commandcode connection returns all 92 catalogue models through
  `GET /api/providers/{id}/models`.
- `cd web && bun run build` and `bun run ratchet:svelte -- --tsconfig
  tsconfig.app.json` (0 unresolved identifiers, 78 errors against the pinned
  baseline of 78); `bun test src/lib/models.test.ts src/lib/providers.test.ts`
  21 pass.
