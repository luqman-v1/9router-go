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
