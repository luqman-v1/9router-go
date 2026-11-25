### Added
- Preview Fetch Models panel for `Import from /models`: the button now fetches the
  provider's catalogue and stages it in a filterable chip panel (search, per-model
  test, add, remove) instead of writing every row straight into the model list.
  Changes in the panel and the main list stay in sync in both directions: the
  panel derives "already added" from the parent's compatible/builtin id sets, so
  removing a chip removes the main-list row and vice versa.

### Changed
- `GET /api/providers/{id}/models` is served through a process-level catalogue
  cache (60s TTL, 200 entries, one body per connection) with `singleflight.Do`
  collapsing concurrent misses, so reopening the panel and opening the same
  connection in two tabs cost one upstream call instead of one per click.
  Responses carry `X-Models-Cache: hit|miss`. Editing or deleting a connection
  drops its entry, because the cached body belongs to the previous baseUrl and
  credential.

### Verified
- Unit: `go test ./internal/handlers/dashboard/` (cache TTL, error non-caching,
  concurrency collapse, eviction, invalidate-on-edit/delete, writer defaults).
- Unit: `cd web && bun test src` (1036 pass).
- Measured against an isolated instance and a fake upstream that sleeps 800ms:
  cold call 820ms, warm call 2-3ms (X-Models-Cache: hit), 6 concurrent cold
  requests = 854ms total with exactly **1** upstream hit.

### Fixed
- Import preview header: the title was a flex item that could shrink, so with
  the search box and sort control beside it "Preview Fetch Models (111 of 460)"
  wrapped onto two lines and the count read as if it had changed. The title is
  now `shrink-0 whitespace-nowrap` and the search box takes the remaining width
  (`flex-1 max-w-64`), so the count is always one line and the placeholder is
  never clipped.

### Browser-verified (playwright-cli, isolated instance + fake 460-model upstream)
- Import from /models opens a "Preview Fetch Models (460)" dialog.
- Search "model-42" -> "11 of 460"; "zzz-nomatch" -> empty state "No models found".
- Add puts the chip into the Added state (badge + red delete) and the model
  appears in the main list; remove drops it from both (verified outside the
  dialog too, so the chip is not counted by mistake).
- Probe button reports "0 passed, 1 failed of 1 tested" against the fake
  upstream, which serves /models only.
- Escape closes the dialog.
- `GET /api/providers/<conn>/models` in the browser: first call 814ms
  `X-Models-Cache: miss`, then 3ms `hit`, 3ms `hit`.
