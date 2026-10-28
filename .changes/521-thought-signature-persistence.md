### fix(translator): keep a thought signature across a restart

A thought signature is the provider's proof that the model produced a given
thought. The gateway replays it on the next tool turn, because a provider
that receives a function call without the signature it issued refuses the
call.

The signature was kept in the process only. A restart — a crash, a deploy, a
`service restart` — threw them away, and the next tool turn was refused with
a signature error that no amount of retrying could fix: the proof had never
been written down anywhere.

The store now writes each signature to SQLite and reads it back on the next
turn. The table is created with the rest of the core schema, so an existing
database picks it up on the next start without a migration step. Hydration
happens when the repository is built, and it fails open: a database that
cannot be read leaves the store exactly as process-local as it was before, so
a broken database costs the persistence, not the gateway.

A signature is namespaced by the conversation that produced it, and an expired
row is treated as absent rather than replayed — a signature the provider has
already forgotten is worse than no signature.

### fix(translator): scope a thought signature to its conversation

A thought signature is only valid for the function call the provider issued it
for. Two conversations running at once on one account mint the same
`tool_call_id` often enough that a global key is not a rare collision: the
signature written by one turn is replayed by the other, and the provider
answers `400 INVALID_ARGUMENT "Corrupted thought signature"`.

The store was keyed by the id of the response being streamed (`chatcmpl-…`),
which changes every turn and could never be looked up on the next one, while
the read side looked the tool call up with an empty session. Both sides now use
the client's conversation id — the same header the rest of the gateway already
threads through the request context — so a session-scoped request reads only
its own namespace. A request that carries no session header keeps the bare-key
behaviour it had, and keeps writing the same key it reads.

### fix(db): bound the signature table where it is written, not an hour later

Two bounds were measured on the store and neither held.

The 10 000-row ceiling was enforced only by the hourly prune ticker, so a burst
of 22 000 tool calls left 27 190 rows (3.7 MB) in the table until the next
tick — the exact window a burst is unprotected in. The background writer now
trims every `trimEveryWrites` rows, which bounds the overshoot at one batch
instead of one hour; the ticker still handles expiry.

The in-memory map was bounded on write but not on read-through: a memory miss
that repopulated from SQLite skipped the prune, so 2 500 lookups left 2 500
entries against a 2 000-entry cap.

Both tiers now keep their own clock. Hydrate and read-through copied the
seven-day disk expiry into memory, which retired the one-hour memory TTL
entirely; the disk row still lives seven days, and memory holds it for an hour.

The trim order gains a `rowid` tiebreak. `updatedAt` is stored at second
resolution, so a burst writes many rows sharing one timestamp, and without the
tiebreak the trim could keep the oldest of them and delete the newest.

Verified with `go test ./internal/translator/ ./internal/db/
./internal/handlers/chat/ ./internal/proxy/ -count=1` and
`go test -tags=integration ./internal/integration/`. New tests fail on the
previous store: cross-conversation isolation, write-time row ceiling, L1
read-through cap, memory TTL on both repopulation paths, and same-second trim
order.
