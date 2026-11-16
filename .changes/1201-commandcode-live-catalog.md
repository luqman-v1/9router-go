### Command Code publishes its live catalogue in `/v1/models`

`commandcode` now resolves its model list from the CLI feed,
`GET https://api.commandcode.ai/provider/v1/models?view=client`, instead of the
23-row static snapshot. The surface cursor in the dashboard picker is what
operators noticed: a model Command Code ships after the snapshot was reachable
by asking for it by name but never appeared in `/v1/models`, so combos and the
model picker could not see it.

Two properties of that endpoint decided the implementation:

- **The query string is load-bearing.** Without `view=client` the same URL
  answers the public list, which carries no `hidden` flag. `parseCommandcodeLiveModels`
  drops every entry whose `hidden` is true — 16 of the 114 rows probed on
  2026-11-16, including `reflection/beam:free`, `poolside/laguna-s-2.1-free` and
  the `stealth/*` canaries. Publishing them would offer models the provider has
  withdrawn.
- **The endpoint validates CLI identity, not the key.** No `User-Agent: cli`
  answers 403 with or without a bearer; `Bearer user_fake` answers 200. The
  header set therefore comes from the registry's `commandcode` entry — the same
  `StaticHeaders` the chat lane dials with — so a CLI version bump cannot reach
  one path and miss the other. A connection whose key was revoked still lists
  the catalogue instead of reporting a failed fetch.

`context_length` from the feed is marked `LimitsAuthoritative`, because it is the
provider's enforced window and spans 200k–1.05M while the static table answers
1M for the whole catalogue. `maxOutput` is deliberately left to the static cap:
`/alpha/generate` rejects `params.max_tokens` above 200k regardless of what the
feed advertises per model.

The resolver is keyed on `providers.ResolveAlias(providerID)`, so `cmc` and
`commandcode` both reach it while `commandcode-goat`/`cmc-goat` keep their own
generated catalogue — no cross-provider aliasing.

Files: `internal/handlers/chat/live_catalog_commandcode.go` (new),
`internal/handlers/chat/live_catalog.go` (dispatch branch),
`internal/handlers/chat/live_catalog_commandcode_test.go` (new).

**Verification**: `go test ./internal/handlers/chat/ -run 'LiveCatalog|Commandcode|HandleModels|Models'`
passes, including a handler-level case that seeds a `commandcode` connection
against a fake feed and asserts the live ids publish, the `hidden` id does not,
and the static `moonshotai/Kimi-K2.5` row disappears. Feed shape was probed live:
`view=client` + `User-Agent: cli` → 200/114 rows/16 hidden; same URL without the
CLI UA → 403.
