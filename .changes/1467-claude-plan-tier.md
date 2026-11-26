Claude: show the seat tier in the quota tracker.

`api.anthropic.com/api/oauth/usage` reports the 5h and weekly windows and
nothing about the seat, so every Claude card read the same regardless of the
plan. The tier now comes from `api/oauth/profile` — the read
`usage_claude_reset.go` already makes for the organization UUID, so no extra
call — and rides on the usage response as `subscription: {plan}`.

Tier only: that profile reports no end date, and the endpoint that carries
billing state is cookie-only. cockpit-tools shows the same tier with no expiry
for the same reason. The Codex and Claude badges now share one helper
(`web/src/lib/planBadge.ts`), where `expiresAt` is optional.
