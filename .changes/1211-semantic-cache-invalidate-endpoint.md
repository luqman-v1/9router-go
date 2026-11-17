### Cache

Added `POST /api/cache/semantic/invalidate`, a net-new dashboard API for dropping semantic cache entries without waiting for TTL expiry or purging the whole store. It takes a JSON body and returns `{ "invalidated": <count>, "success": true }`:

- `model` — the entry's model. The in-memory tier compares it case-insensitively; the SQLite tier compares it exactly.
- `prefix` — the entry's cache key must start with this string (keys are hex digests).

Both filters are ANDed when both are present, and at least one is required (a request with neither returns `400`). Clearing the whole store stays `DELETE /api/cache`, and a single entry stays `DELETE /api/cache/entries`.

Internally this adds `Cache.InvalidateByModelAndPrefix`, with `Store.InvalidateByModelAndPrefix` implemented on `LRUStore` (in-memory) and `PersistentLRUStore` (in-memory plus one SQLite statement, so there is no per-row N+1). The two tiers do not match the model identically: the in-memory filter is case-insensitive, the SQLite `DELETE` compares the model exactly. A request whose model differs only in case clears the in-memory entry and leaves its disk row.
