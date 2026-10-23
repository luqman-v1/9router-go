### fix(oauth): dedupe every provider's refresh in the shared path, not just minimax-code

Upstream wraps every provider refresh in `open-sse/services/tokenRefresh/dedup.js`, so *all twelve* refresh call sites get in-flight collapsing and a 10-second result cache (`REFRESH_RESULT_TTL_MS`). The port had that wrapper only on `minimax-code`; every other provider posted its own exchange even when another caller held the same spent token.

The result cache is what makes a sequential second caller safe. singleflight forgets a flight the moment it completes, so a caller arriving just after the previous refresh finished posts the same single-use token again and gets `invalid_grant` — which the port classifies as grant-dead, so a working account is signed out rather than retried. It also matters when two connections belong to the same account: the chat-side flight is keyed by connection id, so it does not collapse them, while upstream's key — provider plus the spent token value — does.

`oauth.Refresh` now runs the provider refresher through that wrapper, keyed on `provider + spent token`, with upstream's TTL and the rule that a failure is never cached. One layer covers every provider at once, and the per-caller flights go back to collapsing only concurrent work for one connection. The cache is bounded (256 entries, swept on insert) because its key holds a client-supplied refresh token, and each caller gets its own copy of the answer so no caller can mutate what the next one reads.

Covered by `internal/proxy/oauth/refresh_dedup_test.go`: on the parent, two sequential refreshes of one account reach the token endpoint twice where they must reach it once, and sixteen concurrent callers reach it sixteen times; after the change each is exactly one.
