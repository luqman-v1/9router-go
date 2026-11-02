### fix(claude): prevent invalid cache_control TTL order in system prompt anchors

- In `internal/translator/request.go`, enforce non-decreasing TTL order across `tools`, `system`, and `messages` cache control blocks (fixing #838).
- When a `1h` cache_control anchor appears, strips earlier `5m` (or unspecified TTL) cache_control blocks to satisfy Anthropic's rule that `ttl='1h'` blocks must never come after `ttl='5m'` blocks.
- Added unit tests in `internal/translator/claude_cache_ttl_test.go`.
