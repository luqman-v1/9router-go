### Dashboard: track ClinePass plan usage in Provider Limits

Add quota tracking for `clinepass` (`cline-pass`, `cp`) connections in the dashboard Provider Limits view, mapping the upstream plan limits response into percentage quota cards.

- Query `GET https://api.cline.bot/api/v1/users/me/plan/usage-limits`.
- Authenticate via WorkOS Bearer token with Cline identity headers for OAuth connections, or plain Bearer token for API keys.
- Map `five_hour` / `session`, `weekly`, and `monthly` windows into `Session (5h)`, `Weekly (7d)`, and `Monthly` quota rows with reset timestamps.
- Add `clinepass` to `usageSupportedProviders` and `usageApikeyProviders`.
- Unit tests in `internal/handlers/dashboard/usage_clinepass_test.go` covering window mapping, auth headers, error paths, and eligibility.
