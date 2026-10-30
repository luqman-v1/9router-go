### Stop re-fetching a failed live catalog, and skip it for pinned connections

`/v1/models` builds one model set per connection and, for every combo seat,
`liveCapabilityLeaf` resolves that provider's live catalog. Two behaviors made a
large install unusable:

- `resolveLiveCatalog` fetched a catalog even when the connection pinned
  `providerSpecificData.enabledModels`, although the pinned list is the whole
  catalog for that connection — the fetch could only contradict it;
- a failed discovery stored nothing, so every combo leaf re-ran the same fetch
  and waited out the 10s `liveCatalogFastHTTP` deadline again.

On a real install (3800 connections, 309 combos, 174 custom nodes) a single
`GET /v1/models` ran 311s and then dropped the connection; with the pins already
written it still took 52s cold.

`resolveLiveCatalog` now returns early for a pinned connection and stores a 60s
negative cache entry when discovery fails, so a dead endpoint costs one timeout
per minute instead of one per combo leaf. Measured on the same install:
`/v1/models` 311s (aborted) → 1.7s cold, 2.2s warm (4252 models); `/v1/models/<id>`
answers instead of timing out; chat is unchanged (554ms).

Verified by `TestResolveLiveCatalogPinnedEnabledModelsSkipsFetch` (zero upstream
calls for a pinned connection) and `TestResolveLiveCatalogCachesFailure`
(three resolutions, one upstream call); both fail on `main`.
