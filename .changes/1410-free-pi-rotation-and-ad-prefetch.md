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