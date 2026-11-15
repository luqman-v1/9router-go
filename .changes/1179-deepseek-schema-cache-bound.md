### fix(deepseek-web): bound the compiled-schema cache

- `deepSeekSchemaCache` was a package-level map keyed by the client-supplied schema bytes and never evicted, so a client varying its tool schemas grew it without bound (#1167 item 1).
- The cache now caps at 200 entries — the same cap as the repo's session caches (`pplxSessionMaxEntries`) — and evicts the oldest entry on insert, mirroring `pplxSessionStore` and `deepSeekSessionStore.evictOldestLocked`. Entries carry a store timestamp; a nil validator (a cached compile failure) counts against the cap like any other entry. Lookup behaviour is unchanged.
- New test `TestDeepSeekSchemaCacheBounded` pins the bound: cap+25 distinct schemas leave exactly the cap, the newest schema still validates, and a failed compile stays bounded. Verified with Go 1.27.4: `go build`, `go vet`, and the executor package's DeepSeek tests pass.
