Codex: show the plan tier and the day it lapses in the quota tracker.

`wham/usage` reports the rate-limit windows and never when the plan itself
ends, so the tracker showed no plan row at all. The tier and its expiry now
come from `chatgpt.com/backend-api/accounts/check/v4-2023-04-27` — the same
source cockpit-tools and ccLoad read — and ride on the usage response as
`subscription: {plan, expiresAt}`. The card header draws the tier as a badge
and the end date with a countdown; a team seat with no entitlement expiry is
shown as a tier alone rather than as expiring at year 1.
