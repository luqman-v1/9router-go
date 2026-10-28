### feat(quota): ping a connection on a schedule, so its window starts on time

A provider's quota window starts when the window does, not when the first
real request arrives. A connection with a five-hour window, used once a
day, therefore got one real request inside a window that had already been
running for hours — and the next request could land after the window had
exhausted.

The gateway now keeps the window open on its own: a scheduler ticks once a
minute and pings the connections whose anchored slot has come due. The
configuration is read from the settings row on every tick rather than cached,
so a schedule saved in the dashboard takes effect on the next tick without a
restart, and the loop is bound to the shutdown context so it stops with the
other background loops.

**The timezone the schedule uses was wrong by seven hours on Windows.**
Windows reports `SE Asia Standard Time`; that is a Windows name, not an IANA
one, so `time.LoadLocation` failed and the schedule fell back to UTC, while
Node answers `Asia/Saigon` for the same machine. The Windows name is now
translated through the CLDR `windowsZones` mapping (territory 001, the world
default) into an IANA name before it is loaded. The table is generated from
the CLDR source by `scripts/gen-windows-zones.mjs` and embedded in the binary
— no dependency, no network at runtime. `TZ=Local` is refused as well: Go
special-cases that one name and answers it with `time.Local`, so it passed the
lookup and landed in the settings blob as a zone name no other runtime can
resolve.

**The configuration is merged against the stored value rather than written
through.** A client that sends only `connections` would otherwise delete every
schedule, and one that sends only `schedules` would wipe every switch. The
merge happens before the write, so a bad payload leaves the settings row
untouched.

**A ping goes through the engine's own chat handler** — the same instance the
routes are mounted on — so it resolves credentials, refreshes OAuth and picks
an executor the way real traffic does. A second handler would be a second
`PersistentStore` over the same SQLite file with its own in-memory LRU: the
dashboard would report zero entries while the engine served hits, and clearing
entries from the dashboard would leave the engine still serving them. The
handler is returned from the router setup rather than built a second time.

**A ping that fails fires once and does not retry until the next slot.** This
is a decision, and it is the less obvious of the two. The scheduler claims the
slot before it pings, so a provider that is briefly down gets one attempt, one
log line, and the window is simply not started. The alternative — retrying
every tick for the rest of the grace window — would hammer a provider that just
refused, and the operator has no way to tell a provider that is down from one
that is rate-limiting. The window not starting is the recoverable outcome: the
next real request starts it, which is what happened before this feature
existed. `TestSchedulerReportsPingErrors` pins both halves — one error
reported, one call made, and a second tick inside the same slot does nothing.

**A ping pinned to a disabled connection is refused, not redirected.** The pin
was dropped when the connection was switched off, and the strategy then answered
with a different, active account: the ping spent that account's window and left
the disabled one exactly as closed as it was. A probe is pinned to one account
on purpose — that is the account whose window it exists to start — so it now
refuses instead of falling through. Upstream never pings an inactive account
either (`quotaAutoPing.js` filters `isActive: true`).

**The Codex ping asks for its reasoning to be off.** Upstream sends
`instructions: "Reply with OK."` and `reasoning: {effort:"none"}` so the ping is
a near-free warm-up. Without the effort field the model runs its default
reasoning, and the one-token cap is stripped before the request leaves
(`codex_token_params.go`), so the ping was both expensive and unbounded — the
opposite of what a scheduled warm-up is for. `stream: true` is now sent
explicitly too: Codex starts the 5h window only once a streaming response
completes, and relying on the transform to force it was an accident waiting to
break.

**The schedule editor is in the dashboard.** Each claude/codex OAuth row in the
quota tracker carries a schedule control next to the reactive switch, and it
opens the same three-mode modal upstream has (`AutoPingScheduleModal.js`): off,
on reset, or a schedule. The schedule form takes an anchor time, a window and a
slots-per-day count, previews the resulting daily slots and the moment coverage
ends, and refuses a value outside the bounds the scheduler enforces rather than
clamping it silently. Saving writes both halves of the settings key in one
payload, because the mode decides both — a save that wrote only `schedules`
would leave the reactive ping on and fire twice. Choosing Off deletes the entry
rather than storing `{enabled:false}`, matching the merge rule that keeps a
stopped schedule from resurrecting.

The preview arithmetic is a TypeScript port of `SlotLabels()` and both sides
round before the modulo, so a 5.1h window previews 19:42 and fires at 19:42.

**The slot-expiry sweep runs once per tick, not once per claim.** `claim` is
called per configured connection, so purging the fired map there was quadratic
in connection count. The sweep moved to the top of `Tick`.

**A timezone of `Local` is refused in the settings payload too.** The env var
was already refused, but `time.LoadLocation` special-cases the name, so a
schedule payload carrying it was stored verbatim — the same defect, through the
other door. `DefaultTimezoneOr` now refuses it as well.

An unknown quota is reported as unknown rather than as exhausted.
