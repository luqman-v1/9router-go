### Fixed

- The provider detail page's **Copy** button handed out the provider's internal id instead of the prefix its model ids are published under: `opencode/nemotron-3-ultra-free` where the gateway serves `oc/nemotron-3-ultra-free`. Pasting that id back into a client failed, because the router resolves a model id by its published prefix.

### Why

- PR #1259 replaced the copy target with `displayAlias()`, which returned the raw provider id for every registered provider — it only ever returned a node's prefix for a compatible node, which is the case the fix was written for.
- The published prefix is the registry alias (`oc` for opencode, `cmc` for commandcode, `ds` for deepseek) or the node's own prefix. Upstream resolves it the same way (`providers/[id]/page.js:217`).

### How it was verified

- New helper `providerDisplayPrefix` in `web/src/components/connections/types.ts`, driven by the provider catalog's new `modelPrefix` field.
- `ProviderMeta.ModelPrefix` is served by `GET /api/providers/catalog` and filled from `GetProviderAlias(id)` — the same function `/v1/models` publishes model ids with, so the value the dashboard copies and the value the gateway serves cannot drift apart.
- New tests: `web/src/components/connections/providerDisplayPrefix.test.ts` (fails on base, passes with the fix) and `internal/providers/catalog_alias_resolvable_test.go` (every catalog prefix equals the published alias; every published prefix resolves back to its own provider).
- Live check on a scratch gateway: base rendered `opencode/nemotron-3-ultra-free`, head renders `oc/nemotron-3-ultra-free`. Screenshots in `docs/screenshots/provider-model-copy-{before,after}.png`.
- `bun test src scripts` → 888 pass / 0 fail; `bun run build` green; `bun run ratchet:svelte` → 0 unresolved identifiers, 74 errors (baseline 74); `go test ./internal/providers/` green.

### Removed

- `gw-head`, a locally built 66 MB gateway binary that entered `main` through PR #1298, is untracked again and the `.gitignore` entry keeps it out.
