### fix(free-pi): rotate the session id per account, warm the ad creatives in the background

Two changes to the `free-pi` lane, both reported against real behaviour.

**Account rotation was impossible, not merely unreliable.** free-pi binds a
conversation to the `x-session-id` that opened it and refuses a later request on
that conversation from a different account — the refusal reads *"This
conversation belongs to another user/session. Account rotation is not
allowed."* Because the session id was derived per account and reused for every
request, replaying a conversation on a second account always collected that
refusal, however healthy both credentials were.

The executor now answers it the way the 409 path answers a session conflict:
advance to a fresh session id and retry once, then let the caller's failover move
on. The variant is *derived* from `(account, step)` rather than minted, so a
given step always names the same id and a step already handed out is never
re-derived differently. The step itself comes from a process-local counter, so it
is monotonic within a run and restarts with the process — what survives a restart
is the derivation, not the counter's value. An operator's pinned
`freePiSessionId` is a fixed point — every step returns it — so a pin is honoured
rather than rotated out from under them, and the retry is skipped when rotation
cannot change anything.

This is deliberately not a randomized prefix per rotation. The account already
holds a bounded number of session slots, so minting a fresh random id per turn
would consume them and make `concurrent_session` *more* likely, with the reset
path rate-limited behind a three-minute cooldown.

**Ad creatives are fetched and impressions are fired in the background.** A
prefetch pass fetches a creative for every free-pi connection, stores it warm,
and fires the impression server-side on the same 10-minute cadence
free-pi-cli uses. This satisfies the completion gate without the dashboard
ever being open. The render-gated path in `FreePiAdBanner.svelte` still works
when the dashboard is open; the prefetch adds a background impression on top.
The pass runs on the shutdown context, is a single goroutine with a bounded
cache and a capped fan-out. It reads the connection page once and works from
those hydrated rows — re-reading each row by id would be 1+N statements per pass
(AGENTS.md §4.A), so `GetProviderConnectionByID` is not on the reader interface
at all.

**Ad endpoints now send `x-session-id`.** The official CLI sends it on
`/ads/next` and `/ads/impression` because the ad surface is part of the same
logical session as the completions (`free-pi-cli src/vendor/pi-ads/api.ts`); our
calls omitted it, leaving impressions attributed to no session. Both now carry
the same derivation the executor uses.

Verified: `go build ./...`, `go vet`, `gofmt -l` clean on every touched file,
`go test ./internal/proxy/executor/ ./internal/handlers/dashboard/
./internal/app/ ./web/...`, `make test-integration`, `make vet-svelte` (0 errors),
`oxlint` clean, and `-race` on the free-pi tests. Two tests were confirmed
load-bearing by reverting the change and watching them fail: the rotation test
(fails with the retry disabled) and `TestFreePiPrefetchMakesOneReadForThePage`
(reports `per-row reads = 2, want 0` with the per-row read restored). The four
pre-existing macOS-only failures (`mcodelease` unix socket path length,
`daemon`/`proc` pid lookups, `browserlogin` Wayland) are unchanged.
**The conversation-ownership refusal can arrive inside a 200 body, and it was
being relayed as an answer.** free-pi serves this with HTTP 200 and the notice
as ordinary `message.content`, `finish_reason: "stop"`, zero usage:

> *"This conversation belongs to another free-pi account. Sharing or rotating
> accounts isn't allowed; use one account per person."*

`ForwardFreePi`'s status switch cannot see it, so the notice reached the client
as a plausible-looking completion and no failover happened. The shared
`proxy.EmptyUpstreamError` guard does not catch it either — verified directly, it
returns `nil` on the real body, because `carriesAnswer()` accepts any non-empty
carrier and a refusal is non-empty text.

A new `freePiOwnershipRefusalInBody` check runs ahead of `jsonResponse` and
requires **two signals together**: a marker phrase *and* zero completion tokens
in `usage`. The usage counter is what separates a refusal from a genuine answer
about the same subject — a model that emitted the text spent tokens producing
it, so non-empty content with zero completion tokens is structurally impossible
for a real completion. Either signal alone misfires, so both directions are
pinned by tests: a genuine answer quoting the notice survives, and marker text
*with* token spend survives.

