### feat: per-model circuit breaker — one bad model must not park the whole provider

The two resilience layers in the tree answer the wrong question for this case.
`internal/resilience/open-sse` is per-provider, so a single model that starts
failing deterministically inside a healthy provider opens the whole provider and
takes every sibling offline with it. `internal/modelhealth` probes models on a
schedule, which does not watch live traffic — a model that began failing thirty
seconds ago keeps receiving requests until the next pass.

`internal/modelbreaker` adds the layer between them, scoped to one
provider/model pair: five model-scoped failures open a 60-second cooldown, each
further cycle doubles it capped at 10 minutes, and a model that fails through
three cooldown cycles stops serving until an operator resets it.

- **Only the model's own failures are charged.** The scope comes from
  `internal/errclass`, not from a status-code pattern: `ScopeModel` counts
  (the model is gone, the prompt does not fit), while `ScopeProvider` (5xx,
  transport) stays with the provider breaker and `ScopeAccount` (429, quota,
  auth) stays with connection rotation. Charging either of the latter here would
  put every model of a briefly-down provider into cooldown and then disable all
  of them after three cycles — the exact failure this change exists to prevent.
- **`Classify` reads the error envelope instead of the raw bytes.** Providers
  carry their verdict in `{"error":{"type":…,"code":…,"message":…}}`, and
  errclass's precedence is type, then message, then status. Passing the body as
  a plain message skipped the type and fed JSON prose to the phrase table, so a
  typed model verdict fell through to the status map.
- **One gate, both provider kinds.** It is wired in
  `tryForwardWithConnection` — the point every OpenAI-compatible lane converges
  on — and reads its identity from the provider and model already resolved
  there. A custom OpenAI/Anthropic-compatible node is covered without being a
  special case: it is a node id in the provider slot, exactly as it is
  everywhere else in that handler. The refusal is a local `*upstreamError` (503,
  `model_cooling_down`), so no credential is cooled over a verdict about a
  model and the caller's failover and usage paths handle it unchanged.
- **A probe is exempt.** `POST /api/models/test` dispatches into this handler for
  real, and answering "cooling down" would report this layer rather than the
  model the operator asked to test.

Numbers follow Pro pro.26's per-model policy (60s doubling, capped at 10
minutes; three cycles before a disable) from the upstream sweep that added the
same layer there.

Provenance, stated precisely because a reviewer asked: this is
[giienew/9router-pro](https://github.com/giienew/9router-pro) pro.26, a
sibling-reference in the AGENTS.md §1 sense — not a merged `decolua/9router`
commit, and none has been found carrying these numbers. The policy is ported
behaviour from a community edition of the same gateway, not upstream parity, and
this fragment does not claim otherwise.

Verified: `go build ./...` clean, `go vet` clean, `go test` green on
`internal/modelbreaker` (table-driven state machine, cooldown caps, key
splitting), `internal/errclass` and `internal/handlers/chat` (refusal before
dispatch, probe bypass, disabled-survives-refusal, only-model-scope charging),
plus `gofmt`.

One consequence of the registry being package-level: the integration suite runs
every test in one process against isolated databases, so an isolated `Env` does
not isolate a package-level map. A test that failed `deepseek/deepseek-chat`
often enough to open a cooldown then left that pair cooling, and a later test
dialling the same pair was refused before a byte reached its fake upstream. The
harness now clears the registry before each test and in cleanup, the same
treatment the provider breaker's registry already has, so a pair a test parks is
still refused while a pair a neighbour warmed is not. Production behaviour is
unchanged — the gate keeps its own unit coverage that charges a real pair and
asserts the refusal.
**Dashboard coverage.** The panel is a visible surface, so it gets a live suite
rather than a unit test: `web/e2e/modelBreakerPanel.test.ts` boots the real
gateway, points a connection at a fake upstream that answers a model-scoped 404,
and lets the chat lane charge five real failures until the breaker opens its
first cooldown. The assertions then read the row through `/api/model-breakers`
and on screen — a panel that renders an empty table fails. It also drives
**Reset** through the UI and checks the pair returns to `closed`. The capture is
opt-in (`CAPTURE_SCREENSHOTS=1`) so CI asserts behaviour without writing files.

Element-scoped evidence for the visible change lives in
`docs/screenshots/1419-model-breakers-*.png`: the sidebar before and after the
new entry, the panel route before (no such page) and after, and the charged
`deepseek/deepseek-chat` row in **Cooling**. The `before` pair was captured from
a `main` build; the `after` pair from this branch, both at the same viewport.

**Provenance is now an authorised reference, not an implicit one.** AGENTS.md §1
gains a `Secondary Reference: 9router-pro` subsection: Pro is a legitimate source
for *operator-facing behaviour*, explicitly not for `/v1`·`/api` wire parity and
not authority to port its UI. The numbers in this fragment come from
`giienew/9router-pro` pro.26, and the fragment says so rather than implying they
match a merged `decolua/9router` commit.
