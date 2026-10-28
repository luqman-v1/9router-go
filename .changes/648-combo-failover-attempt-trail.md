### Added
- **Combo & Account Failover**: Record attempt trail in `requestDetails` metadata (`internal/handlers/chat/attempt_recorder.go`, `combo.go`, `fallback.go`, `usage.go`) to surface intermediate connection failures (e.g. 429 rate limit, 502/503 service unavailable) across accounts prior to a successful failover (closes #648, upstream `OmniRoute` PR #17277 parity).
- **Dashboard UI**: Render an **Account Failover Trail** badge chain in `RequestDetailsTab.svelte` when multiple account attempts were made for a request.
