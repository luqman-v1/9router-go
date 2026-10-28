### feat(db): per-custom-provider limit storage, token quota readers, and a ledger carryover for them

A custom provider (`providerNodes`) is one upstream endpoint shared by every
connection under it, and until now the gateway had no way to say what that
endpoint tolerates. This lands the storage and repository layer only — nothing
enforces yet — for the six limits planned in `docs/provider-limits-plan.md`:
RPM, TPM, concurrency, a lifetime token quota, a queue timeout, and a contract
expiry date.

- Seven Go-only columns on `providerNodes` (`rpmLimit`, `tpmLimit`,
  `concurrencyLimit`, `tokenQuota`, `queueTimeoutMs`, `quotaResetId`,
  `contractExpiresAt`), applied by the existing `EnsureAdditiveColumns` bootstrap,
  so an existing install picks them up on the next start. The dashboard's node
  `data` JSON blob is deliberately left alone: that blob is rewritten by node
  updates and mirrored into every connection's `providerSpecificData`.
- `internal/db/provider_limits.go` reads and writes the policy.
  `GetProviderLimits` answers the zero value for an unknown node, which reads as
  unlimited; `SetProviderLimits` rejects negative counts and an unparseable
  contract timestamp (the column is parsed on every dispatch, and a value that
  never parses would silently mean "no expiry"), normalises the timestamp to
  RFC3339 UTC, and refuses an unknown node. `quotaResetId` is not writable there —
  it belongs to `ResetProviderQuota`, and a policy save that moved it would either
  forgive spend or charge it twice.
