### Added
- Granular error classification for OpenCode `FreeTierError` refusals in `internal/handlers/chat/fallback.go` (#597).
- Disambiguated `FreeTierError` variants into actionable error codes and types:
  - Region restrictions (`"not available in your country"`, `"country_blocked"`) -> 403 `free_tier_region_blocked`.
  - Rate limits / quota exhaustion (`"FreeUsageLimitError"`, `"rate limit exceeded"`) -> 429 `free_tier_quota_exhausted` with `Retry-After: 60`.
  - Client version deprecation (`"Upgrade Required"`) -> 426 `free_tier_upgrade_required`.
  - Abuse detection (`"Detect Abuse Traffic"`) -> 503 `free_tier_abuse_detected` with `Retry-After: 30`.
  - Upstream temporary gate closures -> 503 `free_tier_unavailable` with `Retry-After: 30`.
- Added unit test suite in `internal/handlers/chat/opencode_closure_test.go`.
