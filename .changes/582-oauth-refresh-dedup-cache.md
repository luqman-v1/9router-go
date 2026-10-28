### Added
- Upstream parity (`decolua/9router` `open-sse/services/tokenRefresh/dedup.js`): implemented global OAuth token refresh deduplication and 10-second result caching in `internal/proxy/oauth/dedup.go` for all providers (#501).
- Keyed on `provider + ":" + oldToken`, collapsing concurrent in-flight requests via `singleflight` and serving completed results from memory within `10s` TTL.
- Bounded memory cache (512 entries max, sweeping expired entries on write) per §4.G.
- Failures are never cached; empty refresh tokens bypass dedup.
- Integrated `DedupRefresh` into `oauth.Refresh` and routed chat OAuth refresh paths in `internal/handlers/chat/gemini_handler.go` through it.
- Added comprehensive unit tests in `internal/proxy/oauth/dedup_test.go`.
