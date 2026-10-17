### feat(deepseek-web): stealth mode — behave like the real web client, not like a bot

A browser-login account was getting muted by the upstream for days at a time.
The cause is not one thing: `chat.deepseek.com` is a consumer account, not an API
plan, and everything about a request says whether a browser made it. Four of
those signals were wrong or missing. All four are now parity with the site's own
client, and the mode is on by default (`providerSpecificData.stealth = false`
opts out per connection).

**The fingerprint was stale and incomplete.** It advertised client `2.0.0` while
the live build sends `2.3.0`, and it sent neither the client-attestation headers
(`x-hif-leim` / `x-hif-dliq`) nor the `Sec-Fetch-*`, `Priority` and client-hint
headers a browser always sends. The attestation values are not a signature to
reverse engineer: the browser fetches them from `hif-leim.deepseek.com` /
`hif-dliq.deepseek.com` and caches them for the TTL the answer announces in
`x-hif-ttl` (600s), so they are now fetched and cached the same way, with a
failed fetch degrading to the cached value and then to no header rather than
failing the turn. The dashboard probe now sends the executor's own header set
instead of a copy of it, so "valid" from the dashboard means the real turn will
be judged the same way.

**Every turn re-sent the whole transcript in a fresh session.** That is the
behaviour a sibling implementation (`snake-aabb-wtf/deepseek-web2api-free`) names
as the main cause of account mutes: with a new session per request and the full
history as the prompt, every message in the upstream session embeds every
previous turn, which grows quadratically and no browser produces it. A
conversation is now pinned to one upstream session and each turn sends only the
part the previous turn did not — threaded on the `parent_message_id` the upstream
itself named. Three conditions must hold (a client conversation id, no other turn
holding the session, a prompt that extends what was sent) and every failure falls
back to the plain behaviour rather than guessing. A turn that did not pin cannot
overwrite the chain a concurrent turn is still building.

**A burst had no ceiling.** An agent client dispatches parallel sub-agents, and
those all arrived on one account at once. A per-account gate now caps in-flight
turns (default 2) and queues the rest, and each turn waits a random part of a
400ms window before its first upstream call so a fan-out does not leave in
lockstep.

**A muted account was read as a cut-off session.** The enforcement arrives as
HTTP 200 with the refusal in the body — `{"data":{"biz_code":5,"biz_msg":"user is
muted","biz_data":{"mute_until":…}}}` — and an in-stream refusal arrives as an
`event: hint` / `event: toast` error frame with no fragments at all. Both were
reported as 502, so the router retried the same punished account, which is how a
brief mute becomes a long one. Both now reach the router as **429** carrying the
wait the upstream announced in `Retry-After`, so the account is cooled down and
the next connection serves the turn. A version refusal ("update to the latest
version") is the gateway's own problem and stays a 502 — it must not cool an
account down. A streaming client also gets the refusal in-band, since the status
is already on the wire.

**Also fixed:** `parent_message_id` was typed as a string; the site threads on an
integer. A pinned conversation is rendered as a role-tagged transcript, because
turn one used to send bare user text while turn two sent a transcript — so no
turn could ever be expressed as an extension of the previous one.

**Verification:** `go test ./...`, `go vet ./...`, `make test-integration` and
`bun run build` all clean. New unit coverage drives real turns against one fake
upstream: session reuse with only the tail sent, divergence starting a fresh
session, the concurrent-turn pin rules, the gate (cap, per-account scope,
double-release, cancelled wait), attestation caching and degradation, mute and
in-stream refusal mapping, stealth off restoring the plain path and its header
set, and the stealth option defaults. Live, against the real attestation host:
`hif-leim.deepseek.com/query` answers `{"data":{"biz_data":{"value":"…"}}}` with
`x-hif-ttl: 600`, and `create_pow_challenge` accepts the request with header
version 2.3.0. `hif-dliq.deepseek.com` is published IPv6-only and had no route
from the probe host, so `X-Hif-Dliq` was exercised by unit test only — the
designed degradation (header omitted, turn still proceeds) is what the live run
did.

**Known limit — the TLS stack is still Go's.** Measured against a local capture,
Go's ClientHello is the textbook `c4ccfa85…` JA3 (13 ciphers, 10 extensions, no
ALPN, no GREASE) and its request headers go out alphabetically, where a Chrome
build negotiates 16 ciphers / 17 extensions with GREASE and `ALPN h2,http/1.1`
and sends headers in its own order. That is the one browser signal this change
does not match. No probed endpoint rejected the Go build for it —
`create_pow_challenge` answered `200` to both stacks with byte-identical bodies,
and the site's HTML shell answers `202 x-amzn-waf-action: challenge` to both — so
it is recorded as an open gap, not as a fix that failed. Adopting a Chrome-profile
TLS stack for this provider is a follow-up on its own: it needs the connection's
proxy handed across to the new transport, and it should not be argued for in the
same PR as a measured ban cause.

### fix(dashboard): a browser-login provider's tile counted none of its connections

The DeepSeek Web tile read "No connections" beside a live connection, and the
counter at the bottom of the sidebar agreed — while the provider's own detail
page listed the connection.

The API Key Providers section renders the browser-login providers too, but it
scored every card against a fixed `['apikey','api_key']` list. A browser-login
credential is stored as `authType: 'cookie'` — the add modal derives that from
the catalog's `webCookie` category — so the tile matched nothing. The detail
page filters by provider alone, which is why the two views disagreed.

`providerAuthTypes` now widens a section's list with the types the catalog
declares (`authType`, `authModes`) instead of a caller passing one fixed list
per section. It only adds, so no provider that used to be counted stops being
counted; the `apikey` / `api_key` spellings travel together because the registry
uses both for one credential. The same call site is shared by the OAuth section,
where a dual-auth provider (`clinepass`, `qoder`, `xai`) declares `apikey` in
`authModes` and its oauth connection was the only one counted.

Verified in Chromium against a real gateway: the tile prints `1 Connected`, and
the new `web/e2e/webCookieProviderTile.test.ts` fails with the exact reported
string (`DeepSeek Web No connections`) when the section list is passed again.
All 22 `(provider, authType)` pairs present in an operator database were
replayed through the helper — every one is counted.
