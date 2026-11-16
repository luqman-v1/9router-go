### Cache

Added `POST /api/cache/semantic/invalidate`, which clears semantic cache entries matching the filters in the JSON request body:

- `model` — case-insensitive exact match on the entry's model.
- `prefix` — case-sensitive prefix match on the entry's cache key (the SHA-256 digest).

Both filters are ANDed when present; at least one is required (a request with neither returns `400`). Clearing the whole store remains `DELETE /api/cache`. The count of removed entries is returned in the response body.

The implementation adds `Cache.InvalidateByModelAndPrefix` (and `Store.InvalidateByModelAndPrefix` on both `LRUStore` and `PersistentLRUStore`), so the SQLite write-through store invalidates in memory and on disk in one statement — no per-row N+1.
