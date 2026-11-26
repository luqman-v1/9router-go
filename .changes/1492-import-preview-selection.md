### Fixed
- Selecting a chip in the import preview now narrows every bulk action to the
  selected ids. The selection was previously decorative: clicking a chip's name
  tinted it and updated the `n/m` counter, but `Add all` kept writing the whole
  filter and `Test all` kept probing it, so a hand-picked subset could only be
  imported one chip at a time. The buttons now relabel to `Add selected (2)` /
  `Test selected (2)` / `Remove selected (2)` when a selection exists and act on
  exactly those ids.
- Bulk add forwarded no capability metadata: it stored `{id, type}` per model
  while a single chip add carried `contextWindow`, `maxOutput`, `vision` and
  `reasoning` from the fetched catalogue entry. The same model therefore landed
  with its limits when added alone and unsized when added in bulk. Both paths
  now read the staged entry.
- `GET /api/providers/{id}/models` could serve a body a sync had already
  invalidated: the sync cleared the cache entry, the in-flight fetch that
  started before it completed and wrote the pre-sync body back, and the next
  request was answered from the cache the sync existed to bypass. The cache now
  stamps a per-connection generation before `produce`, refuses the store when an
  invalidation landed meanwhile, and forgets the `singleflight` key so a request
  arriving after the sync starts its own fetch instead of joining the older one.

### Verified
- `go test ./internal/handlers/dashboard/`: the new
  `TestModelsCatalogInvalidateDuringFlightDoesNotRestoreStaleBody` fails on
  `main` (stale body restored) and passes here.
- `cd web && bun test src scripts` 1062 pass; `bun run build` OK;
  `bun run ratchet:svelte` 0 unresolved identifiers, 0 errors;
  `bunx oxlint --deny-warnings` clean.
- `web/e2e/modelImportPreview.test.ts` gains a case that selects one chip and
  asserts the row reads `Add selected (1)` / `Test selected (1)` and that
  clicking it stores exactly that one id; it fails on `main`, passes here.
  `web/e2e/modelImportNameConflict.test.ts` still passes.
