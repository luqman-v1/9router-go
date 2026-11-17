### Fixed

- Antigravity quota strike-breaker: align with merged upstream behavior (`src/sse/services/antigravityQuota.js`), counting 409 and 429 errors when quota is optimistic or unavailable regardless of error message markers, avoiding infinite retry loops on canonical Google 429 quota exhaustion.
- Antigravity family quota resolution: resolve model-specific and shared family buckets (Argon weekly/session, Ultra fallback) so exhausted parent pools take precedence over optimistic per-model readings.
- Antigravity version: bump IDE client version to 2.26.0 matching the latest native Antigravity release.
