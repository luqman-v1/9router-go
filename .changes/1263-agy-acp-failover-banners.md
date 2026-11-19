### Fixed

- Multi-account failover now works when an agy account's credentials die: agy 1.5.x reports fatal faults ("⚠ failed to construct executor…", "⚠ needs sign-in…") as ordinary `agent_message_chunk` text with `stopReason: end_turn`, which the executor forwarded as a billed HTTP 200 — the fallback loop never advanced. `RunTurn` now buffers turn-leading text that could be a lone "⚠" banner and converts it into a classified error (auth/quota/policy/generic 502, all retryable), so the account loop moves to the next connection. Real model output streams unbuffered as soon as it diverges from the banner shape. Verified live: sabotaged account 1 + healthy account 2 → response served by account 2 (`usageHistory.connectionId`).

### Verification

- `go test ./internal/acp/agy/ ./internal/proxy/executor/` pass, including new banner regression tests (fatal banner → classified error with no text leaked to the client; normal output still streams; sign-in banner → ErrAuth; quota banner → ErrQuota).
- Live proof against agy 1.5.11: account 1 with a sabotaged access token + healthy account 2 → the turn is served by account 2 (`usageHistory.connectionId`), response `PONG`.
- `golangci-lint run --new-from-rev=origin/main ./internal/acp/agy/...` — 0 issues.