- `internal/db/provider_quota.go` mirrors the per-key budget reset (#365): a
  high-water mark on the node row is the `usageHistory` row id the quota is
  measured from, `ProviderTokenUsage` sums the node's ledger rows from that mark
  forward plus any carried spend, and the mark plus the node's carryover are
  written in one transaction so a request landing between them cannot slip onto
  the wrong side of the boundary.
- The ledger prune now carries provider spend too. `usageHistory.provider` holds
  the node id for a custom provider, so a node's quota is derived from rows a
  retention prune deletes: without `providerLedgerCarryover` the prune would have
  silently refunded an exhausted node's quota. The chunk is folded once against
  both the key marks and the node marks, in the same transaction that deletes the
  rows. A fixture without a `providerNodes` table still prunes: the dimension is
  probed and left nil.

Verified: `go test ./... -count=1` passes, including
`TestPruneUsageHistory_ProviderSpendInvariant` (a node's usage is identical before
and after a prune) and `TestPruneUsageHistory_ProviderResetIsNotReBanked` (a reset
that precedes the prune is not charged twice). Formatting checked against LF
content, because a `core.autocrlf=true` worktree makes `gofmt -l` flag every file.

### feat(chat): enforce a custom provider's own RPM, TPM, concurrency and queue timeout

The columns above are now enforced. A custom provider is one upstream endpoint
that every connection under it shares, so what it tolerates belongs to the node,
not to the caller — a different axis from the per-key limits, which bound what one
caller may spend. The key is still checked first, so a caller that has exhausted
its own allowance never consumes a provider's capacity on the way to being refused.

- The existing limiter serves both scopes through a namespaced key
  (`prov:<node id>`), so there is one sliding window and one FIFO queue rather than
  two implementations free to drift. `AcquireProvider` charges TPM first (not
  queueable: a bucket refills continuously, so a parked request could be admitted
  and immediately refused again) and then RPM/concurrency through the same queue,
  up to the provider's own timeout.
- Enforcement runs inside `tryForwardWithConnection`, once per upstream attempt,
  because the provider is only known after model resolution and because a refusal
  must fail over rather than end the request. The slot is released when the
  attempt ends — including on failover — so one slow pass cannot lock a node out
  of the concurrency it was granted.
- A refusal is a failed candidate: the combo loops try the next one and only the
  last candidate produces an error. It is deliberately NOT a cooldown —
  `RecordConnectionScopedError` records `lastError` for the dashboard while the
  account stays in rotation, because a busy endpoint is not a dead credential.
- The node-state axes reach that rule by a second road, and it needed closing. A
  congestion refusal (429) is produced and intercepted inside the failover loop; an
  expired contract or a spent quota answers 403, and the single-provider loop pins
  each candidate by id, which skips the selection-time node-state check. The 403 then
  came back as an upstream error and landed in the generic retryable-status branch
  that writes the account cooldown — parking a healthy connection for 120 s, on the
  model and on the account, for a fact about the node. The typed refusal is now
  intercepted before that branch, so both axes fail over without touching a cooldown.
  The row it records now also carries the status the client saw (403, with wording
  about the contract) instead of a hardcoded 429.
- Debug trail for the provider that was skipped: one structured log line naming
  provider/axis/limit/window count/concurrency/retry-after, the existing
  `router_rate_limit_rejects_total{scope="provider"}` counter, the `lastError` row,
  and an `attempts[]` array in the final 429 body so a client can see which
  providers were tried and why. The wait is reported in the body's `retryAfter`
  field; the response writers pass upstream status and body through and do not copy
  upstream headers, so no transport-level `Retry-After` is set on this path.
- A token charge the queue refuses is refunded in full (that attempt never left),
  and the reservation is reconciled with the turn's real prompt+completion total
  through the same settler the per-key TPM charge uses — an attempt that died
  before metering gets its charge back instead of spending a provider's budget on a
  turn nobody was served.
- One request remembers which providers refused it, so a pass with several
  accounts under the same saturated node waits out the queue timeout once instead
  of once per account; the memory dies with the request, so the next request is
  free to try the same provider again.
- An unreadable policy reads as unlimited: a storage blip must not take every
  custom provider offline.

Verified: 9 limiter tests (window per provider, slot released on failover, queue
timeout names its axis, TPM refusal, refund on queue refusal, settle only corrects
an under-estimate, release refunds an unsettled reservation, snapshot ignores stale
stamps) and 6 request-side tests (no slot for an unlimited provider, refusal is a
429 carrying `attempts`, a refused provider is not waited on twice, the policy is
memoised per request, fail-open on a read error, `lastError` written without a
cooldown), plus `golangci-lint --new-from-rev` clean and `go test ./...` green.

### feat(chat): refuse a custom provider whose contract expired or whose lifetime quota is spent

The two provider-level limits that no queue can absorb — an expired contract and a
spent lifetime token quota — are now enforced on the node itself, and enforced
once: `providerNodeBlocked`/`providerNodeVerdict` in
`internal/handlers/chat/provider_limits.go` is the only place a node's usability
is decided, and both callers ask it. Candidate selection (`connections.go`,
before strategy rotation) skips an unusable node, and dispatch
(`acquireProviderAttempt`) asks the same helper **before** it touches the limiter,
so a node skipped while picking a connection cannot be admitted when the request
is forwarded, or the reverse. The verdict is memoised per request alongside the
policy (`providerLimitState.verdicts`): one limit read and one ledger sum per
node per request, not one per caller.

The refusal's status now follows its axis instead of being 429 for everything:
`contract` and `token_quota` answer **403** with
`provider_contract_expired` / `provider_token_quota_exhausted`, while congestion
stays **429** `rate_limit_exceeded`. No `Retry-After` is sent for a node state —
waiting does not end a contract or give back quota. A spent quota means
`used >= tokenQuota` (99 tokens under a 100 quota stays usable, 100 is refused);
`ResetProviderQuota` makes the node usable again without a restart. When the
ledger cannot be read the node is **failed open**: a storage blip must not drop a
healthy provider out of rotation.

Reason survives the combo path too: both combo selection-error sites capture the
typed refusal and keep it as `lastErr`, instead of replacing the cause with
"all combo models failed".

Verified: 6 new cases in `internal/handlers/chat/provider_limits_test.go` — an
expired contract is refused with 403 `provider_contract_expired` even with no
congestion limits configured, a spent quota is refused at the exact boundary
(3 subtests), a quota reset restores the node, an unreadable ledger fails open,
selection skips an expired node, and an expired node as the only candidate
answers 403 rather than `no active connections`. Test fixtures were moved to the
production schema rather than the production code being loosened:
`dbtest.CreateTables` now also creates the ledger tables (`EnsureLedgerTables`),
and the fixture's `usageHistory` carries the `id INTEGER PRIMARY KEY AUTOINCREMENT`
the quota reset mark reads.

It also hardens `TestQueueIsFIFO`: it established enqueue order with a 5 ms sleep
stagger, and a loaded machine broke that: goroutine 4 reached `Acquire` before
2 and 3, so the recorder saw `0,1,4,2,3,…` and reported a FIFO violation the
queue had not made. Each waiter now starts only once the previous one is parked
(`waitParked`, white-box on the queue depth), so the test asserts the queue's
fairness instead of the scheduler's timing: 50 consecutive runs pass, including
20 with six CPU burners saturating the machine.

### feat(dashboard): a custom provider node's limits, its spend, and the live window

The policy the data plane enforces is now editable and observable from the
dashboard. `PUT /api/provider-nodes/{id}/limits` merges rather than replaces —
an omitted field keeps its stored value, an explicit `0` clears it, a number
sets it — with the same tri-state `mergeLimit` the per-key editor uses, so an
operator does not have to learn a second one. A negative count and an
unparseable contract timestamp answer 400, an id that names no node answers 404
(a silent success would report a policy nobody can find again), and
`POST /api/provider-nodes/{id}/quota/reset` moves the measurement mark without
touching the history the analytics reads. `GET /api/provider-nodes` now carries a
`limits` object per node — policy, `usedTokens`, `rpmUsedWindow`,
`activeConcurrency`, `expired` — filled from two queries for the whole list
(`ListProviderLimits` plus a new `ProviderTokenUsageByNode`, which unions the
carryover so a node whose rows a prune already deleted still reports its spend),
and from the same limiter the gateway meters with, so a dashboard figure and a
refusal cannot disagree.

Three gaps surfaced while writing the integration test, all of which the unit
suite was structurally unable to see:

1. **`SetRateLimiter` had no production caller.** The provider gate returns early
   when the handler holds no limiter, so the whole per-node feature was inert on
   a real boot while every unit test — which sets that field by hand — stayed
   green. `SetupServerRouter` now hands the limiter over right after building it.
2. **`resolvePrefixProvider` swallowed the node's refusal.** With an expired
   contract the lookup fails, resolution fell through to the bare prefix, and the
   client read `502 no active connections for provider: <prefix>` instead of the
   typed 403 that names the cause. A node that is merely unusable now stays
   pinned; a prefix that is also a built-in alias keeps its alias path, so an
   unusable node cannot silently take requests meant for another provider.
3. **The two write routes were mounted only in `dashboard.RegisterRoutes`**, which
   nothing but the package's own tests calls — the binary mounts the dashboard API
   from `SetupDashboardRoutes`. Both tests passed and the live server answered a
   plain `404 page not found`. The routes are registered in both tables now, and
   `TestProviderNodeLimitsAreEditableThroughTheRouter` drives them through the real
   router so the next one added in a single place fails a test instead of a user.

DB export/import carries the seven node limit columns and `quotaResetId`: a
backup that quietly lifted a node's ceiling would turn disaster recovery into an
accidental opening of the throttle, and restoring spend without its mark would
charge the node for tokens it had been forgiven.

Verified: 4 new handler cases (`provider_node_limits_test.go` — merge leaves other
fields alone, negative/malformed/unparseable answers 400 without writing, an
unknown node answers 404, a reset clears spend while keeping the budget and the
history, and the list reports policy, spend and live window/concurrency from a
limiter with one request in flight), 4 integration cases through the real router
and a real SQLite file (an RPM ceiling answers 429 and the refused attempt never
reaches the fake upstream; a spent quota answers 403
`provider_token_quota_exhausted`; an expired contract answers 403
`provider_contract_expired`; and the limits editor saves, lists and resets a
node's policy through the router the binary actually mounts, which is where the
route tables differ), a resolution case pinning that an unusable node still
resolves to the node, and the `usageHistory` fixture in
`provider_nodes_update_test.go` moved to the production column set (it was
missing `promptTokens`/`completionTokens`, so the node list answered 500 rather
than the test failing on the behaviour it was about).

### feat(dashboard): edit a node's capacity limits in place, and add models by the paste

The policy the data plane enforces is now editable where the operator sees it: a
`NodeLimitsEditor` panel on the provider page (and inside the node's edit dialog)
with the five numeric ceilings, the contract end date, a read-only usage block
(tokens against the lifetime quota, requests in the running window, requests in
flight) and a button that restarts the token count. It saves through the limits
endpoint alone, because the node's own update requires name and prefix on every
request — routing a limits edit through it would turn an empty field into a 400
for the whole node, and a limits save must never be able to rename anything.

The contract date is sent as the **end** of the chosen day in UTC
(`YYYY-MM-DDT23:59:59Z`). Midnight is the start of the day the operator picked,
which in a positive-offset timezone is the previous evening — a paying node taken
offline a day early.

`AddCustomModelModal` gained a Single / Bulk Add switch. The bulk mode takes one
paste — spaces, commas, semicolons or newlines — strips a `<alias>/` prefix so
`nara/glm-5.3` and `glm-5.3` land the same row, dedupes keeping paste order, and
reports ids it will not send instead of dropping them: a slash or a pipe would be
read back as an address rather than part of the id. Capabilities and token limits
apply to every id in the paste, and the footer says how many it is about to add.

The server side is one request for the whole paste (D9): `POST /api/models/custom`
accepts either the original single-model body or `{providerAlias, models: [...]}`,
writes the accepted entries in one transaction (`Repo.SetKVMany`, so "8 added"
cannot become three rows plus a crash), and answers with the ids it stored and one
`{id, reason, code}` per line it did not — `already exists on this provider`,
`repeated in this batch`, or the name-collision code a single save would have
answered 409 with. That last part is why `guardNameCollision` was split: one
`nameConflict` decision now serves both callers, so a batch cannot accept a name
the single-save path refuses.

Verified in the running dashboard, not only in the build: the limits panel renders
on a real node page, a save round-trips through `PUT .../limits` and the listing
reports the new value, and the node was returned to its original all-zero policy
afterwards; the bulk mode's counters react to a paste (`2 to add · 1 repeated ·
1 invalid`) and a real save answered `✓ 2 added, 1 duplicate` with both rows
stored under the right alias, which were then deleted again. Gates: `bun run
build`, `bun run ratchet:svelte` (0 unresolved, 81 errors = baseline), `bunx
oxlint --deny-warnings`, 18 parser cases under `bun test`, plus the Go suite and
the integration suite.

The editor lives in the node dialogs, and it was moved there on review. The first
version had it as a bordered card on the provider page, which put six inputs and
two buttons above the connection list and pushed twelve connections below the
fold. Now `ProviderNodeLimitsField` sits inside the Add and Edit dialogs as a
section, separated from Name and Base URL by a rule and collapsed to one header
line: the same `Input` as the fields above it, no card of its own, and one Save
instead of two (the Edit dialog writes the policy before the node, because a URL
suffix change renames the provider id and the rename carries these columns). The
section opens by itself when a node already has a limit and stays closed for a
node that has none. The page keeps a single read-only line — `Capacity … ` plus
`Contract ended` / `Quota spent` when they apply — so the state is still visible
without turning the page into a form. A new node's policy rides along with the
create and is written straight after it, since the limits endpoint needs the id
the create answers with.

Verified in the running dashboard: the page shows no limit inputs and one status
line, the section opens with `Unlimited` in the Edit dialog, its Save sends
`PUT …/limits` with the typed value and the listing reports it (the node was
returned to its original all-zero policy afterwards), and the six fields now carry
ids so their labels are associated. Gates: `bun run build`, the svelte ratchet at
its baseline with no unresolved identifiers, `oxlint --deny-warnings`, and 253
`bun test` cases including 16 new ones for the draft/patch conversions. The Add
dialog's create-and-then-PUT path is wired and type-checked but not yet confirmed
by a click in this environment: synthetic clicks on that dialog's Create button
did not reach its handler in the headless session, while the same technique fired
other controls, so it needs one manual pass rather than a claim here.

Two defects surfaced on the way and are fixed: a count field parsed with
`parseInt`, so a typo like `1.5.2` silently pinned a node to one request a minute
instead of leaving it unlimited, and the shared `Input` had no `id`, so every
label in the app was decoration rather than a control label.

### fix(dashboard): keep the limits section open, make the counter reset clear, and save limits under the new node id

Three things went wrong once the section was inside a dialog that lives next to a
polling page.

The section closed itself. Its open state was seeded from the stored policy on
every change of the `limits` object the page hands down, and the page re-reads the
node list every ten seconds, so a poll collapsed it under the operator's hands
mid-edit. It is now seeded once per node id, with the seed kept in a plain
variable so the effect's own write cannot retrigger it. Measured: open after 23
seconds, two poll cycles.

The counter reset was a bare button that could be hit by accident and said nothing
about what it does. It now sits in a `Token count` row next to the live figures,
explains that the quota counts from the last reset and that usage history is kept,
and asks for confirmation (`Yes, reset the count` / `Cancel`) instead of acting on
the first click.

Editing the URL suffix lost the policy. The limits are columns on the node's own
row, so a rename moves them — but the dialog wrote the policy to the id it was
still holding, *before* the rename, so the node came back unlimited. The dialog
now hands the policy to the page in the save payload, and the page writes the node
first and the limits second, against `res.node.id`. Proof from the running
dashboard: renaming `dahono` to `dahono-omptest` sent
`PUT /api/provider-nodes/openai-compatible-chat-dahono` and then
`PUT /api/provider-nodes/openai-compatible-chat-dahono-omptest/limits` with
`queueTimeoutMs: 700`, and the renamed node reported 700.

The reason all three hid for so long is one type: Svelte binds
`<input type="number">` as a number while the seed filled strings, and the
conversion called `.trim()` on whatever it received. A save with a typed limit
therefore threw `e.trim is not a function` inside the submit path — no request, no
message on the Add dialog — which is what the earlier "unverified Add path" note
in this fragment was actually looking at. The draft and the shared `Input` are now
typed `string | number`, a regression test pins the numeric case, and the Add path
is confirmed end-to-end: create, then the limits written to the id it answered
with.

### fix(dashboard): split a pasted list of model ids instead of saving it as one id

The bulk paste was behind a `Single | Bulk Add` switch that defaulted to Single, and
the field an operator actually uses — the inline `Model ID` input on a compatible
provider's page — had no bulk path at all. Pasting `model1 model2:free model3-ext`,
or the same list with commas or semicolons, therefore stored one model whose id was
that whole string: a row nothing can address without reproducing the spaces exactly.

There is no mode now. One field takes one id or a pasted list, the paste is parsed,
and its shape decides what happens: a single clean id keeps the single-save path (so
it can be tested against the provider first), while more than one — or a line the
paste half-failed on — becomes a single `POST /api/models/custom` carrying an array,
with the response saying which lines landed and why the others did not. Both fields
preview the split before writing (`3 ids to add · 1 repeated · 1 rejected`).

The server enforces the same rule, because the dashboard is not the only client: an
id that still contains a space, comma, semicolon or tab is refused with a typed
`INVALID_ID` 400 on the single save and reported as a skipped line in a batch, with
the reason written for the person who pasted it.

Verified in the running dashboard: all three separator shapes preview `3 ids to add`,
and saving one sends one request whose body lists three ids; the three rows appear
under the node and were deleted afterwards. A single save carrying the unsplit string
answers `400 INVALID_ID`. Gates: `go build`, the dashboard and db suites, `bun run
build`, the svelte ratchet at baseline, `oxlint --deny-warnings`, 450 `bun test`
cases including a new table for the id guard.

