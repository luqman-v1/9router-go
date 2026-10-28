### Added
- **Rate Limiting & Failover**: Add bounded random jitter (100ms–400ms) to internal combo `Retry-After` backoff wait (`internal/handlers/chat/retry_after.go`) to prevent thundering-herd retries on upstream rate limits while keeping client-facing headers exact (closes #669, upstream `OmniRoute` PR #17306 parity).
