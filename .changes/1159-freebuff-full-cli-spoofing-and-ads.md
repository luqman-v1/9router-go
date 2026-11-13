### feat(freebuff): full Freebuff CLI spoofing and dashboard ad banner integration

- Upgrade Freebuff CLI spoofing to modern `Freebuff-CLI/0.2.36` parity:
  - Update User-Agent across session, OAuth, probe, and run calls from legacy `codebuff-cli/0.0.138` to `Freebuff-CLI/0.2.36`.
  - Expand model-to-root-agent mapping to `base3` harness models (`base3-free-deepseek`, `base3-free-deepseek-flash`, `base3-fast`, `base3-free-luna-6`, `base3-free-solar-mini4`, `base3-free-muse-spark-1-3`, `base3-free-gemini-3-8-flash`, etc.).
  - Add all 6 canonical system prompt openings from upstream Freebuff catalog models.
  - Send `surface: "cli"` and `freebuff_multi_session: "1"` in `codebuff_metadata`, and generate CLI-shaped `codebuff-cli-<8char>` fallback fingerprints.
  - Send `x-freebuff-wallet-spend-limit: session`, `x-freebuff-multi-session: 1`, and `x-freebuff-purchase-continuity: 1` on session requests.
- Add Freebuff ad surface matching the `free-pi` model:
  - Serve Freebuff ads via `GET /api/freebuff/ad` and report paint impressions via `POST /api/freebuff/ad/impression` (`internal/handlers/dashboard/freebuff_ads*.go`).
  - Add `FreebuffAdBanner.svelte` in dashboard connection view, reporting impressions only once the banner is painted on screen.
- Integrate Freebuff quota into Quota Tracker (`/api/usage/connections` and `/api/usage/{connectionId}`):
  - Register `freebuff` in `usageSupportedProviders` and `usageApikeyProviders`.
  - Fetch account quotas from `/api/v1/freebuff/session`, mapping daily Freebucks (spent, total, remaining, refill time), wallet balance, and active session countdown to the Quota Tracker table.
- Verified with unit tests (`freebuff_ads_test.go`, `usage_freebuff_test.go`, `freebuff_test.go`, `freebuff_session_test.go`), `bun run build`, and `make vet-svelte`.
