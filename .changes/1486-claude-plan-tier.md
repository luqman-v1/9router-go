Claude: show the seat tier in the quota tracker.

`api.anthropic.com/api/oauth/usage` reports the 5h and weekly windows and
nothing about the seat, so every Claude card read the same regardless of the
plan. The tier now comes from `api.oauth.profile` (`organization_type`, with
`rate_limit_tier` narrowing a Max seat to its multiplier) and rides on the
usage response as `subscription: {plan}`.

That profile read is a second upstream call per Claude usage request — the
one `usage_claude_reset.go` makes is reached from reset-credit target
resolution, not from the usage path, so it is not reused. It runs
concurrently with the usage read: the card waits for the slower of the two
rather than for their sum, and a failed profile read only drops the badge.

Tier only: that profile reports no end date, and the endpoint that carries
billing state is cookie-only. cockpit-tools shows the same tier with no expiry
for the same reason. The Codex and Claude badges now share one helper
(`web/src/lib/planBadge.ts`), where `expiresAt` is optional.
