### fix(claude): prevent invalid prompt-cache TTL ordering

- Fixed Claude Code requests failing with Anthropic HTTP 400 when a client-supplied 5-minute cache marker on an earlier system block appeared before the final 1-hour system cache anchor.
- `AnchorClaudeCache` now removes stale `cache_control` markers from earlier system blocks while keeping the final 1-hour anchor, matching upstream `decolua/9router` behavior.
- Added regression tests for the invalid 5m → 1h sequence and preservation of the final system cache anchor (Closes #838).