The refusal is surfaced as a real 403 carrying the verbatim upstream text so the
fallback layer can act on it. This is deliberately **not** the session-rotation
fix: "one account per person" is an account-identity rule and rotating the
session id cannot change which account is asking. free-pi enforcing its own
policy is upstream's call; the defect was that a refusal was reported as a
successful turn.

The body read reuses the same 10 MB `LimitReader` `jsonResponse` already
applies, on the non-streaming path only. The end-to-end test is load-bearing:
reverting the check makes `ForwardFreePi` relay the refusal as a successful
turn.

**A mid-conversation account switch was refused and never recovered.** Two
independent defects, both rooted in persisting a value that should have been
derived.

The invariant is that a session id is a **function of the account** — account A
must never present account B's id, which is precisely what upstream refuses with
*"This conversation belongs to another free-pi account."* Both derivations
already satisfy it (`freePiAccountSessionID` = `uuid.NewSHA1(ns, accountKey)`;
the rotated variant = `uuid.NewSHA1(ns, "<accountKey>/rotation/<step>")`), so
the arithmetic was never wrong. Persistence broke it:

- **Login wrote `freePiSessionId` as a cache, and every reader treated it as an
  operator pin** — including `freePiRotatedSessionIDFor`, which returned it at
  every step. Rotation therefore could never change anything: every step produced
  the id that had already been refused. Nothing in the product ever set a pin
  (the dashboard has no field for one), so the branch was pure liability. Login
  no longer writes it; both session helpers derive unconditionally and ignore
  connection data; and the ads handler stops preferring a stored id, since an
  impression reported under a different session than the completions is
  attributed to a session that never ran the conversation. **This also covers
  rows written before this change** — they still carry the field, and honouring
  it would let one account present another's id.
- **A free-pi 409 `concurrent_session` was classified as request-scoped.**
  `isRetryableComboError` and `isRequestScopedComboError` both rejected every
  4xx except 429, so an account that had hit its session cap ended the request
  instead of failing over — which is why a second free-pi account never picked up
  a mid-conversation switch. The verdict is about the account's **capacity**, not
  the request, so another account can serve the request unchanged. Keyed on the
  envelope `code` via `providers.FreePiIsConcurrentSessionCode`, never the
  message text, and scoped to that one code: an unrelated 409 stays
  request-scoped so failover cannot mask a real conflict.

Pinned by `TestFreePiSessionIDsNeverCrossAccounts` (no id collision across five
accounts × nine rotation steps) and `TestFreePiForward_RotatesEvenWithAStoredSessionID`.
Both are load-bearing: restoring the old lookup makes the first fail with *"a
stored id let one account present another account's session id"*. Two tests
asserting the old pin behaviour were rewritten to assert the invariant instead.

**Round-robin no longer moves a live conversation to a different account.** With
`round-robin` + `sticky 1`, the rotation cursor was keyed by combo alone
(`key := comboName`), and `sticky 1` advances on the very next request — so turn 2
of a conversation was routed to the next account. free-pi binds a conversation to
the account that opened it and refuses the switch with *"This conversation belongs
to another free-pi account. Sharing or rotating accounts isn't allowed; use one
account per person."*

The rotation state is now keyed by **conversation** as well as combo
(`conversationFingerprint`, anchored on the first user message so it is stable for
the conversation's life), and a provider that declares `BindsConversations`
pins its conversation's seat instead of counting it. Every turn of one
conversation reuses the seat that opened it; round-robin still spreads **new**
conversations across accounts, which is where the parallelism comes from.

The capability is registry-declared (`ProviderConfig.BindsConversations` →
`providers.BindsConversations`) rather than matched by provider id, per AGENTS.md
§3.B/§3.C. The conversation-keyed map is bounded
(`comboStickyMaxConversations = 8192`, evicting idle entries by `LastUsed` after
6 h) because the key is client-driven — without that it would grow without limit
(§4.G).

This is deliberately **not** a request-shape change: the refusal is about which
ACCOUNT owns the conversation, so altering or hashing message content cannot
legitimately fix it, and rewriting the user's messages would corrupt context and
defeat prompt caching. Keeping the conversation on one account is the honest
equivalent of what a human client does.

Pinned by `TestComboConversationSticksToItsAccount`, which is load-bearing:
removing the pin makes it fail with *"turn 2 moved to free-pi/model-b; the
conversation started on free-pi/model-a"*. It also asserts an ordinary provider
still rotates per request, so the pin cannot leak beyond providers that need it.
