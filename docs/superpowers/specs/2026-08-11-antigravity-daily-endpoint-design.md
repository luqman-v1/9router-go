# Antigravity Endpoint Migration to Daily Environment

## 1. Purpose
The `9router-go` proxy is currently hitting severe rate limits (HTTP 429) when routing requests to the `antigravity` (Google Cloud Code) provider, particularly when used by autonomous agents like Claude Code. This happens even with 4 accounts configured in round-robin mode. The legacy Next.js codebase successfully used a staging/daily endpoint which proved more resilient to agent-level request volumes.

## 2. Analysis & Constraints
- **Round-Robin is Working:** The logs confirm that the proxy correctly iterates through all 4 configured connections (`8d911ff1`, `6b4a14b4`, `e3479d23`, `f18d7ab2`). All 4 hit a 429 limit almost immediately.
- **Agent Traffic vs Human Traffic:** A human using the CLI makes 1 request every 10+ seconds. An autonomous agent (like Claude Code) can fire 5-10 concurrent or rapid requests in 2 seconds (reading files, executing tools). The production endpoint (`cloudcode-pa`) treats this as abuse and applies strict rate limiting (or token bucket exhaustion).
- **Environment Parity:** Next.js bypassed this by using `daily-cloudcode-pa.googleapis.com`. Golang was erroneously set to `cloudcode-pa.googleapis.com`.

## 3. Recommended Approach (Selected)
We will switch all hardcoded production Google API endpoints for the `antigravity` provider in the Golang codebase to the `daily` environment.

### Target Changes
1. `internal/providers/providers.go`:
   - Change `https://cloudcode-pa.googleapis.com` -> `https://daily-cloudcode-pa.googleapis.com`
2. `internal/mitm/dns.go` & `internal/mitm/server.go` & `internal/mitm/mitm_test.go`:
   - Ensure the proxy and tests still handle `daily-cloudcode-pa.googleapis.com` securely.
3. `internal/handlers/chat/antigravity_project.go`:
   - Change `loadCodeAssistURL` and `onboardUserURL` to `daily-cloudcode-pa.googleapis.com`.
4. `internal/proxy/gemini.go`:
   - Update any fallback logic/comments to reflect the `daily-` prefix if necessary.

## 4. Risks and Mitigation
- **Uptime Risk:** The `daily` environment is technically a staging environment and might suffer from occasional internal Google downtime.
- **Mitigation:** The user can configure multiple providers (fallback chains) in `9router` so that if `antigravity` ever goes down completely, it fails over to another provider (e.g., Cursor, Codebuddy).
