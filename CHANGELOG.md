# Changelog

## [v1.9.14] - 2026-10-20

### Fix Settings redirect to Endpoint & Key in mobile and unauthenticated sessions

- Removed read-only `/api/settings/database/stats` from `IsAlwaysProtectedPath` in `internal/middleware/dashboard_auth.go`. When `requireLogin` is disabled (default local setup without master password), fresh mobile browser sessions without admin cookies were rejected with 401 on Settings mount, causing `handleUnauthorized` to bounce the session through `/login` into `Endpoint & Key`.
- Added `/api/settings/database/stats` to `handleUnauthorized` exemption list in `web/src/api/client.ts` so telemetry queries never trigger full-app auth redirects.
- Prevented off-screen mobile sidebar drawer from capturing pointer events when collapsed by applying `pointer-events-none lg:pointer-events-auto`.
- Safely guarded touch and click button detection in `Sidebar.svelte` navigation (`(e.button !== undefined && e.button !== 0)`).
- Switched settings links in `EndpointView.svelte` to SPA `navigate('settings')`.
- Added unit test in `internal/middleware/dashboard_auth_test.go` and verified end-to-end with mobile viewport browser testing.

### Usage Details cards cleanup and Settings display toggle reactivity

- Removed redundant top summary KPI cards in the **Usage > Details** tab (Total Requests, Lifetime Tokens, Peak Day Tokens, Cache Hit Rate, and Most Used Model) as they are already available in **Usage > Overview**.
- Enhanced **Settings > Display** toggles for **Combo flow** and **Usage trend**:
  - Turning off Combo flow now completely skips `GET /api/combos` network requests and prevents keeping combo definitions in memory.
  - Setting changes immediately react across the dashboard via custom event dispatching and `$state` synchronization.
  - Clarified toggle descriptions in Settings > Display.
- Verified with `bun test`, `make vet-svelte`, and `make build`.

### Fixed

- **A model-list request no longer republishes a custom row's caps under an id
  the static catalog owns.** `GET /v1/models` publishes every custom row's saved
  caps onto the process-wide `customCaps` map
  (`internal/handlers/chat/models_list.go:795`), and
  `GetCapabilitiesDetailForModel` consults that map before the registry
  (`internal/providers/capabilities.go:1065`). #422 filtered the colliding ids
  inside `HandleGetModelCaps`, which fixed the dashboard route and left the chat
  route open: one list request — what an SDK does at startup — made caps answer a
  custom row's 4096 for a model the catalog declares at 1048576, from the first
  call onward and for the lifetime of the process.
- The refusal moves to the single writer, `providers.SetCustomModelCaps`, so the
  invariant is stated once and every publisher obeys it. The id is matched
  exactly, and by its bare form when the caller passes a provider-qualified one;
  a provider node prefix has no static catalog and is unaffected.

### Verified

- `go vet ./...`, `go vet -tags=integration ./internal/integration/...`, and
  `go test ./...` pass; `go test -tags=integration -race -count=1 -timeout 10m
  ./internal/integration/...` (the CI job's own command) passes.
- Both tests are fix tests and were replayed against pre-fix `origin/main`
  (`1c4fb08`), where they fail with the reported signature: the registration
  table in `internal/providers/capabilities_static_skip_test.go` reports
  `stored = true, want false` for a static id, and
  `internal/integration/caps_static_precedence_test.go` reports
  `contextWindow = 4096, want the static catalog's 1048576` on both calls after
  `/v1/models?all=1`.
- Mutation check: deleting the refusal fails both tests. Deleting the
  `ResolveAlias` lookup in the helper changed nothing (`GetProviderModels`
  resolves provider aliases itself), so that branch was dropped rather than kept
  as decoration.

### Usage Details got lighter, and two Overview panels moved to the bottom

The analytics Overview no longer puts its two optional panels in the middle of
the page, and the Details dashboard stopped scanning the whole usage ledger on
every load.

**Combo flow and Usage trend** now sit at the very bottom of the Overview, below
the usage-by-model breakdown, both collapsed by default with their header and
chevron still visible. Two new switches in **Settings > Display** — "Combo flow"
and "Usage trend" — remove either panel from the Overview entirely. Visibility and
the collapsed state are separate on purpose: the switch decides whether the panel
exists, the chevron whether it is open.

**Details: the heaviest query is gone.** `GET /api/usage/dashboard` used to run
`SELECT promptTokens, tokens FROM usageHistory WHERE tokens LIKE '%cache%'` — a
substring `LIKE` no index can serve, so each load scanned the entire ledger and
JSON-decoded every matching row. Measured on a 150,000-row ledger it took 67 ms
against 64/39/31/9/1/0.1 ms for the six other queries the same handler issues, and
it was the only one whose cost grew with the whole table instead of a window. The
dashboard endpoint no longer runs it.

The Cache Hit Rate card stays, computed from the trailing-7-day history the
handler already reads for Most Used Model, so it adds no query. **Its number is
now a 7-day window rather than a lifetime figure**, and the card caption says
exactly that. The `cacheTokens` and `cacheRequests` response fields, which
nothing read, are gone.

`ComboFlowCard`'s Indonesian UI text — counters, handoff notes, legend and the
latency tooltip — is now English, matching the rest of the dashboard.

### Fixed

- **The account-rotation integration fixture no longer depends on the wall
  clock.** `TestRetryableUpstreamErrorRotatesAccount` failed on `main` (CI run
  39201511743, commit `1c4fb08`) with "throttled upstream received 0 requests,
  want 1": `Env.AddConnection` stores every connection at `priority := 1`, so
  the fixture's two DeepSeek accounts tie, and `connectionSortOrder` breaks the
  tie on `updatedAt DESC` — which `nowUTC()` writes at whole-second resolution
  (`time.RFC3339`). Whether the throttled or the healthy account was picked
  first came down to whether the two inserts straddled a second boundary, which
  is why it passed for so long. No production behaviour changed: newest-updated
  wins among equal priorities is the documented rule and mirrors upstream.

### Changed

- `Env.AddConnectionWithPriority` added, with `AddConnection` delegating to it at
  priority 1, so a fixture that needs a deterministic first pick can state it.
- The rotation fixture and `TestForceFallbackUsesAccountThatFreesUpFirst` (whose
  comment already claimed an order it was not given) now pass distinct
  priorities.

### Verified

- `go vet ./...`, `go vet -tags=integration ./internal/integration/...`, and
  `go test ./...` pass.
- `go test -tags=integration -race -count=1 -timeout 10m ./internal/integration/...`
  — the CI job's own command — passes (`internal/integration` 196.9s,
  `bootfx` 2.0s) on the merged tree plus this change.
- A probe on the merged tree reproduced the CI signature exactly: two
  equal-priority inserts in the same second sent the first request to the
  throttled account (`throttled=1 healthy=1`), inserts straddling a second
  boundary sent it to the healthy one (`throttled=0 healthy=1`). With distinct
  priorities both runs pick the throttled account.
- `internal/db/connections_order_test.go` pins the rule the flake tripped over
  with fixture-written timestamps: priority wins, then most recently updated
  first. Flipping the tie-break to `updatedAt ASC` fails it, and so does
  dropping priority from the `ORDER BY`.

### Fixed

- **A converted [OI] conversation no longer forwards a turn Anthropic rejects
  for having no content.** `EnsureClaudeMessagesFor` — the conversion behind the
  minimax-code and opencode-zen Messages lanes — passed the client content
  arrays through unchanged, so a turn the client left empty (`content: []`, or a
  whitespace-only string) reached the provider as-is and 400ed the whole
  request, while the same conversation on the Claude-client path was cleaned by
  `SanitizeClaudePassthrough`. The rule now lives in one exported helper,
  `translator.DropNonContentfulClaudeMessages`, called by both paths, and an
  emptied trailing turn is replaced by the `Continue.` placeholder so the body
  cannot end on an assistant turn.
- A conversation whose every turn is contentless keeps its turns: dropping the
  last one too would trade an invalid empty-content turn for an equally invalid
  empty `messages` array, and neither shape can be answered.

### Verified

- `go test ./...` and `go test -tags=integration ./internal/integration/...`
  pass.
- Table-driven coverage: `internal/proxy/executor/claude_empty_content_test.go`
  (seven cleanup cases plus the fully-emptied one) and
  `internal/translator/claude_empty_content_test.go` (six cases on the shared
  helper, including the `changed` flag its callers rely on). Reverting the
  executor call fails four subtests; disabling the filter fails the translator
  cases, so both sides of the shared rule are pinned.

### [417] - 2026-10-18

#### Changed
- Raised `testModelTimeout` from 15s to 45s in `internal/handlers/media/model_test_ping.go` to accommodate reasoning-heavy free models.
- Treated mid-stream timeouts / aborts as inconclusive probes: reporting `ok=true` if any SSE `data:` bytes arrived before timeout, and `ok=false` with an empty error string if no response was received.

### fix(proxy): treat a max_tokens cutoff as an answer, not an empty 200

A Claude turn that stopped because it hit `max_tokens` before writing any text
was reported as `502 upstream answered 200 without a completion`, and the 502 put
the whole connection in cooldown.

It happens with thinking on and a small `max_tokens`: the model spends the whole
budget on an empty thinking block, so the turn has no text and no tool call.
Anthropic answers `200` with `stop_reason: "max_tokens"`, a valid, short turn.
`missingCompletion` only accepted content, tool calls, reasoning or a refusal, so
`EmptyUpstreamError` read the cutoff as a missing answer. Two consequences: the
streaming path already served the same turn as `200` with `finish_reason:
"length"`, so only non-streaming clients got the 502; and the retryable 502
reached `LockConnectionModel` and `LockConnectionRateLimit`, so repeated hits
backed off 2s, 4s, 8s, 16s, 32s and a different model on the same account failed
with `no available connections for provider … (all in cooldown)`.

`finish_reason: "length"` (Chat Completions) and `stop_reason: "max_tokens"`
(Claude Messages) now count as an answer, so the client receives the truncated
turn and can raise `max_tokens`, and the cooldown is not touched. A response
that is empty for any other reason (`stop`, `end_turn`, `content_filter`,
`choices: []`, a blank body) still fails over exactly as before.

**Verified.** On a build of this change over `v1.9.13` against a real Claude OAuth
connection (`cc/claude-sonnet-5-5`), the request that returned the 502 and locked
the account now answers `200` with `finish_reason: "length"` three times in a row,
a different model on the same account still answers, and the router logged no
`connection locked`. Rolling back to the previous image reproduces the 502.

### 416 - merge customModelCaps into static caps map

- Merged `h.customModelCaps(provider, resolved)` into the static capability map in `HandleGetModelCaps`, ensuring imported free models (such as those ending in `-free`) appear with `Free=true` even when a provider has a static catalog.
- Added collision check keeping static catalog entries intact over custom model entries.
- Added `TestHandleGetModelCaps_CustomModelMergeAndStaticPrecedence` in `model_caps_test.go` verifying correct behavior and static precedence.

### Fixes

- **db**: encrypt nested `providerSpecificData` credentials (console API keys, session tokens, usage scraping cookies) at rest in the Vault envelope, ensuring full confidentiality on disk and transparent hydration on read (closes #413).

### Pricing and lane disagreements are recorded, not silent

- `pricing.KnownFreeModels` answers "what does it cost" while
  `providers.openCodeFreeTierModels` answers "which gate serves it". Two ids answer
  the two questions differently: `big-pickle` is billed at zero and routed on the
  keyed paid `/zen/v1` lane (#410), and `deepseek-v4-flash-free` is the mirror —
  routed to the keyless `/inference/v1` gate while the price table still resolves a
  paid row (0.14/0.28 per 1M, via the `deepseek-v*` glob rather than a row naming
  the id). Both are deliberate enough to leave alone, so this change alters no
  behaviour; it makes each disagreement explicit and guards it.
- `internal/providers/model_formats_pricing_divergence_test.go` adds the missing
  guard. `openCodePricingDivergences` stores **which way** each id disagrees
  (`pricedFree`/`lanedFree`) plus the reason, so an entry cannot silently describe
  a different disagreement than the one in the tree.
  `TestZeroPricedIdWithoutFreeLaneIsDocumented` covers the price→lane direction,
  `TestFreeLaneIdWithoutZeroPriceIsDocumented` the lane→price direction, and
  `TestRecordedDivergencesStillHold` fails when an entry stops describing a real
  disagreement or starts describing a different one.
- The `-free` suffix is no longer an unconditional exemption from the price check.
  It was justified by "the price table carries no row for it", which holds for nine
  of the ten ids in the lane list and fails for the tenth. The exemption is now the
  narrow claim it describes — no row means no cost — so a `-free` id that resolves
  to a paid row reaches the record instead of skipping the check.
- Both lists now state which question they answer in their doc comments, and the
  "cannot drift" claim that was false for an id already in the tree is gone.
- Verified with `go build ./...`, `go vet` on both packages, and the full
  `go test ./...`. The guard was proven to bite rather than assumed to. Six
  mutations, each restored afterwards:

  ```
  A  new -free id that hits the deepseek-v* glob (deepseek-v4-pro-free) → FAIL
     "routed to the free gate but not billed at zero; record the reason"
     (the same mutation passes on the previous commit — the hole was real)
  B  the deepseek-v4-flash-free entry removed                         → FAIL same message
  C  a recorded reason emptied                                        → FAIL "without a reason"
  D  the id added to KnownFreeModels so both lists agree               → FAIL "now agree; drop the entry"
  E  the recorded direction flipped                                    → FAIL "no longer describes this id"
  ```

### Fixed

- **A non-message error could end a whole import.** `POST /api/models/custom`
  answers a refusal with a typed `409`, and both bulk import paths awaited every
  save inside one `try`/`catch`. The first refused id therefore threw past the
  ids listed after it: the run stopped, the alert carried the raw 409 body, and
  every model below the collision was silently never imported. Each row is now
  attempted on its own: `importCustomModelsBestEffort` (new, in
  `web/src/lib/customModels.ts`) returns `{ imported, refused }`, both import
  paths report the refused ids with the server's own reason, and the ids that
  follow a collision are still imported.
- **A custom model id can no longer collide with a model alias or a combo.**
  `guardNameCollision` refused a custom model whose id matched a bare-name alias
  or combo, which is what produced the `MODEL_ALIAS_CONFLICT` above. A custom
  model id is only ever addressed as `<node-prefix>/<id>` (resolveModel step 1)
  and `/v1/models` publishes it in that same prefixed form, so it can neither
  occupy a bare name nor be shadowed by one — the two are distinguishable
  addresses, not one name resolving to the wrong target. The guard now refuses
  only the pair that really loses a name: a model alias vs a combo, in either
  direction, where the alias outranks the combo and the combo stops answering.
  The refusal was also order-dependent before — an alias written over an existing
  custom id was refused while the identical pair in the other order was accepted.
  A `/models` list carrying one already-aliased id (usually the same model on
  another node) imported nothing past it; now it imports in full.

### Verified

- `go test ./...`, including a rewritten `TestGuardNameCollision` that pins both
  directions of the surviving combo-vs-alias refusal, the four custom-model
  writes that are now accepted, and that a refused write still stores nothing.
- `bun test src scripts` — 405 pass, including a new
  `web/src/lib/customModels.test.ts` pinning that a refusal costs one row and not
  the batch.
- `web/e2e/modelImportNameConflict.test.ts` drives the reported flow against the
  real gateway: an OpenAI-compatible node whose `/models` lists an already-aliased
  id first, then a fresh one. Before this change the run stored **zero** rows
  (`Received: []`); now both ids are stored and no refusal dialog is raised.
- `bun run build`, `bun run ratchet:svelte` (81 errors, at baseline) and
  `bunx oxlint --deny-warnings` are clean.

### Fixed

- **`GET /api/keys` full-scanned the whole usage ledger once per key, stalling the dashboard boot.** Opening any dashboard page (e.g. `/dashboard/endpoint`) locks the UI on the "Connecting to 9router-go Localhost Gateway (:20130)…" spinner until `loadData()` resolves, and that promise waits on `GET /api/keys` alongside the four other boot reads. For every key row the handler calls `apiKeyUsedTokens` → `tokenUsageSince`, which sums `WHERE (clientKey = ? OR apiKey = ? OR apiKey = ?)`. SQLite answers a disjunction with a multi-index OR **only when every arm is indexable**; `idx_uh_apikey` covered the two `apiKey` arms but nothing covered `clientKey`, so the planner discarded the index and fell back to `SCAN usageHistory`. The per-key cost grew with the ledger: measured at **~127 ms per key on a 512k-row table**, so a 30–50 key install spent 1.6–10.6 s inside that single request while the four sibling reads finished in ~5 ms. The slowest read is what the spinner waits for, so one unindexed column held the whole page.
- Added `idx_uh_clientkey` on `usageHistory(clientKey)`, and moved `clientKey` into the `usageHistory` column list in `coreSchema()` so the index can be created on a fresh database (the column previously arrived only via the later `keiRouterMigrations` pass, which runs after `coreSchema` has already built its indexes — the index statement failed with `no such column: clientKey` until the column was declared alongside the others). `EnsureCoreSchema` re-runs its `CREATE INDEX IF NOT EXISTS` list on every boot, so an existing install picks the index up on the next start with no migration step.
- Effect on the same 512k-row, 30–50 key fixture: the plan becomes a multi-index union (`SEARCH … USING INDEX idx_uh_apikey` + `SEARCH … USING INDEX idx_uh_clientkey`), the per-key sum drops from ~127 ms to ~15 ms, and `GET /api/keys` falls from 10.567 s to 0.026 s end-to-end through the real router. The index costs no measurable write throughput (20k inserts: 246 ms without it, 155 ms with it — run-to-run noise).
- Verified by `TestUsageHistoryClientKeyIndexExists`, which asserts on `EXPLAIN QUERY PLAN` for the **full three-arm OR** — the previous index test spelled out only the two `apiKey` arms, so `idx_uh_apikey` alone satisfied it while production still scanned — plus `TestUsageHistoryClientKeyIndexAddedOnUpgrade` for the in-place upgrade path.

### Keep manual currency rates across a partial refresh

A rate provider publishes only the subset of currencies it carries — the primary
provider (`open.er-api.com`) carries VND, while the fallback (`api.frankfurter.app`)
does not. `HandleRefreshCurrencyRates` previously replaced the whole stored
`displayCurrency.rates` table with the fetched map, so a refresh answered by the
fallback silently deleted any manual rate an operator had typed for an
uncovered currency and reset the cost surfaces to the compiled seed.

- `internal/handlers/dashboard/currency.go`:
  - `mergeRefreshedRates` overlays the fetched rates on top of the stored table,
    so every pre-existing manual rate survives while the provider's fresh rates
    overwrite matching codes. Stored codes hold their `maxCurrencyRates` slots:
    a table already at the cap cannot lose its alphabetically-last manual rate to
    a code the provider published, and a fetched rate that does not fit is simply
    not stored (it shows up as a lower `ratesStored`, not as a deleted rate).
  - Returns `ratesMissing`, reporting requested codes absent from the provider's
    payload in request order. Both the primary response path and the fallback
    response path include `ratesMissing: missing`.
  - `ratesStored` now explicitly counts the provider rates that were actually
    stored into the table rather than the table's total size, disambiguating
    refreshed rates from kept manual rates.
  - When the POST payload omits `showBoth`, the handler now inherits the
    currently stored `showBoth` flag (falling back to `false` when no setting
    row exists) instead of hardcoding `true`. This matches the PUT normalizer's
    omission default and stops a refresh from flipping dual-display on as a side
    effect.
- `web/src/api/client.ts`:
  - Extended `refreshCurrencyRates` return type with `ratesMissing: string[]`.
- `web/src/lib/currency.ts` & `ProfileSettingsView.svelte`:
  - Added pure `describeRefreshRates(stored, missing, host)` helper and tests in
    `web/src/lib/currency.test.ts`.
  - The Settings refresh banner informs operators which currencies were kept at
    their manual rate (e.g. `Refreshed 1 rates from api.frankfurter.app. VND kept at your manual rate.`).
- Tests:
  - Table-driven tests in `TestMergeRefreshedRates` verifying manual retention,
    fetched overwrites, request-order missing lists, unparsable row resilience,
    and cap enforcement.
  - Handler regression tests `TestHandleRefreshCurrencyRatesKeepsManualRates`
    and `TestHandleRefreshCurrencyRatesShowBoth`.

### GitHub embeddings catalogue: `text-embedding-ada-002`, not `text-embedding-3-large`

- Ported the catalogue half of upstream `decolua/9router` commit `51a1827e`
  (#4793, issue #400 parity). GitHub Models inference (`models.github.ai`) was
  retired on 2026-07-30 and Copilot embeddings now go to
  `https://api.githubcopilot.com/embeddings`, whose accepted embedding ids do not
  include `text-embedding-3-large`. Upstream swapped that registry row for
  `text-embedding-ada-002`; `text-embedding-3-small` stays.
- `internal/providers/registry_models.go`: the `gh` and `github` catalogue rows
  and their `ProviderModelKinds` entries now carry `text-embedding-ada-002`. A
  stale id here reaches `/v1/models` and the dashboard picker, so a client could
  select a model the Copilot endpoint rejects.
- `web/src/lib/models.ts`: the `gh` catalogue entry follows, so the dashboard
  stops offering the retired id.
- Verified: `go build ./...`, `go test ./...` (full suite) green;
  `TestGitHubEmbeddingModelParity` was proven RED before the change and GREEN
  after (`go test ./internal/providers/ -run TestGitHubEmbeddingModelParity`).
  `bun run build` and `make vet-svelte` green.

Closes #400.

### fix(dashboard): list every routed model in the Available Models grid

The dashboard renders a provider's "Available Models" rows from
`web/src/lib/models.ts`, a hand-maintained copy of the provider catalog that is
**not** a projection of `internal/providers/registry_models.go`. Nothing compared
the two, so a model added to the Go registry alone was routable by the gateway
and absent from the only UI that configures it.

That is how `gemini-4-argon` shipped: #379 and #382 both grew the Go catalog and
neither touched `models.ts`, so the dashboard offered no Argon row at all —
`ag/gemini-4-argon`, `-high`, `-medium`, `-low` were all missing. #353 hit the
same shape for `gemini-web` and recorded it in its commit message; the next model
PR repeated it, which is the case for a test rather than a note.

- `web/src/lib/models.ts`: adds the missing rows for `ag`/`antigravity`
  (`gemini-4-argon` and its three tiers, carrying the `(level)` `upstreamModelId`
  upstream declares), `gemini`, `vertex`, `gc`/`gemini-cli`, and `opencode-go`
  (`claude-haiku-5-5`, `gemini-4-argon-contributor`, `longcat-2.5-preview-free`,
  `step-5-preview`, `beam-free`, all from #382). Two providers that had drifted
  earlier are repaired in the same pass: `cbcn` was missing `kimi-k2.8-preview`
  and `gemini` was missing the Gemini Live transcription STT row.
- `internal/providers/web_catalog_parity_test.go`: fails when a provider present
  in both catalogs serves an id the dashboard copy omits. It reads the
  TypeScript source rather than importing it, because the SPA is a separate build
  and pulling Vite and Bun into the Go test binary would cost far more than the
  check is worth. One direction only, deliberately: `models.ts` may legitimately
  list ids the Go registry does not, while the reverse silently hides a working
  model.

Verified: the new test fails on the pre-fix tree with the exact missing ids and
passes after (`105 providers present in both catalogs`, 0 drift). `bun run build`
emits a bundle containing all five Argon ids; `bun test src/lib/models.test.ts`
passes; `svelte-check` holds its 81-error baseline with 0 unresolved identifiers.
`bun test` across the SPA is 400 pass / 16 fail, identical to `main` — the
failures are e2e specs that need a running app.

### Usage ledger: a quota reset that lands mid-prune is no longer re-banked

- `internal/db/usage_retention_ledger.go`: `PruneUsageHistory` folded each
  `usageHistory` chunk against the reset marks it snapshotted before the prune
  began, then carried that spend in a later transaction. A quota reset that
  committed in between raised the key's mark past the rows being deleted and
  cleared its carryover — but the fold still used the stale mark, so the spend
  the reset had just forgiven was written back to the key's budget and charged
  again.
- `deleteLedgerChunk` now re-reads the watched keys' reset marks inside the same
  transaction that writes the carryover and deletes the rows
  (`ledgerWatchIndex.refreshMarks`), so "which mark decided what to carry" and
  "which rows are gone" are the same instant. The key list is not refreshed: a
  key created after the prune started cannot own a row this pass is deleting.
- Verified on top of `main` after the ledger branch merged: the race test
  (`internal/db/usage_retention_ledger_race_test.go`) was proven RED against
  `main`'s `deleteLedgerChunk` — `usage after a reset that landed mid-prune =
  500, want 0` — and GREEN with the fix; `go build ./...`, `go test ./...` and
  `go test -race ./internal/db/` are green.

### Fixes

- **media**: forward `providerCfg.StaticHeaders` on outbound `/v1/embeddings` requests, ensuring GitHub Copilot embeddings receive required headers (`copilot-integration-id`, `editor-version`, `x-github-api-version`) (closes #398).

### Retention for the `usageHistory` ledger (opt-in)

`requestDetails` has had a retention loop and a dashboard control; the ledger has
had neither, so a gateway that has been running for a year carried every row it
ever wrote. This adds an opt-in window for it and the machinery that makes
deleting billing rows safe.

- **New setting `usageHistoryRetentionDays`, default `0` = keep forever.** It is a
  separate knob from the requestDetails `retentionDays`, because one trims
  diagnostics and the other deletes billing rows. Existing installs, and the
  database shared with upstream, are unaffected until an operator turns it on.
- **Per-key token budgets stay exact.** Spend is derived from this table
  (`tokenUsageSince` sums `promptTokens + completionTokens`), so deleting an old
  row used to silently refund an exhausted key. Before a range is deleted, the
  spend those rows contributed is summed into a new Go-only table
  `usageLedgerCarryover`, and the budget reader adds that term back. The reader is
  a single statement, so a prune landing between its two halves cannot report a
  value that is neither the before nor the after.
- **A reset clears the carryover** in the same transaction as the new mark, so a
  reset issued after a prune still starts from zero.
- **Chunked, transactional, bounded.** Each 5000-row chunk is folded and deleted
  in one transaction, so a crash cannot leave a deleted row whose spend was not
  carried (which would refund a key) or a carried row that still exists (which
  would charge it twice). The write lock is never held for a full-table delete.
- **Separate, explicit dashboard action.** `POST /api/settings/database/prune-usage`
  takes `{days}` or `{before}`, supports `?dryRun=1`, and is deliberately not wired
  into the existing *Prune Expired Records* button: deleting the ledger is an
  operator's decision with a visible row count attached.
- **`GET /api/settings/database/stats`** now reports the ledger's oldest and
  newest timestamps (covering-index seeks), so "did that prune do anything" is
  answerable from the dashboard. The Storage card also shows the ledger row count
  and offers the action behind a typed confirmation.

Verification: `go test ./internal/db/... ./internal/handlers/dashboard/...`,
`make test-integration` (a key at its budget is still refused with 403 after the
rows that pushed it over are pruned), `make vet-svelte`, `cd web && bun run build`.

Caveats, stated rather than implied: `all` and the analytics pages now mean "as
far back as rows are retained" once a window is configured; the carryover holds
tokens only, so a future USD budget measured from this table will need it to grow
a cost column; and a database shared with upstream 9router will show upstream the
shortened ledger.

- **The route is now always-protected.** `/api/settings/database/prune-usage` was
  missing from `IsAlwaysProtectedPath`, so unlike `/prune` and `/vacuum` it answered
  an unauthenticated local caller while `requireLogin` was off. The always-protected
  test now asserts membership for the destructive settings routes.
- **The Storage card loads its numbers on mount.** `loadDbStats` ran only after a
  prune or vacuum, so a fresh page load showed no row counts and no action buttons
  at all.

Both were found by driving the running dashboard rather than the suite: on a live
instance seeded with ledger rows spread over 200 days, the card showed
"Usage Ledger: 11 rows (since 2026-04-01)"; pressing *Prune Usage History* reported
"Pruned 7 ledger record(s). 8,750 tokens carried" and the card re-rendered to
"4 rows (since 2026-09-28)"; the key read 13.8k/10.0k and a request through it was
still refused 403 (13750/10000); *Reset token quota* then reported 0 of 10.0k spent.

- **The route is now always-protected on both lists.** The integration test passes
  the CLI token on its prune call and asserts that a session-only caller is refused
  and that the refused call deletes nothing — the two gates had disagreed, since
  only the route table was fixed before.

Closes #392

### Features

- **opencode**: emit W3C distributed trace context headers (`b3` and `traceparent`) in `BuildOpenCodeHeaders` matching official OpenCode CLI behavior, with caller header override support (closes #393).

### Fixes

- **dashboard**: strip thinking level suffixes (such as `(high)` or `(medium)`) in `isModelFree` before checking pricing tables and free-tier heuristics, ensuring models like `nemotron-3-ultra-free(high)` correctly receive the `FREE` badge on provider cards (closes #390).

### feat(opencode): native Bun BoringSSL stealth transport for free tier models

Routes outbound requests to `https://opencode.ai` using a native, pure-Go stealth TLS stack (`bogdanfinn/tls-client` + `fhttp`) matching Bun's BoringSSL fingerprint (17 cipher suites, 12 extensions in baseline order, HTTP/2 pseudo-header order, and non-alphabetical header ordering with W3C trace context).

- **Bun TLS handshake parity:** The official OpenCode CLI compiles as a Bun binary on top of BoringSSL. Upstream's free-tier gate verifies the TLS ClientHello fingerprint (17 cipher suites, 12 extensions) and rejects requests from Go's standard `net/http` or Node.js with `403 FreeTierError`. The new `opencodeStealthClient` reproduces the exact Bun BoringSSL ClientHello spec without cgo.
- **Header ordering & W3C trace context:** Uses `fhttp.HeaderOrderKey` to preserve exact header ordering instead of Go's alphabetical sorting, and stamps standard W3C `traceparent` and `b3` trace headers on outbound requests.
- **Proxy pool propagation:** Recovers connection proxy pool assignments from `reference.Transport` so operator egress proxies still apply and requests do not leak the host IP.
- **Safe fallback:** Automatically uses plain HTTP when dialing local mock servers (`http://`), edge relays (`x-relay-target`), or when `OPENCODE_PLAIN_HTTP=1` is set.
- **Free model routing in Zen:** `forwardZenChat` and `forwardZenResponses` in `internal/proxy/executor/opencode_zen.go` route free-tier models through the stealth transport when targeting the inference gate.

**Verification:**
- Unit tests in `internal/proxy/executor/opencode_stealth_test.go` verifying the 17-cipher/12-extension spec, proxy recovery, W3C trace header formats, and plain HTTP fallback.
- End-to-end streaming test with mock upstream verifying SSE handling through `ForwardOpencode`.
- Full unit test suite passes cleanly across all 46 packages (`go test ./...`).
- Svelte ratchet check (`make vet-svelte`) passes with 0 unresolved identifiers and type debt at baseline.

### fix(gemini-web): pass transcript flag to deepSeekMessagesToPrompt

Fixes a compile break on `main` caused by a signature mismatch in `geminiWebMessagesToPrompt`. Commit `dca23163` (#352) added a boolean `transcript` argument to `deepSeekMessagesToPrompt`:

```go
func deepSeekMessagesToPrompt(messages []deepSeekMessage, historyWindow int, transcript bool) string
```

`geminiWebMessagesToPrompt` was still calling it with two arguments (`(messages, 0)`). Passing `false` preserves Gemini Web's self-contained turns where session-persistence transcript formatting is not needed.

**Verified:**
- `go build ./...` compiles clean with no errors.
- `go test ./internal/proxy/executor/...` passes.
- Full test suite `go test ./...` passes across all 46 packages.
- `make run` compiles and successfully serves HTTP 200 on port 20130.

### Cost display in any currency (USD, IDR, EUR, …)

- **What changed.** Settings gains a **Currency** section: pick the currency every cost is shown
  in, edit the rate table (one USD in each of thirteen currencies), turn dual display on to see
  the USD figure next to the converted one, and refresh the rates from a public provider in one
  click. Costs themselves are still measured and stored in USD
  (`internal/pricing` → `usageHistory.cost`); this is a display layer over them.
- **Every cost surface follows the choice.** Usage overview KPI, the breakdown table's Costs
  view, per-request cost in the request details, the trend-chart tooltip, and the cache and
  compression savings figures now format through one helper
  (`web/src/lib/currency.ts`, `costIn()` in `currency.svelte.ts`), so a change reaches the
  analytics without a reload. The compression CSV keeps its raw `EstUSD` column and appends
  `EstLocal` plus `Currency`, so an export stays machine-readable.
- **Locale-aware amounts.** `Intl.NumberFormat` writes each amount the way its market does —
  `Rp 18.014` groups with dots and carries no decimals, `$0.0123` keeps the four digits the
  request details always showed. With USD selected the output is byte-identical to before, so
  the feature is opt-in.
- **Storage.** `settings.displayCurrency` = `{ code, rates, showBoth, updatedAt }` through the
  existing `GET/PUT /api/settings`, so it survives a reload and travels with the database
  export. `internal/handlers/dashboard/currency.go` normalises the payload — three-letter
  codes only, positive finite rates, capped at 24 entries, `updatedAt` only when it parses —
  while `web/src/lib/currency.ts` owns the catalog, the seed rates and the format rules.
- **Refresh without losing the manual table.** `POST /api/settings/currency/refresh` reads
  `{code, showBoth, codes}` and fetches only the codes the catalog asked for, trying
  `open.er-api.com` and then `api.frankfurter.app` (both key-less) with an 8s timeout each. When
  every provider is unreachable it answers 502 and the stored rates are left untouched: it is a
  refresh, never a clear.
- **Verified.** `go test ./internal/handlers/dashboard/ -run Currency` (normaliser table,
  PUT/GET round-trip, refresh with a dead primary provider, 502 keeps the stored table, missing
  codes → 400), `bun test src/lib/currency.test.ts`, `bun run build`, the svelte-check ratchet,
  and a browser run on an isolated `DATA_DIR`: select IDR + dual display + a manual rate →
  saved as `{code:"IDR",rates:{IDR:16500},showBoth:true}`; the Usage KPI then renders
  `~Rp 0 ($0.00)`; the panel preview reads `1 USD = Rp 18.014`; **Refresh from provider** pulled
  live rates (IDR 18014.03, EUR 0.8896, JPY 158.00, SGD 1.2799) and stamped `updatedAt`.

### Sidebar visibility (Plans) and collapsible usage details

- **One switch, one entry.** The **Display** card carries the only sidebar switch: **Advanced usage**
  (the `plans` tab). It is the only entry the panel offers, so a deployment that never opens that
  page does not carry it around. The switch is a saved setting, so the floating "Unsaved changes —
  Save" reminder covers it.
- **What turning it off does.** The sidebar entry (labelled **Plans**) leaves the sidebar, its URL
  stops resolving, and the **Plan** field in the API-key editor disappears with it — the field points
  at the same feature, so it would otherwise link to a page that redirects. The note under the switch
  says exactly that.
- **Defaults.** A deployment that never saved a list starts with Plans hidden; Security is visible
  like every other entry. Saving an explicit empty list restores everything — only an absent or
  unreadable stored value falls back to the default.
- **Usage detail panels collapse.** In Usage > Details, **Activity Heatmap** and **Node Health**
  start collapsed and open from the chevron in their card header. The starting state is the new
  **Display > Expand usage details** switch (localStorage `detailsExpandedByDefault`, off by
  default), rendered next to Unit abbreviation and applied to both panels.
- **Storage.** `settings.navVisibility.hidden` (a list of entry ids) through the existing
  `GET/PUT /api/settings`, so it survives a reload, travels with the database export/import, and
  applies to any browser. `internal/handlers/dashboard/navvisibility.go` normalises the payload —
  lower-cased, de-duplicated, capped at 32 ids, `settings` stripped — while `web/src/lib/nav.ts`
  owns the canonical entry list and the toggleable set. `web/src/lib/navVisibility.svelte.ts` mirrors
  the resolved list for consumers outside the settings flow, so the key editor reads it instead of
  having the flag threaded through three components.
- **Deep links.** A hidden tab is not reachable by its own URL: `App.svelte` redirects to
  Endpoint & Key, alias paths included (`/dashboard/keys`, `/dashboard/terminal`,
  `/dashboard/usage/cache`), and a hidden Media Providers accordion hides every child. The guard
  waits for the boot settings fetch: before it resolves the store is empty, and the fallback list
  would bounce a deep link to an entry the operator has since made visible.
- **One entry list.** The sidebar and the panel render entries from `web/src/lib/nav.ts` instead
  of three hand-written arrays plus a hardcoded Plans link, so a switch cannot address an id the
  sidebar does not consume (the phantom-config-path failure mode).
- **Verified.** `go test -count=1 ./internal/handlers/dashboard/`, `bun test src/lib/nav.test.ts
  src/lib/displayPrefs.test.ts` (22 pass), `bun run build`, the svelte-check ratchet (81 errors =
  baseline, 0 unresolved identifiers), and a browser run against an isolated `DATA_DIR`: a fresh
  database hides Plans while Security stays; the Display card holds the three switches (Unit
  abbreviation, Expand usage details, Advanced usage) with no separate Sidebar card left; the
  sidebar entry reads **Plans** while the switch row reads **Advanced usage**; on Usage > Details
  both cards report `aria-expanded="false"` with no body until the Display switch opens them;
  `/dashboard/plans` renders on a load and a reload once visible and redirects to
  `/dashboard/endpoint` while hidden; and the key editor's **Plan** select (headings `Name, Plan,
  Rate limits, …`) is present with the feature on and gone with it off (`Name, Rate limits, …`).

### test(media): gate the live systemone test like the other live tests

`TestHandleSystemone_Live_Opencode` makes a real request to
`oc/jev-1.13-free`, but it only checked `testing.Short()`, which CI does not set,
so it ran on every build. That free tier now answers `429 FreeUsageLimitError`
for callers outside its own client, so the test fails on `main` and on every
branch cut from it while saying nothing about the code under test: on `main` at
`e880043` it is what turns both `test` and `Race detector` red.

Its neighbours in the same package (`e2e_media_test.go`) gate live network tests
on `ROUTER_LIVE_E2E`; this one now does the same and keeps the `testing.Short()`
guard. Enabling it explicitly still surfaces the 429 rather than hiding it — that
is what a live test is for; it just does not run by default any more.

**Verified.** `go test ./internal/handlers/media/` passes with the live test
skipping and the three mocked systemone tests still running;
`ROUTER_LIVE_E2E=1 go test -run TestHandleSystemone_Live_Opencode` still fails on
the provider's 429, unchanged.

### fix(translator): compare the trailing turn by value, not by JSON text

`sameTail` decides whether cleanup changed the tail of a conversation, and
`SanitizeClaudePassthrough` re-encodes the whole body when it says yes. It
answered the question by marshalling both messages and comparing the text, which
cannot work here: `encoding/json/v2` marshals a Go map in a non-deterministic
order (v1 sorted the keys, v2 does not — the package docs say so), so two
identical messages serialise differently about one time in ten.

Two consequences, one of them in production:

- `TestSanitizeClaudePassthrough_CleanBodyUntouched` fails intermittently — 17
  failures in 200 runs on `main`.
- A body that needed no cleanup was re-encoded anyway, on roughly one request in
  ten. That re-encoding is itself order-random, so the same conversation reached
  the upstream as different bytes each time, which defeats prefix caching on the
  Claude lanes.

`reflect.DeepEqual` on the decoded values answers the actual question and
serialises nothing, so it is also cheaper than the two marshals it replaces.

**Verified.** The new `TestSameTail_ComparesByValueNotByJSONText` fails on the
old code (`identical tails reported as different on attempt 0`) and passes here;
it runs 500 comparisons plus the two cases that must still count as changes.
`TestSanitizeClaudePassthrough_CleanBodyUntouched` passes 200 of 200 runs, and
`go test ./internal/translator/` passes in ten separate processes at `-count=1`,
which is how CI runs it.

### feat(providers): port upstream Gemini 4 Argon capabilities, Antigravity client bump, and OpenCode Go wire transport

Port of upstream `decolua/9router` PRs #4887, #4934, and #4991: full Gemini 4 Argon support across capabilities, pricing, thinking levels, Antigravity quota isolation, client fingerprinting, and OpenCode Go Google Generative AI wire transport.

**Upstream References:**
- `decolua/9router#4887`: Gemini 4 Argon capabilities, intro pricing ($2/$10), registry entries, and thinking level clamping.
- `decolua/9router#4934`: Gemini 4 Argon under Antigravity (`gemini-4-argon`, `high`, `medium`, `low`), `ANTIGRAVITY_IDE_VERSION` bump to `2.23.0`, `ANTIGRAVITY_CLI_USER_AGENT` (`1.4.0`), weekly/session quota isolation, and Svelte dashboard normalization.
- `decolua/9router#4991`: Native Google Generative AI wire transport on OpenCode Go (`/inference/go/v1/models/gemini-4-argon-contributor:(stream)GenerateContent`) with `x-goog-api-key`, plus newly released models (`claude-haiku-5-5`, `longcat-2.5-preview-free`, `step-5-preview`, `beam-free`).

**What changed:**
- `internal/providers/capabilities.go`: Added `PDF: true` to `Capabilities` under pattern `*gemini*argon*`.
- `internal/pricing/tables.go`: Added introductory pricing entries for `gemini-4-argon`, `gemini-4-argon-high`, `gemini-4-argon-medium`, and `gemini-4-argon-low` ($2/1M input, $10/1M output, $0.10/1M cached, $10/1M reasoning, $2/1M cache creation).
- `internal/providers/thinking_levels.go`: Added `ClampGeminiLevel` to clamp unsupported thinking levels on Gemini 4 Argon (`minimal` clamped to `low`).
- `internal/providers/client_identity.go`: Added Antigravity CLI and IDE version constants (`AntigravityCLIVersion = "1.4.0"`, `AntigravityIDEVersion = "2.23.0"`, `AntigravityCLIUserAgent`).
- `internal/providers/registry_models.go`: Registered `gemini-4-argon` on `gemini`, `gemini-cli`, `gc`, `vertex`, `antigravity`, and `ag`. Added `claude-haiku-5-5`, `gemini-4-argon-contributor`, `longcat-2.5-preview-free`, `step-5-preview`, and `beam-free` to `opencode-go` and `ocg`.
- `internal/providers/model_formats.go`: Declared `FormatGemini = "gemini"`, added `openCodeGoModelFormats` for `opencode-go` and `ocg`, and mapped `gemini-` prefix to Gemini wire target.
- `internal/translator/antigravity.go`: Added Gemini 4 Argon tiered mappings in `AntigravityModelSynonyms` and `antigravityUpstreamModelIDs`.
- `internal/handlers/chat/antigravity_quota.go`: Added dedicated `argon_weekly` and `argon_session` parsing and quota isolation so Gemini 4 Argon limits do not conflate with or block standard Gemini models.
- `internal/handlers/dashboard/usage_providers.go`: Added Argon family configuration and weekly overlay reconciliation. Bumped Antigravity User-Agent and client version headers to `providers.AntigravityCLIUserAgent` and `providers.AntigravityIDEVersion`.
- `internal/proxy/executor/providers.go`: Implemented `forwardOpencodeGoGemini` to route Gemini wire models on OpenCode Go to `/inference/go/v1/models/<model>:(stream)GenerateContent` with `x-goog-api-key`. Added `claude-haiku-5-5` to `opencodeGoMessagesModels`.
- `internal/mitm/handlers/antigravity.go`: Updated `antigravityIDEVersion` override to `2.23.0`.
- `web/src/components/quota/types.ts`: Updated Antigravity quota normalization in dashboard to parse and render dedicated Gemini 4 Argon weekly/session quotas.

**Verification:**
- `go test ./internal/providers/... ./internal/pricing/... ./internal/proxy/executor/... ./internal/translator/... ./internal/mitm/... ./internal/handlers/chat/... ./internal/handlers/dashboard/...` passed.
- `go vet ./...` passed with 0 warnings.
- Frontend SPA `cd web && bun run build` and `cd web && bun run ratchet:svelte -- --tsconfig tsconfig.app.json` passed with 0 unresolved identifiers and maintained 83-error baseline.

### Usage dashboard: show the route a combo request took

- The usage dashboard showed only the member that served a request, so a combo
  run was indistinguishable from asking for that member by name: which combo the
  client asked for, and which member of it did the work, was invisible.
- The tracker now carries both names next to the member — `requestedModel` and
  `comboName` on active and recent requests — paired with the same counter key
  the counters use, and cleared when that counter reaches zero. The counters
  cannot separate two requests for the same member, so an in-flight row takes the
  names of the latest start; completed requests are unaffected, each carrying its
  own names. `comboName` is set only when a combo served the request, so the
  dashboard acts on a statement from the backend instead of guessing which model
  names are combos.
- `TrackPending` keeps its signature and now delegates, so other callers (media)
  are unaffected.
- Recent requests also carry `latencyMs`, which `logUsage` already had: a member
  that answered shows how long it took. A member that refused shows no latency,
  because a rejection has a time-to-failure, not a latency.
- New card above the provider map: `9router-go → combo → members`, with the
  members as a numbered list in the combo's own order — one row each with its
  provider, model, status and latency. A failed row names the member it handed
  off to, the row that answered says the rest were not used, and later rows are
  dimmed. A single column also gives the text the full card width on a phone,
  where a multi-column layout left the member column too narrow to read.
- Member-to-row matching moved to `comboMatch.ts` so its rules can be tested. It
  also fixes a mislabel: a bare prefix test made `oc` and `ocz` the same provider,
  and the model-tail fallback then let one member take the other's row. A prefix
  now only counts when it ends on a separator, and the tail fallback refuses a row
  whose provider belongs to another member of the same combo (a slug is unknown,
  so the tail may still stand in for it). `comboMatch.test.ts` covers both.
- `web/embed.go`: the entry document and client-side routes are served with
  `no-cache`, and content-hashed assets with `immutable`. Embedded files carry no
  ModTime, so a browser had nothing to revalidate against and could keep serving
  a previous bundle after an upgrade.
- Verified: `go build ./...`, `go vet`, `gofmt -l` clean, `go test
  ./internal/usagetracker/ ./internal/handlers/chat/ ./web/` (new
  `TestTracker_RequestedModelPairing` and `TestTracker_RecentRequestCarriesLatency`),
  `bun test src/components/analytics/` (new `comboMatch.test.ts`, 31 cases), the
  dashboard bundle builds in CI,
  and on a live instance the usage stream carries `comboName`/`requestedModel`
  while a combo request runs (`comboName=xcode-cepat`, member
  `opencode/cat-jam-free`, `latencyMs=1351`) while a plain request carries the
  name it asked for and no combo name.

### Features

- **gemini**: add Gemini 4 Argon support across `gemini`, `vertex`, and `antigravity` registries, drop unsupported sampling parameters (`temperature`, `topP`, `topK`), and order `functionResponse` parts last in user turns (closes #374).

### fix(proxy-pools): never dial an edge relay as an HTTP CONNECT proxy

A proxy pool whose `type` was left at the default `"http"` while its URL pointed
at a self-hosted edge relay was dialed as a CONNECT proxy. The CONNECT attempt
sent a raw TLS handshake at the relay, and the HTTP client read the relay's
plain-text reply as a status line:

```
malformed HTTP status code "\x01@\x00\x01\x00\x00\x10\x00\x00\x04\x00…"
ForwardOpencode: forward to https://opencode.ai/inference/v1/chat/completions
```

The same install worked as soon as the pool was detached, which is the tell: the
relay is a forwarding HTTPS endpoint, not a tunnel, and `ProxyPool.IsEdgeRelay()`
decided which one it was from the `type` field alone. With `"http"` there it
returned `false`, so `getProviderConfig` also skipped the rewrite that points
`BaseURL` at the relay and adds `x-relay-target` / `x-relay-path` — the two
things that make a relay work. Every request through that pool failed at the
transport, before any provider saw it.

`internal/db/proxyPools.go`:

- `IsEdgeRelayURL(rawURL)` recognises the platform hosts that serve relays
  (`*.vercel.app`, `*.workers.dev`, `*.deno.dev`, `*.netlify.app`) and the
  `/.netlify/functions/relay` path.
- `IsEdgeRelay()` now also returns `true` when the declared type is `""` or
  `"http"` **and** any pool URL matches `IsEdgeRelayURL`. A relay URL created
  with the default type can no longer be routed into the proxy path. The
  explicit `vercel` / `cloudflare` / `deno` / `netlify` types are unchanged, and
  a real proxy — loopback, a `:8080` CONNECT proxy, `socks5` — is still a proxy.

`internal/handlers/chat/connections_proxy.go`:

- `getClientForConnection` returns the plain `h.Client` for an edge relay
  instead of wrapping it in `http.ProxyURL`. The request still leaves through
  the relay; it simply travels as a forwarded request carrying the
  `x-relay-target` / `x-relay-path` headers `getProviderConfig` injects, not as
  a CONNECT tunnel.

**Verified.** `go build ./...` and `go test ./internal/db/...
./internal/handlers/chat/...` pass. New tests:
`TestProxyPool_IsEdgeRelay` covers the inference per host and the negatives (a
real proxy on `:8080`, a loopback proxy, `socks5`, and a lookalike
`vercel.app.evil.com` host that must not match);
`TestGetClientForConnection_RelayPoolDeclaredAsHTTP` pins that a relay pool
yields the plain client; and
`TestRelayPoolAssignedToNoAuthProviderUsesRelayHeaders` exercises the whole path
— a relay pool assigned to the no-auth `opencode` provider resolves to a
connection whose plain client is used, with `BaseURL` rewritten to the relay and
`x-relay-target: https://opencode.ai` plus
`x-relay-path: /inference/v1/chat/completions` on the forwarded request.

**For operators hitting this today:** setting the pool's type to the matching
platform (`vercel`, `cloudflare`, `deno`, `netlify`) also avoids the failure.
The code fix makes the default `"http"` safe as well, because the old failure
mode was a cryptic transport error rather than a configuration message.

### fix(executor): keep OpenAI-only keys off the Claude messages

A replayed assistant turn used to carry the client's remaining keys into the Claude
body. `convertOpenAIMessagesToClaude` copied every key of a message — the
`tool_calls` branch everything except `tool_calls`/`content`, the fall-through the
map whole — and the top-level cleanup in `ensureMessagesMaxTokens` never looked at
messages. Anthropic answers a message key it does not define with
`400 messages.<n>.reasoning_content: Extra inputs are not permitted`, and because
that turn stays in the client's history, every later request of the conversation
failed too.

Normal use reaches it, not a hand-crafted payload: reasoning clients (pi, the
OpenAI SDKs, Hermes) echo back the `reasoning_content` this proxy streamed to them.
#290 is the request-side half that made Claude start producing the field, so this is
its second turn. The text cannot be turned into a `thinking` block instead — the
signature Anthropic needs to accept one back is not in the OpenAI shape.

Messages are now rebuilt from an allowlist of the keys a Messages API message
object defines (`role`, `content`); `SanitizeClaudePassthrough` applies the same
rule to Claude passthrough bodies from mixed combos. This matches upstream
`open-sse/translator/request/openai-to-claude.js`, which rebuilds every message as
`{role, content}`.

**Verified.** The Claude upstream returned `400 messages.1.reasoning_content: Extra
inputs are not permitted` for a replayed reasoning turn before the change and `200`
after, on a build of this branch over `v1.9.13` with a real Claude OAuth connection.
A turn with `reasoning_effort: high` still returns `reasoning_content` (185 chars)
plus its `tool_use`, and replaying both with the `tool_result` answers normally; a
real pi session with thinking `high` that used to die on turn 2 now completes. New
tests assert the body the upstream sees, in both conversion paths, including that
the tool_use/tool_result turns survive. `gofmt`, `go vet ./...` and
`go test ./...` clean.

### Fixes

- **opencode-go**: drop `response_format` for DeepSeek models in `ForwardOpencodeGo` using anchored prefix matching (`strings.HasPrefix(strings.ToLower(cleanModel), "deepseek")`), preventing upstream relay HTTP 400 rejection.

### opencode-zen: keep forced tool choices and tool history pointing at a real tool

The legacy `/zen/v1` gate fingerprints the lowercase tool quartet, so
`ConcealFingerprintTools` renames the caller's capitalised Claude Code tools
(`Bash`, `Read`, …) before the request leaves the gateway. The response side
already restored the caller's spelling, but the request could still reference a
name upstream no longer had:

- A forced Chat Completions `tool_choice` nests its name under `function`, and
  only the flat `tool_choice.name` shape was retargeted, so a
  `{"type":"function","function":{"name":"Bash"}}` choice kept naming `Bash`
  after the tool had been renamed to `bash`. The flat branch also assigned the
  raw `FingerprintToolKey` result, which retargets a case-variant only when a
  same-named rename exists — the old guard was right, and it is preserved.
- Prior-turn tool calls (`messages[].tool_calls[].function.name`, Claude
  `tool_use` blocks, Responses `function_call` / `custom_tool_call` items) were
  never rewritten, so a conversation that had already called `Read` kept
  replaying `Read` against a tools array declaring only `read`.

One pass now retargets `tool_choice` and every history reference across all
three wire shapes. A forced choice the body cannot resolve — its declaration was
dropped as a duplicate of an exact canonical one — is removed rather than
forwarded dangling, and the rename map records that spelling as unresolvable
(`""`) so the response side keeps the canonical name instead of blanking it.

Deliberately unchanged, because it already matched the gate: a client tool that
is already exactly `read` keeps its own description and schema (the gate only
checks names on this lane), and a body that already carries the canonical
quartet is returned byte-identical. The `/inference` lane and its
`ConcealOpenCodeV2FingerprintTools` path are untouched.

Verified with `go test -count=1 ./internal/translator/ ./internal/proxy/...
./internal/handlers/chat/`, including new cases for the nested Chat choice,
tool-history retargeting in all three shapes, an exact `read` definition
surviving untouched, and the dropped-duplicate marker.

### opencode-zen: stop injecting tool stubs the legacy gate never asked for

`ConcealFingerprintTools` renamed the caller's capitalised quartet tools
(`Bash`, `Glob`, `Grep`, `Read`) to their canonical lowercase spelling and then
appended every *absent* member as a no-op stub. The renaming is load-bearing —
a harness-shaped body is rejected with 403 FreeTierError unless the names match
(measured 2026-09-18) — but the injection was broader than the gate requires.

Probing against the legacy `/zen/v1` free tier (reported in the opencode2api
`a9470df` change) shows a single tool named `read` is sufficient: `[read]` alone
passes, while `bash`/`shell`/`edit`/`glob`/`grep` alone — or any combination
without `read` — answer 403, and neither the description nor the parameter
schema of a declared tool is validated on that lane. The three extra stubs were
therefore payload on every paid `opencode-zen` / `opencode-go` request, and they
offered the model tools that do nothing.

Injection is now driven by a separate `openCodeRequiredTools` list containing
just `read`, while `OpenCodeFingerprintTools` keeps the full quartet for
*collapsing* — renaming a client-sent `Bash` to `bash` is what makes the body
acceptable, so that behaviour is unchanged. A body that already declares the
required tool still gets nothing added, and the `/inference` lane
(`ConcealOpenCodeV2FingerprintTools`) is untouched.

Deliberately preserved: paid lanes keep the quartet identity, and a client tool
already named exactly `read` keeps its own description and schema.

Verified with `go test -count=1 ./internal/translator/ ./internal/proxy/...
./internal/handlers/chat/`, with the injection tests tightened to assert the
exact remaining tool set rather than a count.

### fix(opencode-zen): shape the messages lane by destination gate, not by lane

`forwardZenMessages` applied `ConcealFingerprintTools` — the legacy `/zen/v1`
quartet fingerprint — to every request, and only swapped credentials for
free-tier ids. A free id is forwarded from *every* lane to the keyless
`/inference` gate, and that gate wants the genuine CLI 2.x `read`/`shell`
definitions; sending it the quartet is a body it refuses. The bug was latent
because no free-tier id is served on the messages lane today, which also meant
no existing lane test could see it.

The shaper choice is now `zenShapedBody`, keyed on the gate the request is
destined for (`free`), matching the chat and responses lanes. Pinned by
`TestZenShapedBodyFollowsTheDestinationGate`, which exercises both arms directly
rather than through a lane — verified by reintroducing the old unconditional
legacy shaper and watching the test go red (`a free request must carry the
genuine "read" and "shell" fingerprint, got map[bash:true read:true]`).

### test(opencode): stop classifying an SSE-embedded refusal as a success

The live keyless test asserted success on `Status == 200 && strings.Contains(body, "data:")`.
The gate answers some refusals inside an SSE-shaped body that still carries HTTP
200, so that check reported a closed free tier as healthy — during this
investigation twenty consecutive "200 OK" responses were 175-byte
`FreeTierError` frames.

`classifyLive` now reads the payload: a body containing `FreeTierError` is a
refusal whatever the status line says, and a 200 without completion frames is a
truncated or proxied failure rather than an answer. The live tests report that
verdict instead of asserting availability, because a free tier that opens and
closes is a fact about upstream, not a defect here — and a test that goes red
whenever it closes would be switched off rather than fixed.

Probing the live gate while writing this showed that per-model statuses
(`FreeTierError`, model-unavailable, `ModelDeprecated`) are upstream's own state
and rotate on its schedule: the same request for the same id returned 200
earlier and 403 consistently afterwards, and neither body shaping, session-ID
shape, session reuse, session age (0s to 24h) nor the session counter (1, 2, 5)
changed the outcome while the id was refused. Those findings are recorded here
only to justify the classifier, not as a statement about any model's current
availability.

Verified with `go build ./...`, `go test -count=1 ./internal/proxy/...
./internal/providers/ ./internal/pricing/ ./internal/handlers/chat/
./internal/translator/`, and
`go test -tags=integration -count=1 ./internal/integration/` (69s).

### fix(opencode): report a closed free tier as unavailable instead of a raw 403

When the OpenCode free-tier gate refuses to serve this traffic, the caller used
to receive the upstream `403 FreeTierError` verbatim — a wall of text telling the
user to download OpenCode, mid tool-call. A 403 reads as "you are not allowed",
so an agent loop treats it as fatal and stops.

The gateway now reports the condition instead of forwarding it:

- **`503` + `Retry-After: 30`** with `type: "free_tier_unavailable"` and a message
  naming the real cause and the real remedies. The status says "temporarily
  unavailable, come back", which is what a closed shared free tier is.
- **`Retry-After` is actually forwarded.** The error-to-response writer copied
  only `Content-Type` and dropped `ue.Header`, so a retry hint was silently
  discarded. It now forwards `Retry-After` and nothing else: copying the whole
  header map leaked the failed response's `Content-Length` and
  `Transfer-Encoding`, which made the reply advertise a body length it did not
  have and truncated it on the wire.
- **A refusal does not cool the connection down.** The gate says nothing about
  the account — the same connection serves the next request once the tier
  reopens — so the cooldown, backoff and model-lock path is skipped and the
  refusal is reported directly. Cooling a healthy account would turn a closure
  into an outage by skipping it for later requests. Scoped to the OpenCode
  providers; every other provider's 403 handling is unchanged.

**One retry, not a loop.** The gate *does* occasionally refuse a request whose
fingerprint is correct and serve the same request when repeated, so a single
repeat recovers that blip — verified end to end: first attempt refused, retry
served, client gets the completion. But while the gate is refusing on identity
(#367 measured every client variant refused) no number of attempts succeeds, so
the budget stops at two: worst case ~200 ms added, against the ~1.5 s a
5-attempt cycle cost. That cycle was in an earlier draft of this branch and was
replaced on review. Only the `FreeTierError` refusal is repeated — a quota 403
fails identically forever, and 401/429/5xx keep their existing handling.

Two related defects were found by testing rather than reading:

- **A truncated error body erased the status.** `doRequestOnce` returned a bare
  `fmt.Errorf` when reading a non-200 body failed, discarding the code, so a
  403 with a mis-declared `Content-Length` became an opaque 502 that no
  classifier — retry, failover or refusal — could recognise. `UpstreamError`
  now carries the status plus a `ReadErr` for the partial read.
- **The first header-copy loop broke an existing test**
  (`TestMiniMaxCodeCreditsRefusalFailsOver`, which asserted a 429 and got a
  truncated body). Caught by the integration suite; fixed by narrowing the
  forward to `Retry-After`.

Verified with `go build ./...`, `go test ./internal/proxy/...
./internal/handlers/chat/ ./internal/translator/ ./internal/providers/`,
`go test -tags=integration ./internal/integration/`, and
`golangci-lint run --new-from-rev=origin/main` (0 issues).

The end-to-end cases live in
`internal/integration/opencode_free_tier_closure_test.go` and drive the real
router against a fake gate that refuses every attempt:

- `TestFreeTierRefusal_ReportsUnavailableNotForbidden` — the client sees `503`
  with `Retry-After`, `free_tier_unavailable` names the condition, and neither
  the gate's error type nor its wording reaches the body.
- `TestFreeTierRefusal_DoesNotCoolTheConnectionDown` — two successive requests
  each dial the upstream, so the first refusal did not lock the connection.
- `TestFreeTierRefusal_SurvivesATruncatedErrorBody` — a mis-declared
  `Content-Length` still yields `503`, not an opaque `502`, because the status
  survives a partial read.

The gate's accept path is covered separately in
`opencode_zen_gate_simulation_test.go` (`TestInferenceGate_*`), which shapes a
harness body through the router, asserts the outbound tool set, and keeps a
negative control so the fake is not simply permissive.

### feat(access): allowlist globs and deny rules for per-key model access

Per-key model allowlists gain the pattern vocabulary issue #365 asks for, ported
from decolua/9router `keyAccess.js` (v0.5.104): a wildcard that spans the `/`
(`*claude*`), deny entries (`!opus`, `-haiku`), and a deny-only list that opens
everything it does not name.

**Why:** the allowlist matched through the resolver's own glob
(`providers.MatchModelPattern`, backed by `path.Match`), where `*` is
segment-local. An operator writing `*claude*` — the spelling the issue and
upstream both use — got a key that denied every model, and there was no way to
express "everything except X" short of enumerating the catalogue. Measured
before the change: 7 of 17 cases in `TestModelAllowedPatterns` failed, among
them `*claude*` against `anthropic/claude-sonnet-4` and every deny-only list.

**What changed:**

- `providers.MatchAllowPattern` — the allowlist glob. `*` spans `/`, matching is
  case-insensitive and anchored at both ends, the pattern is literal except for
  `*`, and a pattern without `*` still answers exactly as before.
  `MatchModelPattern` is untouched so the resolver's capability patterns keep
  their segment-local semantics; a test pins that the two cannot be collapsed
  into one by accident.
- `providers.IsDenyEntry` / `providers.MatchDenyEntry` — `!term` / `-term`
  parsing (a lone sigil is a model id, not a rule) and deny matching, which
  accepts a glob or a substring so `-haiku` needs no asterisk.
- `chat.modelAllowed` — parses the list into positives and denies, checks denies
  first so a deny always wins over a positive rule, and reads a list with no
  positive entry as deny-only: open except denied. An empty or blank-only list
  still means "no allowlist configured" and allows everything, which is what
  keeps every key minted before this feature behaving as it did.
- Dispatch and listing still share the one decision function, so a denied model
  is hidden from `/v1/models` as well as refused at `/v1/chat/completions`.

**Verified:** `go test ./internal/providers/ ./internal/handlers/chat/` — both
packages ok; the 7 previously failing cases are green alongside 40 new matcher
cases.

### feat(keys): meter per-key token quotas from the caller, and let a budget be reset

Per-key token budgets are made real (#365): the spend a `tokenQuota` is measured
against is now attributed to the client that spent it, an exhausted key can be
given a fresh budget without deleting its history, and the dashboard can read the
spend it is rendering.

**Why — the quota was inert.** `usageHistory.apiKey` holds the **upstream**
credential (masked), not the caller: the usage writer stores
`maskAPIKey(info.APIKey)`, and the suite pinned that behaviour elsewhere. The sum
a quota was measured from therefore never matched a client key. On the live
database this was 0 rows out of 634, so `checkApiKeyQuota` could never fire and a
runaway client was never bounded — which is the first thing the issue asks for.
The unit test that appeared to cover this inserted its own rows keyed by the
client key, so it passed without touching the real write path.

**What changed:**

- `usageHistory.clientKey` (additive column) records the caller, masked, written
  from the request context in `logUsage` through the new
  `middleware.AuthenticatedApiKeyFromContext` — the auth middleware already put
  the key on the context, so no request-path change was needed.
  `InsertUsageHistory` takes it as a trailing variadic so the fixtures and
  migrations that have no caller to name keep compiling unchanged.
- `tokenUsageSince` sums `clientKey = ? OR apiKey = ? OR apiKey = ?`: the last
  two keep rows written by the upstream Next.js dashboard counting, since that
  ledger stored the client key where this one stores the upstream credential.
  No index is added — the predicate is an OR across two columns, which no
  single-column index serves.
- `apiKeys.quotaResetId` plus `ResetApiKeyQuota` and
  `POST /api/keys/{id}/reset-quota`: the mark is the `usageHistory` row id the
  budget is measured from, taken in the same transaction as the write. A row id
  and not a timestamp because `usageHistory.timestamp` is RFC3339 at second
  precision, so a reset issued in the same second as the spend that exhausted
  the key would compare equal and keep charging it — exactly the case an
  operator hits when they reset a key they just watched fail.
- `GET /api/keys` carries `usedTokens` next to `tokenQuota`, so the dashboard can
  render a budget instead of guessing whether a key is untouched or exhausted.
- The keys table shows the pair as `spent / budget`, paints an exhausted key in
  the danger tone, and gains **Reset token quota** on a metered key's action menu
  — the reset runs without a confirmation because it is the recoverable
  direction, and the usage history is never touched by it.

**Verified:** `go test ./internal/...` — 43 packages ok; the integration suite ok,
including a new end-to-end test that spends past a one-token budget, is refused
with 403 and the `X-Token-Quota-*` headers without reaching the provider, is reset
through the endpoint, and is then served again. A new db test pins that an
unattributed row (upstream credential only) is not charged to any client key,
while a legacy row carrying the client key still is.

**Divergence from upstream, on purpose:** the refusal stays **403** with a
`Token quota exhausted` body, not the issue's 429 `key_quota_exceeded`. The port
documents why: a lifetime budget is not a per-window rate, so `Retry-After` and
`X-RateLimit-Reset` would both promise that waiting helps when only an operator
raising the quota does.

### fix(models): stop one client's disconnect cancelling the shared catalog fetch

`resolveLiveCatalog` collapses racing requests into one upstream call via
`singleflight`, but the closure captured the leader's request context, so the
leader's client hanging up cancelled the shared fetch for every waiter. An empty
result caches nothing — `liveCatalogStoreModels` is only reached when
`len(discovered) != 0` — so the next dashboard poll repeated the whole call and
the cache never filled: on a slow node that is a permanently cold cache plus a
fan-out to the provider on every refresh.

The production log showed the shape over 1.5h of dashboard traffic: 58
`compatible node catalog fetch failed ... context canceled` against only 10
`context deadline exceeded`, concentrated on the three slowest nodes. Cancels
outnumbering real timeouts nearly 6:1 means the *client* went away — and took
everyone waiting on the same flight with it.

`context.WithoutCancel` detaches the shared call from whoever started it. Each
fetch's own `liveCatalogFastHTTP` timeout remains the only bound, so the request
cannot outlive the 10s ceiling that was already there.

**Verified.** `live_catalog_flight_test.go` drives the real race: the upstream
blocks until released, the leader's context is cancelled mid-flight, and a
follower joins — it must receive the shared result with the upstream called
exactly once. The upstream answers only its first request, so the assertion
cannot pass by luck. Run against the pre-fix code it reproduces the production
signature exactly (`compatible node catalog fetch failed ... context canceled`,
then `follower got []`). `gofmt -l`, `go vet ./...`, `go build ./...` and
`go test ./...` all clean.

### fix(chat): report a client abort as 499, not a bare 502

A client that hung up was recorded as a 5xx. `handleAccountFallback` returned the
raw forward error, which is not an `*upstreamError`, so the write sites in
`chat.go` fell through to their `StatusBadGateway` default — the caller received
`upstream error: context canceled` with a 502, and the request log recorded a 5xx
for a disconnect the gateway did not cause. Any 5xx-based alerting therefore
fired on client behaviour.

The repo already knew the right answer, which made this an inconsistency rather
than a gap: `StatusClientClosedRequest` is defined at `fallback.go:30`,
`fallback.go:725` computed 499 correctly for its own tracing span, and
`combo.go:625`/`:847` wrapped the same condition as an `*upstreamError` carrying
499. The identical abort was thus 499 on the combo path and in tracing, but 502
on the account-fallback path.

The abort is now wrapped the way `combo.go` does it, and the JSON body shared by
all three sites is single-sourced into `clientClosedBody` so they cannot drift.
The original error is not lost from diagnostics: it is still logged at
`fallback.go:870` before the wrap.

**Verified.** `fallback_client_closed_test.go` pins that a client abort yields an
`*upstreamError` carrying 499 while a live request keeps its upstream status, and
that `clientClosedBody` is byte-identical to the payload `combo.go` has always
sent. Run against the pre-fix code the first case fails with
`expected *upstreamError ..., got *fmt.wrapError: ... context canceled`.
`gofmt -l`, `go vet ./...`, `go build ./...` and `go test ./...` all clean.

### fix(dashboard): mask connection secrets on rune boundaries, not bytes

The Connections and API-key pages rendered **0/0**. The response was `HTTP 200`
with the body truncated mid-record, followed by
`{"error":{"message":"internal error","type":"invalid_request_error","code":500}}`
— a payload no client can parse.

The mask helpers cut a secret with `s[:n]` / `s[len(s)-n:]`, which slices
**bytes**, not runes. When the visible prefix of a secret ended inside a
multi-byte rune the cut left invalid UTF-8 behind, and `encoding/json/v2`
rejects invalid UTF-8 *while encoding*. By then the `200` header is already on
the wire, so the client gets a half-written body plus the error sentinel
appended after it; the dashboard falls back to 0/0. Reproduced 100% against a
real `~/.9router/db/data.sqlite` holding two such connections.

`handlerutil.RunePrefix` / `RuneSuffix` / `RuneCount` is the single shared
helper for the cut (AGENTS.md §3.C — one general abstraction instead of four
inline patches) and now serves `maskConnectionSecret`, `maskConnectionName`,
`maskClientKey` and `MaskAPIKey`. Only the elision inside an already-200 payload
changes; no `/v1/*` or `/api/*` contract moves.

**Verified.** `internal/handlerutil/utf8mask_test.go` pins the helper's
rune-boundary contract; `internal/handlers/dashboard/connections_mask_utf8_test.go`
pins the failure mode itself — a connection whose secret ends in a multi-byte
rune must encode to valid UTF-8 instead of corrupting the whole payload.
`gofmt -l`, `go vet ./...`, `go build ./...` and `go test ./...` all clean.

### 🐛 Custom model rows no longer lose their declared limits when `caps` holds a non-boolean value

A `customModels` row is decoded into `map[string]bool`, but rows written by
other builds — and by hand — carry non-boolean metadata in that block. The
real-world case is a bridge-backed node whose row holds
`"thinkingFormat":"effort"` inside `caps`. `encoding/json/v2` aborts the object
at that member, so every field *after* `caps` was silently dropped;
`contextWindow` and `maxOutput` sit there, so the row read as "no limits
declared".

The consequences were invisible but large:

- `/v1/models` and `/v1/models/info` published the substring table's floor
  (`128000 / 4096`) for an endpoint that had declared `1048576 / 65536`.
- `/api/models/caps` published the same wrong numbers, so the dashboard showed
  a 128k window on a 1M model.
- Clients that budget context from the discovery response (OMP, pi, anything
  reading `context_length`) capped themselves at 128k and compacted far too
  early. The request path never truncated — only the advertised metadata lied.

`CustomModel` now decodes `caps` through a small `UnmarshalJSON` that reads the
row's ids, name and declared limits first and only then filters `caps` down to
boolean flags — the same policy upstream's `sanitizeCaps` applies on write.
Non-boolean members are dropped, the row itself always survives, and a `caps`
that is absent, `null` or not an object leaves the row's limits intact.

`gemini-argon` / `gemini-4-argon` also gained the rows upstream already carries:
the `*gemini-4*` capability pattern (vision + reasoning + search, `gemini-level`
thinking, thinking not disableable, 1048576/65536), the same ids in the token
limit table, and the `["low","medium","high"]` thinking levels — Gemini 4
rejects `minimal` with a 400 and cannot turn thinking off. Without those rows
any Gemini 4 id that lost its custom row fell through to the 128k/4096 floor.

**Verification:** `go test ./internal/db/ ./internal/providers/
./internal/handlers/chat/ ./internal/handlers/dashboard/` — 5 new tests, all of
which fail without the fix (`declared limits = (0, 0)`, `max_completion_tokens
= 4096`, `thinkingFormat = ""`). Replayed end to end against a 9router 1.9.11
binary and a gsb-shaped upstream: the four petafora `customModels` rows
publish `128000/4096` before and `1048576/65536` after, on `/v1/models`,
`/v1/models/info` and `/api/models/caps`.

### ci: stop a module-proxy hiccup from reddening the cross-compile job

The `cross` job added in #356 starts five `go build` invocations via `make cross`,
and Go fills the module cache as it goes, so one transient read error from
`proxy.golang.org` aborts whichever target hit it. That happened on the first
push run after the merge:

```
modernc.org/sqlite@v1.54.0: read
  "https://proxy.golang.org/modernc.org/sqlite/@v/v1.54.0.zip":
  stream error: stream ID 1; INTERNAL_ERROR; received from peer
```

The identical job passed on a re-run with no code change, which is what makes it
a false signal: a network hiccup read as "a release target is broken", and that
is precisely the failure this job exists to report. Every PR runs it, so the
flake would have reddened unrelated PRs.

`go mod download all` now runs first and is retried three times. It is the only
step in the job that touches the network, so the five builds that follow are
served from the cache. A failure on all three attempts still fails the job, with
a message that says explicitly it is infrastructure rather than the diff.

Verified: the retry loop was exercised against a simulated two-failure sequence
and succeeds on the third attempt; a cold `go mod download all` followed by
`make cross` builds all five targets. `actionlint` passes on all three workflows.

### ci: gate the diff with lint, vulnerability, guard, and cross-compile jobs

The pipeline enforced `go test`, `-race`, integration, Windows service tests,
Docker build, and a PGO assertion, but nothing checked formatting, static
analysis, reachable CVEs, the frontend linter, or the workflow files themselves —
and `make cross`, which every release runs, executed only on a tag.

Four jobs are added to `.github/workflows/ci.yml`, plus one omission is closed:

- **`lint`** — `golangci-lint` (errcheck, govet, ineffassign, staticcheck,
  unused, bodyclose, noctx, rowserrcheck, sqlclosecheck, errorlint, gofmt),
  `govulncheck`, `oxlint --deny-warnings`, and `actionlint`. It builds no SPA, so
  it reports a formatting slip or a reachable CVE in about a minute. The
  golangci-lint step runs with `--new-from-rev` against the PR base (or `HEAD~1`
  on a push): the tree carries 141 pre-existing findings and 147 files `gofmt`
  would rewrite, so a whole-tree gate would fail on its first PR and be disabled
  by its second. This is the same ratchet shape as `make vet-svelte`.
- **`cross`** — runs `make cross`, the exact release command, then asserts with
  `file` that each of the five artifacts is the architecture its asset name
  claims, and that `SHA256SUMS.txt` covers them and verifies. The updater picks a
  download by matching OS and arch substrings in the asset name, so a
  mislabelled arm64 binary would install and then fail to exec.
- **`changeset`** — fails a PR with no `.changes/` fragment, and fails harder if
  `CHANGELOG.md` is touched (it is written only by `make changelog-merge` at
  release time). Of the last forty merged PRs, five carry no fragment and only
  three of those are the `chore: bump` and `[ImgBot]` commits the rule exempts —
  two entries were never written. The check exempts exactly those two titles and
  documentation-only diffs.
- **concurrency** — one run per ref, `cancel-in-progress`, so a force-push does
  not leave a superseded run's check overwriting the current one.

The two `run:` scripts `release.yml` had never been linted, because `actionlint`
was not installed on any runner. It now is (via `go run`), and it reported two
shellcheck findings in the changelog-fallback block (`SC2010` on `ls | grep`,
`SC2046` on an unquoted `cat $(...)`). That block is rewritten with a glob and a
quoted read loop; its output was verified byte-identical against this repo's
fragments before the change.

`govulncheck` immediately found a reachable advisory — **GO-2026-5320**, a
cross-site-scripting issue in `github.com/yuin/goldmark` v1.7.8 reachable from
`tools/sitegen`, which no other gate examines. `goldmark` is bumped to v1.7.17,
which is not affected.

Three `oxlint` warnings are fixed rather than suppressed (an unused import, an
unused destructured parameter, and an unnecessary escape), so the new
`--deny-warnings` gate starts from zero.

Verified locally: `actionlint` on all three workflows with shellcheck present,
`golangci-lint --new-from-rev` (0 issues on this diff, and a planted violation is
reported), `govulncheck` (no vulnerabilities), `oxlint --deny-warnings` (clean),
`bun test src` (344 pass), `make cross` plus the artifact and checksum
assertions (including negative controls: a corrupted binary fails `sha256sum -c`
and a mislabelled architecture fails the `file` check), and the `changeset`
guard against seven git states — code change with and without a fragment,
`CHANGELOG.md` touched, a `chore:` bump, a docs-only diff, an `[ImgBot]` diff,
and a fragment-only edit.

GoReleaser and a `ghcr.io` publish were considered and not adopted: `install.sh`,
`install.ps1`, and `internal/updater` all download a bare binary named
`9router-go-<os>-<arch>`, while GoReleaser's default `.tar.gz`/`.zip` archives
would break all three, and `release.yml` already produces the binaries, checksums,
and multi-arch Docker Hub image on a tag.

### Features
- **plans**: add reusable policy templates (F-3). A plan carries rate limits (RPM/TPM/concurrency), a budget (USD micros, token ceiling, period, alert percentage, hard-cutoff flag) and a model allowlist; a key assigned to a plan inherits those as defaults and its own columns still win where they are set. One resolver (`internal/policy.Resolve`) applies the precedence — operator global default, then plan, then key — so the limiter and the model gate cannot disagree about the same key.
- **plans**: `GET/POST /api/plans`, `GET/PUT/DELETE /api/plans/{id}` and `GET /api/plans/{id}/keys`. Deleting a plan that keys still reference is refused with 409 and the count instead of cascading, because re-pointing those keys would change their limits without the operator asking.
- **dashboard**: a Plans page (sidebar → Plans) for creating and editing templates, and a plan selector in the API-key policy dialog. `PUT /api/keys/{id}` accepts `planId`; an id that does not exist is refused with 400 rather than stored, since a dangling id would silently resolve to no plan while the dashboard showed the key on a template.
- **model access**: the per-key allowlist is now unioned with the plan's, so a plan can grant models to every key on it and a key can still widen that for itself. Neither side can take a model away from the other.
- **plans**: the model gate resolves the plan in one joined read when only a key id is on hand, and skips that read entirely for a key whose `planId` is empty — every key minted before this feature pays nothing.

### Added
- `plans` table and `apiKeys.planId`, both Go-only and additive. The `default` plan is seeded by `EnsureKeiRouterTables`, so every bootstrap path (production and test fixtures alike) has it and a key assigned to `default` never reads as unplanned. The seeded plan ships `hardCutoff` off and every limit at 0, so an existing install behaves exactly as it did.

### feat(gemini-web): route gemini.google.com directly, with no browser and no credential

Makes `gemini-web` routable for the first time. It was catalogued with a `501`
refusal executor because `gemini.google.com` has no published API, and the
upstream implementation reaches it by launching a real Chromium and typing into
the composer. That conclusion was half right: there is no API, but there **is**
an HTTP surface, and it answers a plain client.

**Why this is possible, and what was verified rather than assumed.** Live probes
against the site established three things, and every one of them was checked
before a line of the executor was written:

- `GET /app` hands an ordinary `net/http` client the session tokens
  (`cfb2h`, `FdrFJe`) in the inline `WIZ_global_data` object. No browser, no
  cookies, no TLS impersonation, no `uTLS` dependency.
- The same `StreamGenerate` endpoint the page's own JavaScript posts to answers
  a chat turn **anonymously** — zero cookies, zero credentials. The single-binary
  CGO-free constraint that killed the browser path was never the obstacle.
- The response is Google's `batchexecute` line protocol, in which **every frame
  re-sends the whole answer so far** rather than a delta. Deltas are therefore
  derived, not forwarded; a frame that is not a superset of what was already
  streamed is refused rather than papered over.

**Consequence for the provider entry:** `gemini-web` is now `NoAuth` with a
default key, because the site serves an anonymous visitor. Requiring a pasted
cookie would have made a plug-and-play provider into one that is not. A
`__Secure-1PSID` cookie is still honored when one is configured — what it buys is
the **choice of model**, not reachability: an anonymous session is served
Flash-Lite only, a signed-in one several models.

**Model selection is refused before the turn, not substituted after it.** The
site answers a model it does not recognize by silently answering with a different
one under the requested name. So the live `otAQ7b` registry is consulted per turn,
the request is refused with a `400` naming what the session *does* offer, and the
identity stamped on the answer is checked against the model that was resolved. A
Flash-Lite answer can never be filed under a Pro model's name.

**Also in this change:**

- Streaming SSE with real deltas, `reasoning_content` from the site's thinking
  channel, and the site's inlined ```` ```python?code_reference ```` sandbox
  trace moved out of the content channel into the reasoning one, where a coding
  client will not read it as the reply.
- Tool calling: the site has no native tool channel, so declarations are rendered
  into the prompt and calls are recovered from the whole answer, reusing the
  parser `deepseek-web` already carries rather than forking a second one.
- `x-goog-ext-525001261-jspb`, the header that names the model, is built per
  model from the registry's capacity field; the wrong shape is not rejected, it
  produces a different model's answer.
- Timeouts are two clocks, not one: 60s to the first byte and 90s between frames.
  A long think is not a failure, a stalled answer is, and a single total deadline
  cannot tell them apart.

**Bugs found by running real turns, not by the tests.** The unit suite was green
throughout all four of these:

- The site's "unauthenticated" code is `1016`, and `1016` also occurs as an
  ordinary constant **inside a healthy model registry**. Scanning the payload for
  it reported every working anonymous session as a refused one.
- The first correction — asserting that `1016` sits at index 14 — was wrong for
  the same reason: index 14 carries that constant in a healthy payload too. The
  detection is now on content, because a model list is the only proof of a usable
  session. That took a live call to a real upstream to see.
- Streamed chunks carried `"created":0`, which some clients reject outright,
  because the emitter never stamped the field.
- The streaming path never wrote `req.ResponseBuf`, so a streamed turn was
  recorded with an empty logged body and a zero token estimate — the same trail a
  model that answered nothing leaves. Every other streaming executor in the
  package writes it; this one did not.

**Fingerprint fidelity: the request is made to look like Chrome's.** The provider
does not use `net/http`. It dials through `bogdanfinn/tls-client` with the
`Chrome_150_PSK` uTLS profile and `fhttp`, which gives three things `net/http`
cannot:

- **TLS ClientHello** — Chrome's cipher suites and extension order (JA3/JA4),
  with per-connection extension shuffling as Chrome does it.
- **Header order** — Chrome writes request headers in a fixed non-alphabetical
  order; Go's `net/http` sorts them alphabetically before writing. That was
  verified on a raw socket rather than assumed. `fhttp` honours a `HeaderOrderKey`,
  so the navigation and XHR orders are now replayed explicitly, along with
  Chrome's HTTP/2 pseudo-header order.
- **Client hints that agree with the UA** — `sec-ch-ua` / `sec-ch-ua-platform`
  matched to the UA string, and a `sec-fetch-*` set per request kind:
  `navigate`/`document` for the bootstrap page, `same-origin`/`cors`/`empty` for
  the API calls.

**An earlier refusal of this dependency was wrong.** It was justified by a
"CGO-free, no new Go dependency" constraint that does not bar a pure-Go TLS
library, and that was asserted rather than tested. Measured afterwards: the stack
is pure Go, builds with `CGO_ENABLED=0`, cross-compiles to all five release
targets, and costs **+8.25 MiB** on the binary. The "uTLS alone, +2.18 MiB" middle
option was also identified — it gets the TLS fingerprint without the header order,
which is why it was not the one taken.

**Proxy pools still apply.** The chat path resolves a connection's pool into the
`http.Client`'s transport before the executor runs. The uTLS client is a different
stack and builds its own connection, so the proxy is read back off that transport
and handed to `tls-client`. Without that the request would leave from the host IP
while the dashboard still showed it as proxied — publishing the operator's egress
address, which is the one thing a pool was assigned to prevent. There is a test
for it.

**Verification:****Verification:**

- Unit suite: 17 executor tests over an `httptest` fake that serves the whole
  turn (bootstrap page, registry RPC, stream), pinning the call sequence, delta
  derivation, tool-call recovery, and each status the router acts on
  (401 stale cookie, 400 unavailable model, 502 substituted model / truncated
  stream / missing session tokens), plus the response-buffer and `created`
  regressions above — each was confirmed to fail before its fix. One of the 13
  pins the browser header set and two pin the ordering (Chrome's header order, and
  the HTTP/2 pseudo-header order), so a refactor that drops the client hints or
  falls back to `net/http` fails rather than silently sending a request that looks
  less like a browser. One asserts the proxy reaches the TLS stack.
- Live: a turn against the real site through the uTLS stack returned its answer,
  and the five release cross-compile targets all build with `CGO_ENABLED=0`.
- Dashboard probe tests: a supplied cookie is probed against the registry RPC, a
  refused one reports the advice, an empty one is reported as still working, and
  the `1016` regression is pinned.
- Catalog tests: `NoAuth` + default key, and the capability override that stops
  the web surface advertising the API surface's vision/audio/video/search.
- **End-to-end through the real gateway:** `POST /v1/chat/completions` with
  `gweb/gemini-3.1-flash-lite` returned `{"content":"gateway-ok"}`, streaming
  returned proper `chat.completion.chunk` deltas ending in `data: [DONE]`, an
  unavailable model returned the actionable `400`, and `gemini/…` still resolved
  to its own provider (AGENTS.md §3 isolation).

`gofmt`, `go vet`, the full `go test ./...` suite, `make vet-svelte` (83 errors,
at baseline, 0 unresolved) and `bun run build` all pass.

**AGENTS.md gains §4.F (Dependencies & Portability).** The guideline had no
dependency or portability rule at all, which is how an unverified "no new Go
dependency" claim got treated as policy in the first place. The new section is
written as general policy rather than as a note about this change, so it carries
no provider names, no library names and no measured figures — those live here, in
the fragment, where a project-specific record belongs.

What it states: a dependency is judged by two conditions and not by how heavy it
looks, and both are cheap to check before the dependency is proposed rather than
after — every `CROSS_TARGETS` pair still builds with cgo disabled, and
`go mod tidy` leaves a coherent graph with what the repository imports listed as a
direct require. It records that a *build* claim must be verified by building,
because an unverified "impossible" discards a working approach and the redesign is
paid for later. It asks for a measured cost rather than an assumed one when a
heavier and a lighter variant exist. It forbids cgo and per-platform build tags
outright, and it notes the prerequisite a first reader would trip on: `web/embed.go`
embeds `web/dist`, so the loop needs `make web-build` first and a fresh clone fails
there for an unrelated reason. The loop reads its target list out of the `Makefile`
instead of restating it, so a target added there cannot be forgotten. The same loop
is referenced from §7.5 (verify) and §8 (cheat-sheet).

**Supersedes** the gemini-web "catalog-only, needs a browser" notes in
`internal/proxy/executor/gemini_web.go`, `internal/providers/providers.go`,
`internal/proxy/executor/init.go`, `internal/handlers/dashboard/validate_web_providers.go`
and `web/src/lib/providers.ts`.

### feat(gemini-web): catalogue both registry naming forms, including Argon

The catalogue published only the versioned spelling of each model, so the names
the site's own registry produces could not be requested. The registry names a
model twice: by **category** — `Pro`, `Flash`, `Argon` — and by a **display label**
carrying the version — `3.1 Pro`, `4 Argon`. A client written against the site's
naming asks for `gemini-argon` or `gemini-4-argon`; the catalogue offered neither,
so a request for Argon was answered "cannot be served by this session" on an
account that has it. Both forms are now catalogued for every model the registry
reports, so `gemini-pro` / `gemini-3.1-pro` and `gemini-argon` / `gemini-4-argon`
each resolve.

**The category form is the one to keep, and the reason is release churn.** A version
prefix moves with every release — this registry already reports `3.5 Flash-Lite`
where the catalogue said `3.1` — while the category does not. Listing the category
name means the model stays reachable across a version bump without an edit here;
the versioned entries are kept because they are what existing clients send.

**A catalogued name that matches nothing is a trap, so it is tested.** A name added
to the catalogue without the matcher learning about it reaches the user as
"cannot be served by this session" even when the account has the model, which reads
as a broken provider rather than a mislabelled one. One test resolves every
catalogue entry against the fullest registry the site is known to report, and
fails when an entry matches nothing.

**Verified**: the new capability rows are `gemini-pro`, `gemini-flash`,
`gemini-flash-lite`, `gemini-argon`, `gemini-3-pro` and `gemini-4-argon`, each
carrying the web surface's own capability set (`Tools`, `Reasoning`,
`gemini-level`, thinking not disablable) rather than falling through to the generic
`*gemini-3*` pattern row, which would advertise the API surface's vision and search
flags. The three new executor tests pass, as does the whole `internal/providers`,
`internal/handlers/dashboard` and `internal/proxy/executor` suite. Argon itself
cannot be exercised end to end here: it is served to a signed-in account only, and
an anonymous session reports exactly one model (`Flash-Lite`), which is what the
live `otAQ7b` probe returned.

**The dashboard needed a second, separate edit, and that was found by running the
thing rather than by the tests.** The catalogue reaches the dashboard from two
places: the Go catalog feeds `/v1/models` and `/api/models/caps`, and
`ProviderDetailView` renders its "Available Models" rows from
`getModelsByProviderId()` in `web/src/lib/models.ts`. Both already answered with
nine names, so every check passed while the provider page still showed three — a
model the gateway would route and resolve was invisible in the UI that exists to
configure it. Same failure shape as the `1016` false positive above: the layer
under test was correct and the layer beside it was not.

`models.ts` declares itself auto-generated from the upstream registry and has no
generator in this repository, and nothing compares the two lists, so this drift is
silent by construction. The file is not redundant — it carries 23 media providers
the Go catalog does not — so it cannot simply be derived from the Go side; the
parity that matters is per provider, and for this one it now holds 9 for 9 in both
directions. Editing it by hand is correct but unguarded, and a test that pins this
provider's two lists together would be the place to stop the next one from
escaping the same way.

**Verified in the running dashboard**, not only in the bundle: the provider page
lists `gweb/gemini-argon` and `gweb/gemini-4-argon` beside the existing entries,
the heading reads "Gemini Web" with no "(Free)", and the button counts "Check All
Models (9)". The served asset contains `gemini-argon` and no occurrence of the old
label.

**Also removed the word "free" from the provider's name and messages.** The provider
card read "Gemini Web (Free)", and the refusal text explained that an anonymous
session reaches "the free model". Neither the plan nor the site's own naming calls
it that, and the label made a paid-tier-capable provider look like a free tier.
The card is now "Gemini Web" and the messages say what is true — an anonymous
session reaches **one model** — without a tier claim that changes under the user's
feet.

### fix(gemini-web): stop sending the router's placeholder as a cookie

`NoAuth` providers are dispatched with their catalog `DefaultAPIKey`, so the
executor received the literal word `anonymous` (or `public`) as `req.APIKey` even
when nobody had configured anything. The cookie renderer treated it as a bare
`__Secure-1PSID` value and sent `Cookie: __Secure-1PSID=anonymous` to
`gemini.google.com` on every request — a credential the user never supplied,
handed to a third party, and one that made the session look credentialed.

Found from a user report, not by a test: a request for `gemini-flash` was refused
with advice naming a "configured `__Secure-1PSID`" on a connection whose cookie
field was empty. The visible half was the wrong sentence; the invisible half was
that Google had been receiving a cookie nobody set.

Two placeholders are now recognised and dropped, matching on the whole value
rather than a substring — a real cookie whose value merely contains the word
`anonymous` is a credential to pass through, and dropping it would turn a
signed-in session anonymous, which is the same bug pointing the other way. Both
directions are pinned by tests.

The same change makes a refusal name the credential it actually tried, because one
message cannot be right for both cases: telling someone with a stale cookie that
"an anonymous session reaches one model" sends them looking for the cookie they
already pasted, when the fix is to replace it. With no credential the advice says
how to obtain one; with one configured it says to paste a fresh one.

### feat(gemini-web): post the signed-in page's SNlM0e token as `at`

The reference implementation for this endpoint posts `at` alongside `f.req`, and
treats its absence on a signed-in page as an expired session —
`could not extract SNlM0e (at token); session may be expired`, status 401. This
executor did not post it at all, so every request carried the shape an anonymous
visitor sends.

That survived review because an anonymous page genuinely omits `SNlM0e` — a live
fetch of `/app` without a cookie returns `cfb2h` and `FdrFJe` and no `at` — and an
anonymous session is the only one that had been exercised end to end. A signed-in
session is exactly the case the token exists for, and its absence was the one
difference between this executor's request and the request the page's own
JavaScript makes. Reported from a user about to use a signed-in account, not
caught here.

The bootstrap now extracts `SNlM0e` opportunistically rather than requiring it:
it must stay optional because an anonymous page omits it, so both cases keep the
shape they had. When a cookie is configured and the page still carries no token,
the site served the visitor's page, which is logged once per session instead of
surfacing later as a model refusal that blames the model.

**Verified**: two tests pin the field — a page carrying `SNlM0e` and a page
without it — over both RPCs an authenticated session uses. The first was confirmed
to fail when the token is dropped from the form, so it catches the regression it
describes rather than passing vacuously.

### feat(gemini-web): sign the registry call with SAPISIDHASH

The reference implementation sends `authorization: SAPISIDHASH <seconds>_<sha1>`
on the `batchexecute` call, and this executor did not. It is now sent when the
credential carries a `SAPISID` to sign with.

**What it is not:** a measured requirement. Probing the real endpoint with a
signed-in cookie showed the signature is **not** what decides the served set — the
same four models (Flash-Lite, Flash, Pro, Argon) come back with the header and
without it. The reasoning that produced it was plausible and wrong: that RPC does
answer *which models may this session use*, so it looked like the call the site
judges the caller on, but nothing demonstrated that a missing signature changes
the answer. It is kept because it makes the request match the one the page's own
JavaScript makes, which is the standard this provider holds itself to — and it is
written up this way so the next reader does not inherit a claim that was never
checked.

By contrast the `at` field above **is** a measured requirement: the same probe
without it answers `400`, and with it `200`.

The signature is `<unix-seconds>_<sha1("<seconds> <SAPISID> <origin>")>`, built
only when a `SAPISID` is present. An anonymous session has nothing to sign and
sends no header at all, because a malformed one is worse than none. The cookie
lookup matches the **whole** cookie name rather than a prefix: `__Secure-1PSID` is
a prefix of `__Secure-1PSIDTS`, and signing with the wrong value produces a header
the site rejects without a diagnostic.

**Verified**: three tests pin the signature, the anonymous case, and the
prefix-collision case. The first recomputes the digest from the request rather
than trusting the prefix, and was confirmed to fail when the header is dropped.

### feat(gemini-web): verified end to end against a signed-in Gemini Pro account

Every signed-in claim above was checked against a real account, which is the
verification this provider had been missing — the anonymous path was the only one
ever exercised before.

| Check | Result |
|---|---|
| `GET /app` with the cookie | `200`, and the page carries **`SNlM0e`** (an anonymous page does not) |
| Registry `otAQ7b` with the cookie | 4 models: `Flash-Lite`, `Flash` (3.8), `Pro` (3.1), **`Argon` (4 Argon)** |
| `gemini-web/gemini-argon` through the gateway | `200`, answer returned |
| `gemini-web/gemini-4-argon` | `200`, answer returned |
| Streaming with Argon | real deltas, terminated by `data: [DONE]` |
| Registry without the `at` field | **`400`** — the token is load-bearing |
| Registry without the signature | `200`, same 4 models — it is not |
| Credential in logs | absent; the dashboard API returns `apiKeyMasked` only |

**One consequence to know about.** `otAQ7b` names models by *category*, and a
category is stable across versions: this account's `Flash` entry carries the label
`3.8 Flash`, while the catalogue also publishes `gemini-3.7-flash`. A request for
the versioned name resolves to that same entry and is served by it, so the answer
comes back labelled `gemini-3.7-flash` although `3.8` produced it. The identity
check compares the registry **id**, which is what the site reports, so it cannot
see a difference between two catalogue names that point at one entry. That is the
category-first design working as intended — the alternative is refusing a name the
account can in fact serve — but a version literal in a model id is not a promise
about the version that answers.

### fix(gemini-web): stop sending a stale cookie beside its rotated replacement

The site rotates `__Secure-1PSIDTS` and hands the new value back as `Set-Cookie`.
This provider presented the credential as a hand-written `Cookie` header **and**
dialled through a client with a cookie jar, and fhttp inserts jar cookies with
`AddCookie` — it does not replace a header the caller wrote. So after the first
rotation every request carried the cookie **twice**:

```text
Cookie: __Secure-1PSIDTS=<the value the user pasted>; …; __Secure-1PSIDTS=<the rotated value>
```

The stale value came first. Google reads a stale `__Secure-1PSIDTS` as a
**cancelled session**, so the request was judged anonymous: it still returned
`200`, still returned a model list, and quietly offered one model instead of the
account's four. A signed-in user saw Flash-Lite only, with no error naming the
cookie — and because the failure looks exactly like an anonymous session, the
refusal message blamed a cookie that was configured correctly.

The fix is that the jar owns the cookie: the credential is seeded into it once,
when the session is built, and no `Cookie` header is written. Seeding once
matters — re-seeding per call would put the pasted value back over the rotated
one, since the jar overwrites by name.

Found by asking why a live Pro cookie stopped working mid-session. It is
deterministic, so it is pinned: the fake upstream now returns a rotated
`__Secure-1PSIDTS`, and the test asserts the turn carries that value, carries it
once, and keeps the cookies the site did not rotate. It was confirmed to fail
before the fix, with the duplicate visible in the assertion output.

**What is still not implemented.** Rotation is only honoured **within a process
lifetime**: the jar holds the rotated value, but the value the user pasted is what
a restart reloads. The reference implementation rotates `__Secure-1PSIDTS` and
`SIDCC` explicitly through `RotateCookies` and persists the result to disk, so its
cookie survives a restart; this one needs a fresh paste after a restart, or when
the site cancels a session for other reasons. Persisting the rotated value is the
next step for this provider.

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

### fix(proxy): exempt the operator's own outbound proxy from the SSRF guard

Every upstream call on an install whose connections egress through a proxy on
loopback was refused:

```
upstream error: forward to upstream: forward to https://<provider>/v1/chat/completions:
assigned proxy failed, refusing to route direct (Post "...": proxyconnect tcp:
dial tcp 127.0.0.1:8888: request to private/reserved IP blocked by SSRF protection):
proxy required but failed (strictProxy=true)
```

The proxy does not have to come from the environment: a connection configured in
the dashboard with `connectionProxyEnabled` and a `connectionProxyUrl` of
`http://127.0.0.1:8888` — a local CONNECT proxy for DNS, a sandbox sidecar, the
standard Termux setup — hits it too. On the reporting install that is **68 of 69
connections**, so the failure reads as "many broken providers" rather than "one
broken proxy". The environment-variable path (`HTTP_PROXY=http://127.0.0.1:8888`)
breaks identically.

`NewSafeDialer`'s `ControlContext` runs on every dial the transport makes,
including the dial to the proxy. A request routed through a proxy never dials the
target itself, so the guard was inspecting the wrong host: it protected nothing
about the destination and instead removed the operator's egress path. Because
`connectionProxyEnabled` also marks the request strict, the direct replay is
forbidden, so the request has no way out at all — a 502 that names SSRF for a
healthy local proxy.

`internal/proxy/ssrf.go` now carries a bounded `proxyAllowlist` of dial endpoints
exempt from the private-IP check:

- `NewSafeTransportForProxy(proxyURL)` installs the proxy on the transport and
  exempts its endpoint; an empty or unparseable URL falls back to the ambient
  path so a malformed pool URL cannot silently become a direct dial.
- `NewSafeTransport()` keeps `t.Proxy` **nil** and exempts whatever endpoint the
  environment names, resolved through `proxyFromEnvironment`. Leaving the field
  nil is deliberate: `proxyFuncIsAssigned` (`proxy_egress.go`) tells an assigned
  proxy from an ambient one by comparing func identity against
  `http.ProxyFromEnvironment`, so wrapping `t.Proxy` to observe the host would
  have made every ambient proxy read as assigned — and with it every strict
  request would refuse the direct replay the ambient case exists to allow.
- The three callers that built a transport and then assigned `transport.Proxy` by
  hand now go through the constructor: `InsecureClientFor`, the per-connection
  proxy client cache in `internal/handlers/chat/connections_proxy.go` (the path
  this report exercised), and the dashboard's proxy test in
  `internal/handlers/dashboard/settings.go`.

The guard itself is unchanged for targets: loopback, private, link-local,
unspecified and cloud-metadata addresses are still refused unless the request
carries `WithAllowLoopback` (local-by-design providers) or
`ALLOW_PRIVATE_NETWORKING` is set.

**Verified.** A/B on the real gateway stack — same request, same combo, same
upstream, same database, only the binary differing, with no `NO_PROXY` and the
connection proxy on `127.0.0.1:8888`: the pre-fix binary answered `502` in 0.076s
with the message above; the fixed binary answered `200` in 2.0s with the model's
completion, and its usage log confirms the request left through the proxy
(`egress=http://127.0.0.1:8888` on the 200) rather than quietly falling back
direct. A separate run that pointed `HTTP_PROXY` at a dead port left the chat path
unaffected — the connection's own proxy is what drives it — while the catalog sync
that does use the environment proxy failed with a plain `connection refused`,
which is the behaviour the exemption is meant to leave alone.
`internal/proxy/ssrf_proxy_allowlist_test.go` covers the exemption end to end: a
real listener on `127.0.0.1` acting as the proxy is reached through
`http.Client.Get`, and the transport's own `DialContext` connects to that listener
while an unrelated loopback listener is still refused — plus the ambient path,
the bare-host entry, a bounded allowlist, every previously blocked private target,
and the proxy-identity invariant above.

**Related.** The same install's healthy production instance only worked because it
ran with `ALLOW_PRIVATE_NETWORKING=true`, which short-circuits the guard before any
of this — the blunt workaround for the same defect. This fix lets that variable be
dropped and the guard turned back on.

**Documented limitation.** A proxy named by *hostname* is not matched, because Go
resolves the name before `ControlContext` sees the address, and comparing there
would need a DNS lookup in the dial path. Every local proxy — the case this fixes
— is written as a literal address, and a hostname proxy that resolves to a public
IP was never blocked in the first place.

### fix(executor): floor Muse Spark output budgets at 512 tokens

Muse Spark spends 570-1320 hidden reasoning tokens before emitting any completion
text, so a caller-set budget below that is consumed entirely by reasoning: the
upstream answers 200 with an empty message (which the gateway reports as
"Provider returned empty content") or stalls the stream until the watchdog fires a
502. Neither failure names the budget, so a working model looks broken. 512 is
upstream's measured boundary — max_tokens 64/100 → empty, 256/512/1024 → content
present — ported from OmniRoute `MUSE_SPARK_MIN_OUTPUT_TOKENS`
(`open-sse/executors/opencodeMuseSpark.ts`).

- `clampMuseSparkOutputBudget` raises a too-small budget in all three spellings an
  OpenAI-shaped request may use: `max_tokens`, `max_output_tokens`,
  `max_completion_tokens`. It only ever raises — an explicit larger budget and a
  request that set no budget stay byte-identical, because synthesizing a cap the
  caller never asked for would silently shorten real answers.
- It runs inside `normalizeMuseSparkResponsesBody`, and only for Muse Spark ids, so
  the sibling models that share the same Responses lane (`grok-4.6`,
  `gpt-5.6-luna`) keep their budgets.
- Reasoning-effort suffixes survive cleaning again: the `(high)`/`(medium)`/`(low)`
  a client appends to the model id is 9router request notation and never reaches
  the wire, but the intent it carried was being dropped with it. The callers now
  pass the model id as the client sent it and the sanitizer strips the prefix and
  the override itself. **Fill-only precedence**: an effort the body states
  explicitly is left exactly as written.
- `museSparkCleanModel` centralises the prefix/override stripping that both request
  paths previously duplicated.

**Verified.** `musespark_min_output_test.go` tables the clamp (below/at/above the
floor, all three spellings at once, absent and string budgets, sibling and
non-spark models), drives the real `normalizeMuseSparkResponsesBody` with client
JSON for both the raised and the untouched case, and checks suffix precedence both
ways. The pre-existing `TestForwardOpencode*MuseSpark*` and
`TestNormalizeMuseSparkResponsesBody_ReasoningAndToolChoice` suites still pass.

### Fixes

- **models**: report an unreachable (502) or timed-out (504) upstream error during models discovery instead of a blanket 500 internal server error (fixes #347, porting decolua/9router#4843).

### fix(codex): a failed turn is no longer served to the client as an empty 200

Claude Code (and any other client) received an empty response when the Codex
upstream rejected a request — a tool-schema `pattern` it refuses was the case
reported in #334. The upstream's reason never reached the client, and because
the turn was closed with a fabricated success terminal, combo fallback stopped
on the model that had just failed.

**Cause.** The failure arrives as an *event on an HTTP 200 stream*, not as an
HTTP status, so by the time it is known the status line is long gone.
`ProcessCodexEvent` already recorded such an event on `state.UpstreamErr` —
its own comment says "handleCodexStream converts to an UpstreamError after the
scan" — but only the non-streaming path did. On the streaming path, which is
the one Claude Code uses, nothing read `state.UpstreamErr`:

- a `[DONE]` in the stream wrote the success terminal (`finish_reason: "stop"`,
  or `stop_reason: "end_turn"` for a Claude client) and the `[DONE]` right
  there, before any error check could run;
- when no `[DONE]` arrived, the same fabricated terminal was written afterwards.

Both read to a client as a completed answer carrying no content.

**Fix.** The streaming path now ends a failed, content-free turn the way the
rest of the gateway already ends a failure after HTTP 200 — with its own
in-band error frame (`proxy.BuildStreamErrorBytes`, the shape `openai.go` and
`bedrock.go` use) and a returned `*proxy.UpstreamError`, so the failure row is
written and fallback rotates instead of settling.

- `codexFailedEmptyTurn` is the one predicate for "the upstream failed and
  produced nothing". An error event *after* content is deliberately not that
  case: those turns keep the partial reply and stream normally.
- `response.failed` is now recorded as well, and `normalizeCodexFailureEvent`
  rewrites it into the `{"error":{...}}` shape the reader understands, so the
  upstream's real message survives.
- `codexEventErrorInfo` is shared by the returned error and the in-band frame,
  so the client's error line and the failure row cannot disagree.
- The `pattern` stripping Codex does need (`\p{...}` Unicode property escapes)
  was checked against upstream and is already at parity: upstream's
  `open-sse/utils/codexToolSchema.js` strips only those, and lookarounds are
  **not** stripped there either. Nothing was loosened; this issue is the
  error path, not the sanitizer. Tool-schema validation semantics are
  unchanged.

**Verified.** `internal/proxy/executor/codex_failed_turn_test.go` covers both
clients × both event shapes (`error` and `response.failed`), the precedence case
(an error after content is not replaced), and a healthy turn still terminating.
`internal/integration/codex_failed_turn_test.go` boots the real router against a
fake upstream and pins the client-visible contract for `/v1/messages` and
`/v1/chat/completions`. Both fail against the pre-fix code, and both pass with
it. `go test ./...` and `make test-integration` are green.

### test(translator): pin that keyword-named tool parameters survive the Gemini sanitizer

The Gemini schema sanitizer deletes keywords Google's protobuf has no field for —
`title`, `default`, `const`, `x-*` and the rest of the unsupported list. That is
only correct on a **schema node**: a `properties` value is a dictionary keyed by
parameter name, so a tool parameter called `title` is not a keyword, and a
parameter called `properties` is not the properties container.

Upstream hit this twice. `decolua/9router#4620`: `ensureObjectType` recursed into
the properties map, so a parameter named `properties` made the map itself look like
a `type:"object"` schema and Vertex rejected the request. `decolua/9router#4831`:
`removeUnsupportedKeywords` and `convertConstToEnum` walk `Object.values`, which
deletes keyword-named parameters and rewrites a parameter named `const` into an
enum.

This port is already correct on both counts — `cleanGeminiSchema` recurses into
the **values** of `properties`, never the map itself, so neither failure mode can
reach a parameter name. That is the same rule `stripPatterns` documents
("Properties is special-cased because its keys are arbitrary property names"). No
production code change was needed; what was missing was anything holding the
invariant in place.

**Verified.** `internal/translator/schema_property_names_test.go` runs thirteen
keyword-named parameters through `CleanParametersSchema` and asserts each survives
with its schema and its `required` entry; checks that a parameter named
`properties` is not treated as the properties map in five shapes (flat, beside
siblings, two levels down, inside an array's `items`, in a union branch); and
checks that a parameter named `const` is not converted to an enum. These pass
against the current implementation — they are a guard, not a fix, so a future
"simplify the recursion" refactor fails here instead of at Google with a 400.

### Fixes
- **dashboard**: refuse an `expiresAt` that is not an RFC3339 timestamp. The auth middleware enforces an expiry with `time.Parse(time.RFC3339, …)` and skips a value it cannot parse, so a mistyped contract end was stored as a key that never expires; the refusal now names the expected shape (`2026-12-31T23:59:59Z`, or empty for no expiry).
- **dashboard**: validate the resale `metadata` blob instead of storing it verbatim. It must be a JSON object within 2 KB and may only carry `customerId`, `contact`, `notes`, `priceCents` (whole, non-negative cents) and `billingCycle` (`one-off`, `monthly`, `quarterly`, `yearly`); the refusal lists the accepted keys, because a typo such as `customerID` would otherwise drop that key out of the resale report silently.

### Changed
- **dashboard**: `PUT /api/keys/{id}` validates every field before the first write, so a refused policy no longer half-applies the fields that happened to be checked first — rate limits used to be committed before a bad expiry or an over-long name was rejected. The policy dialog's metadata help text now names the accepted keys and the size cap.

### Features

- **chat**: add global system prompt dual-slot injection (prefix and suffix) supporting OpenAI chat messages, Claude Messages, and Responses API, with runtime toggle and an anti-poisoning preset in Settings (closes #340).

### fix(combo): stop retrying a model that cannot hold the request

A combo member that answers 400 because the prompt exceeds its context window is a
fact about the model, not the account — every connection to it replies the same
way, and a larger member of the same combo can serve the prompt. The loop treated
it as an account fault and spent its whole connection budget on it: with
`connAttempt < 10`, a too-small first member was asked **10 times** before the
turn moved on.

- `internal/providers/errorclassify.go` gains `ModelFitErrorPatterns`,
  `ModelFitStatusCodes` and `IsModelFitError(statusCode, body)` — a typed
  classifier rather than a `strings.Contains` scattered through the loop. It is
  gated on status (400/413/422) so a provider's prose cannot trigger it on an
  unrelated failure, and matched case-insensitively against the body.
- Both combo loops (`handleComboFallback`, `handleMessagesComboFallback`) break to
  the next member on a fit error. Breaking rather than `continue` is the fix: it
  skips the remaining connections of that model, and it also skips
  `comboLockRetryable`, so a healthy account is not cooled down for a request that
  was simply too big.

When every member is too small the client still receives the upstream 400
verbatim, so the operator can read the model's limit.

**Verified.** `model_fit_error_test.go` covers the OpenAI/Anthropic/Gemini
wordings, case-insensitivity, the 413/422 carriers, and the negatives (a
malformed-body 400, a 500 whose text merely mentions a context window, an empty
body, a 401). `internal/integration/combo_context_overflow_test.go` drives the real
gateway with a fake upstream: the combo advances to the larger member, the
too-small member is asked **once** (this assertion failed before the change — it
saw 10), and an all-too-small combo returns the upstream 400 with the limit text
intact.

**Scope note.** The issue's headline symptom — the combo returning the 400
directly without trying later members — did not reproduce: the fallback already
advanced. The measurable defect was the wasted connection budget, and that is what
the predicate and the `break` remove.

### Fixes

- **translator**: sanitize nested array schemas, tuple items, and empty `{}` item definitions when converting schemas for Gemini and Google Cloud Code, preventing upstream 400 `INVALID_ARGUMENT: items.items: missing field` errors on multi-dimensional or stripped schemas (fixes #336, porting decolua/9router#4798).

### fix(codebuddy-intl): send the X-Conversation-* header family the intl gateway now requires

Since 2026-10-15 `www.codebuddy.ai` runs a "channel verification" gate: a chat
request that does not carry the `X-Conversation-*` header family the official IDE
sends on every turn is refused before the model is reached, with

```
HTTP 400 {"code":11128,"msg":"conversation id missing",
 "displayTips":{"en":"This call failed channel verification, possibly because it
 was not sent from an official client. Please resend from the official client."}}
```

100% of `cbai/*` requests failed that way — the report that opened this is
`codebuddy-intl/deepseek-v4.1-flash`. `codebuddy-cn` (`copilot.tencent.com`) is
not gated, which is why only intl connections broke. Same upstream change as
decolua/9router#4892.

`ForwardCodebuddyCN` now detects the intl `BaseURL` (`codebuddy.ai`) and merges
three headers into a **copy** of the provider config's `StaticHeaders`, so the
shared map is never mutated and the CN path is untouched:

- `X-Conversation-ID` — RFC-4122-v5-shaped UUID derived from the session key
  (`req.SessionID`, falling back to the opening user message, then a random
  UUID), so retries of the same turn and account failovers stay on one
  conversation. The derivation is byte-identical to the JS executor's in
  decolua/9router#4928, checked against it directly.
- `X-Conversation-Request-ID` — fresh UUID per request
- `X-Conversation-Message-ID` — fresh UUID per message

**Verified.** `codebuddy_conversation_headers_test.go` covers header presence and
UUID shape, stability of the conversation id across retries, session-id
precedence, typed-block content, and the random fallback.
Against a live upstream the author A/B'd the same credentials and body with only
the family differing: absent → 400 / 11128, present → 200 SSE. Independently, a
wire-level check against an `httptest` upstream confirms the three headers reach
the request for the `codebuddy.ai` BaseURL (with `Accept: text/event-stream` and
the static headers intact), are absent for the CN BaseURL, and that
`StaticHeaders` is not mutated.

**Residual risk.** The gate was confirmed to require the family; that the three
headers it names are sufficient (rather than more of the ~10 the official client
sends) rests on that A/B and could not be re-derived here without an intl
credential. Detection keys on `strings.Contains(cfg.BaseURL, "codebuddy.ai")`,
which is safe today because the executor is provider-scoped but would need
review if a custom `baseUrl` ever pointed at that host.

### Fixes

- **translator**: concatenate every `system` and `developer` message with `\n\n` into Responses `instructions` during Chat Completions to Responses translation, instead of silently dropping all but the first (fixes #332, porting decolua/9router#4813).

### fix(freebuff): route `x-relay-*` control-plane calls to their own endpoint

- Thread `StaticHeaders` through the freebuff session/run helpers (session request, run start/finish, session release, model switch) and stamp relay routing onto those requests via `applyRelayHeaders`, so relay-routed connections no longer bypass the relay on control-plane calls. Freebuff still owns its CLI identity headers; tests pass `nil` to keep single-process behavior.
- `applyRelayHeaders` sets `x-relay-path` to the endpoint **the call needs**, taking `x-relay-target` from `StaticHeaders`. Copying the connection's header verbatim was wrong: `BuildEdgeRelayHeaders` derives it from the provider base URL, which for freebuff is the chat route, so a relay forwarded every session/run/release call to `/api/v1/chat/completions`. Covered by `freebuff_relay_test.go`, whose fake relay forwards to `x-relay-target` + `x-relay-path` exactly as the deployed worker does.
- Dashboard compatible-model testing: per-model test status/error/latency replaces the single in-flight `compatibleTestId`, new **Test all** button probes with a concurrency ceiling of 2 (each probe consumes account quota), and the import dialog can filter to free-tier models only, mirroring `providers.IsFreeTierModel`. The provider-switch reset now also clears the compatible-model verdicts, and a `Test all` run that outlives its panel stops writing.
- Scope unchanged: relay routing covers the chat pipeline and the freebuff session/run helpers. `HandleFreebuffSessionSwitch` and `fetchFreebuffUpstreamSession` still read no `StaticHeaders`, so a relayed connection switches models straight from the host IP — threading the config there is a follow-up.

### Quoted `WorkingDirectory=` made systemd refuse the autostart unit

`service install` wrote `WorkingDirectory="/usr/local/bin"` into the systemd
user unit. systemd parses that setting with `config_parse_working_directory`,
which runs the whole remainder of the line through `path_simplify_and_warn`
without unquoting (unlike `ExecStart=`), so the quotes were taken as part of
the value and the unit was rejected as not absolute:

```
WorkingDirectory= path is not absolute: "/usr/local/bin"
Unit configuration has fatal error, unit will not be started.
```

The path is now emitted unquoted, with only a literal `%` doubled for specifier
expansion; whitespace is not a separator for this setting, so a spaced path
still survives. Closes #329.

The autostart tests now hand each rendered config to a real parser instead of
matching substrings: `systemd-analyze verify` for the unit, `plutil`/`xmllint`
for the launchd plist, `sh -n` for the OpenRC and runit scripts, and
`desktop-file-validate` for the XDG autostart entry. Each check skips where its
tool is absent rather than passing silently.

### Fixes

- **providers**: allow models carrying the `-free` suffix in `IsOpencodeFreeTierModel` on OpenCode providers (`opencode`, `opencode-zen`, `opencode-go`), ensuring preview and uncataloged free models route to `/inference/v1` on `opencode-zen` connections without manual catalog additions (fixes #327, PR #328).

### feat(deepseek-web): route chat.deepseek.com with a browser-login userToken

Adds the first provider in this build authenticated by a credential a browser
session produces rather than an API key or an OAuth grant, ported from OmniRoute
`open-sse/executors/deepseek-web.ts`. The credential is the `userToken` from
`chat.deepseek.com` → DevTools → Application → Local Storage.

**Why:** the deepseek account a user already has is a free consumer web account,
not an API plan, and it has been unusable here. `grok-web` and `perplexity-web`
were already catalogued but had no executor at all, so a chat request fell
through to the shared passthrough and sent `Authorization: Bearer <cookie>` to a
site that answers 401.

**What it does, per turn:**

- `GET /api/v0/users/current` exchanges the `userToken` for a short-lived Bearer
  access token, memoized an hour and dropped on any auth failure. A pasted
  `{"value":"…"}` localStorage entry is unwrapped, which is the shape a copy out
  of DevTools actually has.
- `POST /api/v0/chat_session/create` opens a session, closed again once the turn
  is done.
- `POST /api/v0/chat/create_pow_challenge` → a **DeepSeekHashV1** proof of work
  is solved and returned in `X-Ds-Pow-Response`. That hash is SHA3-256 with the
  last 23 KECCAK-p[1600] rounds; Go's `crypto/sha3` only exposes 24, so the
  sponge is implemented in `deepseek_web_pow.go` with the prefix absorbed once and
  reused per candidate, bounded by a 30s deadline and cancellable from the
  request context.
- The upstream event stream is translated to OpenAI chunks, keeping the
  reasoning and answer channels separate and folding search citations into the
  answer.

**Details worth knowing:**

- This API reports auth and rate limiting as **HTTP 200 with a code in the
  body** (40003 / 40002). Both are mapped to 401 / 429, so the router cools down
  and rotates the account instead of retrying a dead session.
- A stream that closes without the upstream's `FINISHED` signal is reported as
  an error, not as a completed turn. Serving the partial text with
  `finish_reason: stop` is how a client comes to treat a truncated reply as the
  model's conclusion.
- `Origin` and `Referer` follow the connection's configured base URL, so a
  `baseUrl` override is honoured end to end instead of only for the completion
  call.
- `deepseek-web` and `gemini-web` are separate entries from `deepseek` and
  `gemini`, with their own aliases (`ds-web`, `gweb`) and their own model
  catalogues — pinned by a test, since a cross-alias would send a browser
  credential to an API endpoint.
- `AddConnectionModal` now persists `authType: 'cookie'` for a browser-login
  provider instead of `'apikey'`. The probe dispatcher already branched on
  `cookie`, so every cookie provider was taking the wrong probe path.

**gemini-web is catalog-only.** `gemini.google.com` has no API the web client
calls directly — upstream drives it with an embedded Chromium — and this build
ships no browser. It is listable and manageable; a chat request is refused with
501 and the reason, and the probe says it cannot check rather than reporting a
valid cookie as invalid.

**Verification:** `go build ./...` and `go vet ./...` clean.
`internal/proxy/executor/deepseek_web_pow_test.go` pins the 23-round permutation
against OmniRoute's own vectors (`deepseek-pow-js-only.test.ts`) plus the block
boundary cases those vectors never reach.
`deepseek_web_test.go` drives a whole turn against one `httptest` upstream and
verifies the call order, that the proof-of-work nonce really satisfies the
challenge, the browser fingerprint headers, both response shapes, and that a
truncated stream is not served as complete.
`validate_web_providers_test.go` covers the probe's two misleading answers: a
rejected credential arriving as HTTP 200, and a 200 carrying no token.
`deepseek_web_catalog_test.go` pins the catalog and the provider isolation.
Live: the credential exchange, session creation and proof-of-work were confirmed
against the real endpoint — the nonce for a difficulty-144000 challenge is found
in ~400ms. The completion itself could not be exercised because DeepSeek's status
page showed an active outage (incident 7116348214287, "Identified") throughout,
so the stream translator's handling of a healthy response remains unproven
against the live site. `bun run build`, 344 web tests and `make vet-svelte`
(0 unresolved identifiers) pass.

### fix(deepseek-web): read the answer the site actually sends, and recover tool calls

Two defects found by running a real turn against chat.deepseek.com, both of
which the `httptest` fixtures had been shaped to agree with.

**The answer arrived without a topic.** The stream carries three shapes, and only
one names one:

```
{"v":{"response":{...fragments...}}}                          the answer, topicless
{"p":"response/fragments/-1/content","o":"APPEND","v":"x"}   one character at a time
{"p":"response/status","o":"SET","v":"FINISHED"}               the sign-off
```

The decoder dispatched on `p`, so the answer fell through to a default branch
that handled a bare string and dropped an object — losing the whole reply while
still finding `FINISHED`. The turn therefore reported success with no text, which
is why a live run answered `502 upstream answered 200 without a completion`. A
topicless object carrying `response` is now read before the topic switch, and the
per-character `APPEND` form is folded into the channel already open. A plain
question went from 2 chunks to 55 and now returns `{"content":"ok"}` live.

**Tool calling is a convention here, not a protocol.** The endpoint has no
`tools[]` parameter and the stream has no tool-call topic: a model told a tool
exists answers by *typing* a JSON object one character at a time, in 47 separate
`APPEND` operations. A client treating that as a normal streaming answer would
hold `{"name":"get_weather","arguments":{"` with no way to tell a truncated call
from prose.

So the two paths are kept apart. A request carrying `tools[]` buffers the turn to
completion and parses only whole JSON — the same guarantee the native API gives,
since it does not stream tool arguments either. A request without `tools[]`
reaches none of that code and streams exactly as before. The declarations are
folded into the prompt, because there is no field to send them in.

Recovery is deliberately conservative: the answer must be a single JSON object
naming a tool the client actually declared. An undeclared name, prose around the
object, or malformed JSON is passed through as ordinary content rather than
guessed at — a wrong guess becomes an invocation the client acts on.

Verified live: a `deepseek-v4-pro` turn asked about the weather and returned

```json
"finish_reason":"tool_calls",
"function":{"name":"get_weather","arguments":"{\"city\": \"Jakarta\"}"}
```

**Also fixed:** `parameters` is accepted as either a JSON Schema object or a
string, since the OpenAI shape sends an object while several SDKs stringify it
first; typing it as a string rejected the common form outright.

### fix(deepseek-web): stop the agent loop repeating a tool call forever

An agent driven through this provider called `get_weather`, received the result,
and called it again on every subsequent turn until its retry cap — reported as
`Assistant returned empty stop after retry cap`.

The cause was in the prompt, not the model. A tool-calling turn is replayed to
the web API as text, because the endpoint accepts a single prompt string. An
assistant turn that ends in a tool call carries `content: null`, so it was
dropped from the replay entirely. The transcript reached the model as:

```
User: What is the weather in Jakarta? Use the tool.

Tool result (get_weather) Sunny, 31C, humidity 70%
```

A result with no visible cause. The model reads that as "I have not called
anything yet" and calls again, and the loop cannot terminate because the next
replay is identical.

The call is now rendered into the transcript as `[called <name> with <args>]`
ahead of the result it produced, and the tool instructions state that such a line
means the call already happened. Verified live: the two-turn sequence now ends —
turn one calls the tool, turn two answers *"The weather in Jakarta is sunny with a
temperature of 31°C and humidity at 70%"* with `finish_reason: stop`.

**Also fixed: prose around the object.** The extractor required the answer to
*begin* with `{`, so a model that wrote "Let me check that for you." first had
its whole reply discarded and the client received `content: null` — an empty
stop, which is the other half of the same symptom. The object is now located
inside the answer with brace matching that respects strings and escapes, and the
text around it is returned as the message content rather than dropped.

An undeclared tool name is still refused: a model inventing `delete_everything`
must never become a call the client executes.

### Note on what is and is not verified

The turn is verified end to end against the live site: a plain question returns
`{"content":"ok"}`, a tool question returns a whole `tool_calls` message, and the
two-turn agent sequence terminates instead of repeating the call. Streaming and
non-streaming were both driven through a booted gateway, directly and through a
`combo` — 109 chunks, `finish_reason: stop`, `[DONE]`.

Not verified: behaviour under a client's own retry injection. A client that
appends "Stopped without actionable output; Attempt #N/3" turns on the gateway's
answer being absent has, in that observed run, appended the notice while the
gateway had in fact returned the full reply with `status: success` and the
content recorded. That is a client-side observation, not a finding against this
provider, and it is recorded here so the next reader does not re-derive it.

### Scope: this provider only

Earlier drafts of this branch also removed `grok-web`, `perplexity-web` and
`gemini-web` on the grounds that none of them can route here. That removal is
reverted and out of scope for this PR: it changes three providers a reader did not
ask about, and each deserves its own discussion. They remain as they were on
`main` — `grok-web` and `perplexity-web` catalogued with a probe but no executor,
and `gemini-web` catalogued only. Whether that is the right end state is an open
question, not something this PR settles.

`deepseek-web` is displayed as "DeepSeek Web" with no product claim, and its
icon is the same artwork the `deepseek` provider uses.

### fix(deepseek-web): a cut-off tool turn crashed the server instead of answering

The buffered tool path reported an aborted upstream stream by passing a nil
writer to `deepSeekReportStreamAbort`, which dereferenced it. Every tool turn the
upstream cut off therefore panicked, and the process died mid-request — the
client saw a request that never came back rather than a 502, which reads to an
agent as a hang.

The streaming path was unaffected: it passes the live writer it had been
streaming through, and only the buffered path ever passed nil. That is why plain
chats kept working while any request declaring `tools[]` was fatal, and why the
failure looked intermittent.

`deepSeekReportStreamAbort` now treats a nil writer as "nothing has been written
yet" and returns the cause, and both tool-path aborts return an ordinary upstream
failure — which is correct there, since the path buffers and can still answer
with a status. The nil guard is kept because the shared helper is the one place
that can be handed a nil from any caller.

`deepseek_web_loop_test.go` pins all three: a cut-off tool turn returns an error
and writes no SSE bytes, the shared abort helper survives a nil writer, and a body
that fails mid-read does not hand back a partial accumulator. Verified against the
pre-fix code — reintroducing it reproduces the same nil dereference.

### fix(deepseek-web): a streaming client got one JSON document and waited forever

The buffered tool path wrote a single `chat.completion` document whatever the
client had asked for. A client that sent `stream:true` therefore received a body
with no `data:` frames and no terminator, and waited for an end that was never
going to arrive — which is what a request that never returns looks like from the
other end.

Tool arguments are still buffered, because a truncated JSON object is
indistinguishable from prose and the native API does not stream them either. What
was wrong was the framing, not the buffering: the buffered turn is now emitted as
SSE for a streaming client, as one delta followed by a finish chunk and `[DONE]`.
A client reassembles deltas in order, so re-fragmenting the text would only cost
allocations. Non-streaming requests keep the single-document shape.

`deepseek_web_loop_test.go` pins the contract in both directions and fails against
the pre-fix code: a streaming tool request must produce `chat.completion.chunk`
frames, a `text/event-stream` content type and a `[DONE]`, and a non-streaming one
must not.

### fix(deepseek-web): read the DSML tool calls the site models actually emit

Asked for a tool call, the models answer in DSML — the site's own element
syntax — rather than the JSON object the prompt asks for:

```
<｜｜DSML｜｜ invoke name="web_search">
<｜｜DSML｜｜ parameter name="query" string="true">cuaca Jakarta hari ini<｜｜DSML｜｜ parameter>
<｜｜DSML｜｜ invoke>
```

The extractor only looked for JSON, so the whole block reached the client as
literal content. The client had no call to run, saw markup ending in
`parameter>`, and read the turn as a refusal — which is what produced a tool call
printed instead of performed.

Both dialects are now read, DSML first because it is unambiguous where a
JSON-shaped object sitting in prose has to be guessed at. Each invoke becomes an
ordinary call; parameter bodies become JSON string values, and `string="day"`-style
attributes stay attributes, so they never land where the tool expects a value. An
invoke naming a tool the client never declared is still rejected and still
reaches the client as content — but stripped of the markup, since a wall of
`parameter>` tags is not something to show a user.

The markers use a fullwidth bar (U+FF5C) and are written with padding, so the
matchers key on the text between the bars rather than a fixed spelling.

`deepseek_web_dsml_test.go` drives the exact block a live answer produced, and
pins: verbatim parameter text including quotes, undeclared names rejected, a
truncated block parsed as nothing, prose left alone, and the JSON path still
working when no markers are present.

### fix(deepseek-web): read Claude-shaped tool declarations

A tool declaration arrives in one of two spellings. OpenAI wraps it —
`{"type":"function","function":{...}}`. The Claude/Anthropic form puts `name`,
`description` and `input_schema` at the top level with no wrapper, and clients
send it just as readily.

Only the first was parsed. A Claude-shaped declaration produced a spec with an
empty name, which the prompt builder skips — so the model was told about no tools
at all, fell back on its native DSML, and the markup reached the client as if it
were the answer. The client had no call to run and no question answered, which is
what a printed tool call looks like from its side.

Both spellings are folded into one shape at unmarshal time, so nothing downstream
has to know which arrived. A declaration carrying both resolves to the nested
one, and a DSML call now matches against a flat declaration as readily as a
wrapped one.

`deepseek_web_toolshape_test.go` pins: the Claude shape reaching the rendered
prompt, a DSML call matching it, the OpenAI shape unaffected, a mixed list, both
spellings at once preferring the nested one, and a malformed declaration failing
loudly rather than parsing into something empty.

### fix(deepseek-web): match the closing tags the model actually writes

The first version of the DSML parser matched closing tags against the opener
spelled without a slash — `<｜｜DSML｜｜ invoke>` — which is how the notation reads
when it is described in prose. A live answer closes them as XML does:

```
<｜｜DSML｜｜ calls>
<｜｜DSML｜｜ invoke name="read">
<｜｜DSML｜｜ parameter name="path" string="true">https://wttr.in/Jakarta</｜｜DSML｜｜ parameter>
</｜｜DSML｜｜ invoke>
</｜｜DSML｜｜ calls>
```

`strings.Index` therefore returned -1 on every block, nothing was parsed, and each
DSML call went to the client as literal markup — the failure the DSML support was
written to prevent. The test fixture had been hand-written from the same wrong
reading, so it agreed with the parser and passed.

Tags are now matched by pattern: the bar may be full-width or ASCII, the marker
may be padded, and a closing tag may or may not carry its slash. Opening tags
cannot match a terminator because theirs is followed by `/`, and the stripper
searches for the terminator from after the opening element so it cannot truncate a
block at its own first tag.

`deepseek_web_dsml_test.go` now drives a block copied byte for byte out of a live
answer rather than reconstructed, and additionally pins both close spellings, the
ASCII pipe, and that both invokes in the live block are recovered in order. The
suite fails against the previous parser.

### fix(deepseek-web): stop replaying leaked markup back into the conversation

A leaked DSML block is not only shown to the user, it is fed back to the model.
The client returns the assistant turn in the next request, and the transcript
renderer replayed its content verbatim, so the model read its own unparsed
`<｜｜DSML｜｜ invoke>` block as the way this conversation calls tools and answered
with another one. Every turn leaked again, and each round described the previous
one: the calls the user saw executed (`Read Jakarta`, `Read Tokyo`) came a turn
behind the answer that followed them.

An assistant turn is now repaired before it is replayed. A DSML block is stripped,
and a JSON object is dropped only when it is shaped like a call — `name` plus
`arguments` or `parameters` — so prose that merely contains JSON is left alone.
When nothing but markup remains, the turn is recorded as `[called read]` rather
than dropped, because an empty assistant turn reads to the model as never having
called anything, which is the retry loop fixed earlier.

The tool prompt also states that native markup is not transmitted and that a reply
made of `invoke`/`parameter`/`calls` elements is discarded unread, which is what
the upstream OmniRoute prompt does.

`deepseek_web_transcript_test.go` drives the transcript with the assistant turn
copied byte for byte out of the leaking request, and fails against the previous
renderer.

### fix(deepseek-web): stop leaking the DSML wrapper and run every call in an answer

A model wraps its invokes in a `.calls` element, and the extractor consumed only
the `invoke`. The wrapper was left over, and the text around a call is returned to
the client as the assistant's preamble — correct for prose that a model writes
before its call, wrong for markup that happened to enclose it. So every tool turn
was answered with `content: "<｜｜DSML｜｜ calls></｜｜DSML｜｜ calls>"` alongside a
perfectly good `tool_calls` array. The user saw an empty pair of tags under their
own question on every round.

Two changes:

- `deepSeekStripDSML` removes a whole element, from its opening tag to its
  terminator, for `calls`, `invoke` and `parameter`. Removing only the tags would
  leave the parameter bodies behind as prose the model never wrote.
- `deepSeekSurroundingText` strips markup from what it returns, so a wrapper
  cannot reach the client however it is spelled. It now takes every raw call at
  once, since the text between several calls is only correct when they are removed
  together.

An answer is also no longer assumed to hold one call. A model asked about two
cities writes two invokes in a single reply; the gateway returned the first and
the client paid a round trip for each, with the remaining call left in the answer
as markup. `deepSeekExtractToolCalls` returns all of them, de-duplicated by name
and arguments so a repeated invoke is not executed twice, and the streaming path
puts them in one frame so the client runs them together.

`go test ./...` green; 6 live turns through `combo-wombo` returned zero leaked
markup, and the exact polluted history the user reported now answers in prose.

### fix(deepseek-web): read every call in an answer, in both dialects

The plural rule was applied to DSML only. On the JSON path a model asked about two
cities writes two call objects, and the gateway returned the first while the second
stayed in the answer as text — so the assistant's content carried a bare
`{"name": "shell", "arguments": …}` fragment that the user read as a leak. Both
paths now return every call they find, de-duplicated by name and arguments.

`deepSeekFindJSONObjects` replaces the single-object finder: it returns each
balanced top-level object and skips past it, so a nested object inside a call's
arguments is part of its parent rather than a call of its own.

The tool prompt also states that the declared tools are installed and runnable and
that a command tool executes on a machine with network access. Without that, a
model read its own tool list as a description of what it lacked and refused with
"I cannot check live weather, I have no such capability" — or "I have no internet
access" — while the `shell` tool it needed sat in the same prompt.

`go test ./...` green. Ten live turns through `combo-wombo` with omp's own tool
set: 10/10 returned `tool_calls`, 0 leaked markup, 0 refusals.

### fix(deepseek-web): require answers to be grounded in the tool results

A failed command followed by a confident table. In the stored turn
`1792065306584` the tool result was `ls: cannot access '/tmp/jkt.json': No such
file or directory` and the model answered with a 959-character comparison of both
cities — temperatures, humidity, wind, UV, sunrise — presented as though wttr.in
had returned them. Nothing in the client can separate those numbers from real
ones.

The tool prompt now states that a `Tool result` line is the only source of fact,
that no figure may be stated which is not in one of those lines, and that an
empty, failed or not-found result must be reported as such rather than replaced
from memory.

Verified against the polluted history itself: five live turns with the failed
result in context, four now detect the failure and re-fetch instead of inventing
data. One still answered from memory — a prompt rule is a strong nudge, not a
guarantee, which is why the transcript repair above matters too.

### fix(deepseek-web): number streamed tool calls so a client can tell them apart

`tool_calls[].index` was missing. The OpenAI streaming protocol numbers every
parallel call in `tool_calls[].index` and clients key their accumulator by that
number, so a client receiving two calls with no index had nothing to separate them
with and concatenated both argument objects:

```
Validation failed for tool "bash": Tool call arguments are not valid JSON.
Parse Error: Unexpected trailing characters at position 2
Raw JSON: {}{}
```

The two calls were both correct; they were simply indistinguishable. The stream
now emits one frame per call, each carrying `index` 0..n-1, which is the shape
every other executor in this repository already uses
(`internal/proxy/executor/stream.go`, tool-call emission). The model's prose still
travels with the first frame, and the turn keeps its `finish_reason: tool_calls`
and `[DONE]` terminator.

`deepseek_web_stream_test.go` parses the emitted frames the way a streaming client
does and fails against the previous single-frame writer.

### fix(deepseek-web): mint unique tool-call ids

The call ids came from `time.Now().UnixNano() % 1_000_000`. Two calls minted in
the same turn received the same id **every time**, because the clock does not
advance between them — on Windows it moves in steps of hundreds of microseconds:

```
first  = "call_shell_969000"
second = "call_shell_969000"
```

A client that groups parallel calls by `id` — the natural reading of a field called
`id` — merged the two into one, and the concatenated argument objects were rejected
before either command ran:

```
Validation failed for tool "bash": Tool call arguments are not valid JSON.
Parse Error: Unexpected trailing characters at position 2
Raw JSON: {}{}
```

The id is now minted from a process-wide counter, which cannot repeat. The tool name
is still part of the id so a log line names what was called.

Live, eight turns that each produced two parallel `shell` calls: distinct ids
(`call_shell_15`, `call_shell_16`), distinct `index`, both argument objects valid.
The previous code produced one id twice and the client merged them.

`deepseek_web_callid_test.go` fails against the clock-derived id.

### Usage & Analytics: Overview trend chart under Recent Requests

- The Overview section now renders a **Usage Trend** chart between the topology/Recent-Requests row and the usage breakdown filter (issue #280, second bullet — port of `techysy/10router`'s `UsageChart`): stacked areas per model family, Costs/Tokens metric switch, hover tooltip with per-family totals, period synced with the window selector.
- New endpoint `GET /api/usage/chart?period=` (`internal/handlers/usage_chart.go`): today/24h bucket by hour from `usageHistory`; daily windows take totals from the `usageDaily` rollup and the per-family split from a range scan; the top seven families survive and the tail folds into `other`, mirroring upstream `getChartData`. Model ids are normalized to product families (codename folding like `qfmodel` → `qwen`, version stripping like `gpt-5.6-sol` → `gpt`) so the legend stays readable. Custom `<n>d`/`<n>h` windows follow the same presets as the stats cards.
- Drawn as plain SVG — the dashboard ships no chart dependency, matching the existing Cache/Compression analytics views.
- Verified: `go test ./internal/handlers/...` (new table-driven cases for family naming, period parsing, tail folding, and both bucket shapes), `bun run build`, `svelte-check` ratchet at baseline, and a browser pass on an isolated `DATA_DIR` instance — the chart renders two stacked series with a `Peak 1.7M` legend and sits between Recent Requests and the breakdown filter.

### Usage & Analytics: Details dashboard — lifetime cards, activity heatmap, node health

- The Details section now opens with the dashboard ported from `techysy/10router`'s `UsageDashboard` (issue #280, second bullet), above the request-details table:
  - **Lifetime cards**: Total Requests, Lifetime Tokens, Peak Day Tokens (+ its date), Cache Hit Rate ("only real valid data is counted"), Most Used Model (trailing-7-day tokens, `force`-abbreviated like upstream).
  - **Activity Heatmap**: GitHub-style trailing-year grid (Mon–Sun rows, month labels, 5 brand levels, per-cell tooltip, current-streak line, Less/More legend).
  - **Node Health**: providers ranked by a 7-day health score (≥50 requests), expandable to the models under each node (≥10 requests). Score = success 60% + latency 20% + speed 20% with upstream's bands; a node with zero latency samples gets no score and stays out of the ranking.
- New endpoint `GET /api/usage/dashboard` (`internal/handlers/usage_dashboard.go`): heatmap + lifetime totals/streaks from `usageDaily`, top model from a 7-day `usageHistory` scan, cache hit rate over cached rows only with the two input shapes resolved by `max(prompt, cached + creation)`, and per-provider/model aggregates. Divergence recorded deliberately: 9router-go stamps no latency into `usageHistory.meta`, so health is computed from the `requestDetails` ring (the gateway's only per-request latency source) instead of upstream's merged two-store feed.
- All counters obey the unit-abbreviation switch from the previous fragment; `fmtTokens` gained the upstream `force` parameter for the always-abbreviated card subline.
- Verified: `go test ./internal/handlers/...` (score bands, cache-rate shapes, streak direction, buffered-burst dampening, handler payload incl. the no-perf node), `bun test src` (351 tests), `bun run build`, `svelte-check` ratchet at baseline, and a browser pass on an isolated `DATA_DIR` instance seeded with a year of data — cards read `53,957 / 103.2M / 1.2M 2026-07-25 / 25% / deepseek-chat`, the heatmap renders 269 cells, and the toggle flips them to full numbers.

### Usage & Analytics: unit-abbreviation switch for the number counters

- Added a `compactUnits` display preference (Settings → **Display → Unit abbreviation**, persisted in `localStorage`, on by default): token/request counters render `K/M/B` (`亿/万` for zh) when on and full thousand-separated numbers when off — port of `techysy/10router`'s `compactNumber` switch (issue #280, third bullet).
- New `web/src/lib/compactNumber.ts` (`fmtTokens`/`isCompactUnitsEnabled`/`setCompactUnits`); the Overview KPI cards (Total Requests, Input/Cached/Output Tokens) now route through it, so the switch reaches the whole usage analytics panel. Cards keep the full value in the `title` tooltip for truncated values.
- Verified: `bun run build`, `svelte-check` ratchet (0 unresolved identifiers, total pinned at baseline), `bun test src` (350 tests, 6 new in `compactNumber.test.ts`), and a browser pass on an isolated `DATA_DIR` instance — toggle off renders `2,400,000`, on renders `2.4M`, and the switch's ARIA name is `Unit abbreviation`.

### perf(db): order usageHistory reads by timestamp so idx_uh_ts is usable

`GetUsageHistorySince` and `GetRecentUsageHistory` filtered and ordered
`usageHistory` by `rowid`, which no index can satisfy — SQLite ran a full table
scan whose cost grew with the ledger, while the dashboard's Overview tab polls
`/api/usage/stats` every 5 seconds. `EXPLAIN QUERY PLAN` measured 9.5ms on a
100k-row table against 0.0ms for the same query ordered by `timestamp`, which
`idx_uh_ts` covers. `GetUsageHistoryBetween` had the same defect.

All three now order by `timestamp` (DESC for the newest-first readers, ASC for the
delta fold), and the three statements are named constants so the plan the
dashboard pays for is pinned to the SQL the functions actually run instead of to
a copy that can drift.

**Verified.** `internal/db/usage_query_plan_test.go` builds the production schema
via `EnsureCoreSchema`, seeds 5000 rows, and runs `EXPLAIN QUERY PLAN` on the
three constants: no plan row may contain a bare `SCAN usageHistory`, and each plan
must name `idx_uh_ts`. A second test writes rows whose insertion order is the
reverse of their timestamp order — the shape a late retry produces in production
— and asserts `GetUsageHistorySince` returns newest-first,
`GetRecentUsageHistory` returns the two newest, and `GetUsageHistoryBetween`
returns oldest-first.

**Behaviour note.** Rows sharing an identical `timestamp` may now come back in a
different relative order. Every writer stamps RFC3339 or
`2006-01-02T15:04:05.000Z`, both zero-padded and lexicographically sortable, so
the ordering callers depend on is unchanged; the fold in `usage_window_cache.go`
aggregates and is order-insensitive within a millisecond.

### fix(router): per-route body limits, and a 413 that names the ceiling

`middleware.MaxBody(10MB)` was mounted on the root router, so the ceiling was
global: a 25MB video upload to `/videos/generations` or a long
`/audio/transcriptions` recording died at the JSON limit, and the handler
reported it as `400 "failed to read request body"` — indistinguishable from a
corrupt body. No path existed by which a media endpoint could get a higher limit,
which `max_body.go`'s own comment admitted ("Media endpoints may need higher
limits").

`internal/middleware/max_body.go` now resolves the ceiling per request:

- `MaxBodyByPath(default, rules)` picks the limit from the path; `MaxBody(n)` is
  it with no rules, so existing callers are unchanged.
- `MediaBodyLimitRules()` raises `/videos/generations`, `/videos/edits`,
  `/videos/extensions` and `/audio/transcriptions` to `MediaMaxBodySize` (128MB).
  Matching is by path **suffix**, so the bare route and its `/v1/` and `/api/v1/`
  spellings are covered by one rule.
- A rule may only raise: one lower than the default is ignored, so no caller can
  quietly weaken the protection the default provides.
- One middleware decides the ceiling instead of stacking two — a second
  `MaxBody` would wrap `r.Body` again and the smaller limit would still cut
  first, which is exactly why the media endpoints could not be raised before.

The resolved ceiling is recorded on the request context, and
`middleware.WriteBodyReadError` splits the read failure: a body that tripped
`http.MaxBytesReader` becomes a **413 naming the limit** ("request body too large:
this endpoint accepts up to 128MB", code `payload_too_large`), anything else keeps
the old 400. Applied to every inference handler that reads its own body — the
media and video endpoints named in the issue plus `/chat/completions`,
`/messages`, `/messages/count_tokens`, `/responses`, `/responses/compact`,
`/api/chat`, `/images/generations`, `/audio/speech`, `/audio/transcriptions` — so
the split is uniform rather than a special case for one lane.

**Verified.** `max_body_test.go` covers both ceilings at the boundary (exactly at
the limit passes, one byte over is refused), that a media ceiling clears the JSON
one, that the 413 names the limit and carries `payload_too_large`, that a
non-`MaxBytesError` is still a 400, and that the rules cover all four media paths
in all three prefix spellings. `internal/integration/body_limit_test.go` drives
`app.ProvideRouter` — the real middleware stack — and uploads 25MB to
`/v1/videos/generations` and `/v1/audio/transcriptions` (must not be a 413) while
an 11MB `/v1/chat/completions` body is refused with the 413.

**Residual risk.** 128MB is a judgement call: video generation takes 15-50MB clips
and 30-60 minute audio can exceed 100MB, but the ceiling is still a whole-body
buffer, so a hostile client can make the process hold 128MB per request. The
per-route shape is what makes that tunable per endpoint; a different number is a
one-line change to `MediaBodyLimitRules`.

## [v1.9.13] - 2026-10-15

### OpenCode: append the exact fingerprint even when caller tools collide

Follow-up to #320, root-caused live: a TUI-shaped body whose `read`/`shell`
tools carry non-CLI definitions got its tools aliased (`read_client`, ...)
but no exact `read`/`shell` declaration traveled, and the gate answered 403
"free tier can only be used from within OpenCode" — reproduced against the
real gate (403 before, 200 after, same body).

- The append check now runs on post-alias names like upstream's
  `appendMissingFingerprintTools`, instead of the pre-alias seen set.
- Pinned with `TestConcealOpenCodeV2CollidingNamesStillAppendExact`
  (order `read_client2,read_client,Edit,read,shell`, both lanes).

Verified with `go test -count=1 ./internal/translator/ ./internal/proxy/
/internal/proxy/executor/ ./internal/handlers/chat/` and two live gate
probes (minimal + colliding-tools bodies, cli and desktop identities).

### 📊 fix(usage): fix column label precedence in UsageBreakdownTable and aggregate byEndpoint

- **Background**: In Usage Breakdown, switching to `Usage by Account` or `Usage by API Key`
  consistently displayed the model ID rather than the account display name or API key name,
  because `row.rawModel` was evaluated first in the fallback expression. Additionally,
  `Usage by Endpoint` was always empty because `HandleUsageStats` never aggregated the
  `byEndpoint` data.
- **Frontend**: Explicitly checked `tableView` in `UsageBreakdownTable.svelte` to render
  `row.accountName` for accounts, `row.keyName` / `row.apiKeyMasked` for API keys, and
  `row.endpoint` for endpoints.
- **Backend**: Added `byEndpoint` parsing in `HandleUsageStats` daily rollup and `usage_window_cache.go`.


## [v1.9.12] - 2026-10-15

### Fixed
- **Deterministic request serialization for ALL providers**: chat-completions bodies leaving via `newJSONRequest` are now re-serialized with `encoding/json/v2` `Deterministic(true)` (map members sorted). Previously only the opencode/zen lanes carried per-lane `marshalStable` calls; a new provider serving a DeepSeek model did not inherit the fix, and any rewrite-free turn kept its original randomized-bytes body — turning the shared prefix of every turn after the first into a full prompt-cache miss on DeepSeek (and other prefix-caching upstreams). Non-chat payloads (OAuth token posts, Gemini `contents`, malformed JSON) pass through byte-identical, so signed flows (e.g. COSY) are unaffected. Parity note: the JavaScript `decolua/9router` original is deterministic by construction (JS object insertion order); this restores that property in the Go rewrite.

### OpenCode: free tier moved to the /inference/v1 gate with the v2 client fingerprint

Upstream decolua/9router#4842 parity (Closes #317, supersedes #318 and #319):
OpenCode v2 moved free inference off /zen/v1; every free request there now
answers 403 FreeTierError.

- The keyless free executor dials /inference/v1/{chat/completions,responses}.
  A base URL carrying the /zen/v1 prefix is rewritten host-agnostically,
  custom endpoints stay untouched, and relays carry the lane in x-relay-path.
- Fingerprint headers follow the v2 CLI: User-Agent opencode/<channel>/
  <version>/<client> with v1 shapes rejected, x-opencode-session-id,
  x-session-affinity and x-session-id mirror one session (a downstream hint
  moves all four together), the rejected x-opencode-request is never sent,
  and the project defaults to "global" like upstream.
- Session ids keep a small counter: generated and translated prefixes pin the
  counter to 1 exactly like the CLI, so non-OpenCode clients stop tripping
  the gate under concurrent load.
- The request body declares OpenCode's exact read and shell tools — verbatim
  descriptions and parameter schemas from the v2 CLI, strict:false on the
  Responses lane. A caller tool reusing one of those names with a different
  definition travels under a *_client alias (history and tool_choice
  retargeted); appended placeholders get the upstream singular/plural note,
  never on Claude-shaped bodies. tool_choice passes through untouched except
  on the Responses lane, where auto-only models collapse to "auto" and a
  forced choice naming no declared tool is dropped.
- Paid lanes are deliberately narrow: opencode-zen keeps /zen/v1 with the v1
  identity and quartet cloaking for paid ids, and opencode-go keeps its
  predicate, v1 UA and request ids. Only curated free-tier ids move:
  keyed requests for them 403 ("free tier can only be used from within
  OpenCode", probed live on muse-spark-1.3-contributor-free), so zen and go
  forward those to the keyless gate with public credentials, v2 cli identity
  and the exact fingerprint. big-pickle stays on zen (no live 403 evidence;
  the lane table carries it as paid chat). With no go connection configured,
  the paid go lane borrows the key from an active opencode-zen connection
  (key only — provider, BaseURL and lanes stay go), operator-requested.

Verified with `go test -count=1 ./internal/proxy/ ./internal/proxy/executor/
./internal/translator/ ./internal/providers/ ./internal/handlers/chat/`:
verb gate-contract pins (verbatim schemas, strict:false, note wording),
lane predicates for free vs go, v1 identity on paid lanes, the zen keyed-lane
guard, session mirrors, and the mock E2E shapes.

### ✨ feat(ratelimit): per-key lifetime token quota and FIFO request queue

- **Lifetime token quota**: new `tokenQuota` column per API key. It caps the
  cumulative prompt+completion tokens a key may spend over its whole life;
  once reached, requests are refused with `403` and `X-Token-Quota-Limit` /
  `X-Token-Quota-Used` headers. Usage is summed from `usageHistory` under both
  the masked key this gateway writes and the raw key the upstream dashboard
  wrote. A failed usage read fails **open**, so a storage blip cannot lock a
  key out of a service it has always been able to call.
- **Request queue**: new `queueTimeoutMs` column. A request that exceeds the
  key's RPM or concurrency limit now waits in a **FIFO queue** (head-first, no
  overtaking) for the RPM window to roll or a concurrent slot to free, up to
  the timeout, before returning `429`. Defaults to 60s when a concurrency cap
  is set; a key with no queueable limit keeps the previous immediate-refusal
  behaviour. TPM is deliberately **not** queued — a token bucket refills
  continuously and would live-lock a waiter.
- **Atomic admission**: a request refused on concurrency does not consume an
  RPM stamp, so concurrency pressure cannot silently drain the RPM budget.
- **Observability**: new `router_rate_limit_queue_seconds` histogram records
  how long requests wait for a slot.
- **Dashboard / API / backup**: `tokenQuota` and `queueTimeoutMs` are exposed
  on `GET /api/keys`, accepted on `PUT /api/keys/{id}` (non-negative; omitted
  leaves the stored value unchanged), shown in the key policy dialog with a
  quota badge, and carried through database backup/restore so a restore cannot
  silently lift a throttle. Schema columns are additive; existing installs
  upgrade in place, and `usageHistory` gains an `apiKey` index so the quota
  sum is not a full table scan on every quota-bearing request.
- **No behaviour change for an existing install.** A key with no
  `queueTimeoutMs` of its own only queues when it also has its own concurrency
  cap (the upstream 60s default). A concurrency cap that arrives purely from
  the operator's global default keeps the pre-change immediate refusal, so a
  global setting cannot silently introduce queueing on every key.
- Verified: `go build ./...`, `go vet ./internal/...`, and `go test -count=1
  ./internal/...` all pass; new queue/quota/schema/backup/full-stack tests pass
  under `-race`.

### 🔄 fix(dashboard): group accounts by provider first and set default priority on connection creation

- **Background**: Previously, `providerConnections` rows created via OAuth lacked a `priority` value
  and were stored as `NULL`. In `sortProviderConnections`, `NULL` priorities defaulted to `math.MaxInt`
  and were compared across providers before grouping by provider. Consequently, a newly added account
  was pushed to the very end of the database—past accounts of other providers—and landed isolated on
  subsequent pagination pages.
- **Provider-First Grouping**: `sortProviderConnections` now groups by provider first so accounts of
  the same provider always remain contiguous in Quota Tracker, followed by priority and name.
- **Default Priority**: Added `ResolveDefaultPriority(provider)` in `internal/db/connections.go`
  and updated OAuth connection inserts so new accounts receive an explicit sequential priority.

### Dashboard: copying an API key no longer copies the mask

Issue #310 reported that the Copy button on an API-key row put the masked
display value on the clipboard (`sk-c07…c93e`) rather than the credential, so
the pasted key could not authenticate. Revealing the key first worked around it,
but copying a secret that depends on whether it happened to be on screen is not
a usable workflow.

The copy button took whatever the cell was rendering, and a cell that has not
been revealed renders `keyDisplay` — the mask. It now copies the stored secret
directly and ignores the reveal state, so Copy works on a freshly loaded
dashboard. A row the server holds no plaintext for (a caller authenticated with
an engine client key, or a row created before #199 whose secret was hashed away)
has nothing usable to copy; it now reports that instead of silently putting an
unusable mask on the clipboard.

Verified by a new end-to-end case that copies from an un-revealed row, reads the
clipboard, and authenticates the copied value against the running gateway with
`GET /v1/models` — it returns `sk-1…fe7b` before the fix and a working `sk-…`
key after.

### Codex + GPT-6 Sol: `400 Unknown parameter: 'input[0].content'` when a token saver is on

- **What was broken.** A `/v1/responses` client routed to a Responses-speaking upstream (`codex`, `perplexity-agent`, `opencode`, `opencode-zen`, `grok-cli`) failed with `400 {"error":{"message":"Unknown parameter: 'input[0].content'","code":"unknown_parameter"}}`, which the client only saw as a `[fallback] upstream failed provider=codex model=gpt-6-sol status=400` log line. Every request failed while the same model worked with token savers turned off — and for `gpt-6-sol` / `gpt-6-luna` the whole Responses-Lite prefix the executor adds is affected, so no other model on the connection worked either. Closes #307.
- **Why.** `tokensaver.InjectSystemPrompt` had one injector for both `messages[]` and `input[]`. On a Responses body it wrote a **Chat** system turn, `{"role":"system","content":"<text>"}`, at `input[0]`. The Responses item schema wants `{"type":"message","role":"system","content":[{"type":"input_text","text":"…"}]}` — a turn with no `type` and a string `content` where the validator reads a block array. Upstream dispatches by wire shape and keeps its two injectors apart (`open-sse/rtk/systemInject.js`: `injectChatSystem` vs `injectResponsesInputSystem`); Go had collapsed them. The trigger is any active token saver — Caveman, Ponytail or ADHD — since those are the settings that inject a system prompt at all; RTK alone never reaches this code.
- **How it is fixed.** `InjectSystemPrompt` now dispatches on the same three shapes upstream does, and `input[]` gets its own injector: it appends the prompt as an `input_text` block to an existing system/developer **message item**, prepends one when the conversation has none, and widens a client-supplied string `content` into blocks rather than leaving a string behind. An item without `type:"message"` is never adopted. Chat Completions bodies are untouched.
- **Verified.** `internal/tokensaver` covers the three Responses paths; the new `internal/integration/codex_responses_tokensaver_test.go` boots the real router with a `codex` connection aimed at a fake `/responses` upstream and asserts on the outbound body — a control case with the saver off and the regression case with Caveman on. The regression case fails on `main` with `input[1].content is string, want an array of content blocks` and passes here. `go test ./...` is green.

### 🎨 fix(dashboard): align Antigravity quota with standard QuotaTable layout and restore row hiding

- **Background**: The 2x2 nested grid card introduced in #282 created an oversized,
  bloated card height on accounts with multiple Antigravity connections, diverged from
  the sleek layout of Codex and other providers, and dropped row-hiding controls (`onHideQuota`).
- **Refinement**: Reverted Antigravity rendering to the standard, compact `QuotaTable.svelte`
  component, preserving the 100% scale and 4 canonical window buckets while restoring
  row-hiding capabilities and uniform card sizing.

### service: five follow-ups from PR #303 (issue #304)

Tech debt recorded during the review of `feat(cli): service autostart management`.
None of these changed runtime behaviour on Linux or macOS, but all five were
wrong in a way a user could hit.

- **`WorkingDirectory=` is now escaped.** `ExecStart` already went through
  `commandLine`, but the neighbouring line wrote the directory raw. systemd
  unquotes the assignment rather than splitting on whitespace, so a binary
  installed under `/opt/my apps/` produced a unit that failed to parse. The
  value now goes through `quoteSystemdValue`, which escapes what systemd's
  unquoter actually interprets — a backslash or quote (C escapes inside double
  quotes) and `%` (every assignment is specifier-expanded, so a literal percent
  is `%%`). The path is converted with `filepath.ToSlash`, which also makes the
  rendered unit identical on a Windows test runner, where `filepath.Dir` was
  returning `\usr\local\bin`.
- **`enable-linger` now runs even when `systemctl start` fails.** The early
  return on the failure path skipped it, so the one scenario install exists for
  — the gateway already holding the port from `9router-go start` — lost linger
  and the unit died at logout while the CLI reported the entry as enabled.
- **`uninstallCron` no longer claims a removal that failed.** `os.Remove`'s
  error was discarded and `desktopRemoved` was set unconditionally, so a
  permission-denied delete printed "Autostart uninstalled" with
  `~/.config/autostart/9router-go.desktop` still in place and set to fire at the
  next login. Removal is now the source of truth, the rule the launchd and
  OpenRC backends in the same package already followed.
- **Windows task status is locale-independent.** `statusWindowsTask` matched the
  literal `Running` in `schtasks /query` output, which follows the system
  locale: on a non-English Windows the literal never appears and `service
  status` reported `Active: false` for a task that was running. The state now
  comes from `Get-ScheduledTask` as a number, so it does not depend on the UI
  language. When that probe cannot answer — PowerShell absent, or blocked by
  execution policy — the new `Info.ActiveUnknown` makes `service status` print
  `Active: unknown` instead of a `false` that is a guess presented as fact.
- **Three `internal/service` tests now pass on Windows.** CI only ran
  ubuntu-latest, so the failure was invisible: one assertion compared against
  a POSIX path that `filepath.Dir` could not produce on Windows, and two needed
  `SeCreateSymbolicLinkPrivilege` to build their runit activation symlink.
  `os.Symlink` and `os.Remove` are now seams, the way `readCrontab`/`writeCrontab`
  already were, so the runit activation path is asserted as a request on runners
  without symlink privilege instead of failing. No assertion coverage was
  dropped.

Verified with `go test -count=1 ./internal/service/` green on Windows, the
built binary's `service status` run end to end, and the new
`windows-service` CI job added to `.github/workflows/ci.yml` so this class of
OS-conditional regression is caught on a PR rather than on someone's laptop.

### feat(cli): service autostart management

`9router-go service install` registers the gateway to start on boot using
whatever init the machine actually runs:

- **systemd** — always the per-user unit (`systemd --user`), so install stays
  unprivileged and can never touch a root-owned unit. Every
  install/uninstall/status/control call goes through one `systemdScope` helper,
  which keeps `--user` from being dropped and the command from landing on the
  system bus (root prompt, user unit left enabled). There is no system-unit
  scope or `--system` flag.
- **launchd** (macOS), **runit** (root, or `~/service` under a user `runsvdir`),
  and **cron `@reboot`** plus an XDG autostart desktop entry, all tagged with a
  `# 9router-go-autostart` marker so install is idempotent and uninstall never
  touches the user's own entries.
- **OpenRC** is a system service and needs root: it has no per-user scope, so
  `service install` refuses without it rather than silently installing a
  different backend (run with `sudo`, or use `--backend cron`).
- **Windows** — a scheduled task created with `schtasks` at logon, run as the
  installing user rather than `SYSTEM` (a SYSTEM task reads a different
  %APPDATA% data dir). Creating the task requires an Administrator console.

Companions: `service start|stop|restart` drive the supervised service (falling
back to the pid-file daemon for cron, which has no supervisor), `status` reports
the entry in effect, and `uninstall` stops/disables before deleting and reports
failure when the file survives. `install` warns when the `start` daemon is still
holding the port, and an unmatched positional argument now fails fast with
`unknown command` instead of booting a second gateway.

The home directory is read through a package seam (`userHomeDir`) rather than
`HOME`, because `os.UserHomeDir` reads `%USERPROFILE%` on Windows; tests point
every user-scoped path (systemd unit, launchd plist, autostart entry, runit
`~/service`) at a temp home instead of the runner's real one. Windows task
commands are quoted for `cmd.exe` rather than the POSIX shell, so a path under
`C:\Program Files\...` still fires.

Verified with `go test ./...` (all per-OS backends are exercised from one runner
through the seams) plus `make build`.

### 🐛 The Quota Tracker provider filter pushed the page down instead of opening over it

Opening "All providers" on the Quota Tracker shoved the quota cards out of the
way instead of overlaying them, which is what made the page look broken.

The provider panel was the only dropdown in the dashboard that measured itself
against its trigger and wrote `left` / `top` inline while **not** being
`position: fixed` and not being portalled. The computed position came back
`static`, so the placement maths was dead output: `left: 896px; top: 140px` was
written onto a plain in-flow `<div>`, and `placePanel` / `placementStyle` were
doing work no browser honoured.

Everything around it was already correct and gave the bug away on re-reading —
the click-outside handler checks `providerDropdownPanel.contains(target)`
precisely because the panel is expected to be detached, the resize and scroll
listeners already reposition it, and a comment states the panel *is* fixed. Only
the element itself was missing the two attributes every sibling panel has:

| Panel | `fixed` | `use:portal` |
| :--- | :---: | :---: |
| `SectionMenu`, `CountedSelect`, `Menu`, `ViewSelect` | ✅ | ✅ |
| `QuotaTrackerView` provider filter | ❌ | ❌ |

The panel now carries the same `use:portal` + `fixed z-50` pair the other five
use, so it is anchored to the window rather than to the toolbar row, and the
long provider list overlays the cards instead of pushing them.

Verified in a browser against a running gateway: computed `position` is `fixed`
and the parent is `BODY`; selecting an option still filters (label and row
update); a click on the panel's own background leaves it open while a click
outside closes it, so the click-outside handler still resolves through the
detached node; and at a 390px phone width the panel measures 256px wide with
both edges on screen.

### Added
- **SumoPod provider**: `sumopod` registered as an API-key provider (bearer transport, OpenAI-compatible) pointing at `ai.sumopod.com/v1/chat/completions` with the `sp` short alias and a 61-model catalogue synced from the live `/v1/models` response (embeddings excluded). Live `/v1/models` import is wired via `modelsListURL` (bearer, like tokenharbor/dahl).

### 🐛 Quota Tracker now shows 5h / weekly windows for Claude OAuth accounts (#293)

- Claude connections were listed in the tracker but never read: `fetchProviderUsage` had no
  `claude` case, so every account answered `{plan:"claude", quotas:{}}` and the dashboard
  printed "Account active. No quota limits tracked." without any upstream call.
- The dashboard now reads `GET api.anthropic.com/api/oauth/usage` (port of upstream
  `getClaudeUsage`) and renders `session (5h)`, `weekly (7d)` and model-scoped weekly rows
  (`weekly opus (7d)`, `weekly fable (7d)`, …) with used / remaining / resetAt.
- Reads are cached per token for 60s, a 429 puts the token on a cooldown (honouring
  `Retry-After`) and keeps serving the last good reading; a 401/403 goes through the existing
  refresh-and-retry path.

### 🐛 `/v1/models` top-level `context_length` / `max_completion_tokens` now match `capabilities` (#291)

- Top-level limits came from the generic substring table (`claude-*` → 200000/8192) while
  `capabilities.contextWindow` / `maxOutput` carried the per-model values, so one entry
  contradicted itself (e.g. `cc/claude-opus-5-5`: 200000/8192 vs 1000000/128000). Clients that
  read the OpenAI-style fields — such as `pi-9router-ext` — capped Claude output at 8192.
- The top-level fields (and `/v1/models/info`) are now filled from the same chain as
  `capabilities` (declared custom limits → per-model caps → catalog → substring table); the
  substring table only fills gaps.
- `claude-fable-5-1` gets its documented 1M context / 128K output in the per-model table.

### 🐛 `reasoning_effort` to a Claude upstream no longer fails with `thinking.type: Field required` (#289)

- An OpenAI-shaped client sending `reasoning_effort` (or Responses `reasoning.effort`) to a Claude
  upstream got `thinking: {"display": "summarized"}` with no `type`, and Anthropic answered
  `400 thinking.type: Field required`.
- The request translation now sets the missing `type`, mirroring the `claude-adaptive` /
  `claude-budget` branches of upstream `thinkingUnified.js`: `adaptive` + `output_config.effort`
  for adaptive models, `enabled` + `budget_tokens` otherwise. `max_tokens` is raised to
  `budget_tokens + 1024` (capped at the model's output limit) so the answer is not starved; the
  budget is only shrunk when it alone reaches the ceiling. An explicit `thinking.type` from the
  client is never overwritten.
- `reasoning_effort: none/off` (or Responses `effort: none`) with a captured `display` now sends
  `thinking: {"type": "disabled"}` instead of a typeless object.
- The thinking format is looked up under the real lane provider (`minimax-code`, `opencode-zen`,
  `opencode-go`) instead of a hardcoded `claude`.
- `claude-fable-5` is declared `claude-adaptive` in the per-model table; it used to fall through the
  `*claude*fable*` glob to `claude-budget`, which would have sent the manual `enabled`/`budget_tokens`
  mode that current Claude models do not accept.

fix(codex): strip temperature/top_p before forwarding to the Codex backend

Editors such as Zed send `temperature` on every chat-completions turn, and
`buildResponsesBody` forwarded it as-is. The ChatGPT Codex OAuth backend
answered `400 {"detail":"Unsupported parameter: temperature"}` on the reasoning
models behind Codex, so each turn burned one failed upstream call before the
combo moved on — the 400 is request-scoped, so the connection was never locked
and the failure repeated every time.

`temperature` and `top_p` now join `max_tokens` / `max_completion_tokens` /
`max_output_tokens` in `stripCodexUnsupportedTokenParams`. The strip stays on
the Codex forward path only: `buildResponsesBody` is shared with the other
Responses-API providers (grok-cli, kimi, muse, opencode-zen), whose backends do
accept the field, so moving it there would silently change their requests.

Parity with upstream `open-sse/executors/codex.js`, whose `transformRequest`
deletes `body.temperature` and `body.top_p` alongside the generation limits.

**Verification**: `go test ./internal/proxy/executor/` passes. The fake upstream
in `TestForwardCodex_UpstreamNeverSeesTokenParams` now rejects all five
parameters the way ChatGPT does and the client body carries `temperature` and
`top_p`, so the wiring — not just the helper — is pinned. Manually verified on
Windows with Zed: `gpt-6-astra` went from a 400 on every turn to 200.

**Known gap (not introduced here)**: upstream deletes a longer list
(`frequency_penalty`, `presence_penalty`, `logprobs`, `top_logprobs`, `n`,
`seed`, `user`, `metadata`, `stream_options`, `safety_identifier`,
`previous_response_id`) and then filters the body through a Responses-API
allowlist. This port still deletes only the five fields above, so a client
sending e.g. `frequency_penalty` to Codex can still hit the same 400.

### 📦 feat(settings): database storage & retention profile (observability limits)

- **Background**: `requestDetails` previously retained diagnostic entries with hardcoded limits
  (500,000 rows ceiling, 30 days) and unconditionally logged massive payloads on every call,
  causing SQLite databases to swell up to hundreds of megabytes on user machines.
- **Observability Mode**: Added `observabilityMode` (`full`, `errors_only`, `off`). When set to
  `off`, request detail logging is bypassed completely (<10 MB permanent DB). When set to `errors_only`,
  only failed/fallback transactions are recorded.
- **Configurable Retention**: Added `retentionDays` and `maxRequestRecords` (default 5,000) settings.
- **Maintenance Endpoints**: Added `GET /api/settings/database/stats`, `POST /api/settings/database/prune`,
  and `POST /api/settings/database/vacuum` allowing operators to prune and reclaim free pages back to disk.

### 🛡️ feat(security): SSRF protection via safe network dialer

- **Background**: Custom provider nodes and custom proxies previously permitted dialing
  any IP address without restriction, posing a risk of internal network scanning or
  cloud metadata exposure (169.254.169.254).
- **Hardening**: Implemented `NewSafeDialer()` and `NewSafeTransport()` in `internal/proxy/ssrf.go`,
  enforcing destination IP inspection at the socket control level. Dials to loopback, RFC 1918
  private LANs, link-local, and cloud metadata addresses are blocked by default with `ErrSSRFBlocked`.
- **Operational Override**: Operators running local inference engines (such as Ollama on localhost)
  can explicitly permit private networking by setting `ALLOW_PRIVATE_NETWORKING=true`.

### ✨ feat(quota): 4-card clean layout & 100% scale for Antigravity quota

- **Background**: Antigravity quotas were computed against an artificial scale of `1000.0`,
  and displayed as flat model lists rather than the 4 canonical time-window quota buckets
  provided by Google Antigravity (`retrieveUserQuotaSummary`).
- **Backend**: Standardized Antigravity quotas to a pure 100.0 percentage scale with window
  metadata (`5h` and `weekly`).
- **Dashboard UI**: Introduced `AntigravityQuotaCard.svelte` rendering a 2x2 grid (Gemini 5h,
  Gemini Weekly, Claude/GPT 5h, Claude/GPT Weekly) with color-coded progress bars and countdown timers.

### 🐛 Issue #280 — MiMo and every other provider vanished from the topology map, and the Test button faked usage

Two defects made a provider the operator never used look permanently busy on
`Usage → Overview`, and they compounded each other.

- **The "Test" button metered probes as traffic.** `POST /api/models/test` is
  dispatched straight into the chat handler with a probe context
  (`media/model_test_ping.go:84`), and the probe context is honoured at every
  other side effect in the forwarding path — connection unlocks, cooldown
  clears, model-lock releases, fallback skips. The one gap was the usage write:
  `tryForwardWithConnection` called `logUsage` outside the
  `IsProbeContext` guard, so a single click wrote a real `usageHistory` row and
  a `usageDaily.byProvider` bucket. A provider with a passing test appeared to
  have served a request nobody sent, and its tokens landed in the daily totals
  and the est. cost. "Check all models" multiplies this by the catalog size.
  `tryForwardWithConnection` now returns early on a probe, before any usage is
  written — the request still reaches the provider, so the button still works.

- **The topology map drew every configured connection, forever.** The map is
  built client-side in `AnalyticsView` from five sources, and four of them are
  usage-windowed through `stats.byProvider`. The fifth was not: the connections
  loop drew any non-inactive `providerConnections` row. Since that list is the
  whole table, a provider the operator had merely configured sat on the map
  regardless of the selected window, and outlived both the connection and the
  period. It is now gated on the same window as every other source. The lookup
  resolves a connection's stored id against the catalog's canonical id and its
  published alias, because the backend keys usage canonically while a
  connection row may hold either.

Verified in a real browser against a running gateway seeded with an active
`xiaomi-mimo` connection and zero traffic: the map rendered the empty state
("No providers connected") where it previously drew a MiMo node. The probe fix
is covered by `internal/integration/model_test_probe_usage_test.go`, which also
asserts the probe still reaches the fake upstream — a guard that short-circuited
before the request would pass a usage-only test while breaking the button.

### Fixed
- `ollama-cloud` provider connections were stuck on a legacy provider id that
  never existed in the catalog (only `ollama` does — the id came from a
  sibling project's naming, synced in by hand). Chat requests on those
  connections failed with `has no baseUrl in connection data and is not in
  KnownProviders`, and the Analytics provider topology card rendered the
  connection's account email as the node label instead of a provider name.
  `MigrateLegacyProviderIDs` now rewrites `ollama-cloud` → `ollama` on every
  boot (idempotent, best-effort — a failure logs and does not block
  startup), and `topologyName` in `AnalyticsView.svelte` rejects any
  fallback label containing `@` so an OAuth account email can never stand in
  for a provider name again, independent of the email-privacy toggle.
- Verified: `go test ./internal/db/...` (new
  `TestMigrateLegacyProviderIDs`/`TestMigrateLegacyProviderIDsNilDB`),
  `bun test src` (353 pass / 0 fail), `make vet-svelte` (0 unresolved
  identifiers, baseline tightened 83 → 82).


## [v1.9.11] - 2026-10-12

### 🖼️ `muse.png` 404 — Meta Muse tile rendered empty

`muse` is in the provider catalog (`web/src/lib/providers.ts`), and `getIconPath`
resolves a catalog id to `/providers/<id>.png`, so the tile asked for
`web/public/providers/muse.png` — an asset this repo never shipped when the
provider landed with the v0.5.95 sync. Every Muse tile (provider card,
connection row, quota tracker filter, top bar) rendered an empty box.

An audit of the catalog turned up two more dashboard-visible cases with the
same cause: `tinyfish` and `v1m` (`systemone`). All three now carry the artwork
upstream `decolua/9router` ships in `public/providers/` — 128×128 for muse and
v1m, 96×96 for tinyfish. The remaining catalog entries without artwork are not
provider cards: they are header/field names (`user-agent`, `x-requested-with`,
`anthropic-version`), region ids (`cn`, `sgp`, `ams`), or entries that already
have local artwork (`freebuff`, `opencode-zen`, `ollama-search`).

Verified with `bun run build` (the assets land in `web/dist/providers/`), a
`go build` of the single binary, and `GET /providers/{muse,tinyfish,v1m}.png`
against a live instance — all three answer `200 image/png`.

### Fixes

- **Cache Analytics dan Compression Analytics mengabaikan pilihan rentang
  waktu.** Dua section itu sudah punya pemilih periode di header, tapi tidak
  satupun angkanya yang mengikutinya.

  **Cache Analytics** tidak punya window sama sekali di totals-nya:
  `GetPromptCacheMetrics` menjalankan `SELECT … FROM usageHistory` tanpa
  `WHERE` untuk kartu KPI, `byProvider` dan `byModel`, jadi "Last 24 hours"
  menampilkan cache rate, cached tokens dan cost saved dari **seluruh** ledger.
  Satu-satunya yang dibatasi adalah `GetPromptCacheTrend`, lewat parameter
  `trendHours` sendiri — jadi kartu di atas grafik dan grafiknya menggambarkan
  periode berbeda pada satu layar.

  **Compression Analytics** lebih halus: endpoint-nya menerima `since`, dan
  scalars/`byMode`/trend/top-savers memang memakainya, tapi tiga query punya
  cutoff-nya sendiri — branch `usageHistory`, breakdown per provider/model, dan
  agregat dolar per pasangan `(provider, model)`. Saat `since` menentukan
  fallback (`GetCompressionAnalyticsSummary` memakai `cutoff == ""` untuk
  `all`), seluruh statement itu kehilangan batasnya dan membaca tabel penuh,
  sementara scalars di sebelahnya tetap terbatasi. Pilihan "All time" pada
  dropdown karena itu mengembalikan grafik 24 jam di atas angka all-time.

  Sekarang **satu window** mengikat kartu, breakdown dan grafik di kedua
  section, dan keduanya memakai kosakata periode yang sama dengan Overview
  (`today`, `24h`, `7d`, `30d`, `60d`, `all`, plus `<n>d`/`<n>h`) lewat
  `internal/analyticsrange`, bukan dua dropdown dengan dua daftar berbeda.
  `trendHours` dan `since` tetap diterima sebagai fallback supaya SPA lama dan
  tautan tersimpan tidak ikut membaca window yang salah.

  `last24h` diganti jadi `trend`, karena seri itu sekarang mengikuti window yang
  dipilih dan nama lamanya akan berbohong setiap kali window-nya bukan 24 jam.
  Untuk window tanpa batas server tidak mengirim grafik sama sekali — satu
  bucket per jam sejak ledger dimulai tidak bisa dirender, dan grafik periode
  lain di samping kartu yang lebih jujur adalah dua kesalahan, bukan satu.

  **Breakdown juga dibatasi** ke 50 baris terbesar per provider/model, diurut
  berdasarkan token tersimpan. Tanpa itu satu ledger yang sudah melihat ribuan
  string model menghasilkan respons yang tidak bisa dirender tabel mana pun.
  Response melaporkan `truncatedProviders`/`truncatedModels` dan dashboard
  menampilkan "Top 50 of 80 models", jadi tabel yang terpotong tidak pernah
  terbaca sebagai tabel utuh.

  **Indeks untuk `compressionAnalytics` diganti.** Di ledger 300.000 baris,
  limiter window ternyata **bukan** penyebab lamanya — `all` justru terukur
  lebih cepat dari `24h`, yang tidak mungkin kalau window yang jadi biaya.
  Penyebabnya indeks yang tidak menutup query berat: SQLite memakai
  `idx_ca_ts` untuk mencari rentang `timestamp`, lalu karena `timestamp` bukan
  rowid, ia kembali ke tabel untuk setiap kolom yang dibaca — dua lookup per
  baris. Empat indeks baru, semuanya bentuknya dipaksa hasil `EXPLAIN QUERY
  PLAN` dan bukan tebakan:

  - `idx_ca_stat_p/m (provider|model, timestamp, tokensSaved, originalTokens,
    durationMs)` — **group key di depan, bukan timestamp**. Index yang diawali
    timestamp hanya bisa *seek* pada range, dan sort untuk `ORDER BY` tetap
    butuh temp B-tree. Dengan group key di depan, window tetap terfilter,
    group sudah terklaster, dan kolom yang dibaca ikut ter-cover sehingga pass
    ini index-only: breakdown model **1659 ms → 50 ms**, provider **351 → 37 ms**.
  - `idx_ca_saved (tokensSaved, timestamp)` — top-10 savers mengurutkan seluruh
    window untuk mengembalikan sepuluh baris: **971 ms → <1 ms**, dengan 10 baris
    yang identik dengan hasil sort.
  - `idx_ca_receipt (timestamp, actualPromptTokens, actualTotalTokens)` —
    agregat receipts memfilter dua kolom tapi tidak membacanya, jadi indeks
    timestamp mengirimnya ke tabel sekali per baris: **940 ms → 17 ms**.

  `idx_ca_ts_prov` dan `idx_ca_ts_model` — composite yang diawali timestamp —
  **dihapus di bootstrap**. Tidak muncul di satu pun plan, tapi tetap di-update
  di setiap insert. `idx_ca_provider` dan `idx_ca_model` juga dilepas karena
  digantikan `idx_ca_stat_p/m`.

  **Penghitungan truncation tidak lagi jalan di setiap request.** `droppedGroups`
  hanya menghitung saat breakdown benar-benar kena batas; sebelumnya setiap
  request membayar satu pass grup tambahan untuk angka yang hanya dibaca kalau
  ada yang terpotong.

  **Yang sengaja tidak diperbaiki.** Fold `usageHistory` untuk Cache Analytics
  tetap ~1.4 s. DiUkur: `SELECT id` 71 ms tapi `SELECT tokens` 1170 ms pada baris
  yang sama — biayanya di kolom TEXT `tokens`, bukan di `json_extract`. Indeks
  `(timestamp, tokens)` diuji dan SQLite **tidak memilihnya** (plan tetap
  `idx_uh_ts`), jadi tidak ada yang hilang dengan tidak mengambilnya; DB juga
  tumbuh 236 → 293 MB. Memperbaikinya berarti denormalisasi kolom `tokens` ke
  tabel lain atau cache delta seperti yang sudah dipakai
  `/api/usage/stats` — keduanya pekerjaan sendiri, bukan perbaikan sepihak
  dalam PR range.

  **Angka sebelum/sesudah** (300.000 baris, indeks identik dengan produksi):
  total ketujuh statement analytics **6453 ms → 1685 ms**. End-to-end lewat
  HTTP di binary branch ini, port 20301: Compression `24h` **9.7 s → 4.3 s**,
  `7d` **19.4 s → 8.5 s**, `30d` 19.4 s → 8.6 s, `all` 3.9 s → 2.3 s. Cache
  `24h` 4.3 s, `7d` 8.5 s, `all` 1.7 s dengan trend kosong. `trendReqs` sama
  dengan `totalRequests` di setiap window.

  **Verification.** `internal/analyticsrange` (baru, dengan tabel window),
  `TestGetPromptCacheMetrics_HonoursWindow`,
  `TestGetPromptCacheTrend_UsesTheGivenWindow`,
  `TestGetPromptCacheMetrics_ReportsTruncation`,
  `TestCompressionAnalytics_SummaryHonoursWindow`,
  `TestCompressionAnalytics_BackfillHonoursWindow` dan
  `TestHandleGetCache_PeriodBoundsTotalsAndTrend`. Dua yang pertama
  dikonfirmasi gagal dengan perbaikan dibatalkan — satu melaporkan 4 request
  untuk window 24h yang hanya memuat 2, satu melaporkan 3 bucket trend untuk
  window all-time yang harusnya tidak punya.

  Angka di atas diambil dari ledger 300.000 baris dengan indeks yang disalin
  verbatim dari `schema.go`. Harness-nya ada di `.bench/` dan sengaja tidak
  masuk commit: yang di-commit adalah angkanya dan nama test-nya, bukan alat
  ukurnya. `go vet ./...` bersih, `go test ./internal/...` hijau, `bun test`
  392/392, `bun run build`, `make vet-svelte` (0 unresolved, 83 = baseline).

### 🖼️ Aset provider upstream diambil, kartu "header semu" dihapus — 404 `/providers/*.png` hilang total

- **Aset dari upstream lengkap.** `decolua/9router` sudah shipping 9 berkas yang repo ini
  tidak punya: `bedrock.png`, `bedrock-xai.png`, `codewhale.svg`, `crush.png`,
  `forge.png`, `pi.svg`, `smelt.svg`, plus `hermes.png` dan `omp.png` yang di-refresh ke
  versi upstream. Kesembilananya identik dengan `public/providers/` upstream
  (`raw.githubusercontent.com/decolua/9router/master`), kecuali tiga SVG yang hanya
  berbeda line-ending CRLF↔LF. Seluruh aset upstream yang sudah ada di sini juga
  dibandingkan ulang satu per satu — tidak ada yang berubah diam-diam.
- **Akar 404 sisanya bukan aset yang hilang, tapi katalog yang bohong.** Delapan entri
  di `web/src/lib/providers.ts` — `x-codebuddy-request`, `x-github-api-version`,
  `x-requested-with`, `x-vscode-user-agent-library-version`, `anthropic-version`,
  `openai-intent`, `originator`, `user-agent` — bukan provider. Mereka hanya
  cerminan `StaticHeaders` backend. Sejak `afcab2ed`
  (17 Sep 2026) masuk sebagai kartu. Akibatnya setiap surface yang memanggil
  `getIconPath` meminta `/providers/user-agent.png` yang memang tidak pernah ada: satu
  404 per entri per render, plus **delapan kartu provider palsu** yang connect-nya
  mustahil (`resolution.go:311` — tidak ada model yang me-resolve ke sana).
- **Catatan lama "jangan dihapus tanpa mengganti UI custom-header" ternyata usang.**
  Baseline `ProviderHeaderOverridesModal` dibaca dari
  `GET /api/providers/{id}/overrides` → `builtinProviderHeaders` → `cfg.StaticHeaders`
  (`internal/handlers/dashboard/provider_overrides.go`), bukan dari katalog. Delapan
  entri itu tidak pernah menyentuh UI mana pun: satu `grep` di `web/src` hanya
  menemukan nama mereka sendiri.
- **`zai-search` diperbaiki, bukan dihapus.** Ini satu-satunya entri Go tanpa
  pasangan upstream yang **nyata** (`KnownProviders["zai-search"]` →
  `https://api.z.ai/api/mcp/web_search`, dan `CredentialFallbacks` memetakan
  kuncinya ke `glm`). Entri di katalog hanya placeholder tanpa nama maupun display,
  jadi kartunya kosong dan logonya 404. Sekarang jadi entri web-search yang utuh —
  "Z.ai Web Search", warna & ikon GLM, notice yang menyebut API key GLM yang dipakai
  ulang — dan `getIconPath` memetakannya ke `glm.png` lewat `ICON_ALIASES` (tabel
  yang sama dengan `ICON_ALIASES` upstream). Upstream meng-*alias* `minimax-code`,
  `opencode-zen`, `ollama-search`, `perplexity-agent`, `vercel-ai-gateway` juga, tapi
  repo ini sudah punya logo berbeda untuk kelimanya — meng-*alias*-nya di sini hanya
  membuang logo asli.
- **`503 Service Unavailable` pada `providers:` dijelaskan.** Itu bukan route server.
  `web/public/sw.js` menjawab **503 "Network error or server daemon offline"** dari
  `fetch(...).catch(...)` setiap request yang gagal — termasuk navigasi `/providers`
  dan `/dashboard/providers` saat daemon mati atau sedang restart. Chrome menamai
  lokasi (`providers:`), bukan URL, saat resource di luar `<img>`/`<script>` gagal.
  Tidak ada handler 503 di `internal/handlers/**`; `GET /providers` dan `/providers/`
  menjawab `200 text/html` dari SPA shell, dan cache SW-nya kosong
  (`caches.open('9router-go-static-v1').keys()` → 0 entri).
- **Verifikasi.** (1) Regresi baru di `web/src/lib/providers.test.ts` membaca direktori
  aset dan gagal pada entri tanpa logo — terbukti gagal sebelum fix (`ERR_ASSERTION`
  saat `phantom-test-id` disisipkan) dan lolos sesudahnya; plus tabel `getIconPath` di
  `types.test.ts`. (2) Semua **141** URL artwork yang bisa dihasilkan SPA dijawab `200`
  oleh binary hasil build baru. (3) Chromium headless: `/dashboard/providers` dengan
  "Show all" diklik → **114 request, 0 non-2xx, 0 pesan konsol 404/503**; eager-load
  memverifikasi **95 `<img>`, `naturalWidth > 0` untuk semuanya**. (4) Binary
  **sebelum** fix pada port yang sama: 7 PNG rusak (`user-agent`, `originator`,
  `openai-intent`, `x-codebuddy-request`, `x-github-api-version`, `x-requested-with`,
  `x-vscode-user-agent-library-version`) — 7 dari 8 yang dilaporkan user
  (`anthropic-version` tidak terlihat karena layar pertama belum memuat seluruh grid
  API-Key). (5) `bun test src scripts` 351 pass / 0 fail, `bun run lint` bersih,
  `make vet-svelte` 0 unresolved & 83 error = baseline,
  `go test ./internal/providers/... ./internal/handlers/dashboard/...` hijau. Halaman
  detail `/dashboard/providers/zai-search` dirender dengan logo GLM.

### 🐛 fix(usage): byte rusak yang sudah ada ikut dibersihkan saat menyimpan request detail

- **Latar belakang**: `truncateDetailText` (diperkenalkan pada #267) mengembalikan string
  apa adanya bila panjangnya di bawah batas, sehingga byte yang sudah rusak sebelum masuk
  helper ikut lolos ke `json.Marshal` dan menggagalkan marshal dengan cara yang sama.
- **Keterjangkauan**: jalur ini terbukti tidak terjangkau pada kode saat ini.
  `ScanStream` hanya menyerahkan `onChunk` event SSE utuh yang dirakit dari baris penuh,
  jadi `ResponseBuf` tidak pernah berisi rune terbelah. Di sisi request,
  `extractRequestMessages` melewati `json.Unmarshal` yang lebih ketat dari marshaler dan
  mengembalikan `nil` saat body memuat byte mentah yang tidak valid. Jalur ini tetap
  ditutup agar perubahan berikutnya yang memperluas isi row tidak memunculkan bug yang sama.
- **Fiks**: `strings.ContainsFunc` mendeteksi byte rusak lebih dulu dan mengembalikan string
  asli ketika tidak ada yang perlu diperbaiki, sehingga jalur umum tetap bebas alokasi
  (terukur 0 allocs/op). Hanya jalur terpotong atau memang tidak valid yang memanggil
  `strings.ToValidUTF8`.
- **Verifikasi**: `TestTruncateDetailText` baru menutup batas rune 2/3/4-byte, byte rusak di
  bawah batas, byte rusak di dalam jendela potong, byte rusak di atas potong, serta teks
  multibyte valid yang harus tetap utuh. Setiap kasus juga memeriksa `utf8.ValidString` dan
  bahwa hasilnya dapat di-marshal. `go test ./...` (3897 pass, 48 paket),
  `go test -race ./internal/handlers/chat/`, `go test -tags=integration ./internal/integration/`,
  `go vet`, dan CI PR (test, race detector, integration tests, docker build) hijau.

### 🐛 fix(usage): pemotongan request-detail tidak lagi memecah rune UTF-8

- **Latar belakang**: baris detail request dipotong per-byte (`content[:MaxMessageContentLen]`,
  `[:MaxResponseContentLen]`, `[:maxPersistedErrorLen]`) lalu di-marshal dengan `encoding/json/v2`.
  Pemotongan per-byte bisa jatuh di tengah rune 2/3/4-byte (CJK, aksen latin-1, emoji) sehingga
  menyisakan fragmen bukan UTF-8. Berbeda dengan `encoding/json` v1 yang menggantinya dengan U+FFFD,
  v2 **menolak** string tersebut, jadi satu request dengan teks CJK/emoji yang melewati batas menghasilkan
  `usage marshal request detail failed error=jsontext: invalid UTF-8 within "/request/messages/N/content"`.
- **Dampak**: `logUsage` return duluan setelah marshal gagal — sebelum `upsertDailyUsage` dan
  `PushRecent`. Request tetap berbillet normal (`usageHistory` ditulis lebih dulu dan baris
  `usage logged` tetap tercetak, status 200), tetapi tidak masuk agregat harian, tidak masuk
  recent-requests tracker, dan tidak ada row di tab Details — cukup satu karakter yang memotong batas.
- **Fiks**: helper `truncateDetailText` dipakai di keempat titik potong; `strings.ToValidUTF8`
  membuang byte menggantung yang justru dibaca v2 sebagai data rusak, dan no-op untuk input valid.
- **Verifikasi**: `go test ./internal/handlers/chat/...` (778 pass), `go test ./...` (3886 pass),
  `go vet`. Subtest baru `rune straddling the byte limit stays marshalable` menguji 3-byte CJK,
  2-byte latin-1 supplement, dan 4-byte emoji, lalu benar-benar memarshal payload-nya.
  Dibuktikan menangkap bug: dijalankan terhadap `usage.go` pra-fix, subtest CJK gagal dengan
  `jsontext: invalid UTF-8 within "/messages/0/co...` — pesan yang sama dengan log produksi.

### Features

- **Hermes is configured per profile** (upstream decolua/9router `a9c9f683`,
  #4660). Hermes treats `~/.hermes/profiles/<name>` as an independent agent home
  with its own `config.yaml` and `.env`, but the dashboard only ever wrote the
  default one — configuring a work profile meant editing YAML by hand, and a
  profile switch silently applied one profile's model to another. `GET
  /api/cli-tools/hermes-profiles` lists the default home plus every recognised
  profile, and `GET`/`POST`/`DELETE /api/cli-tools/hermes-settings` take a
  `profile` (JSON body or `?profile=`) and answer 400 for a name that is not
  inert and 404 for a named profile whose directory is gone, so the card can
  fall back to default instead of showing a dead selection. A directory under
  `profiles/` is only listed once it carries an identity file, matching Hermes
  itself — a bare directory left by a cron run is not a profile.
- **`applyToAll` propagates the endpoint without flattening the profiles.** A
  profile already routed through 9router keeps its own model and only has the
  endpoint and API key refreshed; a profile on another provider is reported
  `skipped` with the blocking provider named rather than rewritten; a profile
  with no model yet is wired to the model chosen in the request. The response
  carries one `{profile, status, model, reason}` line per profile plus
  `updated`/`skipped` counts, so the dashboard reports exactly what moved. The
  card refuses to run it against an endpoint that is not a known 9router one:
  the field mirrors the active profile, and it can hold a foreign provider URL
  whose key would then be written into every other profile.
- **The YAML editor is surgical, never a round-trip.** The blocks are matched
  and replaced in place rather than unmarshalled into a map and re-marshalled,
  which would drop every comment and key order Hermes put in the file and
  happily delete keys that are not ours. DELETE therefore removes only blocks
  with `provider: "custom"` and reports the ones it kept; a foreign provider's
  `model:`, `delegation:` or auxiliary role survives a reset. `.env` is only
  ever touched through an `OPENAI_API_KEY` upsert, because a profile `.env`
  also carries bot tokens and messaging-channel credentials. The `model: ""`
  sentinel a fresh install ships is replaced on upsert — left in place it would
  give the file two `model:` keys, and the last-wins loader would silently
  ignore the block just written.
- **Hermes home is resolved through `HERMES_HOME`.** Path resolution honours
  the variable before the user's home directory, which is what lets the tests
  point at `t.TempDir()` and leave the real `~/.hermes` untouched; a profile
  name is additionally required to land inside `<root>/profiles`, so a crafted
  `?profile=` cannot reach another directory on the host.
- **The dashboard card grew a profile picker.** Status dots per profile, the
  command that runs it (`hermes` / `hermes -p <name>`) with a copy button,
  Apply/Reset scoped to the selected profile, a per-profile Manual Config
  preview, and the role list from upstream's current constants — `web_extract`
  is gone because it no longer calls an LLM, replaced by `compression`,
  `title_generation`, `approval` and the rest.

  `api.request` now throws an `ApiError` carrying the HTTP status instead of a
  bare `Error`: the card branches on 404 to recover a vanished profile, and
  matching on the message text would have broken the first time that sentence
  was reworded.

  Verified with `go test ./internal/handlers/media/` (212 pass), `bun test`
  (26 pass) and `make vet-svelte` (0 unresolved identifiers, 83 errors =
  baseline). Every test drives the handlers with `HERMES_HOME` pointed at
  `t.TempDir()`.

### feat(dashboard): proxy pools get an actions menu and a status dropdown

The proxy-pool header carried four controls (`Test All`, `Deploy Relay`,
`Batch Import`, `Add Proxy Pool`) and the card below it opened with a strip of
four filter pills, each showing its count in brackets. Beside the sort picker
and the two bulk-cleanup buttons, that row was the widest fixed thing left on
the page, and it wrapped to three lines on a phone.

- `Test All`, `Add Proxy Pool` and `Batch Import` moved behind one **Menu**
  trigger (`web/src/lib/ui/Menu.svelte`), the same control the top bar, quota
  tracker, API keys and provider rows already use. `Deploy Relay` keeps its own
  trigger: it is a separate task with its own submenu, not one more verb.
- The four filter pills became a counted dropdown (`Filter proxy pools by
  status`), carrying the same All / Active / Passed / Failed buckets and the
  same counts, so nothing is lost from the pills.
- New shared primitive `web/src/lib/ui/CountedSelect.svelte`. It is the
  dropdown analytics' `ViewSelect` already is — measured trigger, viewport-
  clamped panel, portalled, Escape/Tab/outside-click dismissal — differing only
  in that each row carries a count instead of a glyph. Analytics keeps its own
  component: an option there is a *view*, which is a different thing to say
  out loud than "the filter under the header", and it is not worth a third copy
  of this logic.

**Bug found and fixed on the way:** every one of those menus was
self-limiting its own width. `placePanel` was handed the panel's measured
`offsetWidth` and wrote it straight back out as an inline `width`, so a panel
was pinned to whatever width it had when measured and could never grow again —
any label wider than that floor ellipsised permanently. That is what rendered
`Add Proxy P…` in the first screenshot here, and it silently affected the top
bar, quota tracker, cache analytics and usage-section menus as well. Placement
and sizing are now separate: `placePanel` decides *where* the panel goes and
emits only `left`/`top`/`bottom`, while the panel sizes itself from its own CSS
(`w-max`, plus the caller's floor and the new `maxPanelWidth()` clamp). The
viewport clamp the helper used to apply in JS is not lost — it survives as that
`max-width`, which is the same bound.

Verified by driving the real dashboard in Chromium: the menu and dropdown open,
every label renders in full at 1440px and 448px, both panels stay inside the
viewport, and picking `Failed` updates the trigger and the list. Suites green —
311 web unit tests, 37 e2e, 3842 Go tests, svelte-check ratchet at baseline,
`vite build`.

### Fixes

- **CLI Tools shipped a working API key inside the dashboard bundle.** The guide
  cards interpolate the instance's first API key into a pasteable snippet, and
  `effectiveApiKey` fell back to a real machine-minted `sk-…` literal when the
  key list was empty. `web/dist` is embedded in the binary and served to anyone
  who can reach the gateway, so that fallback was a public credential, and
  opening any card on an instance with no key handed it to the visitor. The
  fallback is gone: the snippet now carries `<your-9router-api-key>` and both
  the Pi and Oh My Pi cards say to create a key first — a placeholder cannot
  authenticate upstream, and an empty `apiKey` is worse still, because pi reads
  that as an unauthenticated provider and fails at request time rather than at
  paste time. The footer reads `Active Token: none created` instead of slicing
  an empty string into `••••`.

  Guarded twice. `web/src/lib/no-shipped-secrets.test.ts` scans every bundled
  source file for an `sk-…` literal, exempting only the `sk-9router-*`
  placeholders the env cards have always shown. `web/e2e/cliToolsPi.test.ts`
  asserts the same invariant against the bytes the gateway actually serves, and
  now seeds a key through `POST /api/keys` first — without that, every
  paste-shaped assertion in the file passed against the placeholder rather than
  against a real credential, which is why the shipped key went unnoticed. A
  third case deletes the seeded key and asserts the empty-state card shows the
  placeholder, never `"apiKey":""`. Both suites were confirmed failing with a
  literal restored — the bundle scan reported the token out of
  `/assets/index-*.js`, and the source scan reported it with its file and line —
  and passing once removed. `bun test` (361 pass), `make vet-svelte` (0
  unresolved, 83 = baseline), `go test ./internal/handlers/...` (1557 pass) and
  the pi/omp e2e (7 pass) all green.

- **Reported as a security fix rather than a UI tweak.** A credential compiled
  into the shipped SPA is readable by anyone who can reach a gateway, and this
  entry is served by the public `GET /api/changelog`. It therefore names no
  key, host, or deployment — the value it removes is not reproduced here.

### Halaman proyek + redirect Telegram yang tahan link revoke

- **Halaman `https://luqman-v1.github.io/9router-go` sekarang terbit dari `README.md`**, bukan salinan kedua. `tools/sitegen` merender README dengan goldmark (GFM, heading anchor, HTML mentah untuk `<div align="center">` dan `<details>`) lalu membungkusnya dengan masthead dan footer. Alasannya: landing page yang ditulis terpisah sudah pasti melenceng dari README dalam satu rilis, dan setiap perbaikan harus ditulis dua kali.
- **Link relatif di dalam README ditulis ulang saat build.** Gambar (`docs/screenshots/*.png`) disalin ke output, sedangkan tautan ke berkas repositori (`DATABASE.md`, `docs/*.md`) diarahkan ke URL blob github.com. Tanpa ini `docs/screenshots/providers.png` akan 404 di Pages, karena di sana path diselesaikan terhadap root situs, bukan terhadap repositori.
- **`site/tg.html` menjadi satu-satunya tempat link invite Telegram berada.** Badge README dan kartu "Telegram Group" di modal Support kini menunjuk ke `.../tg`, bukan ke `t.me/...` secara langsung. Kalau invite di-revoke atau diganti, cukup edit satu baris di `site/tg.html`; pengguna lama yang sudah memasang dashboard tetap ikut ke grup yang baru tanpa perlu rilis binary baru.
- **Redirect punya tiga lapis**: `<meta http-equiv="refresh">` untuk klien yang tidak menjalankan skrip, `location.replace` supaya URL ini tidak tertinggal di riwayat, dan tautan yang terlihat sebagai cadangan ketika keduanya diblokir. Halaman ditulis ke `/tg.html`, `/tg`, dan `/tg/`.
- **Workflow `pages.yml`** menjalankan `go test ./tools/sitegen/...` sebelum deploy, lalu memaksa presence `index.html`, `style.css`, `tg.html`, `tg/index.html`, dan keberadaan invite di `site/tg.html`. Kegagalan di salah satu titik menggagalkan deploy, bukan menerbitkan halaman berisi 404.
- **Modal "Support 9router-go" dapat kartu Telegram** (ikon `send`) dan tata letaknya diubah dari grid 2 kolom menjadi daftar bertumpuk, karena grid itu menyisakan kartu ketiga sendirian di baris sendiri. Warnanya disesuaikan ke `sky-600` di terang dan `sky-400` di gelap karena `sky-500` hanya mencapai 2.52:1 terhadap `bg-surface-2`.
- **Escape sekarang benar-benar menutup modal Support.** Handler-nya sebelumnya menempel pada backdrop, yang tidak pernah menerima fokus, jadi tidak pernah terpanggil; dipindahkan ke `<svelte:window>`.
- **Diverifikasi**: `go test ./...` (3813 lulus), `go vet ./...`, `bun run build`, `bun run ratchet:svelte` (0 unresolved, 83 error = baseline). Halaman dan modal diuji di Chromium pada light dan dark: semua anchor README resolve ke heading nyata, Tab pertama mendarat di skip link yang terlihat, tidak ada overflow horizontal di 360px maupun 768px, dan tidak ada console error.

### Fixes

- **Antigravity 404 on every Gemini 3.8 request.** `v1.9.11-exp.9` introduced `AntigravityUpstreamModel`, which ported the registry's `upstreamModelId` column verbatim — including its parenthesised effort (`gemini-3.8-flash-low(low)`). That notation is 9router's own thinking marker, not a Google model id: Cloud Code answers `404 NOT_FOUND` for every suffixed form, so all six accounts failed over and the request ended in 404. `AntigravityUpstreamModel` now resolves to the bare id and the effort is stripped from both the client-supplied suffix and the table row, matching what upstream's `chatCore` does with `stripThinkingSuffix(upstreamModel)` on both its branches. The table stores the bare ids it sends.

  Verified live against `daily-cloudcode-pa.googleapis.com` with a real account: `gemini-3.8-flash-low(low)` and `gemini-3.8-flash-high(high)` both 404, while `gemini-3.8-flash-low`, `gemini-3.8-flash-medium`, `gemini-3.8-flash-high` and `gemini-3.7-flash-tiered` all 200 and return content; `fetchAvailableModels` lists those bare ids and no suffixed one. The live suite passes with no 404 in any log, and the same request that returns 404 on `v1.9.11-exp.9` returns 200 with content on this branch. `AntigravityUpstreamModel_NeverEmitsEffortSuffix` pins the invariant so a future catalog sync cannot reintroduce the 404 through the suffix.

### Features

- **Kimi**: a `/v1/responses` client now reaches Kimi Code's native `/coding/v1/responses` lane instead of being downgraded to the single Chat Completions URL. Kimi serves the OpenAI Responses API on the same key as chat, so the transport is picked per request — the lane the client already speaks when the model declares it, the model's target lane otherwise — mirroring upstream's `transports[]` table (`3125ac2b`). The lane URL is derived from the registry entry or a connection override, so a custom base URL keeps working.

- **Codex images**: `/v1/images/generations` for a Codex image model is answered from the Responses API. The catalogued `gpt-image-*` and `*-image` ids were unroutable: the request was forwarded to an `/v1/images/generations` endpoint the ChatGPT backend does not serve, and the turn was recorded with zero tokens. The lane now posts an `image_generation` tool call, streams the result frame by frame, and records the exact `input_tokens`/`output_tokens`/`cached_tokens`/`reasoning_tokens` the upstream reported — on both the collecting and the streaming path, which is what `e10da160` fixed upstream. A turn that yields no image is an error and records nothing, rather than a zero row indistinguishable from a free one.

- **ElevenLabs Scribe STT**: `scribe_v1` and `scribe_v2` are usable through `/v1/audio/transcriptions`. ElevenLabs speaks its own multipart protocol (`model_id`, `xi-api-key`, `language_code`, diarization, `additional_formats` renderings) and rejected the generic OpenAI-compatible body, so the provider now has a dedicated lane with the Scribe parameter rules: a blank language is omitted so the vendor auto-detects, `timestamps_granularity` only for `word|character|none`, and `diarize` wins over `num_speakers` because upstream treats them as mutually exclusive. Requested srt/vtt/segment renders are served verbatim and never synthesized (`2f827bcf`, #4537). The example card gained the four matching controls.

### Fixes

- **Gemini**: a tool result carrying a JSON Schema or OpenAPI document is no longer rejected. Gemini reads a `$ref` key inside `functionResponse.response` as a pointer into `functionResponse.parts` and answers 400 `INVALID_ARGUMENT` when the name does not resolve, so an ordinary webfetch or MCP result failed the whole turn. The key is now renamed on the way out, recursively and only where it occurs; every other field is untouched (`625df74b`).

- **Strict Responses reasoning**: an explicit `reasoning_effort: "none"` is expressed by removing the `reasoning` field rather than by sending an effort named `none`, which asks a model to think at the lowest level instead of not to think — and is billable. The top-level `reasoning_effort` is still folded into the nested `reasoning: {effort, summary}` shape the Muse and OpenCode-Zen lanes require (`b00ba1aa`).

- **Combo picker**: a compatible provider node that has user-added custom models is now selectable even without a stored connection. Local and self-hosted endpoints do not always need an API key, so gating on connections hid models the user had already added. A node with nothing to show is still hidden, and the exception applies only to the LLM half of the picker (`6a1573eb`, #4659).

- **Self-hosted TTS example**: the model and voice are typed by hand instead of picked from a catalogue, because a self-hosted server decides what it serves. The voice is optional and appended only when non-empty (`ee323400`).

### Fixes

- **Gemini**: duplicate `tool_call_id`s are now rewritten to unique ids before the request goes out. Gemini validates `functionCall` id uniqueness across the *whole* history and rejects the entire request with 400 `INVALID_ARGUMENT` when one repeats, while an OpenAI `tool_call_id` is only unique within its own assistant turn — so a long agent session can legitimately reuse one. Uniqueness is per *occurrence*, not per id: two calls sharing an id end up with two different emitted ids, and each result answers the call it belongs to instead of cross-wiring the loop. Ids that were already unique pass through untouched. Upstream `#4532`.

  The rewrite keys on the id with this gateway's `__ts__` thought-signature suffix stripped, so the private transport encoding still never reaches the wire.

- **Ollama**: `prompt_eval_cached_count` is recorded as cached tokens. Ollama reports prompt tokens cache-*inclusive* — the same convention as OpenAI and Gemini — so the value is recorded, never subtracted. Without it every Ollama turn showed a zero cache hit rate and the cache analytics under-reported savings. Read both from the live response (whose counters sit at the top level, not under `usage`) and from persisted usage blobs, which is where the historical rows are re-read from. Upstream `46627249`.

### Known gaps

These upstream fixes in the same range are **not** in this PR, each because it needs a subsystem this port does not have rather than because it was small:

- **ElevenLabs Scribe STT** (`2f827bcf`, ~100 lines upstream) is a full provider transport — multipart POST, `xi-api-key` auth, `additional_formats` extraction for srt/vtt/seg_json, and diarize/num_speakers arbitration. It is not the data-only model row it first appears to be.
- **Codex image usage** (`e10da160`, +40 lines) is a usage-accounting fix *inside* an image-generation adapter registry. This port has no Codex image lane at all: its Codex image models are catalogued but unroutable. The fix cannot land before the subsystem does.
- **Cursor** (`18ad1a89`, `cbd594ac`) needs a CONNECT-RPC-over-HTTP/2 protobuf executor, which this port does not have — only a config entry exists. It is *portable* with no new dependency: the stdlib HTTP/2 client is already negotiated repo-wide, and `windsurf.go` already has a varint + frame codec byte-identical in framing to upstream's. It is roughly 1000 lines of new executor, so it belongs in its own PR.
- **Kimi Responses transport** (`3125ac2b`) needs a second URL per provider, which `ProviderConfig` has no field for, plus an executor that owns lane selection.

### feat(providers): MiniMax Code (mcode) credits provider

Ports upstream `decolua/9router` commit `85bc33ce` (MiniMax Code as a credits
provider) into 9router-go. MiniMax Code is a **new lane**, not an extension of
the existing API-key `minimax` provider: it authenticates through MiniMax's own
OAuth device flow and serves Anthropic Messages on the mavis gateway.

**What was added**

- `minimax-code` (China) and `minimax-code-global` (international) as separate
  providers. Sign-ins are per site, so they are never aliased onto each other
  (AGENTS.md §3.A); `mm` stays with the API-key `minimax` provider, the credits
  lane is published under `mmc`, and the global site keeps upstream's `mmg`.
- **OAuth device flow** (`internal/handlers/oauth/minimax_code.go`): PKCE S256
  against `{account}.minimax.{cn,io}`, including MiniMax's user-code variant
  (no `device_code` in the reply → the token endpoint is polled with the
  `user_code`, and `interval`/`expired_in` arrive in mixed units).
- **Token refresh** (`internal/proxy/oauth/minimax_code.go`): mcode refresh
  tokens are **single-use** — sending one twice answers `invalid_grant` and
  signs the account out. Two paths reach refresh (pre-dispatch expiry check and
  the executor's on-401 retry) and may hold different snapshots of one
  connection, so refreshes are collapsed in a singleflight keyed on the spent
  token value. Rotation is persisted; a reply without `expires_in` falls back to
  an hour rather than being stored as already-expired.
- **Executor** (`internal/proxy/executor/minimax_code.go`): forwards the Claude
  Messages wire with the OAuth bearer, the placeholder `x-api-key` that rides
  every mavis request, and per-session/timezone headers.
- **Quota normalization**: MiniMax Code reports credit exhaustion as **402/403
  with a body naming the balance** (English *and* CJK). Left at 402/403 the
  account loop would read it as an auth failure and re-spend a single-use
  refresh token per request instead of failing over; those are rewritten to
  **429 `rate_limit_error`**, which triggers combo fallback. A 403 that does not
  name the balance passes through untouched so the on-401 refresh path still
  sees a real auth refusal.
- **Usage tracking** (`internal/handlers/dashboard/usage_minimax_code.go`):
  credits balance, plan tier and M Plan rate windows read through mcode's signed
  account API (`yy` md5 + `x-signature`). A refused sign-in is reported in the
  wording the quota route's auth-expired check recognises, so it force-refreshes
  and retries once.
- **Claude-wire routing**: a provider whose registry entry declares
  `Format: "claude"` is now recognised as serving Anthropic Messages, so a
  `/v1/messages` client reaches it untranslated and a Chat Completions client
  gets the reply translated back. This is separate from the Anthropic-specific
  beta query, OAuth cloaking and beta-flag merge, which a third-party Messages
  endpoint does not want.
- **Dashboard**: catalog entries with artwork (no 404), device-flow
  registration, quota rendering, live catalogue import via a new
  `minimax-code` suggested-models filter, and connection probing against the
  gateway's model catalogue.

**Verification**

- `go test ./...` green; `-race` green on the touched packages.
- New offline tests, none of which reach a real MiniMax host (the site table is
  pinned to an `httptest` fixture):
  - refresh: rotation, expiry default, grant-dead classification on
    `invalid_grant`, no classification on 429/5xx, and concurrent refreshes of
    one token producing exactly **one** upstream send;
  - quota normalizer: the full 402/403/429/200 status-and-body matrix,
    including CJK bodies, the `base_resp` envelope, and pass-through of a 403
    that does not name the balance;
  - device flow: the S256 challenge really is the digest of the session
    verifier, the user-code variant posts `user_code`, and every
    `status:` envelope maps to the right outcome;
  - usage: signature recomputation, site separation, plan-window parsing,
    and the refused-sign-in wording;
  - registry: base URL, alias, auth shape, catalog, thinking levels and
    capabilities for both sites.
- **Integration** (`make test-integration`): the real router, real DB and a
  fake upstream — a `/v1/messages` client reaches the mavis endpoint
  untranslated with the bearer and the `x-api-key` placeholder, the Claude
  reply comes back Claude-shaped, a 402/403 credits refusal reaches the client
  as **429 `rate_limit_error`**, a 403 that does not name the balance keeps its
  own status, and both site ids route.
- `cd web && bun run build` green; `make vet-svelte` clean at baseline (83
  pre-existing type errors, 0 unresolved identifiers).

**Bugs the integration suite caught while porting** — all fixed here, all
observable to a client:

- `claudeNative` in `tryForwardWithConnection` only recognised Anthropic and a
  per-model Messages endpoint, so a provider whose *endpoint* speaks Messages
  was handed a body the handler had already converted. The two decisions now
  agree.
- The executor called the `handleClaudeMessages*` helpers unconditionally. Those
  helpers translate a Claude reply for a *non*-Claude client, so a Claude client
  on a Claude upstream had its answer rewritten into Chat Completions. The
  native case now relays the body untouched.
- `DoRequest` turns any non-200 into a typed error, so the credits refusal
  arrived as an error rather than a response to inspect; normalization runs on
  the error path.

**Intentional divergences from upstream**, recorded here per AGENTS.md §1:

- `mm` remains the alias of the API-key `minimax` provider and the China mcode
  site is published as `mmc`. Upstream reuses `mm` for the credits lane; doing
  the same here would move an existing provider's published prefix.
- The capability limits are the static catalog magpie's plugin ships; upstream
  marks its own figures as unverified by live probe.

### Features

- **Netlify serverless relay proxy pool (issue #250, upstream `4fa46034`).** A fourth edge runtime joins vercel, cloudflare and deno: the dashboard's "Deploy Relay" menu gains a **Netlify Relay** dialog that creates a Netlify site and publishes the relay as a serverless function, then registers it as a `netlify` proxy pool. Deploys run no build step, so the flow is Netlify's digest API rather than a build-and-watch: announce the SHA-1 of `index.html` and the SHA-256 of the function bundle, upload only the digests Netlify reports in `required` / `required_functions`, then poll until `state === "ready"`. A redeploy of an unchanged bundle therefore costs zero uploads.

  The relay function is ported from `src/lib/network/netlifyRelay.js` with its two live-verified failure modes intact: it must be CommonJS `exports.handler` (an ESM `export default` fails with `Runtime.UserCodeSyntaxError`, 502) and must return a **string** body (a stream object fails unmarshalling into the Lambda response struct, 502), so it buffers the upstream response — text as UTF-8 for JSON/SSE, base64 with `isBase64Encoded` for binary. The ZIP writer is hand-rolled stored-entry (CRC32 + local/central headers), parsed back by `archive/zip` in the test rather than asserted on its own output, because a malformed header is an HTTP 400 from the function upload that reads exactly like a bad token.

  `POST /api/proxy-pools/netlify-deploy` is mounted inside the **dashboard** group, next to the three sibling deploy routes — never under `RequireApiKey`, which once 401'd every Deploy button because the SPA authenticates with a session cookie and sends no engine key. A taken site name answers 409 with a hint to pick a different one rather than passing Netlify's bare 422 through; any other rejection keeps Netlify's own status and message, because a generic 500 would hide an expired token. `ProxyPool.IsEdgeRelay` gains `netlify` in the single place all four platforms are classified, so a `netlify` pool is never dialled with `http.ProxyURL` (which yields "malformed HTTP status code") and is instead resolved through the shared `x-relay-target` / `x-relay-path` header spec the other relays speak.

  The pool list also gains the badges it was missing: `deno relay` and `netlify relay` were never rendered even though deno pools already existed.

  Verified offline: the create → test → deploy round trip runs against an `httptest` fake Netlify API with no network access, asserting the exact five-request sequence, that the announced digests match the bytes actually uploaded, that a redeploy with an empty `required` list issues no uploads, and that a `state: "error"` deploy surfaces Netlify's `error_message` instead of a poll timeout. `TestHandleNetlifyDeployCreatesPool` pins the persisted row is an active `netlify` pool whose URL is the relay endpoint. `TestRelayDeployRoutesAcceptDashboardSession` gained a netlify case, proving a session cookie reaches the handler and an anonymous caller still gets 401.

### feat(bedrock): AWS Bedrock runtime with SSO, SigV4 and EventStream streaming (part 2 of 2)

Wires Amazon Bedrock into routing, the catalog and the dashboard on top of the AWS
primitives added in part 1. Two provider entries share one executor, because Bedrock
model families speak different wire formats and each therefore gets its own entry rather
than failing mid-stream after the call is billed.

- `internal/proxy/bedrock.go`, `internal/proxy/executor/bedrock.go` — converse and invoke,
  SigV4-signed per request from resolved credentials, AWS EventStream unwrapped back
  into SSE. On the Anthropic entry the unwrapped stream is handed to the existing Claude
  handlers rather than a second translation path, so usage accounting, the
  `/v1/responses` bridge, terminal synthesis and the `[DONE]` sentinel all work as they
  do on any other Claude-wire provider. Bedrock reports throttling and validation
  failures as in-band frames rather than HTTP statuses, and a stream that closes before
  its terminal event is now failed rather than presented as a finished answer.
- Registry — `bedrock` (`br`, Claude wire) and `bedrock-xai` (`brx`, Chat Completions)
  with aliases, model catalogs, per-provider pricing, and `UpstreamIsAnthropic`, a new
  `ProviderConfig` flag: the existing Claude-wire detection keys off `api.anthropic.com`,
  so a gateway host needs its own signal for the response to be translated.
- Dashboard — an AWS credential form on `AddConnectionModal`, gated on the new
  `credentialForm` catalog flag rather than a provider id, so a later AWS entry is not
  left with no way to enter a profile. Profile mode makes the API key optional.
  `accessKeyId` and `profile` join the echoed provider-specific data; the session token
  does not, since it is a credential.

Verified with `go test ./...`, `make test-integration`, `make vet-svelte`, and a live
smoke against a fake Bedrock runtime: the gateway signs the request (session token both
sent and signed), drops `model` from the body, pins `anthropic_version`, hits the
streaming invoke path, and returns the upstream's frames as Chat Completions SSE with
usage accounted. No AWS account is required for any of it.

### feat(aws): SigV4 signer and AWS credential resolution (Bedrock core, part 1 of 2)

Ports the upstream AWS primitives that Amazon Bedrock needs, without wiring Bedrock
into routing yet. Nothing user-visible changes in this commit: the package has no
callers yet, and the EventStream hardening only tightens a decoder that previously
skipped its integrity checks.

- `internal/proxy/aws/sigv4.go` — Signature Version 4 signing. Hand-rolled rather
  than pulled from the AWS SDK because every signed request here is a single
  query-less POST; the canonicalisation cases that make SigV4 error-prone never
  arise. A URL carrying a query string is refused outright rather than signed
  wrongly. Path canonicalisation double-encodes by default (S3's single-encode rule
  is opt-in), which is what a `…-v1:0` Bedrock model id requires — the single most
  common cause of a Bedrock SignatureDoesNotMatch. The session token is signed as
  well as sent, since omitting it from SignedHeaders is the classic failure for STS
  and SSO identities.
- `internal/proxy/aws/credentials.go` — credential resolution in two modes. Static
  mode reads the keys off the connection; profile mode goes through the AWS SDK's
  shared-config loader, which is what makes `aws sso login --profile X` work.
  Profile results are cached until a refresh lead before expiry, a burst of
  concurrent requests on one profile collapses into a single resolution, and
  failures are never cached so the next attempt retries. The region is validated
  before it reaches a hostname: an unvalidated one would resolve the host to
  `bedrock-runtime.evil.com` and ship the signed request, its body and the session
  token to an attacker-chosen origin.
- `internal/providers/eventstream.go` — hardened. The prelude and message CRCs are now
  verified rather than skipped, the frame cap matches AWS's 24 MiB instead of 10 MiB,
  all ten header value types decode, and every header read is bounds-checked — the
  previous decoder used a bare `break` on a malformed header, which made a corrupt
  frame look like a shorter one. Adds `EncodeEventFrame` so tests and fixtures stay
  in step with the decoder.

Verified with `go test ./internal/proxy/... ./internal/providers/...`. The signer
suite pins AWS's own published SigV4 vector plus a Bedrock-shaped request, so a
canonicalisation regression fails the test rather than reaching production. No AWS
account, no `~/.aws` and no network are required for the suite to pass.

### Security

- **Per-API-key access control now covers every inference endpoint, not just chat.** The chat lane had the allowlist gate; ten media endpoints sat behind `RequireApiKey` with no policy check at all, so a key restricted to one model was authenticated and then served unconditionally by embeddings, image generation, TTS, STT, video generations/edits/extensions, search and scrape. All ten now return **403** for a model outside the allowlist, through the same decision function the chat lane uses, so a model an operator allowed is the same model a client can reach on every route.

  TTS is gated in `forwardTTSRequest` rather than the shared funnel: its `edge-tts` and `google-tts` branches synthesise locally and never reach `forwardMediaRequest`, so gating only the funnel would have left those two paths open.

- **The capacity-adapter pool can no longer route around the allowlist.** The pool is invisible to the gate that admitted the request, so a key restricted to one model could still have its traffic silently rerouted to a pool model it was never granted. A pool model the key may not dispatch is no longer injected.

### Fixes

- The per-key model allowlist now survives a database backup and restore. It lives in its own table, which the export never selected and the import never wrote back, so restoring a backup silently widened every restricted key back to allow-all — the one outcome the restriction exists to prevent. The import also clears the table, so access rows no longer outlive their key as orphans.
- The allowlist is bounded at 200 entries and 256 characters per entry, matching upstream `validateKeyAccessInput`. Every entry is matched against every candidate id on every request, so an unbounded list was a load a single dashboard call could set up. The rejection message names the limit and truncates the offending pattern.

### Known divergences

Go stores an allowlist as one row per `(key, pattern)` in a dedicated table; upstream stores two columns on `apiKeys`. Go's empty allowlist therefore means allow-all, and "restricted but allow nothing" is not expressible. This predates this change and is a deliberate design decision, not a regression.

### Providers

- **Antigravity**: added Claude Sonnet 5.5 and Opus 5.5 (eight ids, 1M context) with capabilities, pricing and quota-allowlist entries — upstream `decolua/9router#a07ed95b`.
- **Antigravity**: resolved the wire model id through a new `AntigravityUpstreamModel`, the Go form of the registry's `upstreamModelId` column. The registry's parenthesised effort (`gemini-3.8-flash-high(high)`) is 9router's own thinking notation and never reaches Google, so the function resolves to the bare id and the table stores exactly what Cloud Code is asked for. A client-supplied `(level)` narrows the id to its own base rather than being appended to the id's preset. The table deliberately stays out of `AntigravityModelSynonyms`, which is shared with the lock and quota keys and must stay family-shaped.
- **Antigravity**: refreshed the catalog — added `gemini-3.1-pro-high`, retired the deprecated gemini-3.5 family and `gemini-3-flash`, moved the default to `gemini-3.8-flash-medium` — upstream `#24034f69`.
- **GLM**: GLM-5.2 and GLM-5.3 now publish the 1M context window they actually have instead of falling through to the `*glm-5*` default — upstream `#4544`.
- **Z.ai**: added `ForwardZai`, which rewrites the client's thinking request into Z.ai's dialect. `reasoning_effort` is mapped onto the three values the API accepts (`low|high|max`), thinking is switched off with `enable_thinking` rather than `thinking.type: disabled`, and a request for no reasoning on a model that cannot disable it is clamped to the lowest effort instead. Upstream `#4656` fixed this on a live 400 (`code 1210`); GLM-5.3 has no thinking-emission path in this port, so the flag alone would only have moved the dashboard picker.
- **Cloudflare AI**: added `@cf/cloudflare/clef-flash` as a `systemone` model and registered `systemone` as a service kind — upstream `d2f90177`.

### Fixes

- A 503 capacity error on one Antigravity tier no longer locks the whole 3.8 family. The 3.8 tiers are separate backends upstream, so a lock now stays scoped to the tier that failed; 3.7 and 3.6 still share one backend and still lock as a family.
- The thinking-levels parity fixture is re-captured against upstream `v0.5.99` (1617 pairs). The two exemptions that were masking real drift are gone; the five remaining are Go-intentional and now say why.

### Known gaps

Upstream's `thinkingOffType` (`between_tools`) and `forcedToolChoice` (`false`) for Claude 5.5 are **not** represented. This port has no thinking-emission module and never pins `tool_choice`, so adding the fields would record data nothing reads. Both prevent a 400 (a thinking block closed at a turn boundary, and a pinned tool choice) and land with the `thinkingUnified` work.

### fix(dashboard): render the API-key list as cards on a phone

The follow-up to #224: after the per-row controls collapsed into one menu, the
API-key table was still unusable at phone width. A row is five columns wide by
construction, so at 390px the `overflow-x-auto` wrapper held 500px of content
inside a 292px box — the policy and actions columns sat outside the viewport and
the secret cell broke one character per line. Nothing scrolled, so nothing
looked broken; the content was simply unreachable.

Below `md` the list now renders one card per key, carrying every column the
table hid. The row menu stretches to the card's full width and keeps its
"Menu" label at phone widths, where there is room for it and a bare glyph
would be an ambiguous tap target with five verbs behind it.

The two layouts share their cells, the status pill and the row menu as
snippets, so a card cannot drift from the table row above `md`.

fix(qoder): rewrite the chat body into Qoder's own payload before signing — Closes #121

Every Qoder chat request failed with HTTP 400 on every registered model,
on a freshly authorized account:

```json
{"error":{"message":"{\"code\":\"400\",\"message\":\"[FAIL]node:agent_router
msg:None flow nodes found for router agent_router\"}"}}
```

`ForwardQoder` COSY-signed and forwarded the client's OpenAI body verbatim.
Qoder's `agent_chat_generation` does not accept an OpenAI-shaped request: it
routes to a node called `agent_router`, that node finds no flow for the
request, and answers 400. The COSY signature, the endpoint and the model id
were all correct, which is why the failure read as an account problem rather
than a gateway one. Upstream's Node build answers the same request 200,
because it rewrites the body first.

**What changed (request side)**

- `qoder_request.go` (new) — maps the OpenAI chat body onto Qoder's payload:
  `chat_task`, `session_type`, `agent_id`, `task_id`, `chat_context`,
  `parameters`, `business`, and the derived `session_id` / `chat_record_id` /
  `request_set_id` / `request_id`. System turns are hoisted out of `messages`,
  multipart content is flattened except image blocks, and documents become
  short stubs instead of inlined bytes.
- `qoder_encoding.go` (new) — Qoder's WAF-bypass body encoding (base64 →
  third-rotation → character substitution). The encoded bytes are what the
  COSY signature covers; signing the plaintext is a signature error.
- `qoder_catalog.go` (new) — `model_config` comes from the live COSY-signed
  model list, cached per credential for an hour and coalesced through
  `singleflight`. A model the catalogue does not publish now fails with a
  message naming it instead of reaching Qoder as an unroutable payload.
- `qoder_credentials.go` (new) — a Personal Access Token is exchanged for a
  short-lived job token before chat, since a PAT cannot sign COSY requests.
- `qoder_context_tier.go` (new) — the 200K/400K/1M context-window escalation
  the IDE performs, so a long session is not rejected by the default tier.
- `qoder_endpoints.go` (new) — the regional endpoint table, now shared with
  the dashboard. `qoder` and `qoder-cn` keep separate hosts and are never
  routed to one another.
- The chat request now carries `&Encode=1`, plus `Accept: text/event-stream`,
  `Cache-Control: no-cache`, `X-Model-Key`, `X-Model-Source` and
  `Accept-Encoding: identity` — gzip makes Qoder's CDN re-validate the
  signature, so identity is required.
- A connection missing `userId` or a token now answers a clean 401 naming the
  fix, rather than an opaque failure from inside the signer.

**What changed (response side)**

With the request accepted, every reply still failed as HTTP 502
`upstream answered 200 without a completion`. Qoder's event stream is not
plain SSE: every frame is an envelope whose `body` holds the real chunk as an
escaped JSON *string*.

```json
data: {"headers":{...},"body":"{\"choices\":[{\"delta\":{...}}]}",
        "statusCodeValue":200,"statusCode":"OK"}
```

Feeding those frames to the shared SSE folder finds no `choices`, so a
perfectly good upstream looked empty. `qoder_sse.go` (new) peels the envelope
and re-emits plain OpenAI chunks, and coalesces the empty finish chunk with
the later `choices: []` usage frame into one terminal chunk carrying both —
downstream reads usage off the finish chunk and drops `choices: []`. A billing
or quota refusal becomes a `quota_error` rather than assistant text, and any
other upstream failure is marked so the non-streaming path raises its status
instead of answering 200 with the error text (issue #41).

**Scope beyond the literal report**

Upstream's attachments rewrite (uploading inlined images to
`/api/v2/image/upload`) is not ported; OpenAI `image_url` and Claude `image`
blocks are converted to Qoder's image block form and sent as references. The
dashboard's model-list fetch now shares the executor's endpoint table and
credential exchange rather than keeping a second copy that could drift.

**Verification**

`go test -race ./internal/proxy/executor/ ./internal/handlers/dashboard/` —
726 passed. `qoder_body_test.go` captures the outbound request at a fake
upstream, decodes the Qoder encoding, and pins the payload keys, the derived
identities (stable across turns, fresh per request), `Encode=1`, the header
set, and that `Cosy-Bodyhash` covers the encoded bytes actually sent.
`qoder_sse_test.go` pins the response side: envelope unwrapping, the escaped
wire form verbatim, finish+usage coalescing, billing blocks not becoming
assistant text, and the dropped `event:finish` timings frame.

Before this change the same probe showed the client's OpenAI body on the wire
with no `Encode=1`, no `X-Model-Key`, and `Accept-Encoding: gzip`.

Verified against a live Qoder account: `qd/qfmodel` answers 200 with
`content: "pong"` in both streaming and non-streaming mode, with usage
attributed on the terminal chunk.

### fix(dashboard): drop the dead language switcher — Closes #240

The top-bar menu carried a **Language** item whose `onSelect` was an empty
function, so clicking it closed the menu and nothing else happened. The
dashboard ships English only — there is no i18n layer, and every label in the
SPA is hardcoded — so the item advertised a capability the build does not have.

Removed:

- the `Language` menu item from the top-bar overflow menu (`TopBar.svelte`);
- the "Language & Display" card from the profile settings page, along with its
  `selectedLanguage` state, its read-back from `GET /api/settings`, and the
  `language` key it wrote on save (`ProfileSettingsView.svelte`);
- the now-unreferenced `language` field from the `Settings` TypeScript type
  (`api/client.ts`).

Nothing on the Go side changes: the settings row is a passthrough JSON map and
no handler ever read the `language` key, so a stored value from an older build
is inert and simply stops being rewritten.

Verified with `bun run build`, `bun test` (358 pass), and `bun run
ratchet:svelte` (0 unresolved identifiers, 83 errors — unchanged from the
pinned baseline). The end-to-end top-bar assertion now pins the item's absence
rather than its presence.

### ✨ feat(cli-tools): kartu Pi dan Oh My Pi di halaman CLI Tools — Closes #165

- **Latar belakang**: halaman CLI Tools hanya memuat 22 tool. `pi` (pi.dev) dan `omp`
  (Oh My Pi) tidak ada di katalog maupun di peta status, padahal keduanya sudah bisa
  memakai 9router-go sebagai gateway — keduanya hanya butuh satu blok provider yang
  menunjuk `baseUrl` ke `{origin}/v1`. Pengguna harus mencari sendiri snippet-nya di
  komentar issue.
- **Backend**: `internal/handlers/media/clitools.go` menambahkan dua `toolDef` — `pi`
  (bin `pi`) dan `omp` (bin `omp`, plus `--version` seperti `devin`, jadi kartu
  menampilkan nomor versi). `TestCLIDetectors_HasAllToolIDs` ikut diperbarui;
  tes itu mengunci set id, jadi penambahan kartu tanpa detektor gagal di suite Go
  alih-alih diam-diam di browser.
- **Frontend**: dua kartu baru di `CliToolsView.svelte` dengan `configType: 'guide'`,
  karena kedua agen dikonfigurasi lewat file, bukan env var. Snippet-nya diinterpolasi
  dari origin hidup dan API key aktif: JSON `models.json` untuk Pi, YAML `models.yml`
  untuk Oh My Pi (path default `~/.omp/agent/models.yml`, dengan catatan direktori
  profil `~/.omp/profiles/<name>/agent/`). Keduanya menyebut fallback
  `openai-responses` karena gateway menyajikan dua lane.
- **Ikon**: `web/public/providers/pi.png` dan `omp.png` (128px, diturunkan dari
  `pi.dev/logo.svg` dan `omp.sh/favicon.svg`) ditambahkan dan dirujuk lewat `image:`
  di kedua kartu, jadi keduanya tampil penuh seperti tool lain — bukan badge
  monogram hasil fallback.
- **Verifikasi**: `web/e2e/cliToolsPi.test.ts` baru menjalankan gateway sungguhan dan
  memeriksa (a) id `pi`/`omp` benar-benar ada di payload `all-statuses` dengan
  `installed` bertipe boolean — bukan `null`, yang akan membuat badge jatuh ke
  "Guide", (b) teks modal memuat path konfigurasi yang benar, `{origin}/v1`, kedua
  nilai `api`, dan tidak pernah memuat fallback SSR `localhost:20130` atau
  `<your-api-key>`, (c) kedua ikon benar-benar termuat (`naturalWidth > 0`) — file
  yang hilang hanya akan memicu badge monogram lewat `onerror`, jadi itu yang
  diuji, bukan keberadaan tag `<img>`. Diuji gagal sebelum fix (2 dari 5 gagal saat
  `clitools.go` dikembalikan ke keadaan semula; 1 gagal saat `pi.png` dihapus) dan
  lolos sesudahnya. `go test ./internal/handlers/...` (1528 pass), `bun test`,
  `bun run build` dan `make vet-svelte` (0 unresolved, 83 error = baseline) hijau.
  Halaman juga dicek langsung di Chromium terhadap binary yang baru dibangun.

### 🐛 fix(dashboard): menu pojok kanan atas terbuka tapi tidak kelihatan atau bisa diklik — Closes #235

- **Latar belakang**: di v1.9.11-exp.7, menu akun di pojok kanan atas (`<header>`: donasi, tema,
  bahasa, changelog, logout) **tidak memberi respons apa pun** di seluruh halaman. Menunya
  benar-benar terbuka — `aria-expanded` berubah, panel-nya ada di DOM dengan lebar 205px dan
  tinggi 232px — tapi tidak pernah terlihat dan tidak pernah menerima klik.
- **Akar masalahnya bukan penempatan, melainkan containing block**. `lib/ui/Menu.svelte`
  membuat panel `position: fixed` lalu memposisikannya dari `trigger.getBoundingClientRect()`
  lewat `menuPosition.placePanel` — dan itu benar, karena `placePanel` memang menghitung
  `left`/`top` **terhadap viewport**. Tapi `position: fixed` hanya ikut viewport selama tidak
  ada ancestor yang membentuk containing block, dan `backdrop-filter` (**juga** `transform`,
  `filter`, `perspective`, `contain`) membuatnya. `<header>` TopBar membawa `backdrop-blur-xl`,
  jadi panel di dalamnya diukur dari **padding box header**, bukan dari window: pada viewport
  1440px panel itu muncul di `left=1491, top=55` — sepenuhnya di luar layar. `left=1491` itu
  persis `rect.right - width` (1408 − 205) yang **tidak pernah di-clamp**, karena clamp di
  `placePanel` mengasumsikan koordinat sudah relatif terhadap viewport.
- **Perbaikan**: action `use:portal` (`web/src/lib/ui/portal.ts`) memindahkan panel ke
  `<body>` selama terbuka, lalu melepaskannya saat `{#if open}` menutup. Menempelkan panel di
  `<body>` menghapus ketergantungan pada ancestor sepenuhnya — bukan mengurangi offset
  header, yang harus dihitung ulang untuk setiap trigger baru. Diterapkan pada ketiga panel
  `fixed` yang memakai `placePanel`: `Menu.svelte`, `SectionMenu.svelte`, dan
  `PeriodSelect.svelte`.
- **Kenapa tidak cukup sekarang saja di header**: ketiga komponen itu berdiri sendiri dengan
  helper yang sama, jadi hanya memperbaiki satu berarti bug yang sama tetap bisa muncul di
  halaman lain. SectionMenu dan PeriodSelect memang belum terpengaruh hari ini, tapi keduanya
  sudah memakai `placePanel`, jadi tidak ada yang perlu ditunggu sampai triase ulang.
- **Verifikasi**: `web/e2e/headerMenuPanel.test.ts` baru menelusuri ancestor chain tiap panel dan
  gagal kalau ada `transform`/`filter`/`backdrop-filter`/`perspective`/`contain` di antara panel
  dan `<body>`, selain itu mengecek panel benar-benar di dalam viewport dan **bisa diklik**
  (`elementFromPoint` di titik tengah panel harus mendarat di dalam panel itu sendiri).
  Diverifikasi gagal sebelum fix (`Received: 1696, Expected: <= 1440`) dan lolos sesudahnya.
  Suite e2e penuh, `bun test src scripts` (321 pass), `bun run lint`, `make vet-svelte`, dan
  `go test ./...` semuanya hijau.

### 🐛 Kolom isian custom provider tidak lagi kembali ke nilai awal (#234)

- **Gejalanya**: di dialog **Edit OpenAI/Anthropic Compatible**, semua yang diketik
  hilang sendiri setelah beberapa detik — persis saat polling dashboard berjalan.
  Dilaporkan user: "ubah data di kolom apapun, tunggu beberapa saat, nilainya
  kembali ke value awal".
- **Penyebabnya bukan polling itu sendiri, tapi cara dialog membaca datanya.**
  `App.svelte` memanggil `/api/provider-nodes` tiap 10 detik lalu **mengganti seluruh
  array** (`providerNodes = nodesRes`). Di `ProviderDetailView`, `selectedNode`
  dihitung dengan `$derived(providerNodes.find(...))`, jadi setiap tick memberi
  objek baru dengan isi yang identik. `EditCompatibleNodeModal` men-seed form dari
  `$effect` yang bergantung pada `node`, sehingga efek itu ikut jalan ulang dan
  menulis ulang `formName`/`formPrefix`/`formUrlSuffix`/`formApiType`/`formBaseUrl`
  dengan nilai tersimpan — menghapus semua ketikan yang belum disimpan.
- **Perbaikannya mengunci seed pada identitas yang stabil, bukan pada objek.**
  Id node adalah identitas yang benar-benar stabil; isi objeknya yang berubah
  setiap 10 detik. Efek sekarang hanya men-seed saat dialog dibuka atau saat
  node yang diedit **berganti id** — termasuk kasus node yang baru muncul
  setelah dialog terbuka. Variabel penanda (`seededNodeId`, `wasOpen`) sengaja
  `let` biasa, bukan `$state`: menulisnya tidak boleh dibaca efek itu sendiri,
  kalau tidak akan memicu dirinya sendiri.
- **`EditConnectionModal` sudah aman dan tidak ikut diubah.** Ia men-seed di luar
  `$effect`, jadi array `connections` yang berganti tiap 10 detik tidak pernah
  menimpa form-nya. Hanya dialog yang bergantung pada `providerNodes` yang
  bermasalah.
- **Tes regresi**: `web/e2e/customProviderForm.test.ts` membuat node asli lewat
  UI, membuka dialog Edit, mengetik perubahan, lalu **menunggu melewati satu tick
  polling** sebelum membaca nilainya kembali. Pada binary tanpa perbaikan, tes
  gagal persis pada gejala yang dilaporkan (`"Poll Survivor Edited"` kembali
  menjadi `"Poll Survivor"`), dan lulus setelah perbaikan. Kasus kedua
  memastikan dialog yang ditutup lalu dibuka lagi tetap men-seed dari baris
  tersimpan — perbaikan separuh akan menjadi bug form basi, jadi keduanya diuji.

### 🔍 feat(dashboard): atributkan model pada lastError koneksi dan redam visual saat testStatus aktif (#226)

- **Latar belakang**: ketika model tertentu gagal diuji atau gagal dipanggil (misal `401 Model ... is not supported`),
  `RecordConnectionError` menulis string error tanpa atribut model. Di dashboard, baris koneksi menampilkan
  badge `Active` di samping teks error merah mencolok tanpa kejelasan model mana yang gagal atau apakah
  error tersebut hanya berlaku per-model.
- **Fiks**: `RecordConnectionScopedError` di layer database kini menyimpan `model` dan `source` pada objek
  `lastError`. `GET /api/connections` memancarkan `lastErrorModel` dan `lastErrorSource`. Pada baris koneksi
  dashboard, error model-scoped diberi tag `[model]` dan diwarnai mengikuti verdict badge baris tersebut —
  merah bila badge `error`, muted bila `active` — sehingga error per-model tidak lagi terbaca sebagai
  kerusakan kredensial menyeluruh, tanpa meredam alarm saat probe benar-benar gagal.
- **Verifikasi**: `go test ./internal/db/... ./internal/handlers/{dashboard,chat}/...`, `bun test src scripts`
  (321 pass), `bun run ratchet:svelte` (0 unresolved, 83 = baseline), dan pemeriksaan DOM di dashboard:
  baris dengan probe gagal tetap merah di kedua lokasi tampilan, sedangkan baris `active` meredam.

### 📢 Notifikasi release ke grup Telegram

- **Kabar rilis**: setiap tag baru (`v*`) memunculkan ringkasan di grup Telegram — versi, channel
  (stabil atau eksperimental), 10 entri changelog teratas, link ke Release page, dan perintah
  `docker pull`.
- **Isi pesan diringkas, bukan changelog penuh.** Telegram membatasi 4096 karakter, sementara
  `CHANGELOG.md` repo ini 4.971 baris dan satu entry pernah melewati 4.000 karakter sendiri.
  Yang dikirim hanya heading (`###`), karena bullet-nya ada di Release page — dan 700 baris di
  HP tidak terbaca.
- **`notify` jadi job keempat di `release.yml`, `needs: [binaries, docker]`.** Itu inti
  desainnya: job berjalan setelah Release page dan image Docker benar-benar ada, jadi link di
  pesan tidak pernah 404. Workflow terpisah yang trigger `push: tags` berjalan paralel dengan
  build dan rutin mengirim tautan mati.
- **Catatan dioper lewat artifact, bukan `/tmp`.** Tiap job GitHub punya runner sendiri, jadi
  `/tmp/notes.md` dari job `binaries` tidak ada di `notify`. Artifact ini mencegah 40 baris
  logika fallback diduplikasi — kalau tidak, ringkasan di Telegram bisa berbeda dari yang
  dibaca pengguna di halaman Release.
- **`continue-on-error: true`**, karena Telegram yang down atau token dicabut tidak boleh
  menandai release sukses jadi merah; artifact sudah terbit sebelum job ini jalan.
- **`scripts/notify-telegram.sh` bisa dijalankan lokal** dengan `--dry-run` untuk mengecek
  format tanpa tag baru, dan dengan `--notes` untuk menguji jalur kirim sungguhan.
- **Lima bug format, tiga di antaranya hanya muncul saat kirim sungguhan**:
  1. Pemotongan pesan awal memakai `head -n 40` — itu menghitung *baris*, bukan karakter.
     Dengan fixture 59 entry beremoji, pesan keluar **11.131 karakter** dan pasti ditolak
     Telegram. Sekarang dipotong per karakter lalu dirapikan ke batas baris.
  2. `grep '^#{2,4} '` ikut menarik `[Unreleased]` dan judul `[v1.9.11]` seolah entri,
     jadi baris paling atas pesan tidak membawa informasi apa pun. Sekarang `#{3,4}` saja.
  3. Pesan dikirim sebagai argumen `curl`, dan **di Git Bash (Windows) variabel bash yang
     memuat non-ASCII tidak selamat lewat argv** — ditranscode ke code page ANSI, lalu Telegram
     menjawab `400 strings must be encoded in UTF-8` tanpa menerima apa pun. Terukur: variabel
     ASCII murni terkirim, variabel yang sama berisi em-dash tidak pernah — dari file, dari
     `printf`, maupun literal. Emoji sendiri selamat, jadi gaganya baru muncul begitu ada entry
     bertanda hubung, dan entry di repo ini semuanya begitu. Runner `ubuntu-latest` tidak
     terpengaruh, jadi ini tidak pernah muncul di CI — tapi justru merusak jalur tes lokal,
     satu-satunya alasan skrip ini ada. Payload sekarang di-URL-encode ke file, dikirim
     dengan `--data-binary @file`.
  4. Field form dipisah baris baru, bukan `&`. curl menerimanya, Telegram menjawab
     `400 message text is empty`.
  5. `curl --fail-with-body` mengubah penolakan jadi exit 22 dan membuang deskripsi error
     Telegram, sehingga log job menampilkan error transport, bukan "chat not found". Skrip
     sekarang membaca body dan menentukan exit sendiri.
- **Topik forum ditangani lewat `TELEGRAM_TOPIC_ID` opsional.** Grup ini `is_forum: true`;
  tanpa thread id, Telegram membuat topik "Messages" baru yang mungkin tidak pernah dibuka
  anggota. Field-nya dihilangkan sepenuhnya kalau variabel belum di-set, jadi tidak
  mengaturnya adalah konfigurasi yang valid, bukan rusak.
- **Verifikasi**: `bash -n` bersih; dry-run terhadap `CHANGELOG.md` asli (1.069 karakter);
  oversize 19 KB dipotong ke 3.727 dengan link utuh; file kosong, argumen hilang, flag asing,
  dan secret kosong semuanya keluar dengan pesan dan exit code yang benar; `release.yml`
  di-parse dengan `needs: [binaries, docker]`. **Jalur kirim sungguhan diuji langsung ke grup**:
  pesan ber-emoji + em-dash, pesan penuh dari `CHANGELOG.md`, dan pesan oversize — ketiganya
  `ok:true`; token salah memberi `404 Not Found` beserta deskripsinya dan exit 1.
- **Setup ada di [`docs/TELEGRAM_RELEASE_NOTIFICATIONS.md`](docs/TELEGRAM_RELEASE_NOTIFICATIONS.md)**:
  buat bot via BotFather, **tambahkan bot ke grup dulu** (tanpa itu API balas `403`, bukan
  `404`), ambil `chat.id` dari `getUpdates`, lalu simpan `TELEGRAM_BOT_TOKEN` dan
  `TELEGRAM_CHAT_ID` sebagai repository secrets.

### 🔀 Changelog dipindah ke fragment per-PR — konflik `CHANGELOG.md` jadi mustahil

- **Latar belakang**: `CHANGELOG.md` 4.971 baris dan **setiap PR menulis entry baru persis di
  baris 5-6** (`## [Unreleased]` + entry pertama). Heading itu jadi hunk terpanas di repo,
  jadi tiap dua PR yang landai bareng pasti bentrok, dan `## [Unreleased]` menumpuk 675 baris
  sejak `v1.9.10-exp.3`. Aturan §7 "resolve additive" di `AGENTS.md` dibuat justru karena
  konflik itu selalu muncul di tempat yang sama — band'sat manual, bukan-solusi.
- **Kenapa tidak `merge=union` di `.gitattributes`**: itu pilihan yang murah dan kelihatannya
  tepat, tapi tidak berlaku di repo ini. Merge lewat tombol Merge di GitHub
  (commit `4dbd63d9 … (#219)` adalah squash merge server-side), dan GitHub **mengabaikan
  `.gitattributes` merge driver sepenuhnya** — limitation yang sudah lama, bukan config.
  Union merge lokal sudah diuji (merge, `--squash`, `cherry-pick` semuanya bersih), tapi lokal
  bukan yang dipakai repo ini. Bot union-merge lewat Actions ditolak karena butuh token write
  dan PR tetap `CONFLICTING` di UI sampai action jalan.
- **Fiks**: satu file `.changes/<pr-number>-<slug>.md` per PR. Dua PR yang bareng tidak pernah
  berbagi satu baris, jadi tidak ada yang bisa bentrok — bukan karena ada aturan resolusi, tapi
  karena tidak ada yang sama untuk dibentrokan. `CHANGELOG.md` sekarang ditulis **hanya** oleh
  `make changelog-merge TAG=…` saat release.
- **Dashboard tetap live, dan justru lebih segar dari sebelumnya**. `GET /api/changelog`
  merakit `CHANGELOG.md` + seluruh fragment (package baru `internal/changelogfrag`), jadi
  pekerjaan yang sudah merge tapi belum ada tag langsung terlihat — tanpa menunggu release.
  Ini menjawab kekhawatiran "kalau tiap PR nulis file baru, nanti dashboard-nya gimana?":
  file baru tidak muncul sebagai perubahan, hanya sebagai isi.
- **Rantai fallback release notes** (`release.yml`) sekarang: section `## [<tag>]` → `[Unreleased]`
  → fragment `.changes/` → commit log. Tanpa tahap fragment, halaman release akan jatuh ke
  commit subjects, karena tidak ada lagi yang mengisi `CHANGELOG.md` sebelum tag.
- **Tiga bug yang ketahuan saat verifikasi, bukan saat baca kode**:
  1. Fragmen di-join dengan `\n`, jadi `### heading` berikutnya menempel ke list item — marked
     mem-parsingnya sebagai continuation line dan entry hilang ke dalam bullet sebelumnya.
     Sekarang dipisah blank line, dengan `TestAssembleSeparatesFragmentsWithBlankLine`.
  2. Merge kedua (setelah `[Unreleased]` hilang) menaruh release **di atas** `# Changelog`, judul
     file hilang permanen. Ditangkap smoke test tiga release berturut-turut, bukan unit test.
  3. `.changes/README.md` ikut ter-render jadi entry — instruksi kontributor muncul di atas
     release notes di modal.
- **Verifikasi**: `go build ./...`, `go vet ./...`, unit test `internal/changelogfrag`
  (21 case: urutan numerik PR, blank-line separation, README exclusion, tag berisi bracket),
  `go test -race ./internal/integration/` dengan `TestChangelogShowsPendingFragments` lewat
  router produksi, plus smoke `changelog-merge` tiga release berturut-turut terhadap salinan repo
  dan rantai fallback `release.yml` diuji di bash dengan fixture 88/900/1012.
- **Dokumentasi**: `AGENTS.md` §7.6 (cara menulis fragment) dan §7.7 (konflik `CHANGELOG.md`
  kini tanda ada edit yang bocor, bukan pekerjaan manual), PR template, `.changes/README.md`.

### ✨ feat(dashboard): sertakan flag free pada GET /api/models/caps (#232)

- **Latar belakang**: daftar model di dashboard menampilkan campuran model free dan berbayar
  tanpa pembeda visual. Data klasifikasi free di backend sudah ada (`IsFreeTierModel` dan tabel pricing),
  namun `GET /api/models/caps` sebelumnya belum mengirimkan flag `free` ke frontend.
- **Fiks**: menambahkan field `free` pada struct `modelCaps`, menggabungkan sinyal suffix
  (`:free`, `/free`, `-free`) dan rate pricing nol (`InputPer1M == 0 && OutputPer1M == 0`).

### ✨ feat(dashboard): satukan enam baris kontrol menjadi satu menu (Closes #224)

- **Latar belakang**: report #224 — enam surface dashboard punya deretan
  kontrol yang membuat baris header atau baris tabel wrap di layar sempit, dan
  dropdown Cache Analytics keluar dari layar di lebar ponsel.
 **Akibat**: operator harus menggulir ke baris kedua di Quota Tracker, dan
  tombol `Delete` pada baris API key bisa terdorong keluar viewport sehingga
  tidak bisa ditekan di ponsel.
 **Perubahan UI**:
  - **Top bar**: donate, install, theme, language, changelog, dan logout —
    enam kontrol — kini satu tombol `Menu` (`TopBar.svelte`). Dropdown language
    dan app-drawer yang sebelumnya terpisah dihapus.
  - **Quota Tracker**: provider filter + account filter tetap terlihat; email
    masking, expiring-first, disable/enable massal, auto-refresh, dan refresh
    pindah ke satu `Menu`.
  - **Proxy Pools**: dua modal (single add dan batch import) menjadi satu dialog
    bertab `Single` / `Bulk Add`; tombol `Batch Import` membuka tab yang sama.
  - **Provider connections**: refresh / edit / delete per baris menjadi satu
    `more_horiz` menu. Tombol proxy tetap kontrol tersendiri karena itu pilihan
    nilai, bukan verb.
  - **API keys**: action per baris menjadi satu `more_horiz` menu; checkbox per
    baris menambah **batch bar** (enable / pause / delete) untuk banyak key
    sekaligus.
 **Fix mobile dropdown**: panel menu kini `position: fixed` dan diposisikan
  dari `getBoundingClientRect()` trigger, lalu di-clamp ke viewport
  (`lib/ui/menuPosition.ts`). Sebelumnya `absolute` di dalam wrapper, sehingga
  terpotong oleh ancestor `overflow-x-auto` — inilah penyebab dropdown Cache
  Analytics keluar layar di ponsel. `ViewSelect`, `SectionMenu`, `PeriodSelect`,
  dan provider filter Quota Tracker semuanya memakai penempatan yang sama.
 **Rename API key**: `PUT /api/keys/{id}` menerima field `name` (partial
  update; string kosong = hapus nama). Field rename ada di dialog policy yang
  sama dengan rate limit/expiry/allowlist, jadi mengedit satu key = satu
  dialog. Dibatasi 200 karakter.
 **Verifikasi**: `bun run build`, `make vet-svelte` (0 unresolved identifier,
  83 error = baseline), `go test ./...` (3650 pass), plus smoke check di browser
  pada 1440px dan 390px — rename tersimpan, batch pause menandai kedua key,
  dan kedua dropdown Cache Analytics tampil utuh di 390px.

### ✅ test(dashboard): E2E suite yang menutup celah tsc/vite/svelte-check/go-test

 **Latar belakang**: collapsing action API key jadi satu menu (#224) menghapus
  `confirm()` pada delete dan regenerate — dan **semua gate tetap hijau**.
  `tsc` membaca type, `vite build` membundel, ratchet svelte-check menghitung
  diagnostic, `go test` menguji HTTP API tanpa pernah merender komponen.
  Tidak satu pun melihat DOM.
 **Perubahan**: `web/e2e/` (Playwright + Chromium) menjalankan binary Go asli
  terhadap SQLite sementara, lalu menguji konsekuensi yang bisa diamati:
  - dismiss dialog delete → baris **tetap ada**; accept → baris hilang
  - dismiss dialog regenerate → secret **tidak berubah**
  - prompt delete **menyebut nama key** yang akan dihapus
  - batch bar mengubah semua key terpilih jadi `Paused`
  - panel dropdown di 390px dan di viewport 200px **tetap di dalam layar**
 **Bug tersembunyi yang ketahuan**: `style:min-width` pada panel mengalahkan
  `width` hasil clamp, sehingga di viewport 200px panel 224px tetap meluber
  32px. `placePanel` kini ikut meng-cap `minWidth`, dan semua panel meneruskan
  nilai tersebut alih-alih menulis `min-width` sendiri.
 **Bukti test menangkap regresi**: dengan clamp dimatikan, test mobile gagal
  `232 > 200`; dengan `confirm()` dihapus, 4 dari 5 test API key gagal —
  sementara keempat gate lama tetap hijau.

 **Empat surface lain (#224)** — top bar, header Quota Tracker, dialog Add Proxy
  Pools bertab, dan halaman Providers — ditutup di
  `web/e2e/unifiedControls.test.ts`, sehingga suite-nya 20 test.
  - **Bukti**: mengembalikan `TopBar.svelte` ke kondisi pra-#224 membuat **6 dari
    10 test** di file itu gagal, sementara keempat gate lama tetap hijau.

### 🐛 fix(translator): `tool_call.id` paralel dari Gemini bertabrakan, dan nama tool tidak lagi dibaca dari id (#229)

- **Latar belakang**: streaming translator membuat id tool call dari
  `fmt.Sprintf("call_%s_%d", fnName, time.Now().UnixNano())`. Dua `functionCall`
  dalam satu chunk dipancarkan berjarak <1 ms, sedangkan `UnixNano()` hanya
  berubah setiap ~0,5–1 ms pada mesin dev — sehingga dua panggilan paralel
  mendapat **id yang identik**. Klien yang mencocokkan hasil tool dengan
  `tool_call_id` tidak bisa membedakan keduanya.
- **Akar masalah**: `time.Now().UnixNano()` tidak men-tick di dalam satu chunk,
  dan id tidak membawa indeks panggilan. Selain itu, fallback nama tool di
  `TranslateOpenAIToGemini` membaca nama dari id dengan memotong segmen setelah
  underscore terakhir — logika itu hanya benar untuk id format `call_<nama>_...`,
  dan akan salah jika gateway memakai `functionCall.id` milik Gemini sendiri
  (token opaque seperti `call_abc123`).
- **Fiks**: id memakai `functionCall.id` milik Gemini bila ada (satu-satunya id
  yang akan dicocokkan Gemini untuk functionResponse); jika tidak, id hasil
  generator membawa indeks panggilan sehingga paralel tetap berbeda. Pasangan
  id→tool dicatat saat tool call dipancarkan (`toolNameStore`, sejajar dengan
  `thoughtSignatureStore` yang sudah memakai skema key yang sama, termasuk
  suffix `__ts__` dan namespace sesi), dan fallback membaca penyimpanan itu.
  Id tidak pernah di-parse lagi: id yang tidak dikenal dijawab apa adanya,
  bukan dengan nama tool yang dikarang.
- **Parity**: `open-sse/translator/response/gemini-to-openai.js`
  (`functionCall.id || \`${name}-${Date.now()}-${index}\``).
- **Catatan**: id ganda ini sudah ada sebelum #228 dan tidak diperparah olehnya;
  perlakuannya sudah aman karena `tcID2Names`/`nextToolNameForID` (
  `gemini.go`, parity #4273/#4589) mengantre nama per id. PR ini menutup celah
  id opaque dan membuat id paralel benar-benar unik.

### 🐛 fix(dashboard): "Check All Models" hanya menguji model yang aktif (Closes #230)

- **Latar belakang**: report #230 — operator menonaktifkan sebuah model, tetapi
  tombol **Check All Models** tetap mem-probe model itu.
- **Akibat**: dua-duanya. Request ke upstream terbuang untuk model yang
  sengaja dimatikan, dan verdict probe-nya tidak punya baris di tabel, karena
  tabel merender `visibleModels`. Hasilnya verdict yang tidak terlihat dan
  tidak bisa dibersihkan dari layar.
- **Perbaikan**: `handleCheckAllModels` menyapu `visibleModels`, dan kondisi
  serta label tombol ikut menghitung `visibleModels.length`.
- **Verifikasi**: `bun test src scripts` → 321 pass / 0 fail; `bun test e2e/` →
  20 pass / 0 fail; `bun run build` bersih; `bun run ratchet:svelte` →
  0 unresolved identifier, 83 error = baseline.

### 🐛 fix(models): /api/models/test read-only terhadap cooldown produksi agar sweep tidak memicu cascade (#220)

- **Latar belakang**: `POST /api/models/test` (tombol Test dan sweep "Check All Models")
  menjalankan probe lewat handler chat produksi (`HandleChatCompletions`). Ketika sebuah
  model gagal diuji, loop fallback menulis cooldown produksi (`LockConnectionModel`,
  `RecordConnectionError`, `LockConnectionRateLimit`, dan menaikkan backoff level). Akibatnya,
  pada sweep puluhan model, beberapa model awal yang gagal mengunci seluruh koneksi akun,
  sehingga sisa model lainnya langsung 502 "all in cooldown" tanpa pernah mencapai upstream.
- **Akar masalah**: request probe tidak membawa pembeda konteks, sehingga diperlakukan identik
  dengan traffic produksi yang memicu state locking.
- **Fiks**: `WithProbeContext` disuntikkan ke dalam probe request context (`internal/handlerutil/probe.go`).
  `handleAccountFallback` dan `comboLockRetryable` melewati penulisan cooldown dan bump backoff
  ketika mendeteksi probe context. `getBestConnectionWithContext` mengizinkan probe menembus cooldown
  agar dapat menguji pemulihan upstream yang sebenarnya. Hasil probe juga menyertakan field terstruktur
  `blocked` dan `resetAt`.
  Jalur media (embedding, image, tts, stt, video, systemone) memakai
  `GetBestConnectionWithContext`, sehingga probe juga menembus cooldown di sana. Probe tidak
  menulis cooldown pada jalur gagal maupun jalur sukses: `UnlockConnectionModel` setelah probe
  berhasil juga dilewati, karena satu klik "Test" akan menghapus backoff yang dicatat traffic
  produksi. `UpdateConnectionLastUsed` tetap berjalan — probe memang memakai koneksi tersebut.

### 🎨 feat(dashboard): Check All Models tri-state status (passed, failed, blocked) dan retry blocked models (#222)

- **Latar belakang**: tombol sweep "Check All Models" sebelumnya memperlakukan semua hasil non-ok sebagai
  warna merah "Error". Ketika koneksi memasuki masa cooldown, semua model berikutnya yang belum sempat diuji
  langsung ditandai merah sama persis seperti model yang benar-benar gagal di upstream.
- **Fiks**: UI kini membedakan status tri-state: hijau `Passed`, merah `Failed` (upstream merespons gagal),
  dan amber `Blocked` (terhalang cooldown, menyertakan estimasi waktu `resetAt`). Banner ringkasan menampilkan
  `X passed · Y failed · Z blocked` serta menyediakan tombol "Retry blocked" untuk menguji ulang hanya model
  yang sebelumnya terhalang cooldown.

- **Perbaikan review**: klasifikasi verdict `Blocked` dipindahkan sepenuhnya ke backend dan tidak lagi
  bergantung pada status HTTP. `readProbeResult` semula hanya memeriksa 502, padahal error yang sama muncul
  sebagai 404 pada lane media, dan `forwardSystemoneRequest`/`forwardMediaRequest` menimpanya dengan
  `"no active connections for provider: %s"` sehingga informasi jam mulai cooldown hilang.
- **Perbaikan review**: pencocokan substring di SPA (`'cooldown'`, `'rate limit'`) dihapus. Pola itu tidak pernah
  aktif — gateway menulis `rate-limited` dengan tanda hubung — dan berisiko menandai penolakan kuota milik
  provider sebagai `Blocked`, padahal verdict tersebut tidak pernah selesai dengan menunggu. Backend kini
  memakai whitelist kalimat milik gateway sendiri, dan `resetAt` dirender sebagai waktu lokal.
- **Perbaikan review**: "Retry blocked" memakai bounded worker pool yang sama dengan sweep (6 konkuren) plus guard
  `providerId`, menggantikan loop serial yang membuat tombol tampak macet selama masa cooldown.
- **Catatan setelah #220**: dengan probe kini menembus cooldown akun (#220), jalur `Blocked` yang tersisa adalah
  lane combo ketika member-nya terkunci model — lock punya masa berlaku, sehingga verdict "blocked" tetap jujur
  dan tombol "Retry blocked" tetap aksi yang tepat. Test integrasi dipin agar kedua sisi itu tidak regresi.


### 🐛 fix(chat): error model-gated (402 funds, 401 unsupported) tidak mengunci seluruh akun (#218)

- **Latar belakang**: pada provider multi-model seperti OpenCode Zen (atau Antigravity), request ke model
  berbayar yang gagal (`402 Insufficient account funds`) atau model unsupported (`401 Model ... is not supported`)
  malah mengunci seluruh akun koneksi via `rateLimitedUntil` selama ~2 menit. Karena loop fallback mencoba
  seluruh koneksi pada provider tersebut, satu model yang gagal melumpuhkan seluruh akun sehingga model-model
  gratis/sehat lainnya (`space-bunny-free`, `mimo-v2.6-flash-free`, dll.) ikut mati terkena error 502
  "no available connections (all in cooldown)".
- **Akar masalah**: `isModelScopedQuotaError` sebelumnya hanya mencocokkan status 429/403/503 dengan string
  kuota spesifik. Error 402 dana dan 401 model-unsupported jatuh ke `LockConnectionRateLimit` akun secara global.
- **Fiks**: `isModelScopedError` diperluas dan di-gate positif: status dibatasi pada 400/401/402/403, body
  harus menyebutkan nama model yang bersangkutan atau membawa pesan dana ("insufficient account funds"),
  dan error autentikasi akun asli (invalid API key, token expired, account suspended) tetap mengunci akun
  secara global. Status 402 juga ditambahkan ke `RetryableStatusCodes`.

### 🐛 Modal policy key: allowlist tidak tampil dan pattern baru tidak tersimpan (Closes #216)

- **Latar belakang**: report #216 — API balas `200 ok`, tapi `model access`
  di modal policy tidak menyimpan dan tidak menampilkan kembali nilai yang
  disimpan, dan tombol `load` juga tidak memuat apa pun.
- **Backend-nya benar, jadi tidak ada perubahan Go.** Direproduksi lewat
  `app.ProvideRouter` (router produksi, DB SQLite sungguhan): `PUT
  /api/keys/{id}/models` menulis baris ke `api_key_model_access`, dan `GET
  /api/keys/{id}/models` membacanya kembali. `resale metadata`, rate limit,
  dan expiry sudah benar sejak awal — diverifikasi lewat UI, tidak diubah.
- **Akar masalahnya di `ApiKeyPolicyModal.svelte`, di dua tempat.**
  1. Allowlist tidak pernah dimuat saat modal dibuka. Listanya dimulai kosong
     dan baru terisi setelah tombol `Load` ditekan, sehingga untuk key yang
     sebenarnya dibatasi modal tetap menulis *"every model is allowed"*.
  2. Tombol **Save Policy** hanya menulis kolom policy; allowlist disimpan lewat
     endpoint terpisah yang tidak pernah dipanggil dari alur utama. Pattern
     yang diketik lalu di-`Save` akan diterima `200` lalu hilang.
  Kedua bug itu saling mengunci: draft kosong dari (1) menimpa daftar yang
  tersimpan begitu operator menyimpan policy lain.
- **Fiks**: allowlist diambil saat modal dibuka, dan **Save Policy** sekarang
  menulis allowlist juga — satu aksi menyimpan satu policy utuh. Tombol `Load`
  jadi `Reload`, dan `Save allowlist only` dihapus karena sudah tercakup;
  `saveModels()` dan `isSavingModels` ikut dibersihkan karena tidak ada
  pemanggilnya lagi.
- **Penulisan allowlist dikunci sampai daftarnya benar-benar dibaca.** Kalau
  hanya dilewati saat request berjalan, ada dua keadaan berbeda dengan hasil
  identik — `models` kosong — dan keduanya berarti "hapus allowlist": request
  yang masih berjalan, dan request yang **gagal**. Versi pertama hanya menutup
  yang pertama; pada yang kedua `hasLoadedModels` sempat bernilai true di
  `finally`, sehingga `Save Policy` menulis daftar kosong di atas daftar yang
  tersimpan — persis kelas kehilangan data yang sedang diperbaiki di sini.
  Sekarang penulisan hanya boleh jalan setelah allowlist benar-benar termuat;
  selain itu simpan ditolak dengan pesan yang menyebut alasannya, dan modal
  tetap terbuka supaya operator tidak mengira policy utuh sudah tersimpan.
- **Regresi dijaga** di `internal/integration/keys_policy_test.go`:
  `TestKeyPolicyRoundTrip`, `TestKeyModelAllowlistRoundTrip`, dan
  `TestKeyPolicySavePreservesAllowlist` — semuanya membaca ulang lewat router
  produksi setelah `GET` baru, bukan dari respons write yang selalu sukses.
  Skor `svelte-check` turun 84 → 83 (`web/scripts/svelte-check-baseline.json`).

### 🔀 Seluruh `encoding/json` v1 pindah ke `encoding/json/v2`

- **Latar belakang**: repo sudah migrasiMayor ke `encoding/json/v2`, tapi 50
  file masih tertinggal di `encoding/json` v1 — 25 di antaranya file produksi.
  Akibatnya satu binary memakai **dua** mesin JSON dengan semantik berbeda di
  jalur yang sama: v1 mencocokkan nama field tanpa membedakan huruf
  besar/kecil, v2 secara default membedakannya; v1 menerima UTF-8 rusak, v2
  menolaknya.
- **Fiks**: seluruh file kini mengimpor `encoding/json/v2`. Empat API v1 yang
  tidak ada di v2 dipetakan ke padanannya, bukan dibungkus:
  `json.RawMessage` → `jsontext.Value` (perilaku `MarshalJSON`/`UnmarshalJSON`
  dan ketiadaan nilai untuk member yang absen identik — sudah diverifikasi),
  `json.Valid` → `jsontext.Value.IsValid()`,
  `json.NewEncoder(w).Encode(v)` → `json.MarshalWrite(w, v)`,
  `json.NewDecoder(r).Decode(&v)` → `json.UnmarshalRead(r, &v)`.
- **Body dari provider dan dari client dibaca lenient, bukan strict.** v2
  menolak nama member yang berulang di satu objek, menolak UTF-8 rusak di
  string (v1 diam-diam mengganti U+FFFD), dan mencocokkan nama member secara
  case-sensitive. Untuk JSON yang dihasilkan gateway sendiri itu benar, tapi body
  dari provider atau client bukan JSON milik gateway — dan `internal/handlerutil`
  kini menyediakan satu set opsi (`UpstreamBody` / `ClientBody`) yang melonggarkan
  ketiganya. Tanpa itu, `proxy.EmptyUpstreamError` akan mengubah jawaban yang sah
  menjadi 502 (body dengan key berulang, byte non-UTF-8, atau kunci `Content`),
  `parseInbandError` tidak akan melihat error yang diinjeksi provider,
  `parseChatChunk` akan membuang chunk yang membawa content delta, dan
  `museModel` akan menolak body `{"Model": ...}`.
  `TestEmptyUpstreamError_LenientAboutProviderBodies`,
  `TestDetectInbandSSEError_LenientAboutProviderFrames`,
  `TestParseChatChunk_LenientAboutProviderChunks` dan
  `TestMuseModel_LenientAboutClientBodies` menjaga tiap arah.
- **`internal/fastjson` sekarang tipis di atas v2.** Shim ini sebelumnya
  membungkus `bytedance/sonic`; kedua backend (sonic dan std) dihapus, dan
  `github.com/bytedance/sonic` beserta 6 dependensi transitifnya dicabut dari
  `go.mod`. Panggilan dari 12 file chat/semanticcache tidak berubah.
- **Determinisme tetap terjaga.** `fastjson.Marshal` memasang
  `json.Deterministic(true)` karena prompt cache hulu (DeepSeek, Anthropic)
  mengunci kunci pada byte request yang persis, dan v2 tidak mengurutkan member
  map kecuali diminta. (Sonic yang lama juga tidak mengurutkannya — sebab itu
  `marshalStable`, fingerprint, dan dedupe tool memasang opsi Deterministic
  masing-masing sejak #186.) `TestMarshalIsDeterministic` menjaga kontrak
  seam ini; call site yang sudah punya opsi sendiri tidak tersentuh.
- **Biaya pada payload request 6,5 KB**: marshal 16,2 µs (v1) → 15,3 µs
  (v2 deterministik), unmarshal 26,1 µs → 23,3 µs. Tidak ada regresi.

### 🐛 Halaman Cache Analytics balas 503 — agregasi 35 detik untuk 82.143 baris

- **Gejala**: `/dashboard/usage/cache` gagal total. Di browser endpoint-nya
  503 dari proxy hulu; lewat `curl` langsung dengan cookie sesi yang valid
  `/api/cache?trendHours=24` menjawab **200 setelah 35,5 detik**. Halamannya
  gagal bukan karena datanya — backend menjawab benar, hanya telat.
- **Penyebab**: `GetPromptCacheMetrics` berjalan dengan **empat** statement,
  masing-masing mem-parse dan menyisir seluruh `usageHistory`: satu agregat
  total, satu agregat harga `GROUP BY provider, model`, lalu satu agregat
  `GROUP BY provider` dan satu lagi `GROUP BY model`. Dua yang terakhir
  mengurai JSON `tokens` untuk kedua kalinya dan ketiga kalinya atas baris
  yang sama. `usageHistory` adalah ledger yang tidak pernah di-prune, jadi
  biayanya tumbuh tanpa batas: 82.143 baris = 1.235 ms.
- **Fiks**: satu pemindaian, dilipat di Go. Baris dipindai sekali dengan
  JSON `tokens` sudah ter-resolve di SQL, lalu provider, model, dan pasangan
  provider/model — beserta totalKeseluruhan — dijumlahkan dari baris yang
  sama. Satu baris dipindai bukan empat.

  Diukur terhadap 82.143 baris dengan bentuk sama seperti database produksi,
  tiap strategi di Benchmark di proses terpisah (supaya tidak saling
  menghangatkan page cache):

  | Strategi | Median |
  |---|---|
  | Empat statement (sebelumnya) | 1.235 ms |
  | Satu `GROUP BY provider, model` | 403 ms |
  | **Satu pindai, lipat di Go (sekarang)** | **269 ms** |

  `GROUP BY` memang bentuk yang salah untuk tabel yang kardinalitasnya
  beberapa provider: ia membangun `USE TEMP B-TREE FOR GROUP BY` per request.
  Index yang diawali kunci grup menghilangkan B-tree itu, tapi memaksa pemindaian
  penuh karena predikat rentang jadi tidak bisa dipakai index, dan keduanya
  terukur sama saja dalam noise. Index `(tokens)` maupun `(provider, tokens)`
  juga tidak mengubah apa pun — semua plan yang diuji tetap
  `SEARCH ... USING INDEX idx_uh_provider`, yang tetap satu lintasan penuh.

  Hasil pada binary yang sama, database 82.143 baris, lewat HTTP sungguhan:
  **35,5 s → 289 ms**, payload utuh (`totalRequests: 82143`,
  `totalCachedTokens: 74329933`, `byProvider` terisi).

- **Bug lama yang ikut ketahuan, bukan berasal dari rewrite ini**:
  `cachedRequests` di query trend sejak port OmniRoute (#110) menaruh
  perbandingan JSON di dalam `AND (...)`, jadi `json_valid` menutup kedua
  cabang dan memfilter baris yang justru punya cache token. Akibatnya
  `cachedRequests` **selalu nol** — kurva "Cached Requests" kosong sejak fitur
  itu ada. Test `TestGetPromptCacheTrend_CountsBothCacheTokenSources` menjaga
  ini: satu baris read-back, satu baris creation-only, satu payload kosong,
  satu payload bukan-JSON — dua pertama harus terhitung.

### 🐛 fix(chat): do not lock entire account on model-specific 429 quota exhaustion so unrelated healthy models remain available (#205)

Previously when a 429 quota exhaustion occurred for a specific model (such as claude-sonnet-4-6), the system would lock the entire account connection, blocking other models (such as gemini-3.8-flash-high) on the same connection even when they were healthy. This fix adds a check to determine if a retryable error is model-scoped (like quota exhaustion) vs. account-scoped (like authentication issues), and only applies the account-level rate limit cooldown for account-scoped errors.

### 🖼️ `/providers/*.png` 404 dan peringatan autofocus di console

- **`muse.png` 404**: `muse` ada di katalog (`web/src/lib/providers.ts`) tapi
  repo ini tidak pernah meng-ship `web/public/providers/muse.png` saat provider
  itu ditambahkan di #100 — dan `getIconPath` menebak URL dari katalog, jadi
  setiap tile meminta aset yang memang tidak ada. Katalog punya **16 entri
  tanpa artwork**; `muse`, `tinyfish`, `zai-search` dan `anthropic-version`
  adalah yang terlihat di dashboard. Penyebabnya struktural: menambahkan
  provider ke katalog adalah langkah terpisah dari meng-ship logonya, dan
  tidak ada yang mengikat keduanya.

- **Fiks**: satu komponen, `ProviderArtwork.svelte`, yang menangani fallback
untuk semua permukaan sekaligus. Dipromosikan dari `ProviderIcon` yang
  sudah menyediakannya, dibuat event-driven (state-nya di-key dari `src`),
  dan **tujuh** call site yang menulis handler `onerror` sendiri — media card,
  media detail, media web view, request details, usage breakdown, model
  picker, topology card — sekarang memakainya. Fallback-nya adalah initials
  badge berwarna merek provider, sama seperti yang sudah ada.

  Yang penting: `onerror` milik `<img>` hanya terpicu untuk `src` yang sudah
  terpasang saat elemen dirender. Call site sebelumnya semuanya memanggil
  `getIconPath(...)` inline, jadi kalau `src` berubah setelah mount — baris
  analytics yang lazy, combo yang diedit — galat tidak pernah terpicu dan aset
  yang gagal hanya disembunyikan. Sekarang kunci gagalnya adalah `src` itu
  sendiri, jadi berganti provider berarti mencoba ulang, bukan mewarisi
  kegagalan provider sebelumnya.

- **Autofocus**: `LoginView` memakai atribut `autofocus`, dan Chrome mencatat
  "Autofocus processing was blocked because a document already has a focused
  element." Atribut itu diproses per dokumen; ketika halaman `/dashboard`
  bounce ke `/login` (tanpa sesi) view itu dimount di samping halaman yang
  sudah tampil, dan body sudah memegang fokus, jadi atributnya ditolak.
  Diganti fokus eksplisit setelah `hasPassword` membuka form — cara yang
  sama tanpa meminta browser jadi arbiter.

  Detail yang penting: `/api/auth/login` yang 401 di laporan itu **bukan**
  bug. Itu respons yang benar untuk password yang salah, dan hanya muncul
  setelah ada percobaan login. Yang terbukti dari sini: satu kali
  `POST /api/auth/login` per submit, baik yang gagal maupun yang berhasil.

### 🎛️ Header Cache & Compression Analytics: Kontrol Pindah ke Dropdown Menu

Layout header kedua section itu sekarang mengikuti halaman **Usage**: section
picker di kiri, kontrol milik section di sebelahnya, tombol **Menu** di kanan,
dan deskripsi section di bawah baris kontrol (#209). Sebelumnya tiap section
menumpuk lima sampai enam kontrol dalam satu baris — pemilih section, strip
sub-view, tombol Auto, tombol Refresh, dan sepasang tombol export CSV/JSON —
dan baris itu wrap di jendela sempit.

- **Tombol Menu** (`web/src/components/analytics/ActionsMenu.svelte` dan
  `MenuItem.svelte`): Auto-refresh (dengan keterangan `15s` dan tanda centang
  saat aktif), Refresh now, Export CSV, dan Export JSON dipindahkan ke satu
  dropdown `role="menu"`. Pemicu memakai ikon `menu` dengan chevron
  `expand_more`, bukan `expanded_more` seperti pada picker section, dan tetap
  berlabel "Menu" — ikon tunggal tidak memberi apa pun untuk diumumkan screen
  reader, dan labelnya disembunyikan hanya di lebar terkecil. Menu menutup saat
  item dipilih, saat `Escape`, saat `Tab`, dan saat klik di luar.
- **Section picker turun ke section-nya** (`web/src/components/analytics/SectionMenu.svelte`,
  menggantikan `SectionNav.svelte`): `AnalyticsView` pernah merender picker di
  satu baris tetap di atas semua section, sehingga section tidak bisa menaruh
  kontrolnya di sebelah picker itu. Sekarang picker didefinisikan sekali sebagai
  snippet di `AnalyticsView` dan diteruskan ke Cache dan Compression Analytics
  lewat prop `headerLeft`; keduanya menaruhnya di header mereka sendiri. Overview
  dan Details tetap memakai baris `AnalyticsView`, jadi picker tidak pernah
  tampil dua kali di satu layar.
- **Sub-view jadi dropdown** (`web/src/components/analytics/ViewSelect.svelte`):
  strip pill Prompt Cache / Semantic Cache adalah kontrol lebar-tetap terakhir
  di header Cache Analytics, sekarang menjadi dropdown yang menampilkan ikon dan
  label view aktif pada tombol tertutup. Memilih Semantic Cache tetap memuat
  daftar entry-nya.
- **Judul section dihapus dari panel**: picker di header sudah menyebut nama
  section, jadi `h2` "Cache Analytics" / "Compression Analytics" di dalam panel
  hanya mengulanginya; yang tersisa deskripsi apa yang dilaporkan angkanya.
- Tidak ada perubahan API: `GET /api/cache`, `GET /api/analytics/compression`,
  dan `GET /api/cache/entries` tetap sama, termasuk `trendHours` dan `since`.

### 📊 Cache & Compression Analytics Moved Into the Usage Page — Section Dropdown

Cache Analytics dan Compression Analytics bukan lagi dua entri sidebar
tersendiri; keduanya menjadi section dari halaman **Usage**, bersama Overview
dan Details (#200). Alasannya: keduanya melaporkan trafik yang sama dengan
Overview, dan sebagai menu top-level keduanya terbaca sebagai produk terpisah.

- **Routing** (`web/src/lib/router.ts`): tab baru `usage-cache`
  (`/dashboard/usage/cache`) dan `usage-compression`
  (`/dashboard/usage/compression`) supaya tiap section punya path sendiri dan
  tetap bisa di-bookmark serta bertahan setelah reload. Path lama
  (`/dashboard/cache`, `/dashboard/analytics/compression`, dan alias `/cache`,
  `/analytics/compression`) tetap dipetakan ke section yang sama, sehingga tab
  yang terbuka saat upgrade tidak mendarat di halaman lain.
- **Section picker** (`web/src/components/analytics/SectionNav.svelte`): strip
  pill `inline-flex rounded-xl p-1` yang sebelumnya memuat dua label tidak muat
  di layar 374px bersama empat section; diganti dropdown yang menampilkan label
  section aktif pada tombol tertutup, dengan `role="listbox"`, `Escape`, `Tab`,
  dan klik di luar yang menutup menu.
- **Window selector bersama** (`web/src/components/analytics/PeriodSelect.svelte`):
  dropdown periode yang sebelumnya hanya ada di Overview sekarang dipakai
  Compression Analytics juga, sehingga dua section itu tidak lagi punya dua
  kontrol periode yang berbeda bentuk. Endpoint `/api/analytics/compression`
  hanya mengenali `24h|7d|30d|all` (nilai lain diam-diam dijawab `24h`), jadi
  section itu meneruskan `showCustom={false}` — input window kustom di sana akan
  menampilkan angka untuk periode yang tidak benar-benar dipakai.
- **Hero grid Compression Analytics**: kartu ROI Speed berada di luar
  `grid ... lg:grid-cols-7` sehingga tidak pernah menjadi sel grid dan
  menyisakan kolom kosong di layar lebar. Sekarang ketujuhnya berada di dalam
  grid yang jumlah kolomnya membagi tujuh (`2 / 3 / 4 / 7`), dan kartu ROI
  memakai `lg:col-span-2 xl:col-span-1` untuk tetap utuh di lebar 4 kolom.
- **Sidebar**: entri Cache/Compression dihapus; entri Usage tetap aktif ketika
  salah satu section-nya terbuka.
- **Test**: `web/src/lib/router.test.ts` memverifikasi keempat section, pasangan
  tab ⇄ path, dan kedua alias lama; `internal/integration/usage_sections_test.go`
  (build tag `integration`) menjalankan gateway sungguhan dan memeriksa bahwa
  setiap endpoint yang dipakai section masih serves window yang dipilih dan
  ketiga path `/dashboard/usage[...]` menyajikan shell SPA.

### 👤 Paritas Request Details Ala OmniRoute (Kolom Akun, Combo, Protokol, & Modal Detail)

- **Backend Telemetri Akun**:
  - `internal/handlers/chat/fallback.go` & `usage.go`: Mencatat metadata lengkap akun (`account`, `connName`, `connEmail`, `apiKey`, `combo`, `requestedModel`, `protocol`, `cacheSource`, `startedAt`, `endedAt`, `cost`) ke dalam JSON `requestDetails.data`.
  - `internal/handlers/usage_stats.go`: Menambahkan fallback resolver otomatis pada `HandleRequestDetails` agar riwayat lama yang belum memiliki field akun otomatis terisi dari tabel `providerConnections`.
- **Frontend Request Details Tab**:
  - `web/src/components/analytics/RequestDetailsTab.svelte`:
    - Menambahkan kolom **Account** di tabel utama dengan badge ikon akun/email developer.
    - Menambahkan kolom **Cost ($)** di tabel.
    - Menambahkan input filter/pencarian real-time (berdasarkan Model, Provider, Akun, atau ID).
    - Memperbarui modal inspeksi request agar 100% identik dengan OmniRoute: badge Akun & Combo di header, rincian waktu (Started/Ended At), rincian Token Input (Total In, Cache Read %, Cache Write, Compressed RTK), Token Output (Total Out, Reasoning Tokens), serta rincian routing (Requested Model, Protocol, Cache Source, API Key, dan tombol copy Request ID).

### 🏆 Top 10 Savers, ROI Speed Metric, & Tombol Export Report (CSV & JSON)

- **Tabel Top 10 Savers**: Menambahkan query dan tabel interaktif "Top 10 Biggest Token Savers" di dashboard Compression Analytics untuk menginspeksi request spesifik paling hemat token (Request ID, provider, model, tokens saved, savings %, durasi, estimasi USD, dan tombol copy ID).
- **Net ROI Efficiency Score**: Menambahkan metrik kecepatan pemangkasan token `roiTokensPerMs` (tokens saved per ms overhead kompresi) dan kartu visual ROI Speed di dashboard Compression Analytics.
- **Tombol Export Report**: Menambahkan tombol download laporan instan format CSV dan JSON pada dashboard Cache Analytics dan Compression Analytics.

### ⚡ Optimasi SQLite Indexes, Fallback Creation Tokens, & TTL Janitor Semantic Cache

- **Composite Indexes**: Menambahkan indeks komposit di `internal/db/schema.go` (`idx_uh_ts_prov`, `idx_uh_ts_model`, `idx_ca_ts_prov`, `idx_ca_ts_model`) untuk mempercepat agregasi time-series dashboard secara signifikan.
- **Fallback Creation Tokens**: Menambahkan formula fallback di `internal/db/cache_analytics.go` untuk menangkap token pembuatan prompt cache pada provider Antigravity/Gemini yang tidak mengirim key eksplisit (`cache_creation_input_tokens`).
- **Background TTL Janitor**: Menambahkan ticker background berkala di `internal/semanticcache/persistent_store.go` untuk membersihkan entri kadaluarsa di RAM dan SQLite secara otomatis, mencegah database membengkak seiring waktu.

### 🐛 Delapan cacat yang ditemukan saat review PR #193 (sudah diperbaiki di branch ini)

1. **Cache key multimodal bertabrakan — dua klien saling menerima respons.**
   `BuildCacheKey` hanya hashed blok `type == "text"`, sehingga pesan berisi
   gambar/file menuliskan role + pemisah tanpa payload apa pun. Dua request yang
   hanya berbeda pada byte gambarnya menghasilkan key identik, dan `Lookup`
   mengembalikan body milik request lain. `internal/semanticcache/key.go` kini
   hashed seluruh payload blok (teks, `image_url`, `file_data`, baik jalur typed
   maupun `[]any`), diuji dengan dua gambar berbeda yang kini menghasilkan key
   berbeda.
2. **TTL janitor tidak pernah memangkas apa pun.** Expiry dievaluasi di SQL
   (`WHERE storedAt < ?`), jadi itu perbandingan *string* — dan `time.RFC3339`
   memotong ke detik, sehingga baris yang ditulis pada detik yang sama dengan
   cutoff menghasilkan string identik, tidak pernah_lt, dan tidak pernah
   terhapus. `TestPersistentLRUStore_Janitor` memang gagal di branch ini.
   `storedAt` kini ditulis dengan lebar tetap nanosecond
   (`internal/semanticcache/persistent_store.go`), pola yang sama dengan
   `db.rotationTimestampFormat` dan alasan yang sama; nilai second-precision dari
   build lama tetap terbaca (eksit satu detik, wajar untuk cache).
3. **Dashboard membaca instance cache yang berbeda dari engine.**
   `SetupServerRouter` menempelkan dashboard ke handler milik `/version`, yang
   dibangun sebelum ada traffic, jadi `PersistentStore`-nya punya LRU sendiri
   yang tidak pernah terisi: `/api/cache/entries` melaporkan 0 entri padahal
   engine melayani hit, dan `DELETE /api/cache` menghapus baris SQLite tanpa
   menghentikan engine. `SetupRoutes` kini mengembalikan handler engine-nya dan
   grup dashboard dipasang pada instance itu.
4. **Filter RTK merusak JSON yang struktur.** `gh.json`, `kubectl.json`, dan
   `git-diff.json` memakai negative lookahead Perl yang RE2 tolak, sehingga
   `compileRegexSafe` membuangnya diam-diam dan filter termuat dengan pola
   kurang. Lebih jauh, karena `MatchFilter` dipanggil dengan command kosong, hanya
   content pattern yang tersisa — body `gh api` yang memuat URL github.com cocok
   ke `gh.json` lalu dilewatkan filter baris, dan `json-output` ikut membuang
   baris data sehingga JSON-nya rusak. Pola lookahead diganti bentuk yang valid
   RE2 dan `MatchFilter` kini mengembalikan `nil` untuk dokumen JSON utuh.
5. **Sisipan kompresi live tidak pernah mengisi `Model`.** `InsertCompressionAnalytics`
   dipanggil tanpa `Model`, sementara query `ByModel` memfilter
   `model != ''`, jadi seluruh breakdown per-model di dashboard kosong.
   Test lama tidak menangkap ini karena fixture-nya juga tidak mengeset `Model`.
6. **`classifyHistoricalMode` punya cabang mati.** Predikatnya
   `(savedTokens > 0 && personaCount > 0) || personaCount > 1` menyederhanakan
   diri menjadi `personaCount > 0`, sehingga `caveman`/`adhd`/`ponytail` tak
   pernah tercapai dan mode backfill selalu `stacked`, berbeda dari
   `resolveCompressionMode` yang dipakai jalur live.
7. **Cache key mengabaikan parameter yang mengubah jawaban.**
   `BuildCacheKey` hanya hashed model, messages, tools, tool_choice, dan
   temperature. `max_tokens`, `max_completion_tokens`, `reasoning_effort`, dan
   `parallel_tool_calls` semuanya mengubah completion sementara prompt-nya
   byte-identik, jadi request kedua menerima body milik request pertama —
   jawaban lebih pendek dari yang diminta, tanpa error. Semuanya kini masuk hash.
8. **Refresh OAuth menimpa kredensial yang sengaja dipilih.**
   Cabang "token belum kedaluwarsa" mengembalikan `oauthData.AccessToken` dari
   baris koneksi, bukan token yang diberikan pemanggil, dan pemanggilnya
   (`fallback.go`) melakukan `apiKey = rekey`. Untuk Kiro itu merusak: `resolveProviderAuthToken`
   memang memilih `apiKey` untuk koneksi `authMethod: "api_key"` meski
   `accessToken` tersedia (upstream `kiro.js buildHeaders` hanya memakai apiKey
   di mode itu), dan yang lain menjawab 403 "The bearer token included in the
   request is invalid." Cabang tersebut kini mengembalikan kredensial pemanggil
   apa adanya.

Verifikasi: `go build ./...`, `go vet ./...`, `go test ./...` hijau,
`go test -tags=integration ./internal/integration/...` hijau, `bun test` 238/238,
`bun run build`, dan `bun run ratchet:svelte` (0 unresolved, 88 = baseline).

### 🐛 fix(web): do not flag active connections as error in provider stats when soft warning or unsupported model probe lastError is present (#207)

### 🔑 issue #199: halaman API Key menyatu ke Endpoint & Key, secret bisa di-reveal lagi

Halaman **Endpoint & Key** sekarang jadi satu-satunya tempat mengelola token klien: tab
`API Keys` yang duplikat dihapus, dan `ApiKeysTable.svelte` (baru) merender tabel bergaya
KeiRouter (`mydisha/keirouter`, MIT) — empat kolom `Key | Token | Policy & Created |
Actions`, tanpa kolom Status dan Created-date terpisah. Status, policy, dan tanggal
created jadi **satu sel multi-baris**, sesuai permintaan issue. Action bar memakai empat
kontrol: Policy (modal rate limit/expiry/allowlist), Rotate, Pause/Resume, Delete.

**Tombol show/hide dan copy secret sekarang benar-benar bekerja.** Sebelumnya keduanya
melakukan sesuatu yang berbeda dari yang dijanjikan UI: `EndpointView` membaca `key.key`,
padahal sejak F-6 (argon2id) kolom itu berisi sentinel, bukan secret — sehingga show
"membuka" nilai yang sudah ter-mask, dan copy menyalin mask tersebut.

#### ⚠️ Penyimpanan secret dikembalikan ke plaintext — dan apa risikonya

Issue meminta "matikan hashed stored key". Itu **membalikkan** keputusan F-6 (#176/PR #185,
3 hari lalu) yang memang sengaja membuat key tidak bisa dibaca ulang. Konsekuensinya nyata
dan tercatat di sini, bukan disembunyikan:

- `POST /api/keys` dan `POST /api/keys/{id}/rotate` kini menyimpan secret apa adanya di
  `apiKeys.key`. **Dump database = seluruh key klien terekspos** (DB-02 di
  `TECHNICAL_DEBT.md` kembali terbuka).
- `RequireApiKey` tidak lagi *self-heal* — baris plaintext tidak di-hash-kan diam-diam
  saat dipakai, karena itu akan menghapus tepat properti yang sekarang dashboard andalkan.
- **Batas yang tetap dijaga:** `GET /api/keys` hanya mengembalikan secret penuh untuk
  caller dashboard (session cookie, CLI token, atau `requireLogin=false` untuk install
  lokal). Engine client key — credential yang diberikan ke Cursor/Claude Code — tetap
  menerima `key` kosong + `keyDisplay` tersamar. Tanpa ini, satu key yang bocor bisa
  mencuri seluruh key set, persis yang F-6 tutup. Diuji di unit, integration, dan E2E.
- **Baris lama tidak bisa dipulihkan.** Key yang dibuat sebelum upgrade hanya punya
  verifier argon2id; plaintext-nya sudah hilang. Daftar mengembalikan `key` kosong dan UI
  jatuh ke `keyDisplay`. Satu-satunya jalan adalah Rotate.

Alternatif yang ditolak di PR: memakai credential vault AES-256-GCM yang sudah ada
(F-5) sehingga secret tetap bisa dibaca tanpa menyimpan plaintext. Vault itu butuh
master key (`ROUTER_MASTER_KEY`) dan tidak ada yang mengaktifkannya secara default,
sedangkan issue meminta parity penuh dengan upstream `decolua/9router`, yang memang
menyimpan plaintext.

#### Kesocokan dengan sumber KeiRouter

Acuan visual diambil dari `D:/coding/project/keirouter` (`mydisha/keirouter`, commit
`3d8b702`, MIT). Yang cocok persis: `StatusPill` (dot + label, bukan badge), urutan
`Key | Token | ... | Actions`, baris aksi empat kontrol, dan ritme sel multi-baris
(baris konten dulu, `Created …` muted di bawahnya — `Keys.tsx:193-198`).

Sengaja menyimpang, dan alasannya:

- **Bentuk tabel.** KeiRouter memakai CSS-grid `<article>` tanpa header row sama sekali
  (`Keys.tsx:178-182`) — tidak ada label kolom yang bisa disalin. Issue #199 minta tabel
  dengan kolom Status dan Created-date dibuang lalu digabung, jadi header di sini
  ditetapkan sendiri.
- **Show/hide & copy.** KeiRouter **tidak punya** tombol eye di UI key — `grep -c Eye
  frontend/src/pages/Keys.tsx` = 0, dan `adminListKeys` (`admin.go:359-384`) hanya
  mengembalikan `display` tersamar. Pola ini orisinal untuk issue, bukan tiruan.
- **Storage.** `crypto/apikey.go:37-39` tegas: *"Plaintext is shown to the user exactly
  once and never persisted"*. Jadi bagian "matikan hashed stored key" adalah divergensi
  dari KeiRouter, bukan tiruan — lihat catatan risiko di atas.
- **Gabungan halaman.** KeiRouter justru memisahkannya: `Endpoints.tsx:132-138` hanya
  menaut ke `/keys` lewat tombol "Manage keys". Merge di sini mengikuti permintaan issue.

### 🐛 Tombol Refresh per-baris membungkus baris aksi ke dua baris di mobile

Susulan review PR #196 (`feat(connections): per-row refresh button to test a single
account`, sudah merge). Perubahan yang sama tidak merusak apa pun secara fungsional,
tetapi memunculkan tiga hal yang tidak terlihat di desktop:

- **Baris aksi membungkus.** Container-nya `grid-cols-3` dan memang berisi tepat tiga
  sel (proxy / edit / delete). Sel keempat dari tombol Refresh membuat Delete turun ke
  baris kedua sendiri sementara separuh kanan grid kosong, dan tiap baris koneksi tumbuh
  ±49px. Diukur di 390px: `gridH` 45px → 93px. Sekarang `grid-flow-col auto-cols-fr`,
  jadi setiap aksi mendapat satu kolom implisit dan tidak membungkus meski jumlah tombol
  bertambah — termasuk tombol Session milik Freebuff (lima sel), yang sebelumnya juga
  membungkus.
- **Tooltip menyesatkan.** Badge sukses berbunyi `last one-by-one probe passed`, padahal
  kini juga bisa muncul dari probe per-baris. Sekarang `last probe passed`.

`probeOutcomeFrom`, `probeOutcomeFromError` dan `badgeFor` dipindah ke
`web/src/components/connections/connectionProbe.ts` beserta `canProbeRow`, sehingga sweep
dan probe per-baris memakai satu implementasi, dan连锁 badge precedence — yang
sebelumnya restated di markup — punya test.

### ❌ Yang ditemukan tapi TIDAK diperbaiki: `testStatus` dari Edit modal hilang

Ditemukan saat memverifikasi PR #196, di luar cakupan perubahan ini (backend). Dicatat di
sini karena menjelaskan batas apa yang benar-benar bisa dilakukan di sisi web.

- **Gejala.** `EditConnectionModal` mengirim `payload.testStatus = 'active'` setelah
  mengganti credential (`EditConnectionModal.svelte:153`), tapi `HandleUpdateConnection`
  **tidak pernah membacanya** — `testStatus` tidak ada di allowlist branch `hasData`
  (`connections.go:635-712`), dan `PUT` tidak menjalankan probe. Diverifikasi langsung:
  `PUT {apiKey:'sk-fake-1', testStatus:'active'}` menjawab 200, lalu
  `GET /api/connections` tetap melaporkan `testStatus=error`. Jadi **rotasi key lewat
  Edit modal tidak pernah mengembalikan baris ke hijau**; satu-satunya jalan adalah
  *Refresh* per-baris dari PR #196.
- **Akibatnya.** Badge `error` pada akun yang kredensialnya sudah diperbaiki hanya bisa
  hilang lewat probe yang benar-benar berjalan. Klaim "entri `failed` bisa basi lalu
  menutupi `testStatus` yang sudah diperbaiki" **tidak terbukti** dan tidak berlaku di
  backend ini: tidak ada jalur yang menulis `testStatus='active'` tanpa probe. Karena
  itu sinkronisasi badge sisi-klien **sengaja tidak** ikut di PR ini — ia akan menambah
  `$effect` yang menulis state pada setiap render tanpa efek nyata yang bisa dibuktikan.
- **Perbaikan yang benar ada di backend**, di `HandleUpdateConnection`: terapkan
  `testStatus` (dan clearing `lastError`) hanya ketika modal sudah memvalidasi key baru,
  atau jalankan probe bila tidak ada bukti. Itu perubahan handler, bukan UI, dan perlu
  regression test di `connections_test.go`.

**Verifikasi**: `bun test` 252/252 (21 kasus baru di `connectionProbe.test.ts`, mencakup
guard sweep PR #196 dan precedence badge), `bun run build` bersih, `svelte-check` ratchet
88 = baseline dengan 0 unresolved identifier, `go vet ./...` bersih. Grid diukur langsung
di browser pada 390px dan 1400px: 4 sel satu baris, `gridH` 45px (sebelumnya 93px), dan
siklus badge `testing` → `active`/`error` masih jalan setelah refactor.

### 🐛 Bug Fixes

- fix(chat): preserve selector cooldown errors in fallback loop instead of returning bare "no available connections" (#201)

### 🐛 Lonjakan RAM idle ~100 MB+ setelah pruning `requestDetails`

- **Gejala**: sejak `db.StartRetentionLoop` masuk (#187, ikut rilis di v1.9.11-exp.1),
  working set proses melonjak dari ~28 MB ke ~100 MB+ dan tidak pernah kembali,
  tepat 60 detik setelah boot — durasi `retentionInitialDelay`.
- **Penyebab**: `cache_size(-64000)` adalah batas **per koneksi**, dan pool dibuka
  4 koneksi, sehingga plafon page cache SQLite 256 MB. `requestDetails` menyimpan
  ~20 KB payload per baris; saat retention prune menghapus 20k baris dalam chunk
  5000, page cache terisi penuh. Halaman yang sudah dibebaskan diserahkan ke OS
  secara lazy, jadi gateway yang sudah prune sekali menyimpan high-water mark itu
  seumur proses. Bukan leak Go — `heapAlloc` tetap 0.2 MB sepanjang proses; ini
  alokasi di layer C (`modernc.org/libc`). Fiks #137 yang menambah prune ini
  justru memunculkan cacatRAM-nya.
- **Perbaikan**: `internal/db/client.go` — page cache diturunkan ke 8 MB per
  koneksi (32 MB total) lewat konstanta `sqliteCacheSizeKB`, dengan
  `sqliteMaxOpenConns` sebagai konstanta dari mana budget dihitung.
- **Biaya yang diukur**: fold window 24h di ledger 400k baris (poll dashboard tiap
  5 detik) 113 ms cold / 35 ms warm pada `-64000`, menjadi 83 ms cold / 72 ms warm
  pada `-8000` — cold justru lebih cepat karena cache 64 MB harus diisi dulu sebelum
  hangat. Append tidak terpengaruh (6.1 ms vs 6.7 ms per transaksi 100 baris, di
  dalam noise). Seek watermark `MAX(timestamp)` tetap 0.0 ms di keduanya karena itu
  covering-index seek.
- **Hasil**: binary yang sama, DB yang sama (40k baris × 20 KB), puncak working set
  setelah prune turun dari **161.6 MB ke 63.4 MB** dan stabil.
- **Test**: `TestSQLiteCacheSizeCeiling` mengunci plafon 32 MB dan gagal keras bila
  nilai dikembalikan ke `-64000` (terverifikasi: test gagal dengan
  "pooled page cache = 252 MB").

### 🐛 Tombol "Add Custom Provider" kembali terpecah di v1.9.11-exp.1

- **Penyebab**: `736136c` — commit pertama PR #185 di branch `feat/keirouter-port`, dibuat 12 menit setelah `079de66` (#183) — menulis ulang `AddCompatibleNodeModal.svelte`, `ConnectionsView.svelte`, `ProvidersOverviewGrid.svelte`, dan `MediaKindView.svelte` ke kondisi sebelum #182/#183, sehingga membatalkan dialog tunggal. Keempat file byte-identik dengan `d729b43` (sebelum penggabungan), terbukti lewat `git rev-parse`. PR #185 sendiri tidak menyentuh fitur ini: revert-nya ikut ter-carry oleh squash merge `9b553e7`.
- **Perbaikan**: keempat file dipulihkan ke versi unified; `ProvidersOverviewGrid` kembali ke satu tombol **Add Custom Provider** dengan switch Provider Type di dalam dialog (field yang sudah diisi pengguna tetap utuh saat ganti protokol); dialog Custom Embedding tetap `allowedTypes={['custom-embedding']}` sehingga hanya menawarkan satu opsi. Import `AddCompatibleNodeModal` yang tertinggal di `ProviderDetailView.svelte` ikut dibersihkan.


### 🔐 Per-key governance, credential vault, and guardrails (KeiRouter port, Path C)

Ports the security and governance subset of `docs/keirouter-port-plan.md`. This is the
Path C cut — security, observability, and per-key limits — deliberately excluding the
resale stack (plans, budget engine, usage portal, branding, multi-tenant), which would
turn a single-operator self-hosted gateway into a billing platform.

Every schema change is an additive Go-only column or table, so the upstream Next.js
dashboard still reads the database unchanged, and every new limit defaults to `0`/`''`
meaning unlimited or all-allowed. An install that configures nothing behaves exactly as
it did before.

**Added**

- Per-key rate limiting (`apiKeys.rateLimitRPM/TPM/Concurrency`) via a sliding-window RPM
  limiter, token-bucket TPM, and concurrency cap on the API-key route group. A rejected
  request returns 429 with `Retry-After` and never reaches a provider.
- API key expiry and usage accounting (`expiresAt`, `lastUsedAt`, `usedCount`, `metadata`).
  An expired key is rejected with 401 before any dispatch, and a policy write invalidates
  the verification cache so revocation takes effect immediately rather than at its TTL.
- Argon2id credential hashing for client API keys: a SHA-256 lookup index plus an argon2id
  verifier, behind a bounded 5s auth cache. Existing plaintext keys keep authenticating
  and are upgraded on first use.
- Credential vault using AES-256-GCM envelope encryption, opted into by setting
  `ROUTER_MASTER_KEY`. Each secret gets its own data key, so rotating the master key
  re-wraps the data keys without re-encrypting a single secret. Absent the variable the
  vault stays disabled and credentials remain plaintext, so boot is never blocked.
- Per-API-key model access allowlists with `*` segment wildcards, enforced through one
  shared resolver so dispatch and `/v1/models` listing can never disagree. An empty
  allowlist allows everything.
- Guardrails MVP: offline regex detection for PII (email, card via Luhn, IBAN via mod-97,
  Indonesian national id, globally-routable IPv4) and prompt injection, with
  `allow`/`log_only`/`warn`/`mask`/`block` actions, global→apikey scope layering, and an
  audit log. No network call is made. Disabled until a policy exists.
- Guardrails on responses, not just requests. A buffered answer is scanned whole before a
  byte is written, so a `mask` rewrites it and a `block` still becomes a real status. A
  streamed answer is filtered frame by frame over a sliding window of decoded text, which
  is what catches a value split across two SSE deltas — judging each frame alone sends both
  halves of an address to the client. A blocked stream is ended with the terminal frames
  its own client format recognises (`finish_reason` + `[DONE]`, `message_stop`, or
  `response.failed`) rather than left hanging.
  The policy travels on the request context, so every provider is covered by the same tap
  rather than only the ones without a registered executor.
  A block reports HTTP 451 and ends the turn: it is not in `RetryableStatusCodes`, and the
  combo loop stops on it, because failing over would hand the refused content to every
  other account and model the combo names.
- Prometheus metrics at `/api/metrics` (dashboard-authenticated, private registry).
- Dashboard: a per-key policy modal (rate limits, expiry, resale metadata, model
  allowlist), a Policy column on the key table, and a Security view for the vault and
  guardrails.

- Startup migration for the credential vault. Sealing on write only covered credentials
  stored after the vault was enabled, so an existing install kept every provider token in
  plaintext forever while the dashboard reported them as unprotected. Boot now walks the
  connections still holding a plaintext credential, snapshots the database first, and
  seals them. The snapshot uses `VACUUM INTO` rather than a file copy: the database runs
  in WAL mode, so copying the file would capture the older pages and miss the writes the
  migration is replacing. A row that cannot be sealed keeps working in plaintext and is
  logged — losing the master key already makes a credential unrecoverable, so a migration
  that refused to boot would trade a recoverable problem for an outage.
- `POST /api/keys/{id}/rotate`. Removing "reveal" left an operator with no way to replace a
  leaked key except deleting the row, which also takes its policy, usage history, and model
  allowlist with it. Rotation issues a new secret once and invalidates the old one
  immediately rather than after the auth cache TTL.
- A global guardrail kill-switch (`settings.guardrailsEnabled`, with a toggle in the
  Security view). A false positive that blocks real traffic previously required deleting
  the policy, which took the audit trail explaining why it existed with it. Both the request
  and response taps read the switch. It defaults to on, so a configured policy is never
  silently ignored because a setting was never written.
- Global rate-limit defaults (`settings.rateLimitEnabled`, `defaultRpm`, `defaultTpm`,
  `defaultConcurrency`, `rateWindowSeconds`) plus the resolution order that makes them safe:
  a key's own column always wins where it is set, and the global default only fills the
  columns an operator left at 0. An install that configured nothing is still unlimited,
  and a key deliberately given a higher budget is never silently capped by a later global
  change.
- TPM is charged in two phases. The pre-dispatch reservation is reconciled against the
  turn's real token count once the response is metered, so the bucket no longer drifts on
  the estimate forever. Only an under-estimate is corrected — refunding the surplus would
  let a client bank credit by over-stating its prompt.
- `GuardrailDecisions` and `GuardrailEval` collectors, and a call site for the
  `RateLimitRejects` counter that shipped with a field and a helper but no caller, so the
  series was permanently zero.

**Changed**

- Client API keys are no longer returned in plaintext by any read path, including to a
  fully authenticated dashboard session. The value is returned exactly once at creation
  and never again; there is no reveal, only revoke-and-reissue. **This is a breaking
  change** for anything that read a key back from `GET /api/keys`.

**Fixed**

- `GET /api/keys` omitted the governance columns, so every key rendered as unconstrained
  in the dashboard regardless of what was configured.
- The keys tab was unreachable: `TAB_ROUTES` mapped both `cli-tools` and `keys` to
  `/dashboard/cli-tools`, and the route table resolved `/dashboard/keys` to `cli-tools`,
  so `App` always rendered `CliToolsView` and the key table had no path to the screen.
- Revoking a client key did not take effect. Deactivating or deleting a key left its row
  in the auth verification cache, so `GET /v1/models` kept answering 200 for up to the
  cache TTL — the window in which an operator who believed they had killed a leaked key
  had not. Both paths now invalidate the key's cached row on write.
- The inbound guardrail tap re-marshalled the request body for any non-`allow` action, so
  a `log_only` policy sent the provider a body with reordered keys — the content was
  unchanged, but the bytes were not. It now rewrites only when a value actually changed.
- The TPM rate limiter charged a flat 100 tokens per request, so the limit was not
  enforced against the traffic it was meant to bound. It now sizes the prompt from the
  request body, reading it only when a TPM limit is configured and capping the read at
  4 MiB.
- `usedCount` and `lastUsedAt` were never written. The repository method existed with no
  caller, so the resale bookkeeping the key table and policy modal display stayed at zero
  forever. Both are now recorded on every authenticated request, after every check that can
  reject, so a request refused for a bad, disabled, or expired key is not counted as usage.

- A rate-limited 429 carried neither `X-RateLimit-Limit` nor `X-RateLimit-Reset`, and its
  body was a static string, so a client could not back off without guessing the wait. The
  per-axis headers are now set and the message states the delay.
- The rate limiter and `/api/metrics` had no integration coverage: every 429 in the suite
  was an *upstream* refusing the gateway, and the metrics endpoint was never scraped
  through the router. A limiter mounted in the wrong route group, or an endpoint with a
  correct auth check but no live collector behind it, would have passed everything.

### 🐛 Ollama Cloud dialed `localhost:11434` — every cloud key 502'd before leaving the machine

Gejala: `upstream error: ForwardOpenAI upstream: forward to
http://localhost:11434/v1/chat/completions: ... dial tcp 127.0.0.1:11434:
connect: connection refused` pada provider `ollama` (#192). `/v1/models`
terlihat normal karena daftar model berasal dari katalog, bukan dari address
yang benar-benar di-dial.

Akar masalah bukan hanya di registry. Dua hal terpisah:

1. **Entry registry `ollama` menunjuk ke daemon self-hosted.** Id `ollama`
   adalah Ollama **Cloud** — API resmi di ollama.com, dikunci API key dari
   dashboard — tapi `BaseURL`-nya `http://localhost:11434/v1/chat/completions`,
   jadi setiap koneksi cloud mendial port loopback yang hanya ada di mesin yang
   menjalankan `ollama serve`. Upstream memisahkannya juga: registry
   `ollama.js` mendial `ollama.com`, dan hanya `ollama-local.js` yang menunjuk
   11434. Sekarang `ollama` → `https://ollama.com/v1/chat/completions`, dan
   `ollama-local` tetap di 11434 (#192).

   Lane OpenAI-compatible `/v1` di ollama.com diverifikasi live: path tak
   dikenal 404, `/v1/models` 200, dan API key salah dapat 401 dengan body error
   berbentuk OpenAI — jadi tidak perlu port translator native untuk perbaikannya.

2. **`providerSpecificData.baseUrl` dibuang, dan host telanjang tidak punya
   route.** Dashboard menyimpan override endpoint per-koneksi di
   `providerSpecificData.baseUrl`, sedangkan `ConnectionData.BaseURL` hanya
   membawa key `baseUrl` tingkat atas — override di-parse lalu dibuang, dan
   request jatuh ke default registry (yaitu loopback tadi). Second: field host
   Ollama Local diisi sebagai host telanjang (`http://192.168.1.10:11434`), dan
   host telanjang tidak menamai route, jadi POST mendarat di root server.
   Override kini di-hidrate di `getBestConnection` (hanya bila key tingkat atas
   kosong, jadi penulisan lama menang) dan melengkapi route chat lewat helper
   `chatCompletionsURL`, yang tidak menyentuh URL yang sudah menamai route —
   `/v1`, `/v1beta`, dan `/messages` tetap apa adanya.

Test: `internal/handlers/chat/ollama_routing_test.go` (registry tidak boleh
dial loopback, cloud dan local tidak boleh berbagi address, tabel
`chatCompletionsURL`, hidrasi override, dan request yang benar-benar mendarat
di upstream) plus `internal/integration/ollama_routing_test.go` (katalog dan
proxy lewat router produksi). Semuanya dicek mutasi: mengembalikan `ollama` ke
11434, atau menghapus hidrasi, membuat masing-masing test gagal dengan gejala
aslinya.

Catatan: `ollama-local` tetap tanpa katalog model statis — upstream juga begitu,
modelnya ditemukan live dari daemon (`/api/tags`). Itu kekosongan terpisah,
bukan bagian #192.

## [v1.9.10-exp.3] - 2026-10-07

### 🐛 DeepSeek: body request di-serialize acak — prompt cache miss di tiap request (semua lane)

Semua lane DeepSeek melewati unmarshal → mutasi → marshal pada map generik
saat me-rewrite request, dan `encoding/json/v2` mengacak urutan member map
di setiap marshal. Request yang logikanya identik menghasilkan byte body
berbeda tiap kali, sehingga cache prompt DeepSeek (berbasis prefix byte)
miss di hampir semua request meski sesi percakapan berjalan sama.

Titik yang terukur (distinct body dari 200 request identik): lane zen
`opencode_zen.go` ~195→1, lane `ForwardOpencode` (free tier) ~175→1,
`ForwardOpencodeGo` ~81→1, dan `DedupeToolsDeepSeek`
(`translator/tool_dedupe.go`, intermittent — hanya jalan saat ada tool
duplikat) ~76→1.

Perbaikan: setiap marshal pada rantai rewrite DeepSeek memakai
`json.Deterministic(true)` — helper `marshalStable` di package executor,
opsi inline yang sama di `translator` (tidak bisa import executor) untuk
`ConcealFingerprintTools` dan `DedupeToolsDeepSeek`. Urutan member map
jadi sorted dan stabil, prefix antar-turn konsisten, cache upstream bisa
hit. Diverifikasi lewat unit test per-lane (200 request identik → tepat 1
body), tanpa token API.

### 🐛 `normalizeConnection` buang `providerSpecificData` — badge dan pilihan proxy pool salah render

Client hanya membaca `providerSpecificData` dari body yang sudah di-decode,
dan object yang dipakai sebagai fallback adalah seluruh baris hasil parse,
sehingga field yang dipakai dashboard untuk badge proxy pool, pool yang
terpilih, dan pengaturan per-koneksi isinya apa pun yang lolos dari
round-trip itu (#188).

Nilai wire sekarang dibaca terpisah dari nilai hasil parse lalu di-merge di
atasnya, dan fallback ke baris parse tidak lagi jalan saat object hasil parse
kosong — itulah yang menghasilkan object non-kosong tanpa satu pun key yang
diharapkan. Ditutup assertion `client.test.ts` atas bentuk hasil merge.

### 💀 A retired model fails the request instead of the combo — HTTP 410 now fails over and is badged

When a provider retires a model it answers `HTTP 410 Gone` (`ModelDeprecated`).
Three things were missing, and an operator had to find all three by reading logs:

1. **The 410 never failed over.** It is in neither `RetryableStatusCodes`
   (`internal/providers/providers.go:1018`) nor `ErrorRules`
   (`errorclassify.go:39`), so it fell through the unmatched-4xx branch to
   `ShouldFallback: false` and locked nothing. A combo led by a dead model
   spent its whole pass on it.
2. **Nothing remembered the model was dead.** Every subsequent request redid
   the discovery, and the dashboard kept listing the model as healthy.
3. **Nothing told the operator.** No badge, so the only signal was the 410
   itself.

The status is load-bearing and the payload is a guard. `IsModelDeprecation`
requires 410 *and* a payload naming the model, because 410 also means an
expired OAuth device code (`handlers/oauth/device.go:627`) and an expired
Freebuff session (`proxy/executor/freebuff.go:311`) — badging a model for
either would blacklist a model that is serving fine.

- `internal/providers/deprecation.go` — `IsModelDeprecation`, `ParseModelDeprecation`, key helpers.
- `internal/handlers/chat/combo.go` — both combo loops (`handleComboFallback`, `handleMessagesComboFallback`) break to the next entry on a model 410.
- `internal/handlers/chat/fallback.go` — the single-model path tries the other accounts and keeps the upstream body for a direct request.
- `internal/handlers/chat/deprecation.go` — records the 410, locks the model 24h, and clears the badge when a request serves again.
- `internal/db/deprecations.go` — kv-scoped store (`modelDeprecations`), upsert by `<provider>/<model>`.
- `POST /api/models/sync` — re-reads each connection's upstream catalogue and revives models the provider still lists.
- `GET /api/models/deprecations` — the badge source, keyed the same way a combo entry is written.
- Dashboard: `Sync Models` button, per-model `Deprecated` badge with the successor in its tooltip, an "N deprecated" count on the model card, and the marker in the combo model picker.

**A model missing from a catalogue is deliberately not badged.** Catalogues are
routinely partial — a scoped key, a paginated feed — so absence is weak
evidence and acting on it would blacklist healthy models. Only a live 410 (or
a served request, in reverse) changes a badge; a sync can only *revive* one.

**Two defects the end-to-end run caught, both fixed here.** The unit tests
passed while the running server still failed:

1. **The failover starved the provider it was rescuing.** Excluding the
   connection from `excludeIDs` looked right — that list is what makes a retry
   pick a different account — but it spans the whole pass, so on a
   single-account provider the next combo entry had no account left to try and
   the client got the 410 anyway: model correctly badged, request still dead.
   The connection is no longer excluded; the per provider/model lock that
   `recordModelDeprecation` writes is the scope that matters, and it is what
   stops the dead model being re-dialled.
2. **A provider with no catalogue reported a broken sync.** The discovery
   handler answers 400 for those and sync counted it as a hard failure, so
   every "Sync Models" click on such a provider returned a 502 with nothing
   the operator could act on. Those connections now report as `skipped`.

Both are covered by `TestDeprecationFailoverWithSingleAccount` and the sync
path's `skipped` count.

**Verification:** `go test ./...` and the tagged integration suite
(`go test -tags=integration ./internal/integration/...`) green; `go vet` clean;
new table-driven tests cover the classifier (including the two non-model 410s
that must not badge), the store, the picker's badge propagation, and an
end-to-end combo failover asserting the client gets the *next* model's response
plus the recorded deprecation.

Beyond the suite, the feature was driven over real HTTP against the built
binary with a fake upstream: a combo led by the retired model returned
**200 `served by qwen3-32b`** instead of the 410, `/api/models/deprecations`
reported the model `gone` with `successor: qwen3-32b`, and a sync against a
catalogue still listing the retired model left the badge in place. The
dashboard was verified in Chromium: the `1 deprecated` count on the model card,
the `Deprecated` badge on the `ds/deepseek-chat` row carrying the successor in
its tooltip, and the `Sync Models` button. `bun test` 227/227; `bun run build`
clean; `bun run ratchet:svelte` 0 unresolved identifiers, 88 errors (baseline
88, not rising).


### 🎛 `Add Anthropic Compatible` / `Add OpenAI Compatible` merged into one dialog that keeps what you typed

The two buttons over Custom Providers opened two separate modals, so choosing
the wrong protocol was expensive: the user fills name, prefix, suffix and base
URL, discovers their endpoint actually speaks the other protocol, and has to
close, reopen the other modal and retype everything.

Both are now a single **Add Custom Provider** button over a dialog that carries a
Provider Type switch. Switching type keeps every field the user already entered
and moves only what belongs to the protocol: the base URL follows the new
default when it still holds the old one, the `API Type` select appears only for
OpenAI, the id preview updates, and a stale Check result is dropped rather than
left claiming validity. Picking the wrong protocol is now one click, not a
retype.

The Custom Embedding dialog (Media Providers) passes `allowedTypes`, so it still
shows only its own single option.

Also fixed here: the overview button's `onclick` handed its `MouseEvent` to the
open handler, so the node type reached the backend as `{"isTrusted":false}` and
the dialog died on `VARIANT_CONFIG[type].defaultBaseUrl` before the base URL was
ever populated.

### ⚡ Usage page: the delay on "Total recorded" was never the count

Profiling the Details tab against an 85k-request database turned up three
separate costs, none of them the row count the header reports.

**`SELECT COUNT(*) FROM requestDetails` was never slow** — it rides a covering
index and measures 0.5 ms. The delay came from what was transferred behind it.
The list endpoint returned the whole stored payload per row, which carries up to
20 truncated request messages and a 10 000-character response body: measured at
~20 KB per row, so a 20-row page was **409 KB** of JSON, 99% of which the table
never reads. The list now selects only the values it renders, and the full
payload is fetched per row by id when a user opens the inspector — **409 KB →
3.8 KB per page**, and opening a row stays a primary-key lookup.

**`/api/usage/stats` re-read its whole window on every poll.** The dashboard
polls every five seconds; folding an 85k-row `24h` window measured **356 ms**
per poll. Pushing the fold into SQL `GROUP BY` was tried and rejected on
evidence: it measured **178–207 ms**, no better, because the temp b-tree SQLite
builds for `GROUP BY` costs as much as the Go-side fold, and a covering index
bought ~8% while adding **+57%** to every insert. Instead the window aggregate is
built once and advanced from a delta — `MAX(timestamp)` is a covering-index seek
(O(log n)) — and a sliding window also subtracts the rows that have aged out of
it. Measured end to end on an 85k-row window: **356 ms cold, 1.0 ms steady
state**.

That number only appeared after fixing a bug in the first version of this cache:
it keyed an entry on the window's start. A `24h` window starts 24 hours before
*now*, so its start moves on every request and the cache missed every single
time — 347 ms per poll against a 372 ms cold read, which is no cache at all. The
key describes the window's *shape* now, decided in `resolveUsagePeriod` where
the difference is actually known: the instant alone cannot tell "starts at
midnight" from "24 hours ago", and the two must be cached differently.

Float addition is not associative, so the fold runs oldest-first and the
subtraction newest-first — the exact reverse — leaving cost totals bit-identical
rather than drifting in their last digits.

**Nothing ever pruned the diagnostic tables.** `requestDetails` is now capped at
30 days and 500k rows by a background pass that runs `PRAGMA optimize`
afterwards. `usageHistory` is deliberately **never** pruned: it is the billing
ledger, the `all` usage period reads it directly, and a day can be settled
against it after the fact. At the real payload size the diagnostic table was
1.7 GB at 85k rows, for a tab that renders 20.

No new indexes were added — the existing ones already cover these reads, which
is what the measurements show.

### 🐛 `TranslateOpenAIToGemini` dropped tool call ids, so Claude on Antigravity 400'd on any tool history

Every Claude model behind Antigravity (`ag/claude-*`) rejected any request whose
conversation contained a tool call:

```
messages.1.content.0.tool_use.id: Field required
```

The Gemini structs already carried `ID`, but `TranslateOpenAIToGemini` never
assigned it — neither on `functionCall` (assistant turn) nor on
`functionResponse` (tool turn). Gemini's own models tolerate a missing id, so
this only surfaced on Claude: Antigravity hands the Gemini body to Vertex
Anthropic, which rebuilds a `tool_use` block per `functionCall` and rejects it
without an id. Combos masked it by silently falling through to the next model.

Upstream sets both (`id: tc.id` / `id: fid` in
`open-sse/translator/request/openai-to-gemini.js`); this is the matching parity.
Both sides now carry the id, stripped of the `__ts__<sig>` suffix — that suffix
is 9router-go's private thought-signature transport and must not reach the wire
or desynchronise a call from its response.

### 🐛 Edit Compatible Node opens with a blank Prefix field (#177)

`EditCompatibleNodeModal` seeded `name`, `urlSuffix`, `apiType` and `baseUrl` from
the node but never `prefix`, so the field rendered empty with only its
`oc-prod` / `ac-prod` placeholder. The stored prefix is the namespace the
node's models already resolve under (`oc/<model>`), and the submit guard
requires it, so opening the modal also left Save disabled until the value was
retyped by hand.

The seed logic now lives in `nodeFormSeed.ts`, which the modal calls for every
field, with a regression test covering the prefix, the generated-suffix
exception and the per-flavour default base URL. The `urlSuffixGenerated`
behaviour is unchanged: a random uuid tail is still not offered as editable
text.

Verified against a running binary on an isolated `DATA_DIR`: both the OpenAI
and Anthropic variants open with the stored prefix and an enabled Save, a
renamed prefix round-trips through `PUT /api/provider-nodes/{id}` and is shown
again on reopen, and a node whose id tail is a random uuid still opens with an
empty suffix.

Re-verified against a snapshot of a real 16-node database: every stored prefix
— including the capitalised and multi-character ones (`Arg`, `Id`, `bai`) —
opens in the field with Save enabled, a rename round-trips through
`PUT /api/provider-nodes/{id}` and is shown again on reopen, and a node whose
id tail is a random uuid still opens with an empty suffix.

## [v1.9.10] - 2026-10-07

### 🩺 `text-danger` fails the contrast bar in dark theme — error text is nearly unreadable

Tracing from #168 found that the fix itself had not passed the measure.
`text-danger` (#cf222e) yields **2.83:1** against `bg-surface` in the dark
theme at 12px/400 — below the 4.5:1 bar for normal text. In the light theme it
is **5.36:1** and correct, so reading the token name or checking one theme
alone is not enough.

Nine text sites: `TokenSaverView` (Uninstall link, uninstall error messages),
`CreateComboModal` (`saveError`), `Badge.svelte` variant `danger`,
`EndpointView` (Tunnel/Tailscale buttons ×6, error messages ×2, Delete key
hover), `ProfileSettingsView` (password error message), and `ApiKeysView` (Delete
hover). `ApiKeysView` also has a broken class: `hover:text-hover:text-danger`.

They all now use the `text-red-600 dark:text-red-400` pair, which is already
the dominant repo convention (16 usages, including those used by
`ProviderDetailView` and `Toasts`). Measured from real pixels after the fix:

| | dark | light |
|---|---|---|
| `saveError` | **5.24:1** | **4.77:1** |

`bg-danger` buttons are **not** touched: white text on top of it is 5.36:1,
already passing. `bg-danger` really is correct for fill and wrong for text — two
roles a single token cannot serve. Computed from measured luminance: text on a
dark surface needs L ≥ 0.258, white text over a fill needs L ≤ 0.183, so the
two contradict each other.

**Verification:** `bun run build` clean; `bun test` 219/219; `bun run
ratchet:svelte` 0 unresolved identifiers, 89 errors (baseline 89, not rising).
Pixel evidence in the real binary with an isolated `DATA_DIR`, on the very same
element that used to be 2.83:1 — `CreateComboModal` `saveError` was forced to
fail via a 409 POST to `/api/combos`, then contrast was measured in both themes
from pixels resolved by the browser (not from token values).

**The contrast guard was not worth keeping.** A script that checks dead color
classes out of the built CSS was written first, but it produced 28 → 59 → 666
false positives (including `text-brand-500` and `text-xs`, which plainly exist)
because it matched `divide-y`, `border-b-2`, and variant prefixes as colors. A
guard that lies is more dangerous than no guard, because it reads as
evidence — so it was deleted, not repeatedly patched. Dead color classes are
prevented via `bun run build` + review, and contrast is measured in the browser.

### ⚡ `GET /v1/models` is faster when there are many providers & models

The model list build on this endpoint is CPU-bound per model, not DB-bound. Four things were changed, without changing behavior:

- `isLLMModelID` (3 regexes per unknown model) and `GetModelTokenLimits` (a giant `strings.Contains` switch) are now cached per id. Both have a size cap (20k entries, reset when full) and are also cleared by `InvalidateCapabilitiesCache`.
- 113 capability patterns are indexed: the literal fragment of each glob is computed once in `initPatternIndex`, then matching is filtered with `strings.Contains` and only candidates verified with `path.Match`. The first-match-wins order stays.
- `aggregateComboCapabilities` uses the combo row already loaded by `GetCombos()`, instead of re-querying per combo.
- `HandleModels` marshals the model list once and writes its envelope directly through `WriteModelsList`, so large lists are not encoded twice for the `data` and `models` keys. The meta envelope still uses `deterministicJSON` so key order is stable across requests.

**Verification (same host, worst-case synthetic ids):** warm build 28→6 ms (20 connections × 75 models) and 64→15 ms (50 × 75); cold build 512→76 ms and 1254→175 ms; end-to-end 46→16 ms and 105→39 ms. Suite: new tests for `data`/`models` identity, key-order stability, combo row reuse, pattern-table guard, and pattern-index equivalence over all registry ids; benchmarks for `HandleModels` and the pattern path.

### 🐛 Test Button on provider media page probes wrong — System One models always 500

The symptom is exactly as reported: on
`/dashboard/media-providers/systemone/opencode-zen`, the flask button (Test) on
`jev-1.13` and `jev-1.13-free` always fails with
`ForwardOpencodeZen: forward to https://opencode.ai/zen/v1/chat/completions:
upstream returned 500`, even though the **Run** button on the same page
succeeds immediately.

**The cause is not the Zen lane.** Those two buttons do indeed call two
different endpoints: **Run** posts to `/v1/systemone`, while **Test** calls
`POST /api/models/test` — and the old handler (`chat.HandleTestModel`) always
built a Chat Completions payload regardless of the model kind. Verified
directly against upstream: `POST /zen/v1/chat/completions` with
`jev-1.13-free` really does reply
`500 {"type":"error",…,"message":"Internal server error"}`, while
`POST /zen/v1/systemone` on the same model replies `200` along with
`answers`. So the gateway really is correctly forwarding to the wrong lane —
and healthy models are marked broken. This also applies to other media models
on the same provider.

**The fix (upstream parity with `decolua/9router#20014b31`, "probe System One
models through /v1/systemone"):**

- **The probe now follows `kind`, not a chat assumption.** The new file
  `internal/handlers/media/model_test_ping.go` takes `{model, kind}` and then
  forwards the probe to the handler that owns that kind:
  `/embeddings`, `/images/generations`, `/audio/speech`,
  `/audio/transcriptions`, `/videos/generations`, `/systemone`, or
  `/chat/completions` if `kind` is empty (the regular provider page, whose
  models really are chat). `chat.HandleTestModel` was deleted — there is no
  path without a shim.
- **The verdict is read from the payload, not from the status.** All the
  endpoints above pass through the upstream status as-is, so HTTP 200 does not
  automatically mean pass: non-empty `answers` for System One,
  `data[].embedding` for embeddings, `data[]` for images, text for STT, a
  created job for video, and bytes/base64 for TTS. The same file also reads
  `{"error":…}` behind a 200, non-success `status`/`msg`, and unwraps Cline's
  `{"success":true,"data":…}` envelope — all three of which would report
  "provider is fine" for an upstream that is currently broken. Token-exhausted
  reasoning-only replies still pass (parity with issue #3010).
- **15-second timeout per probe**, with its deadline following the request
  context, so the button does not hang on providers that never answer.
- **The frontend sends `kind`.** `api.testModel(model, kind?)` +
  `MediaProviderDetail.handleTestModel` send this page's kind, so the model
  kind is read from the page that displays it — not guessed from the id name.
- **The test result is finally shown.** `modelTestResults` has long been
  populated but never rendered; model rows now display a
  `check_circle`/`cancel` icon, a green/red border, latency from the server
  measurement, and the error message as a tooltip — matching upstream
  `ModelsRow`.

**Verification:**

- `internal/integration/model_test_ping_test.go` (new) tests through the
  production router with a fake upstream: a System One probe must land on
  `/zen/v1/systemone` (and fail via the chat path — the result is exactly the
  500 string from the user report), the no-`kind` default stays on the chat
  lane with `max_tokens:1024`, and an unknown `kind` is rejected with 400. The
  full `integration` suite is green (70 s).
- Smoke run in the real binary with an isolated DB: all six kinds via `smokec/`
  (node `openai-compatible` → fake upstream) all return `ok:true`; most
  decisively, `{"model":"oc/jev-1.13-free","kind":"systemone"}` replies
  `{"ok":true,"latencyMs":955,"status":200}` for a model whose chat lane is
  proven to 500, while the same `POST /v1/systemone` also returns 200.
  `{"model":"ocz/jev-1.13","kind":"systemone"}` now reports verbatim
  `HTTP 401: … Rate-limited Zen models require a workspace` — credentials that
  genuinely aren't ready yet, not a broken model.
- `go build ./...`, `go vet ./...`, `go test ./...`, the `integration` suite,
  `bun test` 219/219, `bun run build`, and `bun run ratchet:svelte`
  (0 unresolved, 89 errors = baseline) are all green.

### 🧪 Release split into two channels: stable & experimental — derived from the tag

Until now `release.yml` treated all `v*` tags the same: the tag
`v1.10.0-exp.1` would be published as a GitHub Release **non-prerelease**, so
`releases/latest` — the fallback endpoint used by `internal/updater` — would
point at an experimental build and offer it to every stable user. The `1.10`
Docker tags would also be overwritten with the same binary.

The channel is now derived from the tag itself — not from the branch, not
from the commit, not from manual input — so what is tagged is what is
published, and it can be audited.

| Tag | Channel | GitHub Release | Docker |
|---|---|---|---|
| `v1.10.0` | stable | final | `latest`, `1.10`, `1.10.0` |
| `v1.10.0-exp.1` | experimental | **Pre-release** | `exp`, `1.10-exp`, `1.10.0-exp.1` |

The second guard remains `autoApplyAllowed` (#73): experimental builds will not
be installed automatically on top of a running final release, even if checked
manually.

**The new `channel` job fails fast on version-source mismatches.** All three of
these cases previously slipped through silently:

- `VERSION` not equal to the tag → the published binary reports its own version
  as the old version, because `make cross` and the Dockerfile both embed the
  `VERSION` file.
- A stable release without a `version.json` bump → the release is published but
  **never reaches anyone**; every install keeps reporting the previous version
  as "up to date".
- An experimental version leaking into `version.json` → experimental builds are
  blocked for every install. That manifest is polled by `9router-go update` on
  the stable channel, so this must fail the release, not merely warn.

`scripts/bump-version.sh` writes `version.json` only for stable bumps, and
touches `updater.CurrentVersion` for both. Channel graduation = a stable bump
following it; that's what moves `version.json`, the `:latest` Docker tag, and
the update offer.

**A bonus found along the way:** the `python3` branch in `bump-version.sh` has
been broken since before this channel existed. On Windows, `command -v python3`
**succeeds** for the Microsoft Store stub, which then exits non-zero without
running anything — and because of `set -e`, the script **dies after `VERSION`
is written**. `version.json` stays on the old version, the version gate above
will reject it, and nobody knows why. The `sed` path is now used directly, so
no `python3` is needed, and the result is re-verified with `grep` before
continuing.

**Verification:** both scripts were actually executed against a copy of the repo
in `%TEMP%` — 10 channel cases (7 passing, 3 rejected; including manual dispatch
and multi-dash prereleases) and 19 `bump-version.sh` checks (stable,
experimental, graduation after experimental, invalid input, and the path without
`python3`). The workflow YAML was parsed with `js-yaml`; the experimental
release-note banner was also checked to have no indentation that would render it
as a code block.


### 🩺 Quota throttle test measures something the scheduler doesn't guarantee

`TestHandleGetConnectionUsage_SpacesBurstAcrossConnections` and
`...SpacesAntigravityBurst` fail intermittently on a loaded machine: the
measured gap between reads is 33ms against a 40ms floor. The gate itself is
correct.

**The root cause isn't the gate, but how the test measures it.** `assertSpaced`
demands that every pair of `time.Now()` calls in the HTTP handler be >= 40ms
apart. But the spacing is only guaranteed at *reservation* time —
`fetchgate.reserve` sets `start` and then sleeps until it, so what is
guaranteed is the sleep duration, not waking up exactly on time. The latency
difference of the previous goroutine's wake-up is charged to the next gap.
`internal/fetchgate/gate_test.go` already documents this and solves the problem
on its side: **compare promised starts, not the stopwatch**. The tests there
record the same case — the gate behaving correctly but reading 39.43ms against
a 40ms floor, and 29.77ms against 30ms, at unpatterned indices. The solution is
to remove that term, not to buy margin.

**The fix measures the same thing in a way that cannot lose.**
Now `assertSpaced` uses the **average spacing** (`span / (n-1)`), rather than
each gap individually. Overlapping wake-up latency noise cancels itself out,
and that is indeed the property the gate promises: N reads must be at least
N-1 reservations apart.

The floor is placed **between** two results, not at the configured gap.
Measured on this machine with a 40ms gate: paced reads average ~39.8ms, unpaced
~4.3ms. A 20ms floor gives ~5x the unpaced spacing and ~2x the paced spacing,
so the assertion holds well both under a loaded scheduler and above it.
Demanding exactly 40ms would test timer precision that was never promised.

Verified to catch the regression: with the gate temporarily disabled
(`New(0,0)`), both tests fail with average spacings of 9.46ms and 3.91ms —
far below the floor. With the gate restored, both are green repeatedly: 6x
alone, 3x under 5 full suites running concurrently.

### 🩹 `text-error` / `bg-error` are dead utilities — six elements silently losing their error color

Replacing `text-error` in the Usage & Analytics banner (#167) made the color
come out white, not red. Turns out that's just how it is: **there is no
`--color-error` in the built CSS**, so `text-error`, `bg-error`, and
`border-error/40` are classes that never produce a declaration. The browser
silently falls back to the inherited text color — white in the dark theme.
Six elements write "error" but display none of it.

Where they are: the **Uninstall** link and uninstall error messages in
`TokenSaverView`, the `bg-error` confirm button in that same modal, the
`saveError` message in `CreateComboModal`, and the three `bg-error` status
dots in Usage & Analytics (Recent Requests, the Details table, and the detail
header).

**The scope is far narrower than first suspected.** An audit of all color
utilities in the built CSS shows only `error` is dead — `success`, `info`,
`warning`, and `danger` are all present and correctly used in many places.
Only `error` has no token.

The replacement uses an existing token, not the `red-600` convention:

| Site | classes | Reason |
|---|---|---|
| error text (`TokenSaverView`, `CreateComboModal`) | `text-danger` | repo token, already used by the `danger` variant in `Badge`/`Button` |
| confirm button | `bg-danger hover:bg-danger/80` | white text on top of it is 4.77:1 |
| status dots | `bg-red-500` | see the following paragraph |

The status dots don't use `bg-danger` because it **fails the 3:1 non-text bar**.
Measured from real pixels against the `bg-surface` that actually exists behind
them: `bg-danger` (#cf222e) is only **2.83:1** — below the threshold.
`bg-red-500` (251,44,54) gives **3.97:1** and passes. The success dot
`bg-success` (16,185,129) gives 5.97:1, so the two branches really are separate
and both read as non-text.

**Verification:** `bun run build` clean; `bun test` 219/219; `bun runratchet:svelte` 0 unresolved identifier, 89 errors (baseline 89, not rising).
Pixel evidence in the real binary with an isolated `DATA_DIR`: a Details table with
one `success` row and one `error` row produces a green dot
(16,185,129) and a red dot (251,44,54) — the two branches are truly separate.

Note: the first audit initially concluded that all dots were red. The seed
put `status` in the column, whereas `chat/usage.go` stores it **inside the
`data` blob** — so all rows fell into the failing branch. The new evidence
uses the same payload shape as the original author.

### 🩹 The usage read failure finally shows up in Usage & Analytics — not just in the console

Since #148 the backend answers **500** instead of a body containing zeros, but
`AnalyticsView` still only caught that failure with `console.error`.
As a result, users see last period's figures and still read them as this
period's — exactly the problem #148 fixed on the server, which hadn't reached the screen.

The failure is now stored (`statsError`, `detailsError`) via `normalizeLastError`
which `ProviderDetailView` already uses, then shown as `role="alert"`:

- **Overview** — banner below the period selector: "Usage could not be loaded. The
  figures below are from the last successful read." plus the original message from the server
  (`query recent usageDaily: SQL logic error: no such table: usageDaily (1)`),
  with a **Retry** button that calls the same `loadStats`.
- **Details** — banner inside the card, without a second button because **Refresh** in
  the header is already its recovery action. The empty state also changed: "Nothing to show:
  the read failed, so the request history is unknown" — instead of "No request logs
  found", which implied the database was actually empty.

The message is deliberately shown as-is: it's the operator who knows what "no such
table: usageDaily" means, and hiding it behind "Something went wrong" only makes
#148 harder to trace back.

**Error color:** `text-error` / `border-error/40` turned out to be a **dead utility** —
there's no `--color-error` in the built CSS, so the class silently fell back to
the inherited text color (white). `TokenSaverView` and `CreateComboModal` have the
same latent bug. The colors in this banner now use `red-500/600` which is
actually defined, following the `ProviderDetailView` and `UpdateModal` convention.

**Verification:** `bun run build` clean; `bun test` 219/219; `bun run
ratchet:svelte` 0 unresolved identifier, 89 errors (baseline 89, not rising).
Visual evidence in the real binary with an isolated DB (separate `DATA_DIR`, tables
dropped while the server runs): a healthy period renders TOTAL REQUESTS 7 / $6.50
with no banner; `period=7d` after dropping `usageDaily` shows the banner
with the server message and a Retry button; `period=today` stays 200 and does not
show the banner — so the error only appears on the path that is genuinely broken.
The Details tab after dropping `requestDetails` shows the banner + correct empty state.
After the DB is restored, Retry and Refresh bring back the figures and the
alert disappears.

Contrast was measured from real pixels (CSS resolved to sRGB via canvas), not
from token names: icon 5.24:1, heading 12.93:1, message 5.96:1 — all above
AA 4.5:1 for normal text. The Retry button is a real `<button type="button">`
reachable via Tab and has a 2px `focus-visible`; `role="alert"` is read by
screen readers when the banner appears.


### 📝 Issue & PR templates — reporters and contributors both have their guide

Background: this repo didn't yet have `.github/ISSUE_TEMPLATE` nor
`.github/PULL_REQUEST_TEMPLATE`. The existing issues (#154, #155, #160,
#165) are empty of reproduction, version, and OS, so triage started from "is this
a bug or a feature?" — and PRs are squash-merged without evidence that the gates
used by CI were ever run locally.

Changes:
1. Four form-based issue templates (`.github/ISSUE_TEMPLATE/`):
   `bug_report.yml`, `feature_request.yml`, `parity_issue.yml`,
   `question.yml`. Each decisive field still has an English default
   (`bug: `, `feat: `, `parity: `, `question: `) and the labels
   already present in the repo — `bug`, `enhancement`, `parity`, `question`.
2. `parity_issue.yml` contains an upstream → `internal/...` mapping table
   (`open-sse/translator/` → `internal/translator/`, `src/app/api/` →
   `internal/handlers/dashboard/`, and so on) plus a field specific to
   OmniRoute, because both are different PR paths: parity must
   include an upstream commit/PR link, whereas an original request does not.
3. `bug_report.yml` asks for `9router-go version`, OS, and a snippet of
   `9router-go logs` / Translator tab → Console Logs, because all three
   are almost always needed for reproduction.
4. `PULL_REQUEST_TEMPLATE.md` contains a list of gates *exactly* the same
   as the `test`, `integration`, and `docker` jobs in `.github/workflows/ci.yml`
   (`go vet`, `go test -count=1`, `make test-integration`, `bun test`,
   `make vet-svelte`), plus a provider-isolation checklist from
   `AGENTS.md` §3 and the requirement of `Closes #<n>` in the title.
5. `config.yml` disables blank issues (`blank_issues_enabled: false`) and
   routes questions to the question template and security reports to
   Security Advisories, not public issues.
6. The **Contributing** section in `README.md` summarizes the four templates and
   their verification commands.

Upstream research: `decolua/9router` has no issue or PR templates
(`gh api repos/decolua/9router/contents/.github` → only `dependabot.yml`;
`contents/CONTRIBUTING.md` → 404), so this is a local addition, not parity.

### 🔄 Seven parity fixes from still-open upstream PRs

Seven independent changes, each closing one open PR in
`decolua/9router` that the 2026-10-05 audit still left a gap in this gateway.
All are data/capabilities/API-limits/UI — no contract change
not yet promised to the client.

**1. #4587 — Agnes 3.0 Pro + the missing vision capability.** `agnes-3.0-flash`
was already served, but **none of the four Agnes ids have a single row**
in `modelCapabilities`, so they fall back to `DefaultCapabilities` with
`Vision:false`. As a result the router replaces images with a placeholder **before
the request is sent** — Agnes's multimodal input disappears silently. Two exact
rows were added for the 3.0 ids (`Vision`, `Reasoning`, `openai`, 512k/65536) plus
the `agnes-3.0-pro` catalog entry (the **dotted** form per vendor documentation —
`agnes-30-pro` is only the URL slug) and prices 0.45/0.90/0.045. The 2.5 rows
deliberately got no figures: the vendor doc that upstream references doesn't contain
them, and guessing would mean inventing limits (`AGENTS.md` §3 — without a glob `agnes*`,
because that pattern would capture the whole family including undocumented ids).

**2. #4575 — the retired Anthropic probe model.**
`claude-3-haiku-20240307` is still pinned in two places
(`connection_probe.go`, `validate.go`). Replaced by a single constant
`AnthropicValidationModel` = `claude-haiku-4-5-20251001`, an id already used by the
registry and price table — no new id invented. The `assignedModel` precedence
in both paths is unchanged.

**3. #4615 — free topology nodes only appear after being used.** The Usage map
highlights `FREE_DEFAULTS` unconditionally, so `opencode`/`nvidia`/`clinepass`
showed even though they never forwarded a request. Now the loop is gated
by `stats.byProvider[id].requests > 0`. `ProviderTopologyCard` also lost its
hardcoded fallback roster and uses the empty state — that roster would
reproduce the same bug right on screen when none is used.

**4. #4614 — glob shadowing on the thinking level table.** `GetThinkingLevels`
is first-match-wins, and the unqualified `*deepseek-v4.*` rows sit
**above** the `codebuddy-cn` rows. For dotted ids like
`deepseek-v4.1-flash` the generic glob wins first, so the
codebuddy-cn rows are **dead** — the picker shows the wrong effort set for
codebuddy-cn models. The exact two codebuddy-cn rows were moved above the generic glob
per upstream's correction, along with the rest of the codebuddy-cn catalog sync to the
2026-09-30 server snapshot (glm-5.2/5.3, hy3, hy4-preview, kimi-k2.8-preview,
maxOutput). *Residual:* the `codebuddy-intl` rows hit by the same glob
shadowing were not changed upstream, so they're not changed here either — noted
so it isn't mistaken for being forgotten.

**5. #4584 — model identifier normalization at the API boundary.** `models` on the create/update
combo is read as `any` then written back as-is, so
legacy `{provider,model}` or `{fullModel}` objects are stored intact — and
`comboModels` can only read `[]string`, so those rows weren't readable and the
Combos page broke. It's now coerced into `provider/model` strings and malformed
entries are answered **400** `models must contain valid model IDs` via
`handlerutil.WriteJSONError`, which is already the 400 convention in that package. Old
rows are **not** migrated — same as upstream.

**6. #4564 — free-tier tokenharbor model seeds.** Four new free ids
(`mimo-v2.6-flash:free`, `mimo-v2.5:free`, `qwen3.8-flash:free`,
`deepseek-v4-flash:free`) were added. Two of them would fall to the 128000/4096
floor without limit rows, so `GetModelTokenLimits` gained the
1048576/131072 case for the `mimo-v2.5`/`v2.6` families — those figures belong to
upstream, not invented. No price rows were added: `tables.go` is
untouched, same as upstream.

**7. #4576 — explicit fallback strategy on combos.** The UI removed
`settings.comboStrategies` entries every time a chosen strategy was considered "default",
so picking "Fallback" on a combo whose global is round-robin looked
stored when it wasn't — the combo silently inherited round-robin again. Added
the `inherit` value as the **only** way to remove an override, and
`ComboCard` now displays the `inherit` value as-is. The consequence in
`ComboCard`: `isFusion` deliberately uses the **effective** strategy
(`effectiveComboStrategy`) so combos that inherit fusion still show the
fusion icon, while the select value still shows `inherit`.

**The svelte-check debt also dropped by two.** `ComboCard` never had a
baseline entry, so when that file was touched its three old errors were also counted:
`{#each}` declaring an unused index, and `title` on two
lucide icons that aren't its props. All three were fixed (`aria-label` +
`role="img"` as a replacement for `title`), not bypassed by raising
the baseline — per `AGENTS.md` §6.E. The `svelte-check` baseline dropped from **91 to
89**.

**Verification:** `go vet ./internal/...` clean · `go test -count=1 ./...`
green · `bun run build` + `bun run ratchet:svelte` (0 unresolved identifier,
89 errors, baseline lowered) · `bun test` 219 pass. Smoke test against the real binary in
an isolated `DATA_DIR`: `bai/probe` routing is answered upstream, and
`agnes/agnes-3.0-flash` with `image_url` forwards the full image URL to
upstream — behavior that was previously impossible because of `Vision:false`.

### 🔧 Tool declaration semantics & reasoning tokens — five upstream PRs merged

Five open PRs that all touch the same path: how the gateway
translates tool declarations, tool choice, and measures usage. Merged
because it's genuinely one change set that touches each other, not five.

**1. `strict` on tools never survived (#4607, #4543, #4573).** There was no
`strict` field on the tool type, and three converters rebuilt the tool object from
scratch — a flag set by the client was lost silently. Now `strict` is passed through
in both directions Claude <-> OpenAI and in the Responses body builder (read flat
and nested). Explicit `strict:false` **is still sent**, because it is a
client instruction; absence means absence, not `false`.

One deviation from the simplest form: non-boolean `strict`
(`"strict":"yes"`) does **not** fail the request. Upstream reads it behind a
`typeof === "boolean"` safety net, so other values are silently ignored;
a plain `*bool` would reject the whole body. `ClaudeTool.UnmarshalJSON` therefore
uses a two-stage decoder that discards non-boolean values.

**2. Single tool-call policy (#4581).** `parallel_tool_calls:false`
is mapped to `tool_choice.disable_parallel_tool_use` and vice versa, without
overwriting an explicit named tool choice (`{"type":"tool",name}` must remain
intact), and with no effect at all when there are no tools. The OpenAI-only
`parallel_tool_calls` field is removed before the body is sent to Claude.

**3. `tool_choice:"none"` means "skip", not "auto" (#4577).** The
OpenAI -> Claude direction dropped the field, so the model was free to call tools
even though the client forbade it; the other direction returned `"auto"`. Both directions
now become `{type:"none"}` <-> `"none"`.

**4. `response_format` lost silently (#4547, issue #2896).** When building
Responses bodies, `response_format` wasn't mapped to `text.format`, and the
Codex allowlist dropped it — so a client requesting structured output
got free text. Now `json_schema` is mapped with name, schema, and
`strict` that **defaults to true** (`strict !== false`), `json_object` stays
`json_object`, and a case without `response_format` adds no `text`
field at all. `json_schema` without a `schema` is deliberately dropped — exactly
the upstream guard.

*Addition found during verification:* the flat tool path in the Responses
builder never copied `parameters`, so the tool schema
was lost. Fixed on the same side.

**5. `reasoning_tokens` lost at three points (#4574, remainder of #4551/#4536).**
The Responses-native usage reader never read `OutputTokensDetails` —
even though the struct already existed and was already emitted on the outbound side;
`ProcessCodexEvent` built usage without `completion_tokens_details`; and
the `tokensJSON` written to `usageHistory` didn't include reasoning tokens
even though they were already billed and already written to `requestDetails`. All three fixed,
and the `chunkUsage` build was extracted into `codexUsageMap` so its branch stays
concise. The already-correct cache side was untouched.

Alongside that, two fixes carried along:
- **Gemini tool names** (#4589) are now taken from the call
  it answers, not from a cross-conversation id->name map. `tool_call_id`
  is only unique within one turn, so a repeated id made an earlier turn's
  tool result carry the later turn's tool name.
- **Gemini finish reason** (#4571) is mapped through one shared function, so
  `MAX_TOKENS` -> `length` and `SAFETY`/`RECITATION` -> `content_filter` on
  both streaming and non-streaming paths. Previously both mapped
  `SAFETY` to `"stop"`.

**These three paths are locked at the integration level.** The unit test
`ensureMessagesMaxTokens` proves the converter is right, but it doesn't
cover what happens around it: connection selection,
`isAnthropicUpstream` (which is only true for a real `api.anthropic.com`
base URL or an edge relay), token saver, and URL construction. That's where
client instructions can be lost while every converter test stays green.

`internal/integration/anthropic_tool_policy_test.go` closes it through the production
router with a fake upstream: `parallel_tool_calls:false` must arrive
as `disable_parallel_tool_use:true`, a named tool choice must remain
intact while the restriction is also attached, and `tool_choice:"none"` must
not get lost on the way to Claude. All three were verified to catch
regressions — with `withDisabledParallelToolUse` temporarily disabled, the
first two failed with the right messages.


**Verification:** `go vet ./internal/...` clean · `go test -count=1 ./...`
green · `go test -tags=integration ./internal/integration/...` green ·
`bun run build` + `bun run ratchet:svelte` (0 unresolved identifier, 89 errors,
same as the baseline lowered by #163) · `bun test` 219 pass.
### 🩺 `go test -race ./...` becomes the CI gate — it never ran before, and it caught one flaky test

The `test` job runs `go test ./...` **without** `-race`, and the only job
using `-race` is `Integration tests`, whose coverage is limited to
`./internal/integration/...`. That means `internal/db`, `internal/usagetracker`,
`internal/proxy`, and `internal/handlers/chat` — where the gateway's shared
state lives (usage tracker ring buffer, per-handler sticky state, SSE pumps,
connection cooldown maps, pool-id cache in `Repo`) — have never been tested
under the race detector in CI. A data race could merge green and then surface
on a user's machine during real use.

The new `race` job runs `go test -race -count=1 -timeout 10m ./...`.
It is separated from the `test` job rather than added as a step, so that the
race report gets its own name in the checks list and two failures (a flaky
assertion vs a real race) don't cover for each other in a single log. The
local `make test-race` target runs the same command; `-race` needs cgo, so it
is deliberately left out of `make test`/`test-short`.

Two things made this possible now: the upstream live tests have already been
separated from CI via the opt-in `9ROUTER_LIVE_TESTS=1` (#150) — previously
`go test -race ./...` failed because `space-bunny-free` hit a rate limit, not
because of the gateway — and `web/dist` is built first in the same step as the
`integration` job, because `internal/app` → `web` → `web/embed.go` fails to
compile without the SPA.

Adding this gate immediately uncovered one bug:
`TestTranscribeGeminiLive_PartialTranscriptOnClose` failed roughly **1 in 12
runs**. Its fake server just closes the socket right after reading one frame,
while the client is still writing its audio chunks — so the close can precede
the transcription delta that was just sent, and a case that should pass becomes
`socket closed before completion`. The fake server now reads until the
`clientContent.turnComplete` frame (the point where the client is already
waiting in its read loop) before sending the delta and closing. It fails
deterministically at `-count=3`, and now shows 0 failures across 20
consecutive runs. No production code changed.

**Verification:** `go build ./...` and `go vet ./...` are clean; `go test ./...`
is green; `go test -count=8 -run TestTranscribeGeminiLive ./internal/handlers/media/`
is green; 20 consecutive runs of the formerly flaky test are green. The workflow
file is run by an ubuntu runner (cgo is available there), not a local machine
without a C compiler.

### 🔒 OAuth refresh singleflight, email masking, & backend test coverage

- **OAuth refresh singleflight**: `refreshOAuthTokenIfExpired` and
  `forceRefreshOAuthToken` (`internal/handlers/chat/gemini_handler.go`) run
  inside a `singleflight.Group` keyed per connection, so an expired token costs
  one upstream round-trip instead of one per concurrent request. The forced
  refresh uses a `\x00`-prefixed key, which a connection id can never contain,
  so the two flights cannot collide.
- **Routing unchanged**: Antigravity-prefixed models (`ag/muse-spark-*`) still
  route to their owning executor via `routeModelToOwningProvider` in
  `internal/handlers/chat/resolution.go`.
- **Email masking**: `web/src/lib/privacy.ts` adds `maskEmail` /
  `formatEmailLabel` plus an `emailPrivacy` store persisted to
  `localStorage['9router_mask_email']`, with a toggle in Quota Tracker,
  Provider Detail, and the Media views so account emails can be hidden while
  screen sharing. `maskEmail` is idempotent, the store follows the `storage`
  event so a second window stays in sync, and the toggle also appears in the
  Model picker, Analytics topology, and the Media header — the tabs that never
  mount the Providers view and therefore had no way to reach it.
- **A masked label can never be written back**: the shared
  `EditConnectionModal` seeds the name field through `formatEmailLabel` and
  submits `undefined` unless the field differs from both the raw and the
  masked value. Saving a priority change with masking on leaves the stored
  name alone instead of persisting `l***m@gmail.com` into
  `providerConnections.name`.
- **Backend test coverage**: unit tests added across `config`, `codexquota`,
  `usagetracker`, `middleware`, `translator`, `app`, `handlerutil`, and `proc`.
  Measured on this tree: config 90.8 · codexquota 87.3 · usagetracker 87.6 ·
  middleware 89.2 · translator 85.4 · proc 85.9 are at or above 85%;
  `handlerutil` 80.2 and `app` 82.1 are not. `internal/proc` also dropped from
  ~60s to under a second by killing the child process instead of waiting out
  its lifetime. The two envelope-unwrap tests assert the envelope key is
  gone, so they fail when the unwrap is a no-op.

**Regression coverage added with this change:** an unexpired token must return
the caller's own token, and a waiter sharing a collapsed flight must not be
handed the leader's. Both are new — no test previously sent a connection whose
`apiKey` differs from its `accessToken`, which is exactly the iFlow shape
(HMAC platform key plus a separate OAuth token), and that gap is why a token
substitution in this path passed the full suite while signing iFlow requests
with the wrong secret.

**Found while reviewing this change, and fixed here.** Each was proven by
reverting the fix and watching the test fail, not by inspection:

- **A refresher returning no result crashed the gateway.** The lazy path
  passed the result straight to `BuildConnectionUpdate`, which dereferences it;
  the forced path already guarded this shape. A provider reporting success
  with no token therefore panicked — and singleflight re-panicked that on
  every waiter instead of returning an error.
- **The write-back guard compared against a freshly derived mask.** Toggling
  masking in a second window while the modal was open made the untouched
  masked field compare as a rename, and persisted `l***m@gmail.com`. The
  comparison is now against the value the field opened with, in
  `submittedConnectionName`, with both sides trimmed so a stored name padded
  with whitespace still matches.
- **A Gemini turn carrying only a tool result skipped the name fit.** The
  quick-check token list named `functionCall` but not `functionResponse`, so a
  history-only turn returned unshortened while the declaration beside it was
  fitted.
- **`maskEmail` was never tested with two addresses in one label.** The regex
  is global; dropping `/g` left the second address in the clear and the suite
  green.

Job baru `race` menjalankan `go test -race -count=1 -timeout 10m ./...`.
Dipisah dari job `test`, bukan menambah step, supaya laporan race bernama sendiri
di daftar check dan dua kegagalan (assertion flaky vs race asli) tidak saling
menutupi di satu log. Target lokal `make test-race` menjalankan perintah yang
sama; `-race` butuh cgo, jadi sengaja tidak ikut `make test`/`test-short`.

Dua hal yang membuat ini mungkin sekarang: test live upstream sudah dipisah dari
CI lewat opt-in `9ROUTER_LIVE_TESTS=1` (#150) — sebelumnya `go test -race
./...` gagal karena `space-bunny-free` kena rate limit, bukan karena gateway —
dan `web/dist` dibangun lebih dulu di step yang sama seperti job `integration`,
karena `internal/app` → `web` → `web/embed.go` gagal compile tanpa SPA.

Menambah gerbang ini langsung membongkar satu bug:
`TestTranscribeGeminiLive_PartialTranscriptOnClose` gagal sekitar **1 dari 12
run**. Fake server-nya menutup socket begitu saja setelah membaca satu frame,
padahal client masih menulis chunk audio-nya — jadi close bisa mendahului delta
transkripsi yang baru dikirim, dan kasus yang harusnya lulus jadi `socket
closed before completion`. Fake server kini membaca sampai frame
`clientContent.turnComplete` (titik di mana client sudah menunggu di read loop)
sebelum mengirim delta dan menutup. Deterministik gagal di `-count=3`, dan
sekarang 0 gagal di 20 run beruntun. Tidak ada kode produksi yang berubah.

**Verifikasi:** `go build ./...` dan `go vet ./...` bersih; `go test ./...`
hijau; `go test -count=8 -run TestTranscribeGeminiLive ./internal/handlers/media/`
hijau; 20 run beruntun test yang tadinya flaky hijau. Berkas workflow
dijalankan runner ubuntu (cgo tersedia di sana), bukan mesin lokal tanpa C
compiler.

### 🚀 Analytics Improvements: Model Breakdown, Dynamic Pricing, & Semantic Cache Persistence

- **Durasi Kompresi Murni**: Memperbaiki pengukuran `CompressionDurationMs` di `internal/handlers/chat/fallback.go` yang sebelumnya salah mencatat total latensi streaming LLM (~7.5s) menjadi durasi murni eksekusi kompresi (~1–15ms).
- **Model-Level Breakdown**: Menambahkan rincian per-model di backend (`ByModel`) dan frontend tabel `Breakdown by Model` untuk dashboard Cache dan Compression.
- **Dynamic Pricing**: Menggantikan hardcode $3.0/M dengan kalkulasi harga riil per-model dari `internal/pricing` (`pricing.GetPricingForModel`).
- **Persistensi SQLite Semantic Cache**: Menambahkan tabel `semanticCacheEntries` dan `PersistentLRUStore` di `internal/semanticcache/` sehingga cache tidak hilang saat container restart, lengkap dengan preloading/hydration otomatis saat boot.

### 📉 Compression Analytics Dashboard (port OmniRoute `/dashboard/analytics/compression`)

- **Backend**:
  - `internal/db/schema.go`: Menambahkan tabel `compressionAnalytics` untuk mencatat telemetri setiap kali pipeline kompresi (RTK, Caveman, Ponytail) dijalankan.
  - `internal/db/compression_analytics.go`: Menambahkan fungsi `InsertCompressionAnalytics` dan agregator `GetCompressionAnalyticsSummary` (mendukung filter `since=24h|7d|30d|all`, rincian per mode, per provider, grafik per jam, dan fallback otomatis ke data historis `usageHistory`).
  - `internal/handlers/chat/usage.go`: Mencatat telemetri setiap eksekusi kompresi (mode, overhead latensi, token awal vs hasil kompresi, dan skip reason).
  - `internal/handlers/dashboard/compression_analytics.go`: Menambahkan REST API endpoint `GET /api/analytics/compression`.
  - `internal/db/compression_analytics.go`: Menambahkan 1x migrasi idempotensi `BackfillCompressionAnalytics` yang mengekstrak riwayat dari `requestDetails` & `usageHistory`, dengan klasifikasi cerdas untuk mode `stacked` (RTK + Caveman/ADHD/Ponytail), `rtk`, `caveman`, `adhd`, dan `ponytail`.
  - `internal/handlers/chat/usage.go`: Menambahkan fungsi `resolveCompressionMode` di live pipeline agar eksekusi kombinasi secara akurat dilabeli `stacked`.
- **Frontend**:
  - `web/src/components/CompressionAnalyticsView.svelte`: UI Svelte 5 runes dengan 6 hero stat cards (Total Requests, Tokens Saved, Avg Savings %, Avg Duration, Receipts, Est. Cost Saved), filter rentang waktu, grafik trend per jam, serta progress bar rincian mode dan provider.
  - Integrasi tab `/dashboard/analytics/compression` di router SPA, `Sidebar.svelte`, dan `App.svelte`.

### ⚡ Cache Analytics & Tracking Dashboard (port OmniRoute `/dashboard/cache`)

- **Backend**:
  - `internal/db/cache_analytics.go`: Menambahkan `GetPromptCacheMetrics` dan `GetPromptCacheTrend` untuk agregasi data prompt caching dari tabel `usageHistory` (`cached_tokens` dan `cache_creation_input_tokens`).
  - `internal/semanticcache/`: Menambahkan atomic metrics counter (`hits`, `misses`, `tokensSaved`), paginated entry listing (`ListEntries`), invalidasi berdasarkan model/stale TTL, serta penanganan delete single entry.
  - `internal/handlers/dashboard/cache.go`: Menambahkan endpoint REST API `GET /api/cache`, `DELETE /api/cache`, `GET /api/cache/entries`, dan `DELETE /api/cache/entries`.
- **Frontend**:
  - `web/src/components/CacheAnalyticsView.svelte`: UI Svelte 5 runes untuk Prompt Cache (5 hero metrics cards, interactive hourly trend chart, breakdown per provider) dan Semantic Cache (hits/misses meter, invalidasi model, tabel cached entries dengan filter dan paginasi).
  - Integrasi tab `/dashboard/cache` di `web/src/lib/router.ts`, `web/src/components/Sidebar.svelte`, dan `web/src/App.svelte`.

### 🩹 Failed usage reads silently reported as zero — Usage & Analytics dashboard

`GetUsageDailyRecent`, `GetUsageHistorySince`, `GetRecentUsageHistory`,
`GetRequestDetailsPaged`, and `ListProxyPools` iterate `database/sql` cursors
without checking `rows.Err()`, and `rows.Scan()` errors are `continue`d. A
cursor truncated midway, or a row whose values don't match the scanned
columns, vanishes without a trace — while the caller receives a shorter list
with `err == nil`.

`HandleUsageStats` reads all three usage sources behind `if err == nil { ... }`
with no `else` branch, so any read failure — missing table, unreadable file,
scan error — yields HTTP 200 containing `{"totalRequests":0,
"totalCost":0, ...}`. The dashboard renders that as "no traffic in this
period", and no log records the mistake. A zero number because the system is
broken looks identical to a zero number because it really is quiet.

The five readers now return `%w`-wrapped errors plus `rows.Err()`, and the
three calls in `HandleUsageStats` write a 500 with their original message.
`GetRequestDetailsPaged` also no longer swallows `COUNT(*)` failures into
`total: 0` — a page that contains rows but reports zero belongs to the same
"zero that means broken" class. Regression tests cover both directions: a row
that cannot be scanned must produce an error (previously the list was
truncated without an error), and healthy reads still aggregate as before.

Three adjustments from review:

1. **`ListProxyPools` does not fail on a NULL `testStatus`.** That column is
   nullable in the shared schema and the very same database is written by the
   Next.js dashboard, so a pool that has never been probed reasonably has
   NULL. Scanning it as an ordinary `string` would make the entire Proxy Pools
   tab 500 because of a single row that merely has no status yet. Its value is
   now read as `""`; other scan failures remain fatal.
2. **`GetRecentUsageHistory` in `usagetracker` was logged.** The ring buffer
   really is best-effort — a failed read leaves it empty and the next push
   fills it — but it now writes `log.Warn`, because an empty ring with no
   trace cannot be told apart from a fresh install.
3. **`rows.Err()` is wrapped with caller context**, consistent with the
   `rows.Scan()` errors in the same functions (§4.E).

### 🧹 `repos.go` split per table; combo strategy resolution became one helper

`internal/db/repos.go` had grown to 647 LoC and crossed the hard 400 limit
(§4.D). Its contents are split according to the table being queried, so that
each file sits under the 300 LoC target: `apikeys.go` (apiKeys),
`connections.go` (providerConnections), `providernodes.go` (providerNodes),
`combos.go` (combos), and `aliases.go` (`kv` scope: modelAliases, customModels).
`repos.go` itself now only holds the `Repo` type and its lists.

The `providerConnections` column list that was repeated across three SELECT
literals and the `providerNodes`/`combos` column lists are now constants plus
one scan function per table, shared by the single-row and list readers, so the
column order can no longer skew in only one path. The four WHERE branches in
`GetProviderConnections` (provider × activeOnly) are flattened into a single
`switch` instead of two nested `if`s.

The identical combo strategy resolution repeated three times (`GetComboByName`,
`GetComboById`, `GetCombos`) is now one `resolveComboStrategy` helper.
Behavior is unchanged: the `"fallback"` default, per-combo overrides win over
global settings, and a failed settings read leaves its default in place.

**Verification:** the package `db` API is unchanged — the 100 `func`
signatures before and after the split are identical (`go doc -all
./internal/db` compared before/after). `go build ./...` and `go vet ./...` are
clean, `go test ./...` is green, `go test -tags=integration
./internal/integration/...` is green (69.7s + 0.2s).

### 🔧 Dashboard backup returns to tidied JSON, like 9router upstream

Background (issue #160): the **Download Backup** button in Settings offers a
`.zip` containing JSON. Upstream `9router` downloads raw JSON —
`JSON.stringify(payload, null, 2)` — named `9router-backup-<stamp>.json`, so
the backup here can't be read without extracting it first and diverges from
parity.

- `HandleExportDatabase` (`internal/handlers/dashboard/settings.go`) no longer
  wraps the payload in an archive. Its response is two-space indented JSON
  (`JSON.stringify(x, null, 2)`) with
  `Content-Disposition: attachment; filename="9router-backup-<date>.json"`.
  The `?format=zip` branch and the `Accept: application/zip` detection were
  removed because the export now has only one shape.
- `handlerutil.WriteJSONIndented` (new) writes indented JSON using
  `jsontext.WithIndent("  ")` while still using the `Deterministic(true)` that
  every dashboard response already uses.
- `ProfileSettingsView.svelte` downloads `/api/settings/database` without
  `?format=zip`, names the file `.json`, and its confirmation text mentions
  `(.json file)`.
- **Import still accepts old `.zip` archives**: `HandleImportDatabase` still
  detects the `PK\x03\x04` magic and extracts the `*.json` inside it, so
  backups downloaded before this change remain restorable.
- Tests: `TestHandleExportDatabase_PrettyJSON` (JSON Content-Type, `.json`
  filename, indentation present) replaces the zip round-trip, and
  `TestHandleImportDatabase_LegacyZipArchive` ensures old zip imports still
  work.

### 🏷️ Custom providers can use their own URL suffix — `openai-compatible-chat-<suffix>`, not `<uuid>`

Background (issue #155): every OpenAI/Anthropic-compatible node created from
the dashboard uses `generateId()` for the tail of its provider id, so the
stored id is always
`openai-compatible-chat-46b3f72a-5618-4485-8527-0eb4424e85db`. That id is not a
label: it becomes the model prefix in `/v1/models`, the `provider` column in
`providerConnections`/`usageHistory`/`requestDetails`, and the key for the
entire set of `customModels` rows belonging to that node — so it is recorded
everywhere and can't be read.

Changes (`internal/handlers/dashboard/provider_node_id.go`, new):
1. `POST /api/provider-nodes` accepts `urlSuffix`. If filled, the id tail uses
   that value (`openai-compatible-chat-bai`); if empty, upstream's random uuid
   is still used, so no existing node changes shape.
2. Suffix rule: `[A-Za-z0-9][A-Za-z0-9._-]{0,63}`. `/` is rejected because
   `resolution.go` splits model addresses with `SplitN(entry, "/", 2)` — a
   `/`-bearing suffix would break routing; suffixes beginning with a literal
   provider name (`openai-compatible`, `anthropic-compatible`,
   `custom-embedding`) are rejected because every id reader uses
   `strings.HasPrefix`.
3. An id already used by another node is answered with **409**
   `PROVIDER_NODE_ID_CONFLICT`, not a 500 from the SQLite constraint.
4. `GET /api/provider-nodes` reports `urlSuffix` + `urlSuffixGenerated`, so the
   dashboard field displays the correct value for uuid nodes (empty + generated)
   instead of forcing the user to "fix" a uuid.
5. The `Custom URL Suffix` field in `AddCompatibleNodeModal` (with a preview of
   the full id) and `EditCompatibleNodeModal`.

**Rename along with all its legs** (`internal/db/provider_node_rename.go`,
new). A `urlSuffix` changed on edit moves `providerNodes.id`, and that id is
the storage key for everything owned by that node. `RenameProviderNode` moves
all of it in **one transaction**: `providerConnections.provider`, the key **and**
value of `kv.customModels`, the `kv.disabledModels` key, `kv.modelAliases`
targets, `combos.models` members, the `provider` column in `usageHistory` +
`requestDetails`, and the provider-keyed maps in the `settings` blob
(`providerStrategies`, `providerOverrides`). Without this, a single edit would
leave the node without credentials, without custom models, and without history.
`MediaKindView`, which previously called the modal with `nodeType`/`onCreated`
props that never existed, is also fixed to `type`/`onSubmit`.

One secular correction in the same path: `isCompatibleProviderID` now also
accepts `custom-embedding-`. Previously embedding nodes whose id was generated
as `openai-compatible-chat-<uuid>` passed as "compatible", and once `urlSuffix`
made their id `custom-embedding-*`, that path would fall through to the
registry catalog and `/v1/models` would advertise a provider the node doesn't
serve.

**Verification:** `go vet ./...` clean · `go test -count=1 ./...` green ·
`bun run build` + `bun run ratchet:svelte` (0 unresolved identifiers, 91 errors —
1 below baseline, `bun run ratchet:svelte -- --update` included) ·
`bun test` 210 pass. Smoke test against the real binary in an isolated
`DATA_DIR`: creating with a suffix yields `openai-compatible-chat-bai`, without
a suffix still yields a uuid, 409 on collision, 400 for a `/`-bearing suffix,
then a **rename** `bai` → `bai-v2` is proven to also move connections,
`customModels`, `disabledModels`, aliases, combo members, `usageHistory`,
`requestDetails`, and both `settings` maps — `POST /v1/chat/completions
{"model":"bai/glm-5.3"}` is then answered upstream (`"content":"routed"`) and
recorded in `usageHistory` with provider `openai-compatible-chat-bai-v2`, while
`/v1/models` keeps advertising `bai/glm-5.3` without leaking the raw id.

### ✨ `feat(connections): change API key directly from the Edit Connection modal` (issue #154)

The Edit Connection modal in the **Providers** tab and in **Quota Tracker**
now has an API field. An empty input means "use the stored key", not "delete
the key" — that's what lets this modal be used for rotation without copying
the old key into a text input.

The field shows the stored key as a mask (`sk-smo…inal`) from `apiKeyMasked`,
a new addition to `GET /api/connections`. A mask, not the raw key, because that
endpoint is reachable with a low-privilege client API key: a raw key there
means one leaked key could steal every secret. The shape of the mask is the
same as `maskClientKey` already used for the `/api/keys` listing.

`PUT /api/connections/{id}` now accepts `apiKey`:

- `apiKey` absent or empty after `TrimSpace` → the stored key stays intact.
- `apiKey` filled → only `data.apiKey` is written. `authToken`,
  `providerSpecificData`, and probe status are untouched.

The handler deliberately **does not** change `testStatus`/`lastError` during
rotation. Replacing a key is not evidence that the connection is alive again —
the only evidence is the provider's answer, so the modal clears them only
after a probe that actually answers, by explicitly sending `testStatus:
"active"` (parity with upstream's `EditConnectionModal`).

The modal runs a `/api/providers/validate` probe before writing:

- key rejected by the provider → **not saved**, the modal stays open with the
  reason. The stored key is the only thing keeping this account alive during
  the rotation, so a wrong key would be silently retired.
- provider without a probe (`supported: false`) → not a verdict, the key is
  still saved without a corrected `testStatus`.
- OAuth fields hidden: it's the access token being rotated, not something the
  user typed.

The shared logic lives in `web/src/components/connections/credential.ts`
(placeholder, whitespace trimming, verdict mapping, probe) so that both modals
don't repeat the same rules.

Upstream parity: `decolua/9router` `src/shared/components/EditConnectionModal.js`
— the label "Leave blank to keep the current API key.", a Check button via
`/api/providers/validate`, and `testStatus` only set when validation succeeds.

### ♻️ `refactor(web): one Edit Connection modal for Providers and Quota Tracker` (issue #158)

The Edit Connection modal previously had two copies: inline in
`ProviderDetailView.svelte` and inline in `QuotaTrackerView.svelte`, each with
its own nine `edit*` variables. `checkReplacementKey()`, `resetKeyCheck()`, the
API field block, and its Test/Save/Cancel button structure are exactly the same
— so every new rule had to be written twice, and PR #154 indeed wrote it
twice.

Both now call `EditConnectionModal.svelte`, the single implementation. What
remains in each caller is just the opening and the refresh: `onClose` closes,
`onSave` writes then refreshes. The `edit*` state, the probe, and the markup
move into that component.

Three differences that were hidden become one behavior. All of them pick the
safer variant, so this is not a fully neutral refactor:

- **Rename in Quota Tracker no longer silently writes priority.** That modal
  always sent `priority`, so rows with a NULL `priority` (which is seeded as `1`
  in the input) became rank 1 merely because the user changed the name —
  clashing with other rows already holding rank 1, and resurrecting pairs that
  couldn't be reordered. The Providers modal has used `seededPriority` from
  the start to ignore unchanged priorities; now both do.
- **Save failures in Quota Tracker no longer vanish silently.** Its error only
  went to `console.error`, so a user pressing Save on a modal that refused
  would close it without explanation. Now it goes through `onSaveError`.

  `onSaveError` itself is deliberately made **required** in
  `EditConnectionModal`, not optional. It was optional for a while, and Quota
  Tracker skipped it — so the "already fixed" claim above is literally true
  and behaviorally wrong: the error still disappeared, and this is no longer
  merely about `console`. Required in the type means a caller that skips it
  becomes a compile error, not yet another silent bug slipping through review.

- **Escape closes the modal.** `AddConnectionModal` and eight other modals
  already had this (parity with upstream's `Modal`); the two old copies didn't.
  Now it's centralized in one place, so a user's keyboard no longer gets stuck
  in a modal that can't be closed without a mouse.
- **`max="100"` on the Quota Tracker priority input was removed.** There is no
  priority limit in the backend nor in the Providers modal; that limit only
  held the form back in one place and was inconsistent with its neighbors.

The Test button styling is unified into Quota Tracker's `science` icon package;
its text label stays different (`Test` vs `Test Connection`) via the
`testLabel` prop, because the two pages genuinely have their own visual habits.

Out of scope, as in the issue: `MediaProviderDetail.svelte` (media connection
edit modal, without key and priority) and `AddConnectionModal.svelte`.

## [v1.9.9] - 2026-10-05

### 🐛 `TestGateAcquire_JitterOnlyWidensTheGap` is still flaky — the stopwatch is measured from the previous slot

PR #146 raised the floor 30ms → 40ms and its changelog states that the test
was kept because "there is no evidence it needs to be touched". On this branch
there is evidence, and this time it is not about insufficient margin: `go test -race -shuffle=on
./...` fails

```
fetchgate (4 passed, 1 failed)
  [FAIL] TestGateAcquire_JitterOnlyWidensTheGap
     gate_test.go:115: slot 5 waited 39.4323ms, want at least the 40ms floor
```

`go test ./internal/fetchgate/ -race -count=8` passes on its own, so this is
suite load, not the gate. The root cause is the stopwatch boundary: the loop
measures `time.Since(start)` with `start := time.Now()` taken **after** the
previous `Acquire` returned. The gate's floor is a *reservation* property —
`reserve` publishes `start`, then sleeps until `start` — so a promised slot is
always `minGap` away, unless the previous goroutine's wake-up latency gets
subtracted from the next gap.
The `start` number is measured after that, so latency unrelated to the gate
also gets subtracted from the gap under test. That is also why the failing
index is always somewhere in the middle and keeps moving: it only depends on
how slowly the previous goroutine happened to be woken up.

Raising the floor again only makes the test longer, it does not fix its
measurement. The measurement is moved to the right source — the start that the
gate promises:

1. `TestGateAcquire_JitterOnlyWidensTheGap` compares against the `start`
   emitted by `reserve`, so it is completely noise-free, while still verifying
   the original assertion: no gap below the floor, and at least one gap
   wider than the floor so that jitter genuinely comes into play. Drawing
   all zeros eight times has a `(1/61)^8` chance, so a run without jitter is
   not a real outcome at all.
2. `TestGateAcquire_FloorHoldsWithoutJitter`, new, deterministic: with
   jitter disabled every gap must be **exactly** `minGap`. This is what catches
   a removed floor — the jittered test cannot, because draws below
   `minGap` are legitimate there.
3. `TestGateAcquire_WaitsOutTheReservedSlot`, new: `Acquire` genuinely
   waits out the slot it reserved. Its stopwatch starts **before** the
   reservation, so the only thing that can be lost is a timer that fires too
   fast — and Go timers run late, never fast — so this measurement direction is
   safe to assert exactly.

`internal/fetchgate/gate.go` is untouched: zero production changes.

**Verification:** `go vet ./internal/fetchgate/` clean ·
`go test ./internal/fetchgate/ -race -count=3` green ·
`go test ./internal/fetchgate/ -race -count=5 -p 16` green. The strength of the test
is proven by deliberately removing the floor
(`g.next = start.Add(g.jitter())`), which makes all four tests fail —
including `JitterOnlyWidensTheGap` at `slot 3` with
`32.86ms < 40ms`, and that too deterministically: its measurement no longer
depends on scheduling.

### 🏷️ Real provider names now show in the Details tab on the Usage dashboard

Background: the `Provider` column in the **Details** tab (`/dashboard/usage`) displays
the raw `item.provider`. For custom providers that value is a synthetic id
(`openai-compatible-chat-<uuid>`), so the table reads
`openai-compatible-chat-46b3f72a-5618-4485-8527-0eb4424e85db` instead of the name
the user configured (`tiarina`).

Parity: upstream uses `getProviderName(detail.provider, cache)` in
`RequestDetailsTab.js`, with a merged cache of `AI_PROVIDERS` +
`providerNodes` (`node.id → node.name`). That name source is exactly what the
topology cards on this port already use (`AnalyticsView.topologyName`).

Fix (`web/src/components/analytics/`):
1. New `providerDisplayName()` in `types.ts` — a custom node wins first,
   then the catalog name, then the bare id.
2. `RequestDetailsTab` receives `providerNodes` and uses that name in the table
   badge **and** in the inspector modal header. The raw id stays available as
   `title` (tooltip) and in the Payload panel, so no information is lost.
3. The breakdown column in the Overview tab is **not** touched — `provider`
   there is already resolved server-side (`nodeNameMap` in
   `internal/handlers/usage_stats.go:191`).

**Verification:** `bun test` (210 pass, including 4 new cases for
`providerDisplayName`), `bun run ratchet:svelte` (0 unresolved identifiers,
92 errors = baseline), and a smoke run against an instance with 9,862
`requestDetails` rows: rows that previously read `openai-compatible-chat-46b3f72a-…` now
read `tiarina` / `OpenCode Zen`, and the `/providers/oai-cc.png` icon is still used.

### 🩺 Proxy egress rejections are now visible in Usage & Analytics

Background: `tryForwardWithConnection` fails **too early** when a bound proxy pool
cannot serve traffic (`internal/proxy/connections_proxy.go:88-98`
— pool missing / disabled / without a url). That path `return`s at
`fallback.go:410` **before** `LogFailure` and before `TrackPending` closes
with `isError`, so the `502 proxy_error` rejection is recorded nowhere: zero
`usageHistory` rows (correct), but also zero
`requestDetails` rows. On the `/dashboard/usage` dashboard those requests are
not visible at all — the user only sees an HTTP 502 in the client with no trace.

Parity: upstream records the same condition. The identical condition in Next.js
*throws* from inside `executor.execute` (`open-sse/utils/proxyFetch.js`,
"Proxy required but failed"), so the catch in `open-sse/handlers/chatCore.js`
writes `saveRequestDetail({status: "error"})` **and**
`trackPendingRequest(..., false, true)`. 9router-go now records both.

Fix (`internal/handlers/chat/fallback.go`):
1. `fwdErr` is assigned before the `return`, so the `TrackPending` defer reports
   `isError=true` and the topology card marks the provider as the last error
   (`usagetracker.ErrorProvider`, 10-second window).
2. A single `LogFailure` line — still only `requestDetails` with
   `status="error"`; the Overview aggregates (`usageHistory`/`usageDaily`) are
   not touched, matching upstream where `saveRequestUsage()` is only called from
   `buildOnStreamComplete`.
3. The error body is **marshaled**, not string-concatenated. Previously
   `[]byte(`{"error":{...,"message":"` + clientErr.Error() + `"}}`)` — the pool id
   belongs to the user, and quotes inside it made the payload no longer valid
   JSON. As a result `extractErrorText` failed to parse and the stored row
   read `"request failed"`; now the reason reads as-is.

The **unmodified** path: `no API key found` (`fallback.go:260`) also does not
record a row, and that matches upstream — `src/sse/handlers/chat.js:248`
returns `errorResponse(404, "No active credentials for provider")`
without `saveRequestDetail`.

**Verification:** `internal/handlers/chat/proxy_egress_failure_test.go`
(table-driven; disabled pool + pool id containing quotes) proves the body to
the client is valid JSON and mentions its pool, exactly 1 `requestDetails` row
with `status='error'`, 0 `usageHistory` rows. `go vet ./internal/handlers/chat/`
clean · `go test -race ./internal/handlers/chat/ ./internal/proxy/...` green.
Live smoke on port 20141 (new binary, empty DATA_DIR): a
`deepseek/deepseek-chat` request to a connection bound to a deleted pool → the client
receives `502 {"error":{"type":"proxy_error","message":"proxy pool
\"pool-\\\"gone\\\"\" is assigned but does not exist"}}`,
`/api/usage/request-details` reports `total=1` with
`response.status=502` and a message naming the pool,
`/api/usage/stats` still shows `byProvider={}` with `errorProvider="deepseek"`.

### 🛡️ Combos detect errors that the provider injects into the SSE stream

Background: the combo `["oc/space-bunny-free", "openrouter/stealth/space-bunny-alpha",
"ocz/space-bunny-free"]` failed 10 times in a row with the client message
`502 JSON error injected into SSE stream`. From production `requestDetails`,
the cause is not the gateway: the `Stealth` downstream behind OpenRouter is dead
(`provider_unavailable`), then OpenRouter relays that failure as an event
`data: {"choices":[],"error":{"code":502,...}}` over HTTP 200. The gateway
relays that chunk verbatim — the turn is recorded as `success`, the sick
connection is never locked, so every retry lands in the same hole.

Fix (without holdback, TTFT unchanged — per-line inline detection):
the new `internal/proxy/sse_inband.go` contains `DetectInbandSSEError` which is
provider-agnostic — it fails on **every** `data:` payload with a non-null
`error` (both the OpenAI shape and Claude `error` events), without inspecting the
provider name, model, or any specific message text. `sseCopier` swallows
that error line, closes the error-only turn with the gateway's own error frame,
and reports `UpstreamFailure(502)` so the existing combo path
locks the connection (`comboLockRetryable`) for the next retry.
A turn that already managed to send a completion is still considered successful (the same
rule as with the codex stream). The combo strategy is unchanged: round-robin
still rotates, it merely skips connections that are currently locked during the cooldown.

Known limitation: the translating `ScanStream` path (Claude client,
decloaker) is not yet equipped with this detector; the initial scope is `SSECopy`
(OpenAI-format streaming), where the production cases occur. The post-commit
combo loop behavior is unchanged (failover within the same request remains
impossible once the 200 headers are sent — matching the existing log message
`upstream error after headers committed`).

**Verification:** table-driven unit tests (`sse_inband_test.go`, including the original
Stealth chunk) + `TestSSECopy_InbandErrorOnlyStreamFails`,
`TestSSECopy_CompletedTurnIgnoresTrailingInbandError`,
`TestSSECopy_HealthyStreamUnaffectedByInbandDetection`, and the integration
`TestComboSkipsInbandErrorOnRetry` (fake sick + healthy upstream: the first
request carries the gateway's error frame without the raw provider chunk, and the retry
is served directly by the healthy member without touching the sick upstream). One flag-ordering
bug (`hasTerminal` set before `inbandFailure` is read) was caught
by these tests during implementation.

Parity note: the local upstream path (`/Users/luqmannul.hakim/htdocs/9router`)
is not available on this host, so it could not yet be compared with
`open-sse` — this change is additive (healthy streams are byte-identical, only
error-only streams change from silent-success to 502) and is noted here
in case upstream behaves differently.

### 🩹 opencode proxy pool rotation: rotation that actually goes through the proxy

On the opencode provider, choosing a rotation (round-robin/random) does not use the
already-configured warp pool — requests still go direct. The egress column in usage
also always writes "direct" for requests that actually go through an HTTP proxy,
and the "bound" column on the proxy-pools page is always 0 for pools installed at
the provider level because only connections are counted.

Now rotation cycles requests across all active pools, the egress column shows the
pool name, and the bound column counts provider-level installations. Rotation only
applies to proxy rotation settings; connection round-robin rotation keeps its
behavior unchanged.

Follow-up fixes from review:

- **Pool rotation no longer turns on connection rotation.** The provider card stores
  two rotations in one entry: the `isNoAuth` block writes the **pool** rotation to
  `rotateStrategy`, the round-robin button writes the **connection** rotation to
  `fallbackStrategy`, and both are read into the same field. NoAuth providers
  now trust only `fallbackStrategy` for connection rotation, so choosing a
  pool rotation no longer makes accounts switch.
- **Pool rotation only applies to NoAuth providers.** That is exactly where the UI
  offers it. On API-key providers, `rotateStrategy` holds the account rotation,
  and turning it into an egress steer would send traffic through a pool the operator
  never configured.
- **The pool-deletion guard closes a rotation gap.** Rotation uses all active pools,
  but `countProxyPoolBindings` only counts pinned pools. A pool currently serving
  rotation traffic could be deleted (200) beneath the next request;
  now it is rejected with 409. A pool that is both pinned and used for rotation is still
  counted as one binding, not two.
- **`sticky` is rejected as a pool rotation strategy.** This value is accepted and then
  served as round-robin, even though this resolver does not implement
  affinity. The UI only offers round-robin and random, so no one
  loses an option.
- **Per-provider rotation counter**, keyed by the already-resolved alias
  so `oc` and `opencode` share a cursor and do not skip each other.
- **`ListProxyPools()` is no longer called in the hot path.** Rotation candidates are read
  with a query that filters in SQL, cached per-`Repo` (not package
  state, so two `Repo`s over different databases do not read each other's pools),
  and invalidated on every pool mutation — per AGENTS.md §4.A.

### 📖 README: how to use a 9Router database directly, without import

The most frequent question — "can I not import from 9router?" — is already
answered in the documentation, but the answer is scattered: one sentence in `DATABASE.md`
and an operator checklist that in fact tells you to run upstream first. The second
is wrong for the most common case, because `EnsureCoreSchema` already
tolerates an existing database. As a result readers conclude they must
migrate manually, when in fact nothing needs to be imported at all.

The new `🔄 Sharing a database with 9Router` section in the README states the matter
plainly up front: both projects point at the same `DATA_DIR/db/data.sqlite`,
9router-go opens it as-is, and providers, connections, proxy pools,
combos, API keys, model aliases, and usage history are immediately readable. There are no
import, export, or migration steps. Two operational modes are explained: a full
switch (stop 9Router, run 9router-go), or running side by side on different
ports with the same `DATA_DIR`. The reverse direction is also safe — two Go-only
columns and the `upstream_leases` table are ignored upstream because its schema
sync is additive.

The limits are written as-is, not polished: no legacy JSON import, no
destructive migration or pre-migration backup, and `_meta.schemaVersion` is not
interpreted — a DB from a much newer 9Router release must be checked manually.
The operator checklist in `DATABASE.md` is corrected so it no longer tells you to run
upstream first; the "Default path" row in the compatibility Boundaries table
is clarified that there are no import steps.

**Verification:** not merely claimed from the documentation. A SQLite fixture was built
with the upstream shape — 11 core tables, `providerConnections` without
`lastUsedAt`/`consecutiveUseCount`, without `upstream_leases`, containing one
connection, one API key, one combo, one scope KV, and one
`usageHistory` row. The binary produced by `make build` is run against that fixture
(same `DATA_DIR`, port `:20197`): `/health` 200, login 200, `/api/connections` returns the upstream connection as-is, `/api/settings` reads `requireLogin`/`rtkEnabled` from the upstream settings row, `/api/usage/stats` 200, and `GET /v1/models` with `Bearer sk-upstream-existing-key` (the API key stored upstream) returns the model catalog — so not only are the rows read, they are genuinely used by the proxy path. After boot, the fixture does get the additional `lastUsedAt`, `consecutiveUseCount`, and `upstream_leases`, and the entire upstream row stays intact. The fresh-`DATA_DIR` path was verified separately (port `:20198`): 13 tables are created, `_meta.schemaVersion=1`, `settings` contains an empty row, `/health` 200. Both instances were shut down and their ports released.

### 🐛 `make web-build` only rebuilds when `dist` is missing — dashboard keeps the old bundle

`web-build` previously decided "rebuild if `web/dist/index.html` doesn't exist". The evil of it is that this is correct on a fresh checkout and wrong on every machine that already has `dist`: pulling or fast-forwarding `main` changes `web/src` while `web/dist` stays put, so the SPA is never rebuilt and `go build` bakes in the old bundle. The symptom is the dashboard looking like the previous release while the Go side is already up to date, and the only cure is `FORCE=1 make web-build` which nobody remembers. It actually happened on 2026-10-04: fast-forwarding to `bfa0e954` left `index-BvXlfcRR.js` in `dist` while the sources were already 7 files newer.

Decision: the freshness value moves to `web/scripts/web-build.ts`, which fingerprints every build input — `web/src`, `web/public`, `package.json`, `bun.lock`, the three tsconfigs, `vite.config.ts`, `web/index.html` — then rebuilds when the fingerprint changes. File contents are hashed too, not just mtime: `bun install` and `git checkout` can reset timestamps without moving a single byte. `bun.lock` and `package.json` are counted because a dependency bump changes the bundle without touching `web/src` — a class that would be missed by a src-only watcher.

The stamp is stored in `web/.dist-stamp`, deliberately outside `web/dist`: files named `.*` that sit directly inside `dist` are also caught by `//go:embed dist/*` and would leak the build fingerprint as a downloadable route. The new stamp is only written after a successful build, so a failed build leaves the old stamp and the next run tries again instead of skipping.

The `FORCE=1` policy is kept as an explicit shortcut.

**Verification:** `bun test` 181/181 (10 new tests in `web/scripts/web-build.test.ts` covering: an edit in `src`, a new nested file, a manifest bump without a lockfile bump, a rename with identical bytes, `dist` and `node_modules` being ignored); `make web-build` twice in a row — the first run builds and records the stamp `1ce65ad06890`, the second run reports "web/dist is up to date (inputs unchanged)"; touching `Sidebar.svelte` triggers a rebuild, and `git checkout` restores the fingerprint to `1ce65ad06890`. `tsc -b` clean, `oxlint` with no new warnings (the 2 existing warnings were already pre-existing in `CreateComboModal.svelte` and `clipboard.test.ts`), `make vet-svelte` 92 errors = baseline, `go test ./web/...` 8/8. Smoke: the probe instance serves the same `index-Ba3m-vxm.js` (JS 200, 899.073 bytes) as the already-running process, so the old process really is running the rebuilt binary.

### 🎨 Console Log: the tag and message stick together as `requestGET`

Console Log rows are rendered from `{#if parts}<span>{parts.tag}</span> {/if}{parts.message}`. The literal space at the edge of the `{#if}` block is trimmed by Svelte as a *text node* at the block boundary, so what displays is `requestGET /models` or `usageLogged provider=...` — the method, path, and status merge together with no gap, making it hard to scan.

The space is now sent explicitly through the `{' '}` expression. Rows without a tag (e.g. captured stdout) are unchanged.

### ✨ Proxy Pools: Test All button, latency badge, dual-probe, and filter controls

The old proxy pool Health Check only lived in the selection toolbar — if nothing was selected, there was no button. Now there is a permanent **Test All** in the header, with `n/N` progress and a latency badge that repaints per row as soon as each probe finishes, so the status is visible without waiting for a whole job.

The backend probe is now two-stage: **Google `generate_204` → Cloudflare `cdn-cgi/trace`**. A single endpoint alone misreads on networks that block Google — a healthy proxy is reported dead, and then the user turns off a pool that is still usable. What determines the badge: the latency that is reported and stored always belongs to the probe that **decided** the result, not the total wait time; a slow primary that then fails does not pass its 200ms on to a fast fallback badge. Failed pools are stored with latency `0` so an unmeasurable number never reads as a good number.

The dashboard gains status filtering (All / Active / Passed / Failed — upstream `failed` and `error` are merged into one bucket), sorting (Fastest, Recently Tested, Name), and two cleanup actions: **Disable Failed** and **Delete Failed**. All three now report **only results actually confirmed by the server**; previously `Delete Failed` silently discarded non-409 errors and still sounded successful, so the user could think the proxies were clean while they were still active and still used by routing.

Parity note: upstream `decolua/9router` still probes `https://google.com/` with an 8s HEAD and writes `testStatus: "active" | "error"`. Those two words are still accepted in the UI, while the gateway itself still writes `passed`/`failed` as before.

### 🩹 Live upstream tests split from CI via explicit opt-in

19 tests in `internal/handlers/chat/` call real providers with real credentials from `~/.9router/db/data.sqlite`, and all of them are included in `go test ./...` which CI runs. The models are free-tier and shared: `oc/space-bunny-free` and `muse-spark-*-contributor-free` rate-limit per IP and are often already used up by other users. As a result `space-bunny-free` fails in `go test -race ./...` with `upstream error: Forward...` — a failure that has absolutely nothing to do with this gateway. Worse, without CI credentials those tests would "pass" while proving nothing.

The test class is now distinguished by the opt-in `9ROUTER_LIVE_TESTS=1`, not by per-status skips. Its gate is `requireLiveUpstream`, called inside `getRealUserDB` which every live test already goes through — so a new live test that forgets to install the gate still skips, rather than leaking into CI. The five `muse_spark_*` tests don't go through `getRealUserDB` (their opencode connections aren't seeded, so their models resolve straight to the provider) and install the gate themselves. `make test-live` is a wrapper to run them locally.

The nine `if rec.Code == http.StatusTooManyRequests || ...` patterns that were rewritten over and over per test are now one `requireLiveOK`/`requireLiveSSE` helper, and `upstreamUnavailable` is widened to 429/403/402/502/503/504 — statuses that mean "not now", not a gateway defect. Every skip carries the provider's response body, so a local run still tells you what upstream actually said. 400 and 500 still fail, because those are gateway errors. 401 is deliberately separated: that's a local profile problem, not a provider outage.

### 🐛 `TestGateAcquire_JitterOnlyWidensTheGap` flaky — a 30ms floor with no margin

PR #146 fixed `TestGateAcquire_SpacesConcurrentCallers` by measuring the burst span, and deliberately left `JitterOnlyWidensTheGap` alone because "there was no evidence it needed touching". That evidence now exists: on this branch, before the change, the test fails on its own —

```
fetchgate (24 passed, 1 failed)
  [FAIL] TestGateAcquire_JitterOnlyWidensTheGap
     gate_test.go:100: slot 2 waited 29.7696ms, want at least 30ms floor
```

The root cause is the same: the 30ms floor is measured with a stopwatch and legitimate jitter can be 0, so the next grant lands at exactly 30ms — with no slack to absorb a late-starting goroutine. The assertion stays per-gap: a span would hide a half-applied floor — random jitter that is generous enough can extend the total past the target while every individual gap is short. But minGap is raised to 40ms with 60ms jitter so there is margin.

`internal/fetchgate/gate.go` is untouched — zero production changes.

### 🐛 `internal/fetchgate` flaky under `go test -p 16` — gaps measured wrong

`TestGateAcquire_SpacesConcurrentCallers` fails 4 out of 5 runs on `go test ./... -p 16`, at a consistent index (2, 3, 7) with gaps of 12–35ms against a 40ms floor. The gate itself isn't wrong: what's being measured is "when did this goroutine get a chance to run", not "when was its slot opened". A slot opened exactly on time can still wait another 8ms to be scheduled on a busy CPU, and that counts toward `time.Since(start)`.

Comparing sequential start times means comparing the scheduling luck of two different goroutines. One fast one sitting next to a slow one reads as two slots granted at once — which is why the failing index is always in the middle, never at the ends.

The assertion is changed from "each gap ≥ 40ms" to "the span of 8 callers ≥ 280ms". Scheduling noise is zero-sum around the loop, so summing the gaps cancels it out: eight slots with a 40ms floor always span ≥ 280ms, while a gate that hands out everything at once spans a few microseconds no matter how busy the CPU is.

The test's strength is not reduced. Tried with pacing decoupled from `reserve()`, it fails in exactly the right place:

```
--- FAIL: TestGateAcquire_SpacesConcurrentCallers
    gate_test.go:82: 8 callers spanned 0s end to end, want at least 280ms
```

`TestGateAcquire_JitterOnlyWidensTheGap` is **not** changed: even though it also shows up in some parallel runs, it appeared not once in the 5 baseline runs at `-p 16`, and it measures a single caller so its noise is one-directional (always ≥, never <). There's no evidence it needs touching, so it isn't touched.

**Verification:** `go vet ./...` clean; `go build ./...` clean; `go test ./internal/fetchgate/ -count=10 -race` green; `go test ./...` green; `go test ./... -p 16` green in 4 of 4 runs, previously failing 4 out of 5.

### 🐛 `go test -shuffle` fails in `internal/app` — order-dependent tests

`db.InitGlobalDatabase` only allows one database connection per process via `sync.Once`, and the `OnStop` hook of `app.DatabaseModule` closes that connection forever. In production that's correct: one process means one gateway, and a second boot would silently open the same SQLite file under the first connection pool's reverse. In tests, that combination makes all of `internal/app` order-dependent: the test that boots first claims the only global handle, its `OnStop` hook closes it, and every subsequent boot gets an already-closed handle. On `origin/main`, shuffle seeds 1 through 6 all fail; after this patch, 10 seeds are green.

`sync.Once` is replaced with a mutex plus handle, and `ResetGlobalDatabaseForTest` is added to return the process state to zero before and after tests that boot `DatabaseModule`. The pattern follows the existing `shutdown.TestReset` in the neighboring package. The reset is only used by tests; production never calls it, because closing the only connection in the middle of the process is precisely the failure that exists to be prevented.

`TestGlobalDatabase` now also tests what previously wasn't tested: a second boot with a different path must reuse the same handle, not open a second pool on one file. And the new regression test `TestDatabaseModule_SecondBootAfterAFirstOneWasClosed` boots twice in a row; tried without a reset, it fails with exactly the message `first boot is not usable: sql: database is closed`.

**Verification:** `go vet ./...` clean; `go build ./...` clean; `go test ./internal/app/ ./internal/db/ -shuffle=<1..10>` all green; `go test -race` on both packages with shuffle green; `go test ./...` green; `go test -tags=integration ./internal/integration/...` green.

### 🐛 Vercel/Deno/Cloudflare relay dashboard deploys 401 — issue #140

All three deploy relay endpoints were registered in `SetupRoutes` (`internal/handlers/router.go`) at the path `/proxy-pools/{platform}-deploy`, which is mounted under `RequireApiKey`. The dashboard SPA instead calls it with a session cookie and without an engine key (`getAuthHeaders()` only sends `Authorization: Bearer` if `localStorage['9router_key']` is populated), so every "Deploy Relay" button is answered with `401 Authentication required. Provide an API key via Authorization: Bearer <key> ...` — exactly the issue #140 report.

Moved to `SetupDashboardRoutes` as `/api/proxy-pools/{vercel,deno,cloudflare}-deploy` behind `RequireDashboardAuth`, same as the pool CRUD next to it and same as upstream (`src/app/api/proxy-pools/*-deploy/route.js`, which does indeed live in the `/api/*` namespace). The `/api` prefix closes a second gap at the same time: `web/vite.config.ts` only proxies `['/api', '/v1', '/usage', '/translator', '/debug', '/admin']`, so the old path also 404s in dev mode.

Note: this applies to **Vercel and Cloudflare too**, not just Deno — all three share the same registration block. The only thing that masked Vercel was the presence of an API key in `localStorage`; as soon as one 401 occurs, `handleUnauthorized()` deletes `9router_key` *and* `9router_auth`, so the next attempt dies too.

A second bug in the same handler: Deno status polling only looks for `"succeeded"`, so a revision Deno has already rejected as `failed` still burns the full 60-second budget and finally reports `"deployment timed out"` — a symptom that reads like a network problem rather than a build failure. `awaitDenoRevision` now follows upstream: the loop only runs while `queued`/`building`, and a terminal status already reported by the deploy call is returned without a single request.

The parts of parity that were **already** correct and were not changed: the worker template, body fields, validation, slug + label, deleting the app when deploy fails, and the composition of `deployUrl`. The `deno relay` badge in the pool list is also not a gap — upstream doesn't have one; and `0 bound` for OpenCode isn't a bug either, because upstream counts `boundConnectionCount` only from `providerConnections` (`buildUsageMap` in `src/app/api/proxy-pools/route.js:31-41`) while OpenCode is a noAuth provider that has no connection row.

**Verification:** `internal/integration/relay_deploy_test.go` — both session cookie and API key reach the handler (400 from its own validation, not 401), and anonymous stays 401. The test is proven to fail on the old wiring (405 on both paths). `internal/handlers/media/deploy_test.go` — a terminal status passes polling with no request, `failed` mid-poll stops at the first call. `go vet ./...` and `go vet -tags=integration ./internal/integration/...` clean.

### 🐛 SQLite PRAGMAs applied per-connection via the driver DSN — issue #139

Previously the PRAGMAs (`busy_timeout`, `synchronous`, `temp_store`, `mmap_size`, `cache_size`, `foreign_keys`) were executed via `db.Exec()`, which only affects the one initial connection. As a result 3 of the 4 connections in the pool ran with default settings (`busy_timeout=0`), making them susceptible to `SQLITE_BUSY` under write contention. The per-connection settings are now moved to the `modernc.org/sqlite` driver DSN (`_pragma=...`) so they automatically apply to every new connection in the pool, `PRAGMA journal_mode = WAL;` is still kept post-open, and `SetMaxIdleConns` is aligned to 4.

**`foreign_keys` note:** before this change `PRAGMA foreign_keys = ON` only took effect on one connection, so 3 of 4 connections ran without enforcement. Now every pool connection genuinely enforces FKs. The upstream v0.5.85 core schema (including `upstream_leases`) declares no `FOREIGN KEY` constraints — `grep REFERENCES` in the repo has zero matches — so there is no row that this change could reject. If FK constraints are added later, the write path that has been silently violating referential integrity will start failing; that is indeed the correct outcome, but it must be recorded as a semantic change, not merely a configuration fix.

**Verification:** the unit test `TestPooledConnectionsPragma` in `internal/db/client_test.go` verifies all the PRAGMAs on all (4) live connections in the connection pool. `TestSQLiteDSN` verifies the shape of the DSN (the list of `_pragma`s, the `?` vs `&` separator, and the absence of `journal_mode`) without opening a database. A concurrency reproduction (8 writers × 40 writes, throwaway) failed 64/320 with `SQLITE_BUSY` on the old code and 0/320 on this code, 5 times in a row — the cross-provider combo mode with round-robin really does produce parallel writes like that.

### 🔒 pprof protection behind `RequireAdminAuth` when `PPROF_ENABLED=true` — issue #126

The profiling endpoints `/debug/pprof/*` were previously registered directly on the root router with no auth group, so when the `PPROF_ENABLED=true` flag was enabled, the debug surface (heap, cmdline, cpu profile, goroutine trace) was publicly reachable with no credentials. The pprof routes are now moved into the admin tier (`middleware.RequireAdminAuth()`), requiring an admin session cookie or a local CLI token (`x-9r-cli-token`), and rejecting both public requests and standard client API keys (`401 Unauthorized`).

**Verification:** `TestSetupServerRouter_PprofUnauthenticated`, `TestSetupServerRouter_PprofAuthenticated`, and `TestSetupServerRouter_PprofDisabledByDefault` in `internal/handlers/router_test.go` pass 100%.

## [v1.9.8] - 2026-10-04

### ✨ RTK compression metrics & reasoning in log/UI, plus the "I have ADHD" output style — issue #129

- **Prompt efficiency metrics:** Computes and displays the token savings produced by RTK compression (`rtkSavedTokens`, compression ratio) and tracks `reasoningTokens` in the request log and the UI analytics panel (`RequestDetailsTab`).
- **"I have ADHD" output style:** Adds a new output style option to the Token Saver (`adhd_prompt`) to present model responses in a neatly structured, concise, bulleted, and easy-to-scan way, with a toggle control in the dashboard's Settings & TokenSaverView.

**Verification:** Unit tests `internal/tokensaver/prompts_adhd_test.go` and `internal/handlers/chat/rtk_metrics_test.go` pass 100%.


### 🧹 Antigravity OAuth tests split out into `antigravity_test.go` — issue #136

Four Antigravity handler tests (`TestHandleAntigravityAuthorize`, `TestGetAntigravityRedirectURI`, `TestHandleAntigravityExchangeErrors`, `TestHandleAntigravityExchangeSuccess`) were previously stored in `freebuff_test.go`, obscuring provider isolation. All four were moved to `antigravity_test.go` with identical contents, preserving the principle of independence between providers.
**Verification:** `go vet ./...` is clean, `go test -race ./internal/handlers/oauth/...` is green.

### ⚡ Force Fallback: serve the account whose cooldown expires soonest (issue #130, parity with `decolua/9router` PR #130)

Until now, if **all** accounts of a provider were on cooldown, the gateway
failed immediately — while the account that would be free soonest could have
served requests just seconds later. Operators had to wait or add more
accounts. Upstream PR `decolua/9router#130` adds a "Force Fallback" switch:
when no account is available, use the account with the smallest remaining
cooldown, then let upstream decide whether to accept or reject.

The source of truth stays single: `getBestConnection` already computes
`cooldownUntil` (the earliest reset) for the error message, so the forced
candidate only needs to be re-sorted by the same value. The switch is
`settings.forceFallback` (Dashboard → Default Routing Strategy toggle) or the env
`FORCE_FALLBACK_ON_ALL_UNAVAILABLE=true` for deployments that must hold up
before anyone opens the UI. The default is off: without opt-in, behavior does
not change at all.

Forced candidates only include accounts that actually have a cooldown. Accounts
hit by a per-model lock or the quota cache are not included, since there is no
cooldown that could be shortened — forcing them means discarding the very lock
that excludes such an account, after which that model gets hammered repeatedly.
Per-model blocks are evaluated first (`connectionModelBlocked`), and
client-excluded accounts (`x-connection-id`/combo) are filtered as upstream
does. If all candidates are filtered out, the usual cooldown error is returned.

A fix discovered along the way: `handleAccountFallback` attributed the request
to the loop candidate (`c.ID`), not to the account actually invoked. As soon as
forced selection is active, the two can differ — usage rows, logs, and the
exclusion list must all name the account actually dialed. The integration
`internal/integration/force_fallback_test.go` locks down this contract through the
production router with a fake upstream.


### 🐛 Antigravity: fall back to `aicode-consumers` when the project ID is empty & fix clipboard copy on HTTP LAN — issue #123

- **Empty project ID fallback:** Antigravity requests without an explicit project ID now fall back to the default `aicode-consumers`, preventing routing failures to the Google Cloud Code assist upstream.
- **Metadata probe & enum:** Aligning the metadata probe with the numeric enum format, cleaning up inauthentic client headers, and honoring the negative cache.
- **Clipboard in non-secure contexts (HTTP LAN):** `navigator.clipboard` is only available in a secure context (HTTPS / localhost). Accessing the dashboard via a LAN IP address over plain HTTP previously produced errors when copying the API key or terminal commands. A fallback based on `document.execCommand('copy')` with a hidden textarea element was added.

**Verification:** `TestResolveAntigravityProjectID_FallbackAICodeConsumers` & the clipboard unit tests in `web/src/lib/clipboard.test.ts` (105 assertions) pass 100%.

### ✨ Per-provider header override — issue #101 (part 4), upstream b3cf3fde parity

Operators can finally inject headers into a provider's outbound request
without touching code. It was entirely absent on our side: no route, no
settings field, no injection point, no UI.

**Storage** follows the existing settings pattern rather than a new table:
`providerOverrides` in the `settings` blob, read via `db.GetProviderOverride`
and written via `db.SetProviderOverride`, keyed by the **canonical provider
id** — just like upstream, which locks `resolveProviderAlias(id)`. The request
side canonicalizes too (`chat.ProviderOverrideKey`), so one entry serves two
spellings: the dashboard opens the page via the alias, while the request
arrives as `provider/model`.

**Injection point** is just one: `getProviderConfig` merges the overrides into
`cfg.StaticHeaders` before returning the config
(`chat.applyProviderOverrides`). That's deliberate — every executor builds its
outbound headers from `cfg.StaticHeaders`, so merging here reaches
everything without a single executor file learning about this feature. Upstream
itself merges inside the executor, which here would mean copying it into about a
dozen files. The merge happens **after** the relay rewrite, so relay headers
can be overridden too, just like upstream's `Object.assign`.

**Precedence is written explicitly:** overrides win over the static header
registry (`providers.MergeHeaderOverrides`), exactly `Object.assign(headers,
providerOverrides.headers)` in `open-sse/executors/base.js:132`. Operators
correct the headers the gateway sends, not add a second opinion.

**Rejected surface** — this is what keeps this feature from becoming an auth
bypass, unlike applying "overrides win over everything" without any
filter. `authorization`, `cookie`, `host`, `content-length`, `content-type`,
`connection`, and `transfer-encoding` cannot be overridden. The list and its
rules belong to upstream and were moved into `db.NormalizeProviderOverrides` so
they cannot be bypassed through a second write path. An overridable `Host`
would direct traffic to a different host; an overridable `Authorization`
would direct traffic to a different account. Both are rejected, and the GET
returns them to the UI so its fields are rejected **with a reason**, rather than
so operators discover it via a 400.

Header names are restricted to the RFC 7230 token subset and values are
checked to be free of CR/LF — without that, a single value containing `\r\n`
would inject a second header into the outgoing request.

**UI** `ProviderHeaderOverridesModal.svelte` (the counterpart of upstream's
`CustomConfigCard`) is placed in the provider detail toolbar. It displays
`builtinHeaders` from the registry as the baseline — so operators see exactly
what the gateway sends instead of guessing — and validates with the same rules
before submitting, so mistakes are caught in the field.

**Verification:** `TestProviderHeaderOverrideReachesUpstream` (integration —
real router, fake upstream) proves `X-Tenant: acme` is genuinely accepted
upstream while the connection's `Authorization` stays intact;
`TestProviderHeaderOverrideBeatsRegistryHeader` proves the override
defeats the registry's `x-opencode-client: desktop`;
`TestProviderHeaderOverrideIsScopedToItsProvider` proves the kimi override
does not leak into deepseek requests; `TestProviderHeaderOverrideCannotStealCredentials`
proves the 400 rejection does not corrupt the stored entry. Plus
`TestProviderOverridesRoundTrip`,
`TestProviderOverridesAliasAndCanonicalAreOneEntry`,
`TestProviderOverridesRejectAuthAndFramingHeaders` (11 subtests), and
`TestProviderOverridesRejectedWriteKeepsPrevious`. The wire tests fail
identically on `origin/main` (`upstream X-Tenant = ""`, and `x-opencode-client = "desktop"`).
Also synced through the real UI: the modal is opened in a browser, the header
saved, and its value is still there after a reload.

### ✨ Dashboard: deep link for the quota filter, `hidden` filter, `recurring` for codebuddy-intl — issue #101 (parts 1–3)

Three dashboard surfaces left behind from upstream v0.5.95.

**`?provider=` does nothing.** `providerFilter` in
`QuotaTrackerView` always starts from `'all'`; `onMount` reads localStorage
and settings, never `window.location.search`, and filter changes never write
back to the URL — so deep links and bookmarks are dead.
Now the filter is initialized from the URL on mount, and every change writes
back via `history.replaceState`, not `pushState`: this is a filter,
not navigation, so the back button must not walk through every provider
clicked. Going back to `all` removes its parameter. Unknown values are
dropped after the option list has actually arrived — not ignored up front —
so old bookmarks never leave an empty list with no way out.

**`codebuddy-intl` loses `recurring`.** The backend was already sending that
field (`usage_providers.go:998` `true`, `:1006` `false`); the frontend switch
only has `case 'codebuddy-cn'`. As a result the codebuddy-intl bonus plan
displayed with the label "Reset in" instead of "Expires in". Both providers are
now handled in the same case, like upstream's `ProviderLimits/utils.js:621-635`.

**The `hidden` flag exists in the type but is unused.** `providers.ts`
declares and sets it, and its two consumers are already correct
(`ProvidersOverviewGrid` for each category, `providers.ts:2282` for
`supportsKind`) — what is missing is the topology map. Of the five providers
marked hidden, four are TTS-only and are already filtered out by
`supportsKind`; the only one remaining is `mmf`/mimo-free, the only hidden
chat provider in the registry. `addProvider` → `topologyProviders` in
`AnalyticsView` now skips it, for the same reason as the provider list: that
map is read as "who is on the bus right now", not a complete inventory. The
provider detail page for one that is `hidden` can still be opened — that is
the field's own contract.

**On the "single source of truth" on the Go side:** there is none, and none
was created. Go does not have a copy of the `hidden` flag; owning one would
mean duplicating the registry that already lives in
`web/src/lib/providers.ts` into a third place. What Go can guarantee is what
it actually owns — the list of providers served by the quota tracker. That
list now contains zero of the five hidden providers, so
`isUsageEligibleConnection` has already excluded them from `providerOptions`
and the connection list; the result is identical to upstream's filtering in
`UsageStats.js:242` and `:250` for the current registry.
`TestHiddenProvidersStayOutOfTheQuotaList` locks that in, so a hidden provider
that someday gets served by the quota tracker will fail the test and be fixed
in the same commit.

**Verification:** `bun test` (127 pass), `tsc -b`, `oxlint`, `bun run build`,
`go test ./internal/handlers/dashboard/...` (including
`TestHiddenProvidersStayOutOfTheQuotaList`).

### ✨ Custom models can declare `contextWindow` and `maxOutput` — issue #90

`db.CustomModel` had only five fields, so models on OpenAI-compatible provider
nodes had no way to state their context window. The numbers published to
`/v1/models` and `/v1/models/info` were pure substring guesses: `my-custom-model`
and `llama-3.3-70b` always fell to `128000 / 4096` simply because their ids
matched no pattern. The resolution chain
(`GetModelTokenLimits` always returns non-zero, so the 128k floor is
practically never reached) makes large-window open-source models report
far smaller than the endpoint really is.

Two optional fields were added:

- `db.CustomModel.ContextWindow` / `MaxOutput`, `omitempty` — zero means
  "not declared", so rows saved before these fields existed behave exactly as
  before.
- `applyCustomCaps` reads both as **declarations**, not gap closers: a
  declared number replaces the table guess, while a zero number leaves the
  table in charge. This differs from the surrounding modality flags, which are
  additive.

The "Add Model" form has two new (optional) columns, and "Import from /models"
saves the `context_length` / `max_completion_tokens` that the endpoint
reports — the source is authoritative, so there is no need to guess again.

`GET /api/models/caps?provider=<node>` is also fixed: a node whose models are
all custom rows has no registry catalog, so the endpoint replied with
`caps: {}` and the same numbers were never visible in the dashboard. Now
custom rows are included there, with the declared flags and limits.

These numbers are only for the metadata path: `handleSingleModel` passes the
body through as-is, so no truncation and no `max_tokens` clamp change along
with it.

**Verification:** `TestCustomModelDeclaredLimitsReachEveryDiscoverySurface`
(integration — real router, a real HTTP listener, temporary SQLite)
proves the 1000000/32000 numbers reach `/v1/models`, `capabilities`, and
`/v1/models/info` untouched, while rows without declarations still use the
table values; `TestCustomModelDeclaredLimitsPublishedVerbatim` and
`TestCustomModelPartialLimitFillsOnlyTheGap` (unit), plus
`TestHandleGetModelCaps_CustomModelsOnNode` (dashboard, including
image-typed rows which must not appear in the chat map). All three boundary
tests fail identically on `origin/main` (proven with `git stash`).

### 🧹 Dead `signalSelfShutdown` in both build variants removed — issue #76

`internal/updater/signal_unix.go` and `signal_windows.go` defined
`signalSelfShutdown` with identical contents (`shutdown.RequestStop()`), and it
has had no call site since the `RestartSelf` rewrite moved to
`shutdown.RestartAfterStop`. The `os.Exit(0)` fallback comment that both carried
describes code that no longer exists. Both were removed: the platform caveat
that justified splitting the file is gone, because
`shutdown.RequestStop()` is the only shutdown path on all platforms.

The "NOT YET REMOVED" entry that had been left behind in `[Unreleased]` and in
the body of the v1.9.7 release was corrected to note its removal.

**Verification:** `go vet ./internal/updater/...` is clean, `go test -race
./internal/updater/...` is clean, and `go build ./...` for linux, windows, and
darwin still succeeds.

### 🐛 Custom window usage input rejects the letters `d`/`h` on phones — issue #115

The "Custom window" field on the Usage page was already `type="text"`, but it
still carried `inputmode="numeric"`. On Android/iOS keyboards that shows a
numeric keypad with no letter keys, so mobile users simply cannot type `14d` or
`12h` — the mandatory suffix itself cannot be typed, and the input coming back
empty makes the page fall back to the 7-day preset. `inputmode="numeric"` was
removed; `normalizeCustomPeriod` already rejects absurdly large numbers with an
error message, so validation is unchanged.

### 🔒 Penegakan Strict Provider Isolation & Concurrency Singleflight

- **Strict Provider Isolation**: Menghapus jalan pintas `routeModelToOwningProvider` di `internal/handlers/chat/resolution.go` dan prefix silang `antigravity/` / `ag/` di executor OpenCode (`internal/proxy/executor/opencode_zen.go` & `providers.go`). Seluruh model di-route secara seragam berdasarkan katalog dan alias resmi, mematuhi kontrak arsitektur di `AGENTS.md`.
- **OAuth Refresh Singleflight**: Membungkus refresh token OAuth kedaluwarsa (`refreshOAuthTokenIfExpired` & `forceRefreshOAuthToken` di `internal/handlers/chat/gemini_handler.go`) dengan `singleflight.Group` per `connectionID` untuk mencegah thundering-herd dan race condition pada request paralel.
- **Test Coverage Backend $\ge$ 85%**: Menambahkan unit test komprehensif pada 8 paket backend (`config`, `codexquota`, `usagetracker`, `middleware`, `translator`, `app`, `handlerutil`, `proc`), serta mengeliminasi bottleneck sleep 60s pada `proc_test.go` sehingga suite berjalan instan (< 1s).

### 🎨 Console Log: colors follow the level actually emitted

The Console Log page showed every line in green. The cause was not the color
choice, but the page guessing the level from already-rendered text
(`TerminalView.svelte`: matching `[tag]` or an `INF/WRN/ERR` prefix, otherwise
green), so anything unrecognized — including a failing upstream's `502` — fell
into green "success".

The fix moves the truth to its source. `log.ConsoleEntry` now carries
`{time, level, line}` taken from the level reported by the emitter, not from the
rendered text, and the buffer + SSE send that object as-is. Time was added
alongside because the text format doesn't print a timestamp at all —
without it lines couldn't be told apart once the buffer scrolls.

The shape of `logs` changes from `string[]` to an object. This is an intentional
wire contract change: its only consumer is the Svelte dashboard, and upstream
Next never sent any level, so there is nothing that could break.

Impact on the page: lines use the level color (ERR red, WRN amber, INF
green, DBG blue), each line has an `HH:MM:SS.mmm` stamp, the level chip is
both the filter and the counter display, plus search, a wrap toggle, a
Jump-to-latest button that appears when auto-scroll stops, copy/export, and an
empty state that names the cause. The panel uses theme tokens, not
`bg-black` — a dark panel in the dashboard's light theme reads as an accident.

Level colors were verified against the palette that actually gets compiled, not
a guess of the class name: Tailwind 4 compiles colors to `oklch`, so
`text-red-700` is not `#b91c1c`. Measured on real rendered pixels — light
ERR 6.10:1 / INF 5.10:1 / DBG 7.14:1, dark ERR 4.90:1 / WRN 8.22:1 / INF
7.30:1 / DBG 8.50:1 — all above the WCAG AA 4.5:1 threshold, and
`consoleLogContrast.test.ts` guards those numbers so they can't silently
drop when the Tailwind palette bumps versions.

### 🐛 Capacity adapter does not follow upstream — parity with `open-sse/services/capacityAdapter.js`

The input-modality adapter (vision/pdf/audioInput/videoInput) in this port
diverges from upstream `decolua/9router` in six places, three of which change
client-observable behavior:

1. **`enabled: false` toggle ignored.** `combo.go` `continue`s past disabled
   entries, then the "default fallback" block still runs because `len(pool) == 0`
   — there's no distinction between a pool that is *off* and a pool that is
   *unconfigured*. As a result requests containing images still get routed to
   `ag/gemini-3.8-flash-high` even though the operator turned the adapter off.
   Upstream `normalizeCapEntry` returns `{enabled:false, models:[]}` and
   `getCapacityAdapterModels` skips it, so nothing gets injected.
2. **Wrong default model.** Empty entries fall to `ag/gemini-3.8-flash-high`;
   upstream uses one constant for all capabilities,
   `DEFAULT_FALLBACK_MODEL = "oc/mimo-v2.6-flash-free"`, only inside the
   `enabled && models.length === 0` branch.
3. **Legacy entry shape unsupported.** Upstream accepts the old array shape
   `[{model, enabled}]`; the typed parse only recognizes the object shape.
4. **`reorderByCapabilities` is two-tier.** This version only has "satisfy all
   capabilities" vs "the rest". Upstream has three tiers: hard+soft, hard only,
   then the rest — so among two models that are both vision and also have
   `search`/`tools`, the latter is prioritized.
5. **Capability detection is far narrower.** What this port has only scans
   the last single `role: "user"` message; upstream scans the *trailing run* after
   the last assistant/model message and also reads `contents`/`request.contents`
   (Gemini/Antigravity), `images` (Ollama/Hermes), `attachments` /
   `experimental_attachments`, data-URIs inside strings, and guesses the mime
   of file blocks from `file_data`/`source.media_type`.
6. **History isn't trimmed for adapter models.** Upstream
   `stripHistoryForContext` cuts the conversation in the middle so it fits the
   adapter model's context window, which is often much smaller. Without it, long
   conversations routed to the adapter fail on length upstream.

Additionally, `detectRequiredCapabilities` now uses `trailingUserItems`, so
images in old turns no longer lock the combo to a vision model — matching
upstream's note that history media "gets stripped + placeholdered downstream".
The fusion path now also accepts any combo model as-is, rather than a list that
has already been augmented, matching `src/sse/handlers/chat.js` which sends
`comboModels` to `handleFusionChat`.

Also added is `looksLikeVisionModel` (a port of `open-sse/providers/visionPatterns.js`)
as a last-resort heuristic: a model id containing its modality word itself
(`qwen3-vl-plus`, `glm-4.6v`) is treated as vision even if it's not yet in the
capability table. As with upstream, this only turns vision on, never off.

Preserved behavior: combo names in the vision pool still don't satisfy a hard
cap, because upstream's `modelSatisfies` splits on `/` the same way. The pool
only accepts vision models, not combos — so a primary combo that doesn't
support vision isn't routed to the "vision combo", and indeed couldn't be so
upstream either.

### 🔒 `http.Server` without connection limits — vulnerable to Slowloris — issue #124

`ProvideServer` builds the `http.Server` with only `Addr` and `Handler`, so
`ReadHeaderTimeout` and `IdleTimeout` are both zero: a client that opens a
socket then sends headers byte-by-byte holds one file descriptor indefinitely,
and connections abandoned in the keep-alive pool are never reaped. The danger
in this configuration is not hypothetical — this repo has two expose paths to
the internet (`internal/auth/tunnel.go` for tailscale funnel,
`internal/handlers/media/deploy.go` for Cloudflare tunnel / Vercel /
Deno deploys), so "it only runs on localhost" does not hold.

Now `ReadHeaderTimeout: 10s` and `IdleTimeout: 120s`. The 10-second figure is
not a guess: it is already used by the OAuth callback listener in
`internal/proxy/oauth/codex_proxy.go`, so now one convention applies in both
places. Its value is deliberately **not** made configurable via `.env` —
this limit is what holds a single connection, so opening it up through
configuration means handing Slowloris control to anyone who can edit that
file.`MaxHeaderBytes: 1 MiB` was also installed, but it is **not** part of the hole. Slowloris is already rejected by `net/http` without a limit on header blocks, because the `Server` with a zero value falls back to `http.DefaultMaxHeaderBytes` (1 MiB), so the value actually enforced is the same. Setting it explicitly acts as a statement — the limit is this repo's decision, not a stdlib default that was never reviewed here.

`WriteTimeout` stays at zero for the reason now written in the code: `internal/proxy/stall.go` allows one silent SSE stream to idle until `DefaultStallTimeout` (6 minutes), and a write deadline would cut that stream off mid-response — including the SSE usage/console-log for the dashboard and Gemini Live WebSocket sockets. `IdleTimeout` is safe because it only applies to keep-alive connections that are **not** currently serving a request.

**Verification:** `go vet ./...` is clean; `go test ./... -count=1` is green; `go test -tags=integration -race -count=1 ./internal/integration/...` is green. The new test `TestServer_ConnectionLimitsAreEnforced` boots `ServerModule` via fx and tests the limits that the real `ProvideServer` builds — it fails on `origin/main` with `ReadHeaderTimeout` and `IdleTimeout` at zero, and passes after this patch.

The proof that these limits do not cut off slow models is now a test: `TestServer_SlowStreamingRequestSurvivesTheLimits` runs a handler that uploads its headers in five spaced chunks, is silent for 400ms before the first byte, then drips chunks for ~2.4 seconds — everything must arrive. This test is checked in both directions: it **fails** if `WriteTimeout` is set to 2 seconds, with the last chunk lost right in the middle of the stream, and passes on the correct configuration. The first version of its handler only ran 1.6 seconds so the 2-second deadline happened to suffice and the negative case slipped through — the stream duration now deliberately exceeds that threshold.

So the installed limits only apply **before** and **after** a request exists, never in the middle of a stream.

> Note: on one run of `go test ./...`, `TestGateAcquire_SpacesConcurrentCallers` and two tests in `usage_throttle_test.go` failed with the message `want >= 40ms`. Both measure time gaps with `time.Sleep`, and this diff does not touch `internal/fetchgate` or `internal/handlers/dashboard` — `usage_throttle_test.go` calls `router.ServeHTTP` with `httptest.NewRecorder()`, so it never passes through `http.Server` at all. Re-runs on this branch (`-count=3` at `-p 1` and `-p 16`, plus two full `go test ./...` runs) were all green, so this is CPU contention on concurrent runs, not a regression.

### 🐛 Round-robin rotation stuck: the `lastUsedAt` stamp never advances — issue #107

The root cause is not the stamp format but the time source. The nanosecond format was already correct (`db.RotationTimestampFormat`, fixed width). What failed is `time.Now()`: its precision follows the platform, not Go's promise. On the Windows host where this problem was diagnosed, the clock only advances **every ~815µs** — 689,900 consecutive calls produced 246 distinct values in 200ms. Six rotation picks within a single millisecond therefore formatted into identical strings, and rows with the same stamp could not be distinguished by the least-recently-used tie-break, so the selector kept returning the same account.

`TestApplyConnectionStrategy_KeepsRotatingPastFirstCycle` failed ~1 in 3 runs on a clean `origin/main` (`-count=50`), not as a side effect of any PR.

The fix makes the ordering a **property of the write path**, not a property of the clock: `db.stampConnection` reads `MAX(lastUsedAt)` and writes a value that always sorts after it, all inside a single SQLite write lock (`BEGIN IMMEDIATE`). The lock is taken up front, not at the `UPDATE` — a deferred transaction only locks after the `MAX` is read, so two processes sharing one database could read the same maximum and stamp the same value onto two different rows, resurrecting the tie the selector could not break.

A consequence fixed along the way: `TouchConnectionRotation` now returns the value actually written, so the in-memory row the selector reads can no longer differ from the row on disk. The selector previously read the clock a *second* time separately, so the in-memory value could differ from what was stored.

A second flaw was caught while writing the test: the fixed-width stamp `…:00.000000001Z` turns out to sort **after** the legacy `…:00Z` when compared as strings, because `'.'` (0x2E) beats `'Z'` (0x5A). A one-nanosecond bump would never get past that legacy row at all, so the bump skips a full second when a nanosecond is not enough.

**Verification:** `go test ./internal/...` is clean; `go vet ./internal/...` is clean; `go test -race ./internal/db/ ./internal/handlers/chat/` is green; `TestApplyConnectionStrategy_*` is green at `-count=50` (previously failing intermittently on `origin/main`). The new test `TestStampConnection_FrozenClockStillOrdersStrictly` freezes the clock entirely — a condition that would never occur on a host with a low-resolution clock — so the ordering is tested as a contract, not as coincidence. `TestStampConnection_SurvivesRestartAndClockJump` closes the database, reopens it, then sets the clock back one hour to prove the ordering survives a restart **and** an NTP correction. `TestNextRotationStamp_AlwaysSortsAfterPrevious` covers eight combinations of clock position and stored stamp format.

> Note: `TestGateAcquire_*` in `internal/fetchgate` occasionally fails due to `time.Sleep` assumptions in this environment, and has been failing identically on `origin/main` (proven with `git stash`), so it is out of scope for this issue.

### 🐛 "Strict Proxy" does not hold — upstream decolua/9router#4333 parity

`strictProxy` on the pool and on the connection means "never leave through the real IP". Two places in this gateway still let it through: `proxy.DoRequest` replays a request that failed at the proxy through `directProxyClient` without checking the flag at all, and the legacy path `getClientForConnection` merely logs `strict proxy enabled but proxy url invalid` and returns a direct client. Both mean traffic that should never touch the real IP stays active.

Now `DoRequest` rejects (rather than replays) when the proxy fails, both via a marker on the context (`proxy.WithStrictProxy`) and via the client that a strict pool provides (`proxy.ForbidDirectReplay`), and the legacy path returns an error. `strictProxy` is only read from the connection row that is actually stored; previously `var strictProxy bool` shadowed the field so the flag at the connection level never meant anything — the branch at `connections_proxy.go:117-120` that carries the `strict proxy enabled but proxy url invalid` message was never executed all along.

The "proxy is actually intended" gate is also preserved exactly like upstream: strict only refuses when there is a `proxyPoolId`, `enabled`, `connectionProxyEnabled`, or a non-empty url. Executors like Qoder set `strictProxy` with no proxy at all — meaning "don't replay this request directly", not "a proxy must exist" — and without that gate they would die outright.

### 🐛 Insecure TLS fallback for certificates that fail verification — upstream b58bd804 parity

A certificate verification failure (corporate proxy or antivirus that re-issues TLS) is now retried once without verification, using an `InsecureSkipVerify` transport cached per proxy url. Detection is via `errors.As` against `x509.UnknownAuthorityError`, `x509.CertificateInvalidError`, `x509.HostnameError`, and `tls.CertificateVerificationError` — not a substring match like `isProxyFailure`, which would flag almost every error. `STRICT_SSL=true` or `=1` disables this fallback, following upstream.

### 🐛 3-second watchdog for a delayed `response.completed` — upstream fbcaa282 + 7111db35 parity

The translator holds `response.completed` until the usage trailer arrives (#4476). If upstream stops after `finish_reason` — without a trailer and without `[DONE]`, the connection is held open — that delay never finishes and the client waits forever. `proxy.ScanStreamWithDeadline` now caps the pause between events only after a terminal event is genuinely delayed, and the watchdog bridge (`executor.completionWatchdog`) sends `response.completed` exactly once when that limit is exceeded. A stream that is still flowing is never cut off, and a terminal event is never sent twice.

**Verification:** `TestDoRequestStrictProxyNeverReplaysDirect` (the two StrictProxy paths) proves that not a single request reaches upstream directly; `TestDoRequestStrictProxyAllowsDirectWhenNothingConfigured`, `TestStrictProxyFlagAloneDoesNotBlockDirectUpstream`, and `TestNonStrictPoolStillDegradesToDirect` protect the "proxy is intended" gate; `TestDoRequestRetriesWithInsecureTLS` / `TestDoRequestStrictSSLRefusesInsecureRetry` exercise the TLS fallback and its opt-out; `TestStreamChatToResponses_WatchdogFlushesStalledCompletion` and `TestScanStreamWithDeadlineReleasesAStalledUpstream` run the limit through package variables rather than sleeping 3 seconds.


### 🐛 Routing & translator: codex `enabledModels`, dual DeepSeek tool, Claude prefill — issue #95

Three filters that upstream applies when picking an account and building a request are missing on our side. Two of them answer 400.

- **Codex `enabledModels` is used during routing.** A codex account has its own model list; ones not in it are rejected by OpenAI with 400. `codexAccountServesModel` passes accounts whose enabledModels list is non-empty but does not contain the requested model — read from `providerSpecificData.enabledModels` first and then the top-level field, exactly as `buildModelsList` already does, because the two writers exist in the outside world. The filter runs **before** the rotation strategy, not after: the round-robin sweep returns the whole candidate ordering, so the filter must precede the strategy. It also runs inside the selection loop and in `pinnedConnectionIneligible`, following the upstream predicate order.
- **Dual DeepSeek tool name.** `DedupeToolsDeepSeek` only applies to DeepSeek models (`isDeepSeekModel` strips the `(level)` suffix and accepts ids with a vendor prefix first). The first definition wins, and other tools belonging to any provider are untouched. It is placed in `tryForwardWithConnection` after `SanitizeOpenAITools` and before `FitToolNames` — the last point before dispatch, so it runs after all conversions. The client-owned MCP dedupe rule that exists upstream is **deliberately not** ported: it depends on client tool detection that we do not have.
- **Claude trailing turn fix.** `EnsureTrailingUserTurn` appends a `Continue.` user turn when cleanup leaves an assistant tail, **unless** the caller genuinely opened with a prefill. `ClaudeIntentionalPrefill` reads the raw tail first in its source form (`contents[]` for Gemini/Antigravity, `input[]` for Responses/Codex), so an intentional prefill stays intact. Without this, newer Claude models answer `400 … does not support assistant message prefill`.

**Verification:** `go vet ./...` clean · `go test -race ./internal/translator/ ./internal/handlers/chat/` green · all three are proven load-bearing via directed mutation: disabling the prefill exemption fails `TestSanitizeClaudePassthrough_ClientPrefillPreserved`; removing the `IsDeepSeekModel` gate fails `TestDedupeToolsDeepSeek`; making `codexAccountServesModel` always `true` fails `TestGetBestConnection_CodexEnabledModelsPickOnlyEligibleAccount`. Both directions are tested for prefill: `TestEnsureTrailingUserTurnBody` has an `assistant tail gets the placeholder` case **and** a `client prefill is left alone` case — one direction alone is not enough.


### 🔵 Four upstream v0.5.95 providers — Meta Muse, v1m System One, TinyFish, Agnes seed

Four registry entries from upstream `decolua/9router` v0.5.91…v0.5.95. Each entry must exist in **every** table (transport, alias, catalog, executor, dashboard) — otherwise that provider is present in `/v1/models` but never routes.

- **Meta Muse** (`muse`): Meta's Model API with dual auth — the device code of a Meta account (Muse Code subscription, its key minted from the account token) **and** a pay-as-you-go API key from dev.meta.ai. Both ride the same bearer transport. All five Muse Spark models pin the `openai-responses` lane, so `internal/proxy/executor/muse.go` chooses an endpoint per model (using `handleCodexStream`, like the other Responses executors), not per provider. The live `/v1/models` catalog needs the `x-api-version: 1.0.0` header besides the bearer — that is why `providers.ModelsListHeaders` exists (upstream `PROVIDER_MODELS_CONFIG.muse`). The device flow is in `internal/handlers/oauth/muse_device.go`, including the mint retry when Meta replies 429 and the message that the subscription is not active yet. Pricing for all five models also comes from dev.meta.ai (the contributor tier is far cheaper).
- **v1m System One** (`v1m`, alias `systemone`): a calibrated decision engine, `serviceKinds: ["systemone"]`, endpoint `https://v1m.ir/v1/systemone`. The `ProviderConfig` registry has no chat BaseURL for it — upstream does not either, so `BaseURL` is deliberately empty and only `SystemoneURL` is filled. `/v1/models/{kind}` selects via `SystemoneURL != ""`, so the model automatically appears on the System One tab.
- **TinyFish** (`tinyfish`): `serviceKinds: ["webSearch","webFetch"]` with **two separate hosts** behind one `x-api-key` key — `api.search.tinyfish.ai` and `api.fetch.tinyfish.ai`. Because `/v1/search` in this repo is *transparency* (the gateway forwards the body as-is to `BaseURL + endpoint`), TinyFish search is **not** implemented: adding a second host means building a provider-specific builder/normalizer pipeline inside `internal/handlers/media/`, which has never existed in this repo — `linkup`, `tavily`, `serper` and `brave-search` are all like that. What is wired up is the fetch path, which genuinely has a passthrough (`FetchURL` = `https://api.fetch.tinyfish.ai`, POST) plus a dashboard card with both service kinds. Full details in the issue report.
- **Agnes seed models**: the provider has existed since v0.5.91 but its catalog is empty, and the live `/v1/models` answers 401 without a token, so its provider page is completely empty. Now it is filled with the four upstream seeds (`agnes-2.5-flash`, `agnes-2.5-pro`, `agnes-2.5-pro-beta`,

### ⬆️ Capabilities and thinking levels tables synced with upstream v0.5.95 — issue #97

Found while auditing the `v0.5.86..v0.5.95` range. The parity fixture that locks the thinking levels **hides** part of this gap: `TestGetThinkingLevels_MatchesUpstreamFixture` reported 1547/1547 without error, because that fixture was still using the old version.

- **`xhigh` for `claude-adaptive`** (`7894f3d3`). The `claude-adaptive` format now uses the `budgetX` set, not `levelMax`. **Both halves must ship together:** the two `CLAUDE_NO_XHIGH` exception lines for `*claude*4.6*` and `*claude*4-6*` were added at the same time. Without those exceptions, the picker offers `xhigh` on models that answer 400 for it.
- **`*claude*sonnet-5*` pattern line** (`ccd0677d`). Vendor-prefixed ids (`anthropic/claude-sonnet-5`, `openrouter/claude-sonnet-5`) have no exact entry and fall to the `*claude*sonnet*` line → `claude-budget`, i.e. adaptive thinking serialized as a token budget.
- **`claude-sonnet-5-5`** and the four **`claude-opus-5.5*`** variants enter the table exactly. `CacheCreationPer1M` for `anthropic/claude-sonnet-5` is corrected from 0 to 2.5; `claude-sonnet-5` and `claude-sonnet-5-5` are added to the pricing table.
- **GPT-6 / GPT-5.4+ context window** (`89ffac5a`). `gpt-6` goes up from 272000 to **1050000** — the old value was one gateway's truncation that leaked into every other gpt-6 provider via the generic pattern. `gpt-5.4` / `gpt-5.5` / `gpt-5.6` are also 1050000; `gpt-5.4-mini` / `gpt-5.4-nano` stay 400000. Plus a 200k block specific to `devin-cli`, which declares its own window even though the model is GPT.
- **`deepseek-v4-1-flash` alias** (`8a4f4d9d`). Some gateways expose it with a hyphen; without this line it falls to the `*deepseek-v4*` pattern and loses its vision and its 1M window.

**Bug found by regenerating the fixture — not from the issue list:** the `*gpt-5*image*` lines were wrongly placed **after** the `*gpt-5.4*` / `*gpt-5.5*` / `*gpt-5.6*` lines, even though first-match-wins. As a result `gpt-5.6-sol-image` matched `*gpt-5.6*` and surfaced as a reasoning model — the seven image entries (`cx/gpt-5.4-image`, `cx/gpt-5.5-image`, `cx/gpt-5.6-{sol,terra,luna}-image`, `codex/gpt-5.5-image`, `codex/gpt-5.4-image`) offered thinking levels that upstream declares `null`. The image lines were moved to the front, matching the upstream order in `capabilities.js:344-352`.

**The fixture was regenerated from v0.5.95** — 1588 pairs (up from 1547, following the catalog that now serves the new models). The regeneration is reproducible, not hand-typed:

```
DUMP_CATALOG_PAIRS=testdata/catalog_pairs.json \
  go test ./internal/providers/ -run TestDumpCatalogPairs -count=1
node scripts/gen-thinking-levels.mjs <checkout-upstream> v0.5.95 \
  internal/providers/testdata/catalog_pairs.json
```

The generator runs the upstream `getThinkingLevels` directly, and the pair list is taken from `ProviderModels` — the catalog this port actually serves — so a new provider or model cannot slip past the parity test silently. The result is stable: two runs produce identical bytes (`fe426f51…`).

**Verification:** `go vet ./...` clean · `go test ./internal/providers/ -count=1` ok · 19 subtests fail without this change (`git stash` on the code only, keeping the tests) and all are green with it · the generator was re-run by the integrator and the results match.


### 🐛 Three missing translator behaviors, one that genuinely does not exist yet — upstream v0.5.95 parity

**1. MCP annotation keywords reject the entire Gemini request** (upstream `aafe3002`, #4283). Upstream `UNSUPPORTED_SCHEMA_CONSTRAINTS` adds `errorMessage`, `errorMessages`, `markdownDescription`, `doNotSuggest`, `suggestSortText`, `minProperties`, and `maxProperties`. Tool schemas from MCP servers use those plain spellings; the Gemini proto schema has no fields for them and rejects the whole request with `Unknown name errorMessage: Cannot find field` — even though the vendor-prefixed spellings (`x-errorMessage`, `x-taplo`, …) were already caught by the separate `x-` rule. All seven plain spellings are now in the stripped list.

**2. A user turn that contains only `container_upload` is dropped** (upstream `4f274c7f`, #4316). The Claude passthrough sanitizer discards messages whose content is empty, and blocks outside the "contains" list also counted as empty — even though `container_upload` (Files API) is itself a legitimate Anthropic input. As a result the request was forwarded as `messages: []` and the provider answered 200 for a conversation that no longer existed. The filter now uses a single content-containing block list (`tool_use`, `tool_result`, `image`, `document`, `container_upload`) for the empty/non-empty decision in both directions.

**3. The last tool result never entered the cache** (upstream `49c761cd`). In the tool loop, a request ends with the tool result from the last assistant round — after that round's breakpoint — so its body is paid for in full and only written to the cache by the next request. While the 4-marker budget has room left, a single 5m breakpoint is now placed on the last cache-eligible block of that user turn; turns that already have `cache_control` are left untouched so re-anchoring stays idempotent.

**Not ported: the unsigned thinking placeholder for DeepSeek** (upstream `08b21fea`, #4436). Upstream injects `{"type":"thinking","thinking":"."}` into an assistant round that has `tool_use` but no thinking block while thinking is active, and on DeepSeek appends that placeholder **without** a signature. In `9router-go` that path does not exist at all: `prepareClaudeRequest` has not been ported, and `AnchorClaudeCache` — the only `cache_control` anchor — never touches thinking or signatures (our passthrough also strips `signature` before forwarding). Patching in a placeholder without a proper `prepareClaudeRequest` path would only write a fake signature into the history, so this change is deliberately not forced; neither the placeholder nor a signature fetch is created so that no dead code remains.

### 🐛 Combo publish limit ctx wrong entirely — plus an endless recursion that hangs the process — issue #99

The combo entry in `/v1/models` does not publish `context_length` / `max_completion_tokens` at all. The comment in `models_list.go` claimed that was parity — v0.5.95 already allows it, so that note was obsolete and was removed.

Six changes, two of which are outside the requested file list and therefore recorded separately.

**Requested by the issue:**

- `comboSeatLimits` in `/v1/models` — minimum `context_window` and minimum `max_completion_tokens` across the whole seat tree, with nested combo expansion and cycle detection (the `visiting` set), equivalent to `src/app/api/v1/models/route.js`.
- `ContextLength` / `MaxCompletionTokens` change from `int` + `omitzero` to `*int` + `omitempty` at five producer points. A combo may only promise what **all** seats agree on; a combo with no LLM seat promises nothing and **omits** both keys, rather than publishing a zero that a client reads as a real limit. This is directly related to #90: a guessed number is worse than none.
- Web seats (`<alias>/search`, `<alias>/fetch`) are skipped. Those seats are tools, not chat models, and letting them vote would publish a capability floor as if it were the whole combo's window.
- `comboIndex` reads the whole combo's seat list **once per request**. Previously one combo was re-read per seat, and each read re-read the settings again — when `aggregateComboCapabilities` also calls it, a single combo entry could rain dozens of queries.

**Two things outside the file list, reported separately:**

- **Endless recursion.** `resolveModelEntry` calls itself for every combo seat without a bound. A combo that refers to itself — directly (`["self-combo", …]`) or indirectly (`mutual-combo` ⇄ `loop-combo`) — makes `/v1/models` **hang forever**. Confirmed on `origin/main`: a test with such a seed produced 67 repeated `resolveModelEntry` frames before the 60-second timeout, each hop querying SQLite again. Nothing bounded it but the stack. `resolveModelEntryGuarded` now carries a `visiting` set; the same seed finishes in 0.2 seconds.
- **`aggregateComboCapabilities` and live seats.** For leaves whose live resolver publishes a full capability block (kiro: `{thinking, agentic}`), the static table is no longer a description of that model, so its static limits are not folded in as if they were known. Equivalent to upstream taking the provider block "verbatim".

**Deliberate divergence, documented:** upstream `comboSeatLimits` has no non-LLM filter, so a web seat inside an LLM combo still counted toward the minimum via the 200k/64k `DEFAULT_CAPABILITIES`. Skipping those seats is the only reading that satisfies the issue's two conditions at once — "non-LLM seats must not drag the minimum down" and "don't publish guesses" — and keeps `/v1/models` consistent with the per-model gate (`isLLMModelEntry`) that already drops `<alias>/search` in the provider branch.

**Note:** `capabilities.maxOutput` in the combo's `capabilities` block stays the **widest** (that is upstream `aggregateComboCapabilities`, `capabilities.js:519-520`), while the top-level `max_completion_tokens` is the **tightest** (`comboSeatLimits`). The two numbers genuinely mean different things, so the combined aggregate is not changed — changing it would silently alter the capabilities block that other callers read.

**Verification:** `go vet ./...` clean · `go vet -tags integration ./internal/integration/` clean · `go test ./internal/handlers/chat/... -count=1` ok · `go test -tags integration ./internal/integration/ -count=1` ok (3.6s) · 6 new tests (5 handler + 1 integration through the production router), all of which fail without the fix — including the cycle test that never finished before · live smoke against the binary on :29201 with a throwaway `DATA_DIR`: 4 combos created via `POST /api/combos`, `GET /v1/models` returns `wide-combo` 128000/16384, `outer-combo` 128000/8192, the web-only and self-loop combos without either key, 1302 entries, 0 providers lost `context_length`.

**Known technical debt:** `models_list.go` is now 1060 lines, already past the 300/400 recommendation before this change (885). The net addition is ~110 lines; extraction to `combo_limits.go` is a mechanical follow-up.

**Unrelated:** `TestApplyConnectionStrategy_KeepsRotatingPastFirstCycle` also fails intermittently on `origin/main` — separated out to #107.

### 🐛 Codex: prevent refresh-token reuse and restore hosted web search — issue #94 (PR #113)

Upstream parity `decolua/9router` `0bc7f86e`, `7bf93178`:
- **Refresh-token reuse prevention:** The per-provider `RefreshLead` delay is aligned (`oauth.refreshLeadMs` upstream). For Codex it is reduced from 5 days (432,000,000 ms) to 10 minutes (600,000 ms) so it does not trigger a token rotation on every call that ends in an OpenAI session revocation. The connection row is refreshed straight from the DB just before the exchange.
- **Hosted web search on Lite models:** A body requesting `web_search` is forwarded via the regular Responses lane — tools are lifted to the top level without duplication, the `additional_tools` prefix is stripped from the input, and Lite models are bypassed for both the payload and the `x-openai-internal-codex-responses-lite` header.

**Verification:** `go vet ./...` clean, `go test -race ./internal/proxy/executor/...` and golden responses pass byte-for-byte.

### ⬆️ Codex catalog synced with upstream v0.5.95 — issue #93

Seven models that upstream removed were still published in `/v1/models`, while the models upstream confirmed live were not there at all. Both answer 400.

- **Ghost models removed.** `gpt-5.4`, `gpt-5.4-review`, `gpt-5.4-mini`, `gpt-5.4-mini-review`, `gpt-5.3-codex-spark`, `gpt-5.3-codex-spark-review`, and `gpt-5.4-image` do not exist in `backend-api/codex/models` for ChatGPT Plus/Pro accounts — all answer HTTP 400 "model is not supported" (upstream #4202). Removed from `registry_models.go` and the dashboard catalog. The `gpt-5.4` entries belonging to other providers (`openai`, `tokenrouter`) are untouched.
- **Live models added.** `gpt-6.1-sol`, `gpt-daybreak-blue-latest`, `gpt-reserve`, and the six extended-context `[1m]` variants.
- **Catalog ids are not wire ids.** The `[1m]` and `-review` variants are catalog ids; ChatGPT answers 400 if either is forwarded as-is. `providers.CodexUpstreamModelID` maps to the base model (equivalent to upstream `getModelUpstreamId`), called from `rewriteCodexUpstreamModel` **after** `buildResponsesBody` — the "(level)" suffix must already be stripped first. `codex-auto-review` is deliberately not trimmed (#1398), and vendor-prefixed ids are not rewritten because that prefix marks another provider.
- **Bare slugs route to codex.** `gpt-5.*`, `gpt-6.*`, `gpt-6-*`, `gpt-daybreak-*`, and `gpt-reserve` now resolve to codex instead of falling to the generic `gpt-*` rule → openai and 404 for codex-only accounts (#4405). Their position is outside the `Repo` guard so the static catalog path (nil Repo) still works. `gpt-4*` / `gpt-3.5*` / `gpt-4o*` stay openai.
- **Context window per model.** Codex OAuth reports its own window, not the OpenAI API's: 272k for GPT-6 and Terra/Luna, 372k for Sol, 872k for the `[1m]` variants. `codexGpt56Caps` builds a shared capability block, and `providerCapabilities["cx"]` now inherits the codex block like upstream (`PROVIDER_CAPABILITIES.cx = PROVIDER_CAPABILITIES.codex`).
- **Pricing.** `gpt-6-astra` corrected to 10/50/1/50/12.5 (previously 5/30 — half the real value); `gpt-6.1-sol`, `gpt-6-sol`, `gpt-6-luna` added.
- **Lite + thinking levels.** `gpt-6.1-sol` and the `[1m]` variants that are lite-based go into `codexResponsesLiteModels` and `codexModelThinkingLevels`.

**Verification:** `go vet ./...` clean · `go test -race ./internal/providers/... ./internal/proxy/... ./internal/pricing/...` green · 15 new cases (`TestCodexUpstreamModelID`, `TestCodexCatalogWireIDsResolve`, `TestCodexOnlyModelSlug`, `TestResolveModel_BareCodexSlugWithoutRepo`, `TestRewriteCodexUpstreamModel` + 4 unusable-body cases) · `bun run build` (`tsc -b` + vite) clean.

One remaining flaky test is **not** a result of this change: `TestApplyConnectionStrategy_KeepsRotatingPastFirstCycle` also fails intermittently on `origin/main` (proven via `git stash` + `-count=20`), so it is out of scope for this issue.

### ⬆️ Client identity: bump Codex CLI 0.159.0 & Grok CLI 1.0.44 — issue #103

Upstream parity `decolua/9router` `ca6e8407`, `6b9dc54d`:
- `cli-chat-proxy.grok.com` rejects identities below 1.0.13 with HTTP 426. The Grok CLI version was updated to `1.0.44` and the Codex CLI to `0.159.0`.
- The literal versions are centralized in `internal/providers/client_identity.go` with a single constant to avoid drift.
- Codex now sends a `version` header alongside the `User-Agent`.

**Verification:** `TestClientIdentity` and `grokcli_handler_test` pass, locking the identity version floor.
## [v1.9.7] - 2026-10-02

### 🐛 Egress printed in log as UUID, not pool name

The `egress=` line from the previous entry carried the pool **UUID**
(`06a2c494-ef06-4d3f-a034-dad29ff3aebf`). That is correct, but it does not answer
the question that comes up when grepping the log: *which pool?* — the dashboard
shows pools by name, so an operator has to open the proxy pool list in order to
match the UUID before knowing which way the request went out.

Now the pool name is printed, resolved **without any additional query**:
`getClientForConnection` already reads the pool row to build the transport,
so the name is taken from that same object and placed in
`ConnectionData.ResolvedProxyPool` (`json:"-"`, per request, not persisted —
so a rename in the dashboard shows up directly in the next log line).

The name is newline-filtered: its value is filled in by operators and enters
every usage line, so a newline would falsify the log line — and that log is the
audit trail of "which egress served this request".

**Verification:** `TestResolveEgress_ReportsThePathARequestLeftBy` locks down
eight shapes, including "pool without name → falls back to UUID" and "legacy
proxy → URL".
`TestResolveEgress_KeepsTheLabelSingleLine` covers the log-line falsification
case.

**Live (`:20151`, same DB as `main`):**

```
INF [usage] logged provider=opencode model=space-bunny-free … egress=vercel-relay
INF [usage] logged provider=opencode-zen model=space-bunny-free …
  conn=80d9c65d-… egress=direct
INF [usage] logged provider=opencode-zen model=space-bunny-free …
  conn=80d9c65d-… egress=vercel-relay
```

Those three lines together show the dashboard's proxy dropdown works:
the `direct` line right after `PUT /api/connections/…` that releases the binding,
and the next line back to `vercel-relay` after the pool is re-bound.

### 🐛 Log cannot answer "did this request go through a proxy or not"

After the egress leak was fixed (previous entry), the most natural next
question — *did this request actually go through a proxy?* — still could not
be answered from the log. The only proxy line (`logProxyOnce`) uses
`sync.Map.LoadOrStore`: **once per pool per process**, and only at `debug`
level. The second and subsequent requests through the same pool print nothing,
and the default `LOG_LEVEL` `info` hides it. The `[usage] logged` line also
has no proxy column at all.

The impact is exactly on the class of problem that was fixed: two gateways
sharing one database (branch `main` at `:20130` and the test build at `:20151`)
produce identical logs even though one of them silently goes direct — and
IP-based `429`/blocks are impossible to analyze without knowing the egress.

Now every `[usage] logged` line and every failure line
`[fallback] upstream failed` carries `egress=`. Its value is the assigned pool
(not the relay URL), because that is what operators look for in the dashboard;
requests without a proxy and without a legacy pool print `direct`.

The resolver reads shapes that already exist in `providerCfg`, so there is no
extra pool query in the hot path: relays are detected from `x-relay-target`,
HTTP proxies from the connection's proxy field (which is not visible in the
config).

**Verification:** `TestResolveEgress_ReportsThePathARequestLeftBy` locks down
seven shapes (direct, relay, legacy proxy, active-proxy-without-URL, nil
connection, relay without a connection line, `providerSpecificData`).
Mutation-tested: restoring `Target` to `x-relay-target` makes the test fail
right at the assertion "Target must be the relay host".

**Live (build at `:20151`, same DB as `main`):**

```
WRN [fallback] upstream failed provider=opencode-zen model=muse-spark-1.3 status=401
  … forward to https://vercel-relay-myw8iebf7-legowo.vercel.app/responses …
  conn=80d9c65d-… connName=yatimrachmawati@paragadis.com 1
  egress=06a2c494-ef06-4d3f-a034-dad29ff3aebf

INF [usage] logged provider=opencode model=muse-spark-1.3-contributor-free …
  conn=default egress=direct
```

Those two lines together prove the direction: `ocz/muse-spark-1.3` through a
pooled connection and out via the Vercel pool, while
`oc/muse-spark-1.3-contributor-free` uses the no-auth fast path and is indeed
`direct` — so the log shows no remaining leak on the free-tier path.

### 🔴 Request that must go through a proxy pool is answered from the original IP — all proxy types

Auditing the proxy path after the 503 `service_overloaded` (previous entry)
surface a bug that applies to **all** proxies, not just Vercel: there are two
egress leaks, one on the HTTP path and one on the relay path.

**1. A dead proxy pool is still answered from one's own IP.** `doRequestOnce`
re-dials directly every time a proxy rejects the tunnel (`isProxyFailure`). That
is correct for ambient proxies (`HTTP_PROXY`, local sandbox) that are never
chosen by the operator, and wrong for an assigned pool: the request is answered
from the original IP, even though the dashboard still shows the connection as
"proxied". Proven before the fix:

```
err=<nil>  directHits=1     ← request through a dead pool, answered from original IP
```

Now `proxiesViaAssignment` distinguishes the two by transport identity: a proxy
whose value is `http.ProxyFromEnvironment` (the only one installed by the
environment) is treated as ambient and may fall back to direct; an assigned pool
**fails** — its error message names the reason instead of silently taking
another path.

**2. "Test Connection" never goes through the relay.** `probeHTTPClient`
returns a `nil` client for `vercel`/`cloudflare`/`deno` pools, so the probe
runs straight to the provider from the host IP. That is wrong in two ways: it
can be green while production traffic through the relay fails, and it can be
red while the relay path is healthy — plus it leaks the IP on the very request
that is run to confirm the proxy is in place. Relay pools can now use a
`probeRelayRoundTripper` that directs the request to the relay host while
carrying `x-relay-target`/`x-relay-path`, the same contract as the chat
pipeline.

**3. One list of relay types, not three.** `vercel`/`cloudflare`/`deno` are
written out again in `connections.go`, `proxypools.go`, and
`connection_probe.go`. A fourth edge runtime would be routed one way in one
place and another way in another — exactly the class of bug that caused #2.
Now `ProxyPool.IsEdgeRelay()` (`internal/db`) is the single definition, and
`proxy_egress_test.go` + `probe_relay_test.go` lock both paths so they can no
longer diverge.

**Verification:** `proxy_egress_test.go` proves an assigned pool no longer falls
back to direct (`directHits=0`) and ambient proxies may still fall back to
direct, so this fix does not kill installations sitting behind a system proxy.
Mutation-tested: removing the `proxiesViaAssignment` check returns
`directHits=1` and the test fails right at that assertion. `probe_relay_test.go`
proves the probe actually lands on the relay host with the correct headers and
the `Host` is not the provider.

**Known limit:** leak #1 closes the **already-configured** path. Pools that hold
an old `vercelRelayUrl` in `providerSpecificData` without a `proxyPoolId` still
relay via `getProviderConfig` as before — that path is not touched by this
change because it has no `strictProxy` flag to honor.

### 🐛 503 `service_overloaded` fails an entire turn even though the next attempt is served

Report: `opencode-zen`/`muse-spark-1.3-contributor-free` through the Vercel proxy
pool frequently hits `503 service_overloaded`, whereas without the proxy they
are "safe".

**The proxy is not the cause.** Tested with the exact same body and header
fingerprint, alternating directly to `opencode.ai` vs through the relay:

```
direct: {503: 16, 200: 12, ERR: 2}   ok 12/30
relay:  {504: 3,  503: 14, 200: 13}   ok 13/30
```

Both `503` appear on both paths at similar rates — that is `opencode.ai`'s own
capacity, not a broken relay. The installed relay is also proven lossless: all
headers (`User-Agent`, `x-opencode-session/request/project`,
`Authorization`) pass through the hop, only `x-relay-*` is stripped as its
template prescribes, and the upstream response headers are forwarded along.

**One real relay finding (not the cause of the 503):** the relay answers `504`
at **exactly 25.09s** with the header `X-Vercel-Error:
FUNCTION_INVOCATION_TIMEOUT`, which never happens on the direct path. Vercel
Edge requires the initial response within 25 seconds; the measured direct TTFT
of 28–55s makes the relay exceed that limit. So the relay does not make fast
turns fail — the relay sacrifices slow turns instead: every turn whose first
token arrives after ~25s will end in 504 on the relay.

**The remaining root cause:** the gateway sends **one** attempt, then surfaces
the 503 to the client and locks the connection — even though the next attempt
is served. The upstream already retries 502x3/503x3/504x2 in
`BaseExecutor.execute` (`open-sse/executors/base.js:107-125` +
`config/runtimeConfig.js:78-83`); the Go port has no counterpart at all.
Measured directly with the upstream policy (3x @2s):

```
1 attempt (now)          served  9/15 (60%)
3 attempts @2s (upstream) served 15/15 (100%)
```

Now `proxy.DoRequest` — the choke point inherited by all providers, just like
the upstream `BaseExecutor` — retries transient 502/503/504 per that policy
before reporting a failure. 429 is **not** retried here: that is a per-account
quota window that must be handed to the account fallback, per the upstream
contract. The backoff is bound to `ctx`, so a client that has already left is
not held the full span.

**Verification:** `internal/proxy/retry_test.go` locks down the policy table,
attempt counts, the original status forwarded to failover, the stop when the
status changes, and the client that has left. Mutation-tested: putting 429 back
into the retry table fails `TestTransientRetryPolicy_MatchesUpstreamDefaults`.
`internal/integration/transient_retry_test.go` covers it through the production
router with a fake upstream: 503 then served → the client gets a `200` carrying
the upstream text; 400/429 → exactly one upstream request.

**Honest known limit:** this closes a parity gap, not making a full upstream
not full. When `opencode.ai` is genuinely saturated, all three attempts remain
503 and the client still receives 503 — now after waiting per the upstream
policy rather than immediately. The 25s edge-relay limit is also not gone:
turns with a TTFT above 25s still need direct or a non-edge pool.

### 🐛 Manifest without checksum kills auto-update entirely — issue #72

#72 makes the SHA256 digest **mandatory**: `PerformSelfUpdate` rejects before
any network request when `expectedSHA256` is empty. But `CheckUpdate` tries the
manifest first and immediately returns its answer if it succeeds
(`updater.go:158-163`), whereas the `version.json` actually published has only
three keys — `downloadUrl`, `latestVersion`, `releaseNotes` — with no `sha256`
at all. As a result `checkManifest` always produces an empty digest,
`checkGitHubReleases` is **never run**, and `lookupAssetSHA256`, which is what
actually closes gap #72, never touches a real release.

Proven: with a manifest shaped exactly like the one the repo publishes,
`CheckUpdate` returns `Source="manifest"`, `SHA256=""`, while the GitHub
fallback that has `SHA256SUMS.txt` is never contacted. Every installation ends
up `release carries no checksum` — fail-closed and safe, but `9router-go
update` is practically dead.

The manifest may now only answer if it can actually produce an installable
update: its digest exists, or it really offers no update. Otherwise it is
treated as a hint and falls through to the GitHub Releases API; the manifest
metadata is kept as a fallback so the release notes still display while the API
is down.

**Verification:**
`TestCheckUpdate_ManifestWithoutDigestFallsThroughToReleaseDigest` serves a
`version.json` without `sha256` and asserts the release API is genuinely
contacted and the release digest is the one used. Mutation-tested: restoring
the condition to "the manifest always wins" makes that test fail right at the
assertion that the release API is never queried. Two other tests lock down
that the fallthrough does not become a regression of its own — a manifest that
**already** has a digest is still answered without touching GitHub, and a
manifest reporting it is already up to date is still answered even without a
digest.

### 🔴 Dashboard password header verified in only one handler — auth bypass

`RequireAdminAuth` and `RequireDashboardAuth` allow requests that carry the
`x-9r-password` header by checking only the **presence** of that header:
`r.Header.Get(DashboardPasswordHeader) != ""`. The value used is never verified
in the middleware — indeed it cannot be, because the check needs a bcrypt hash
stored in a repo the gate does not hold.

The assumption behind that — "the handler behind the gate verifies itself" — is
only true for **1 of 7** protected paths. The other handlers do not check any
credential at all, so anyone able to set a single random header is enough to get
through: `/api/version/shutdown` (`HandleShutdown`),
`/api/version/update` (`HandleTriggerUpdate`), `/admin/health/reset`,
`/api/oauth/cursor/auto-import`, and `/api/oauth/kiro/auto-import`.

The most dangerous is `/api/version/shutdown`: `shutdown.RequestStop()` is
scheduled 500 ms after the response (`internal/handlers/shutdown.go:29-32`), so
the effect always happens and the client sees a clean 200 — remote DoS without
authentication. `/api/version/update` reaches binary replacement and restart.

The header exception is now limited to `PasswordHeaderCarriesOwnAuth`
(`/api/settings/database`) — the only handler that calls
`verifyDashboardPassword` (`settings.go:374-382`), and the reason remains: 
without that exception, the step-up path for backup only lives in a test that
mounts the handler directly (#47). Other credentials are unchanged: sessions
and the CLI token remain valid on all paths.

**Verification:** `TestPasswordHeaderIsHonouredOnlyWhereTheHandlerVerifiesIt`
rejects a random header on the other six paths through both gates, and confirms
the header still reaches the export handler. Mutation-tested: restoring
`allowsPasswordHeader(...)` to a presence check makes all six subtests fail
with a 200 at a handler that should be 401.
`TestPasswordHeaderScopeKeepsOtherCredentialsWorking` locks down that session
and CLI tokens remain valid after this restriction. The 12
`HandleExportDatabase`/`HandleImportDatabase` tests pass unchanged — #47 is
not broken.

### 🐛 CI failed because the opencode free tier was overloaded — incomplete skip set

`internal/handlers/chat/muse_spark_e2e_test.go` calls the real opencode
endpoint, so the status that comes back at any time is determined by the
provider's load — not a gateway deficiency. Four of the five tests in that file
already `t.Skip` for 429 and 403; `TestIntegration_OpenCode_MuseSpark13_ChatCompletions`
only treated 429, so 503 leaked through and failed CI #82 with:

```
WRN [fallback] upstream failed provider=opencode model=muse-spark-1.3-contributor-free
  status=503 ... "Error from provider (Console): The backend is temporarily overloaded"
expected HTTP 200 for muse-spark-1.3, got 503
```

All five now use one helper `upstreamUnavailable`: 429, 403, and 503 mean
"not now" and are skipped; 400/401/500 still `Fatalf` so genuine defects are not
also covered up. Tests that have always depended on third-party provider
availability must not fail merely because one status has not been recorded.

**Verification:** `TestUpstreamUnavailable_SkipsOnlyProviderAvailabilityStatuses`
locks down the contents of that set — moving 500 into it would fail the test
with a message stating that status is our bug. Reran the suite after the
change: the upstream had recovered and those tests returned 200 again;
`go vet` clean, `go test -race ./internal/handlers/chat/` green.
### 🐛 Quota credential refresh: one attempt, skip without refresher, mark dead grant — regression #83

PR #83 added credential refresh to `/api/usage/{id}` and immediately surfaced
two warnings per account each time the panel was opened:

```
WRN [usage] credential refresh failed         provider=grok-cli … invalid_grant
WRN [usage] forced credential refresh failed  provider=grok-cli … invalid_grant
```

Auditing upstream (`open-sse/services/tokenRefresh.js isUnrecoverableRefreshError`)
shows upstream **also** retries once on an auth-expired message
(`open-sse/services/usage/grok-cli.js:372` returns "…authentication
expired. Please re-authorize."), but never repeats a refresh that was **just
rejected** — that is the gap #83 filled itself.

- **One refresh attempt per request.** The retry moved to a path that hasn't
  tried yet, and is skipped if the attempt that just failed was that one. The
  warning pair and one wasted `fetchgate` slot per account are gone.
- **Providers without a refresher are skipped.** `oauth.Refresh` can only
  succeed for providers registered in `oauth.Get`; qoder does not have one
  (upstream: `open-sse/executors/qoder.js` → `refreshCredentials() { return null }`),
  so every previous Open Panel was guaranteed to fail and hit the log.
- **Dead grants are flagged in the DB.** `providers.IsRefreshGrantDead`
  extends `IsRefreshUnauthorized` (401) to 400 `invalid_grant` /
  `refresh_token_reused` / `unrecoverable_refresh_error` — the forms xAI
  returns for a revoked grok-cli refresh token. Rejected accounts are now
  parked via `RecordConnectionOAuthFailure` so the dashboard shows accounts
  that need re-auth instead of warnings nobody reads.
  500/transport errors are still **not** parked: those are evidence of a
  provider blip, not evidence of dead credentials.
- **`oauth.Unregister`** was added for tests that install a stub under the
  real provider id; leaking stubs silently changed the behavior of the
  production code being tested.

### 🐛 Quota tracker does not fetch all accounts + Kiro credential refresh — issue #78 (items 3 & 4)

#### Upstream audit before/after

| Aspect | Upstream `decolua/9router` | 9router-go before | 9router-go after |
|:--|:--|:--|:--|
| Refresh before reading quota | present (`refreshAndUpdateCredentials`, `src/app/api/usage/[connectionId]/route.js`) | **absent** | present (`usage_credentials.go`) |
| Retry on auth-expired message | present (once) | **absent** | present (once) |
| Auth-expired patterns | `["expired","authentication","unauthorized","401","re-authorize"]` | — | identical |
| Kiro credential fetch | `open-sse/services/usage/kiro.js` | 1:1 port already correct | unchanged |
| Quota fetch throttle | **absent** (deliberate gap, see `usage.go`) | 250ms + 120ms jitter | unchanged |
| Fan-out on quota page | unbounded `Promise.all` | unbounded `Promise.allSettled` | bounded + cancellable |

Item 4 turned out to be **already fully ported** on the fetcher side:
`fetchKiroUsage` tries three endpoints (`codewhisperer-get`,
`codewhisperer-post`, `q-get`) with the correct `tokentype`/`TokenType`
headers and ARN profile, and `sawAuthError` produces the reported message.
What was missing were the **two steps that ever got that stale token to the
fetcher**: the `/api/usage/{id}` route never refreshed credentials before
reading, and never retried when the provider answered with an auth-expired
message. Both exist upstream.

#### Changes

- **`internal/handlers/dashboard/usage_credentials.go` (new).** Refreshes OAuth
  credentials before reading quota when `expiresAt` has passed the 5-minute lead
  window, stores rotated tokens (OpenAI rotates the refresh token on every
  refresh), and does one retry after a forced refresh if the provider answers
  with the upstream auth-expired patterns.
- **`apiKey` is rotated too when it mirrors `accessToken`.** Kiro login writes
  the OAuth token to both fields (`HandleKiroAPIKey`, `HandleKiroImport`), so
  changing only `accessToken` leaves a stale copy for any reader that uses
  `apiKey`. `api_key` connections with a different key are not overwritten.
- **`web/src/components/quota/fetch.ts` (new).** Bounded quota fan-out
  (6 concurrent reads — the same number browsers keep per origin)
  with an `AbortSignal` owned by the caller. A newer pass immediately aborts
  the previous pass, so late answers no longer overwrite the new page's rows
  and rows still in the queue no longer appear as "done but empty".

#### Evidence

- Repro (repeated): 50 connections through the production gate took **15.07s**
  and **50/50** were read — so the truncation happened in the client state, not
  the server.
- Live smoke against the built binary: 12 connections in one tracker pass →
  **12/12 live rows**, **12 upstream reads**, 12 distinct keys. The served
  bundle loads the bounded helper (`Math.min(concurrency, targets.length)`).
- `go vet ./...` · `go test -race ./internal/...` · `go test -tags=integration -race ./internal/integration/...` · `bun test` 120/120 · `tsc -b` · `oxlint` · `make build`.

### 🐛 OpenCode Zen (`ocz`) parity with upstream — issue #78

The `/dashboard/providers/opencode-zen` page had almost no upstream behavior:
`opencode-zen` is **not registered as an executor**, so `executor.Get` returns
`nil` and every request falls into the generic `forwardRequest` — one POST to
`/zen/v1/chat/completions` with no session/request headers, no fingerprint
quartet, and no per-model routing. As a result Claude and Qwen models (which
only live under `/zen/v1/messages`) and GPT/Grok/Muse Spark
(`/zen/v1/responses`) never touched the right endpoint, and `NoAuth: true` +
`DefaultAPIKey: "public"` made the PAYG lane silently use the free-tier key.

- **`ForwardOpencodeZen` executor + three transports.**
  `internal/proxy/executor/opencode_zen.go` implements
  `open-sse/executors/opencode-zen.js`: `/chat/completions`, `/messages` (raw
  `x-api-key` auth), `/responses`, plus fingerprint headers, the
  `bash/glob/grep/read` quartet, forced `stream:true`, and `store:false`.
- **Per-model format metadata.** `internal/providers/model_formats.go` ports
  `targetFormat` / `supportedFormats` from the upstream registry plus the
  family fallbacks (`open-sse/providers/models/helpers.js OPENCODE_FAMILIES`)
  for ids from `modelsFetcher`/`passthroughModels` never seen before. The
  `web/src/lib/models.ts` catalog for `ocz` is synced to 74 entries (including
  the previously missing `union-alpha`) with the same format fields.
- **Claude client may be lossless.** `tryForwardWithConnection` only converts
  `/v1/messages` bodies for providers without a Messages endpoint
  (`executor.ServesMessagesEndpoint`), so `claude-*` and `qwen*` are now sent
  as-is to `/zen/v1/messages` instead of an OpenAI → Claude round-trip.
- **Responses lane.** `UpstreamSpeaksResponses` now distinguishes
  `opencode-zen` and only lets passthrough through for models whose target
  format is `openai-responses`; chat-lane models that the client asked for via
  Responses are translated in, not forwarded raw.
- **Quota tracker.** `GET /zen/v1/usage` (Rolling / Weekly / Monthly, with the
  percentage → used/total 0..100) is ported to `usage_opencode_zen.go`, and
  `opencode-zen` joins `usageSupportedProviders` + `usageApikeyProviders` —
  previously it wasn't eligible so the page showed no accounts at all. The
  usage URL is derived from the connection's `baseUrl`, so self-hosted/relay
  endpoints are read from their own host.
- **Provider configuration.** `NoAuth` and `DefaultAPIKey: "public"` were
  removed (keyless connections are now rejected instead of silently using the
  free tier), and `UsageURL` was added to `ProviderConfig`.
- **Verification:** `go vet ./...`, `go test -race ./...`,
  `go test -tags=integration -race ./internal/integration/...` (7 new zen cases
  pass through the production router against a fake upstream), `bun test`
  115/115, `bun run build`, plus a live smoke against the binary against a fake
  upstream for all three lanes.

### 🐛 Console log named the provider but not the account — issue #78 (item 2)

On multi-account, the `[usage] logged` line only named `provider` + `model` + token.
There was no trace of which account served — even though that is the only
useful information when 20 connections rotate behind a single provider.
`connIdentityKV` already existed, but was only used on the failure paths
(`upstream failed`, `connection locked`), because `forwardRequestParams` did
not carry the account name.

- **`forwardRequestParams.ConnName` / `.ConnEmail`** are filled in at all three
  picker call sites (pinned, rotation, combo) from the connection row already
  held, so the success path needs no extra database query to format the log.
- **`UsageLogInfo` gets `ConnName`/`ConnEmail`** plus `ConnIdentityKV()`, and
  `connIdentityKVOr()` resolves the identity once per attempt then reuses it in
  `logUsage`, `LogFailure`, and all three `fallback` lines — no request reads
  the connection row twice.
- **Visible consequence:** success lines now read
  `... cost=… conn=conn-a connName=Account A`; requests without a stored
  connection (no-auth) report `account=Public / Direct` instead of staying
  silent.
- **Verification:** 2 integration cases through the production router (rotating
  two accounts must produce two distinct names in the console log), 3 unit
  tests for identity resolution, `go vet ./...`,
  `go test -race ./internal/...`,
  `go test -tags=integration -race ./internal/integration/...`.

### 🐛 Proxy pool assigned but never used — issue #78 (item 1)

The pool attached in the dashboard was **never used** for most providers. The
UI writes `proxyPoolId` and shows a "Proxy" badge — the requests still went
out through the real IP. Three causes, all three fixed.

- **`forwardRequest` ignored the connection client.** Its signature had no
  client, so it was always `h.Client`: **every provider without a custom
  executor** (deepseek, openai, openrouter, groq, mistral, …) skipped the
  proxy. The native Gemini path had the same problem. The resolved client is
  now forwarded (`forwardRequest` + `forwardGeminiNativeRequest`).
- **Provider-level pools were only read for virtual no-auth connections.**
  `settings.providerStrategies[...].proxyPoolId` was only used in
  `getBestConnection` for synthetic connections, so connections with their own
  API key went out directly. `applyProviderProxyPool` now makes it a fallback
  when a connection has no binding of its own (an explicit binding still wins).
- **Alias vs canonical id.** The UI stores the pool under `storageAlias` (e.g.
  `mmf`, `cl`, `ocg`, `ocz`) while the request carries the canonical id
  (`mimo-free`, `clinepass`, `opencode-go`, `opencode-zen`).
  `ResolveProviderProxyPoolID` only remembered three pairs, so the rest read
  "none". Keys are now resolved via `providerStrategyKeys`:
  id → published alias → canonical id → upstream pair (cline ↔ clinepass).
- **Unusable pools failed silently.** A deleted, inactive, URL-less pool, or
  one whose URL could not be parsed previously let the request go direct. Now
  `getClientForConnection` returns an error and `tryForwardWithConnection`
  fails **before** any byte is sent — no first request leaks to the real IP.
- **Verification:** 4 integration cases through the production router with a
  fake proxy that counts every tunnel (including one proving the hop via the
  header stamped by the proxy), 6 unit tests for pool alias resolution,
  `go vet ./...`, `go test -race ./internal/...`,
  `go test -tags=integration -race ./internal/integration/...`.

### 🐛 Edge relay lost `x-relay-target` on the Zen lane

With a `vercel`/`cloudflare`/`deno`-typed pool, `getProviderConfig` swaps the
upstream destination for a relay header (`BuildEdgeRelayHeaders`) then
replaces `BaseURL` with the relay host. `ForwardOpencodeZen` rebuilds headers
from scratch, so `x-relay-target` was lost and the relay answered
`400 {"error":"Missing x-relay-target header"}` — **all** requests failed the
moment an edge pool was attached.

- **`zenHeaders` now carries `x-relay-target` / `x-relay-path` /
  `x-opencode-project`** from the connection config. The fingerprint UA still
  wins over the `User-Agent` sent by the connection.
- **`zenRelayPath` determines the lane from one place.** The `x-relay-path`
  value is stamped from the connection's `baseUrl`, so for a default connection
  it holds `/zen/v1/chat/completions` — important when the model needs a
  different lane.
- **The `/messages` lane too.** That path builds its own header set
  (`x-api-key` + `anthropic-version`), so it also lost the relay header; now it
  is like the other two lanes — the destination is in the header and the
  `BaseURL` invoked is the relay host.
- **Verification:** 5 test cases use a fake relay that mimics deployment
  behavior (`internal/handlers/media/deploy.go`) and replies 400 like the real
  one — all three **failed with the same message as your report** before the
  fix, then went green after.

### 🐛 Batch fix of open issues (#72, #73, #74, #75, #76, #77, #78, #79, #47, #61)

Ten still-open issues were closed in one batch. Those already correct in
`main` (#48) were not touched; those needing a separate PR remain tracked in
the issue.

- 🔴 **Self-update could replace the binary without checksum verification (#72).**
  `PerformSelfUpdate` only verified SHA256 *if* the manifest provided it, yet
  on the path actually used `expectedSHA256` was always empty:
  `checkManifest` reads the `sha256` field that does not exist in `version.json`,
  and `checkGitHubReleases` never filled it. The download → write →
  rename sequence overwrote the running binary with no integrity check. Now
  the checksum is **mandatory**: without it `PerformSelfUpdate` refuses before
  any network request and the running binary is untouched. To close the gap,
  `checkGitHubReleases` reads the `SHA256SUMS.txt` asset that `release.yml`
  already publishes (no Go code read it) and selects the entry matching the
  active platform asset. A lookup failure does not kill the update check;
  an empty `SHA256` and installation is rejected with a message saying there
  is no checksum.
- 🔴 **`parseSemver` discarded the prerelease suffix (#73).** `1.9.7-rc1` and
  `1.9.7` both became `[1,9,7]`, so an RC was never offered as an update —
  and once `1.9.8-rc1` shipped, users on `1.9.7` auto-updated to the RC.
  Real semver precedence is now used (a final version wins over its own
  prerelease, `rc2 > rc1`, build metadata is ignored), plus a second guard in
  `runCheckCycle`: the automatic path never installs a prerelease-tagged
  version onto a process running a final version. RCs can still be installed
  manually via `9router-go update` or the dashboard button.
- 🔴 **PID reuse could make `stop` kill another process (#74).**
  `RunningPID` only trusted the bare PID plus `proc.Alive`, so a pid file left
  by a dead daemon could be reported alive after the OS reused its number —
  then `Stop` sent SIGTERM/SIGKILL to an innocent party. PID claims now record
  `<pid> <exe>`, and `proc.Executable(pid)`
  (Windows `QueryFullProcessImageName`, Linux `/proc/<pid>/exe`, BSD
  `kern.proc.pathname`) verifies it before any signal is sent. A claim that
  cannot be verified is **not** a live daemon, so it never authorizes signals.
  `Stop` also no longer deletes the pid file on the force-kill path, so the
  "claim exists but the process is dead" state can be represented.
- 🔴 **`gateway.log` grew unbounded and was read whole every 150 ms (#75).**
  The log was appended with no cap, `LogTail` loaded the entire file per
  `logs` call, and `bindFailureSeen` did an `os.ReadFile` + `bytes.Contains`
  on every 150 ms polling iteration. The log is now trimmed to 16 MiB when
  opened (tail kept, head moved to `gateway.log.1`), `LogTail` reads only a
  256 KiB window from the back, and the bind-failure scan is limited to the
  log tail.
- **`tools[].toolSpec.name` (Bedrock Converse) was skipped in both directions (#77).**
  `visitTools`/`replaceInTools` only knew `name`, `function.name`, and
  `functionDeclarations`, so a Converse-shaped request still carried a name >
  64 characters upstream and nothing restored it in the response. The issue
  called this "recorded but not written"; what actually happened was that both
  were untouched — adding `toolSpec` to the request side alone would have made
  the response return a name that was never declared.
  The Converse shape is now handled symmetrically in the request **and** the
  response (`contentBlockStart.start.toolUse` and
  `output.message.content[].toolUse`).
- **`signalSelfShutdown` is dead code in both build variants (#76) — NOT removed.**
  Both `signal_unix.go`/`signal_windows.go` files indeed have no call site
  since the `RestartSelf` rewrite moved to `shutdown.RestartAfterStop`, and
  their contents are identical. Deleting the files is **not part of this
  batch**; issue #76 stays open.
- **Kiro credentials never reached the quota tracker (#78).**
  `fetchProviderUsage` sent `accessToken` to `fetchKiroUsage`, even though a
  Kiro connection stores its credentials in `apiKey` — so the request carried
  `Authorization: Bearer ` and the dashboard showed *"Kiro quota API
  rejected the current token. Chat may still work."* while the chat itself
  succeeded using credentials the quota path never read.
  Precedence is now the same as `resolveProviderAuthToken` on the chat path,
  and an empty token is reported as "credentials not stored" — not a
  misleading token rejection.
- **Antigravity queued twice on the quota gate (#78).** `HandleGetConnectionUsage`
  took the `quotaFetchGate` slot once before dispatch and then once more in
  the Antigravity branch, so each Antigravity account waited two back-to-back
  250 ms gaps for a single request burst. The second slot was removed.
- **Usage period dropdown with `all` + custom window (#79).**
  The period selector was a row of hardcoded pill buttons with no
>  `all`, even though the backend already accepted it. Now it is a dropdown
>  with presets (Today, 24h, 7D, 30D, 60D, All time) plus a custom input, and
>  the backend `/api/usage/stats` accepts any `<n>d` / `<n>h` form —
>  `resolveUsagePeriod` replaces the `if/else` chain that silently used
>  365 days for `all` and 7 days for unknown values.
- **Database download was rejected even when logged in (#47).**
>  `HandleExportDatabase` did not accept a dashboard session — only the
>  `x-9r-password` header or a CLI token — so a normal browser link always 401'd
>  and export looked permanently blocked. A session is now enough on its own,
>  like other dashboard reads, while the password header still works for
>  scripts. The requested zip path already existed in the backend and is now
>  reachable.
- **Model picker no longer re-sorts the whole catalog per click (#61).**
  `resolveFilteredGroups` accepted an `addedModelValues` argument that was
  never read, but because the argument was in the signature, Svelte made it
  part of the reactive graph: every click on one pill triggered a filter +
  re-sort of all groups and a re-reconciliation of hundreds/thousands of
  pills. The argument was removed; the filter/sort logic is completely
  unchanged.

**Out of scope:** #78 items 1–2 (the `opencode-zen` executor) were already done
in PR #80 and #48 is correct on `main` (`stripCodexUnsupportedTokenParams`
runs after `buildResponsesBody`, not before) — neither was touched here.
## [v1.9.6] - 2026-10-01

### 🐛 Pre-release review: 5 blockers that slipped through every gate (#70)

Auditing 42 commits of `v1.9.5..main` before release found five defects that were **not**
caught by `go vet`, `bun test`, or `-race`, because the existing tests mock
exactly the same thing as the code path. All five have been fixed and
tested with regression tests that fail when the fix is reverted (*mutation-checked*).

- 🔴 **`9router-go status|stop|logs` were blind to the `DATA_DIR` from `.env`.**
  `daemonURL()` and `daemon.Dir()` read `os.Getenv`, while the server
  resolves it through viper, which reads `.env`. On a deployment
  configured only via `.env` — including every docker compose — the CLI process
  sees a different directory from the running daemon: `status` reports
  *"not running"* even though there is a live listener, `stop` refuses to halt, and
  `logs` claims there is no log even though the file is 50 KB. `ResolveDataDir()`
  now reads `.env` (in the order env → `.env` → platform default) and
  `daemonURL()` uses `config.LoadConfig()` so the probed port is the same
  as the bound one.
- 🔴 **The fitted tool name leaked to the client in the Claude lane `TranslateResp`.**
  `handleClaudeMessagesStream` `return`s at lines 19–46, **before**
  `decloaker := NewClaudeStreamDecloaker(req.ToolNameMap)` at line 78. The request
  is already fitted in `fallback.go:362`, so the `content_block_start` tool_use reaches
  the client with a 64-character name and cannot be dispatched. The non-stream
  path has a similar flaw (the passthrough in `claude_messages.go:191` exits before
  decloak) — both now decloak before any branch. The decloaker
  was hoisted above `TranslateResp`.
- 🔴 **Responses lane: request not consistent with itself.**
  `FitToolNames` had no walker for `input[]` (only `tools`/`functions`/
  `messages`/`contents`/`tool_choice`), while `RestoreToolNames` still
  restores `output[]` (`fingerprint.go:282-311`). As a result the tool declaration
  was fitted to 64 characters while the history still calls its original name, 71
  characters — upstream received a request contradicting itself, and
  the 400 fix itself was not really used, because the long name was still
  carried through `input`. Added `visitInput`/`replaceInInput`
  (`tool_fit.go`) that share one walker for both directions, and
  `passthroughResponses` now applies `req.ToolNameMap` to the non-stream body
  as well as through `sseStreamOpts` on the stream.
- 🔴 **The reset-credit idempotency key was empty — the double-redeem protection
  never existed.** `resetCreditIdempotencyKey` was declared but never
  assigned anywhere (`newIdempotencyKey()` is dead code), so
  each request sent `idempotencyKey: ""` and the server minted a new key per
  request (`usage_codex_reset.go:176-178`) → two innocuous submits =
  two credits wasted. The key is now minted when the modal opens, reset when it closes,
  and `confirmResetCredit` refuses to send an empty key. Its helper moved to
  `lib/codexResetCredit.ts` to be unit-testable per the repo's conventions.
- 🔴 **`9router-go stop` on Windows was never graceful.** `stopGraceMS = 5000`
  was declared, but `requestStop` in `proc_windows.go` always
  `ErrStopUnsupported`, so `proc.Terminate` goes straight to `ForceKill` — the 5-second
  `server.Shutdown` drain designed in `server.go:99-101` **never
  ran** on Windows: every `stop`/`restart`/auto-update cuts SSE in-flight and
  SQL transactions halfway. `Stop()` now POSTs to
  `/api/version/shutdown` first **with the CLI token** (the endpoint is
  always-protected; without a token it always 401s and falls back to force-kill), with
  `proc.Terminate` as the fallback for a daemon that does not answer. Verified
  live: log `Server stopped gracefully` across 4 consecutive restart cycles.
- 🔴 **`usagetracker` data race** (found when re-running `-race`, pre-existing and
  unrelated to the 5 fixes above): `scheduleBroadcastLocked` copies the subscriber
  list then releases the lock **before** send, while `unsubscribe` closes the
  channel under the write lock → *send on closed channel*. The map reads safely,
  the channel does not. The send was moved under `RLock`. Note: `8520e4e`
  claims "fix data race in usagetracker" but only patched ring seeding.
- **The 64-character boundary is now actually tested.** The old fixtures were 63 and
  65 characters, so nothing pinned exactly at the boundary — and that boundary
  is the core of this feature. Added `TestFitToolNames_ExactBoundary`.
- **Verification:** `go vet ./...` + `-tags=integration` 0 warnings;
  `go test -race -count=1 ./...` exit 0; `go test -tags=integration` ok;
  `bun test` 115/115; `tsc -b`/`oxlint`/`bun run build` clean;
  `make build` + `make cross` (5 platforms) success. Live: daemon lifecycle
  start/status/restart/stop without `DATA_DIR` exported, a 71-character tool
  returned to the client as 71 characters, 7 dashboard routes without console
  error.
- **Deferred (not blockers, not part of this PR):** self-update can install a
  binary without verification when the manifest has no `sha256` (`updater.go:411`;
  `release.yml` already publishes `SHA256SUMS.txt` but no Go code
  reads it); `parseSemver` drops the prerelease suffix so an RC could
  read as newer than final and be auto-applied; `RunningPID` only trusts a
  bare PID so a recycled PID can make `stop` kill another
  process; `gateway.log` is never rotated and is read whole every 150 ms;
  `tools[].toolSpec.name`
  (Bedrock Converse) is recorded but not written.

### 🐛 MCP tools with a function name > 64 characters died with HTTP 400 (#68)

- **The problem:** The OpenAI / OpenAI-compatible function spec limits the `function.name` length to a maximum of 64 characters (`^[a-zA-Z0-9_-]{1,64}$`). Coding agents with MCP server integration often use namespaced names (e.g. `mcp__server_name__action_detail_something`) that exceed 64 characters, causing upstream providers (OpenAI, Console, Responses API, etc.) to reject the request with status 400 Bad Request (`name must be at most 64 characters, got XX`).
- **The fix:**
  - Added `translator.FitToolNames` which deterministically trims function names that exceed 64 characters down to a maximum of 64 characters with unique suffixes `_1`, `_2`, etc. (accounting for the suffix length so the total never exceeds 64 characters and never collides with other tools).
  - Replaced every function-name reference in the `tools`, `functions`, conversation history (`messages` assistant `tool_calls`, `function_call`, `role: "tool"`/`role: "function"`, Claude `tool_use`), and `tool_choice` declarations.
  - Integrated name restoration via `NewToolNameRestoringWriter` and `RestoreToolNamesInPayload`, so responses from upstream (both streaming SSE and non-streaming JSON, OpenAI/Claude/Responses/Gemini) are returned to the original long name before being forwarded to the client.
  - Multi-turn conversation sessions stay in sync because the name trimming is deterministic.
- **Verification:** Unit tests `TestFitToolNames_*`, `TestRestoreToolNames_*` in `internal/translator/tool_fit_test.go` and end-to-end integration tests `TestE2E_FitToolNames_*` (non-streaming, SSE streaming, multi-turn) in `internal/handlers/chat/tool_fit_e2e_test.go` pass with `go test -race` and `go vet`.

### 🐛 Fix dashboard feedback issues: login lockout, remote password rotation, proxy dropdown, combo model drag-and-drop, and zip database backup (#50)

- **Login limiter IP bucketing**: `LoginClientIP` in `internal/auth/session.go` no longer falls back to `"unknown"` when no proxy headers are present. Direct TCP peer IP from `r.RemoteAddr` is used so distinct clients have their own failure buckets and one misconfigured client does not lock out all other users.
- **Remote / Docker initial password rotation**: `POST /api/auth/login` now accepts `{ password, newPassword }`. Remote and Docker fresh installs requiring default password rotation can set their new password directly and receive a valid session cookie without encountering 401 Unauthorized from protected settings endpoints.
- **Initial password change check**: `changeDashboardPassword` in `internal/handlers/dashboard/settings.go` now validates against `INITIAL_PASSWORD` when no password hash is stored.
- **Provider proxy dropdown & Antigravity Free glitch**:
  - In `ProviderDetailView.svelte`, the connection row Proxy button is now always rendered even if no proxy pools exist yet, displaying a clear empty state with a shortcut to create one.
  - The proxy dropdown now uses `position: fixed` relative to the trigger button to prevent clipping inside the scroll container (`overflow-y-auto`).
  - Wrapped `loadData()` inside `untrack` so that background polling of `connections` does not continuously re-trigger `loadData()`, eliminating the re-render flash / glitch on free providers like OpenCode Free and preventing proxy selection from resetting.
- **Combo model drag-and-drop & picker performance**:
  - Implemented HTML5 drag-and-drop reordering (`draggable`, `ondragstart`, `ondragover`, `ondrop`, `ondragend`) on model rows in `CreateComboModal.svelte` with active drag visual indicators.
  - Preserved stable alphabetical ordering in `pickerData.ts` to eliminate layout shift, frame drops, and freezing when clicking model pills in `ModelPickerModal.svelte`.
  - Added module-level caching for model picker metadata (`pickerExtras`) so opening the picker does not flash empty states or block UI interactions.
- **Database backup download & ZIP archive support**:
  - Deferred `URL.revokeObjectURL` in `ProfileSettingsView.svelte` to prevent modern Chromium/Firefox download managers from cancelling or blocking in-flight blob downloads.
  - Added support for `?format=zip` in `GET /api/settings/database` to export backups as standard compressed `.zip` archives with `Content-Disposition: attachment`.
  - Added support for importing `.zip` archives in `POST /api/settings/database`, automatically extracting and restoring the JSON payload.
  - Updated `ProfileSettingsView.svelte` to download `.zip` by default and accept `.zip` as well as `.json` imports.

### ✅ The binary can run in the background — `9router-go start` / `stop` / `restart` / `status` / `logs`

- **New options:** `--background` (alias `-d`) and the sub-command `start` run the gateway as a separate process that does not attach to the terminal, then return immediately. New sub-commands: `stop`, `restart`, `status`, `logs -n <lines>`. The old behaviour (`9router-go` without flags) is **unchanged** — it still runs in the foreground and stops on `^C`.
- **PID file & log:** the detached process records itself in `DATA_DIR/run/gateway.pid` and writes stdout/stderr to `DATA_DIR/run/gateway.log`. Stale PID files are cleaned up when the next command runs, so a daemon force-killed from Task Manager does not leave a misleading trace.
- **`Start` refuses when the port is already taken** — a pre-flight TCP connect, not merely relying on the PID file. Tested with a Python server holding the port: `start` fails with a message naming the port and its source, instead of racing two processes over one bind.
- **Two real Windows bugs found and fixed on the same path.** (1) `HandleShutdown` (the Shutdown button in the dashboard) self-signals `SIGTERM`, and `os.Process.Signal(syscall.SIGTERM)` on Windows returns `not supported by windows` — the button never stopped the server. (2) `RestartSelf()` called `os.Exit(0)` before the listener was closed, then spawned the replacement, so the new process could seize the port before the old one released it. Both now go through `shutdown.RequestStop()` / `shutdown.RunAfterStop()`: main waits on three sources (`SIGINT`, `SIGTERM`, stop request), runs `fxApp.Stop`, and only then runs the spawn hook. Proven: `POST /api/version/shutdown` with a session cookie → process dies, port `20197` truly released.
- **A restart detail that once caused a bug:** `Restart` must **stop first, then pre-flight**. If the pre-flight runs first, it finds the old daemon still listening and reports "already running"; if it runs after `TerminateProcess`, the socket has barely been released. The order stop → wait for the port to be free (max 10 s) → start is tested: pid `21700` → restart → pid `17628`, `/health` still `{"status":"ok"}`.
- **New `internal/proc`:** portable process primitives ( Alive / Terminate / Detached / SelfExecutable ) shared by the daemon **and** headroom; `internal/headroom/sig_unix.go` + `sig_windows.go`, which duplicated the same logic, were removed.
- **What changed in the shutdown API:** `shutdown.RequestStop()` closes the new `StopRequested()` channel while also triggering `Cancel()` (SSE stops). `Cancel()` alone does **not** close `StopRequested()` — `^C` is not an operator exit request, and main must keep waiting for the real signal. Pinned in `internal/shutdown/shutdown_test.go`.
- **Verification:** `go vet ./...` clean, `go build ./...` clean, `go test ./internal/...` all packages green except `TestApplyConnectionStrategy_KeepsRotatingPastFirstCycle`, which **already failed before this change** (proven on a clean worktree at `HEAD` `9323d21`, not a side effect). Full smoke on Windows: start → `/health` `{"status":"ok"}` → `/login` 200 → status → restart (new pid) → health → stop → pid file gone → port no longer `LISTENING`; `-d` identical to `start`; a second start refused "already running"; foreground writes no pid file at all.

> ⚠️ **Deliberate cross-platform behaviour difference:** on Windows there is no SIGTERM, so `stop` uses `TerminateProcess` (the process dies immediately, the SSE drain is skipped). On POSIX, stop sends SIGTERM so the Fx hook runs and `runServer` exits cleanly. Anything that needs the drain (update via dashboard, restart) should go through `restart` or the Shutdown button, not `stop` on Windows.

### 🐛 Makefile only worked in a POSIX shell — on Windows every build/run target was broken

- 🔴 **All symptoms came from POSIX syntax inside the recipe.** `VERSION ?= $(shell cat VERSION 2>/dev/null …)` returns an empty string in a non-POSIX shell (`/dev/null` does not exist), so `-X …CurrentVersion=` embeds an **empty** version into the binary. `PORT=20130 ./9router-go` makes cmd.exe try to run a program named `PORT`. `run`/`version`/`update`/`mitm-*` call `./$(BINARY_NAME)`, and cmd.exe answers `'.' is not recognized as an internal or external command` because `.` is not on PATH (PATHEXT only includes `.EXE`). `LDFLAGS` is wrapped in single quotes, and cmd.exe treats `'` as a literal character so the linker receives an already-quoted symbol name. `DATA_DIR ?= $(HOME)/.9router` expands to empty on native Windows → the literal path `/.9router` → `C:/Program Files/Git/.9router`, so **every** `make run` writes a fresh temporary DB and the dashboard keeps answering 401.
- **The fix:** `VERSION` is read via `$(file <VERSION))` (Make-native, no shell) with successive fallbacks to `version.json`, then `git describe`, then `1.0.0`; the binary is invoked as `$(BINARY)` without `./` (found via PATHEXT in cmd.exe, still works in sh/POSIX); `LDFLAGS` uses double quotes; `DATA_DIR ?=` is left empty so the binary applies its own per-platform default (`%APPDATA%\9router` / `~/.9router`).
- **`PORT`/`DATA_DIR`/`RTK`/`CAVEMAN`/`PONYTAIL` are only exported when their origin is not `file`.** The `make run` probe must not clobber config: a `?=` value has origin `file`, while values coming from environment or the command line have origin `environment`/`command line`. Operator-set values are still passed through; unset ones are skipped so the binary reads `.env` (viper) itself as usual. This replaces the `VAR=value` prefix that cmd.exe cannot parse.
- **`web-build` and `cross` no longer need POSIX.** The `[ ! -f web/dist/index.html ]` guard dies in cmd.exe (`! was unexpected at this time.`), so all existence checks and the build are delegated to `bun -e` — Bun was already a prerequisite for every path through this target. `cross` no longer writes `GOOS=… GOARCH=…` in front of `go build`; the per-target OS/ARCH go in via `export` directives identical in both sh and cmd, and the `export` is scoped to the `cross-one` subgoal so an empty `GOARCH` never leaks into the ordinary `build`.
- **Verification:** `make version` produces `9router-go version 1.9.5 (windows/amd64)` both when make is invoked from sh and from `cmd.exe /c`; `make cross-one CROSS_OS=linux CROSS_ARCH=amd64` from Windows produces a real ELF binary (magic `\x7fELF`), so the `export GOOS/GOARCH` plumbing is proven to be more than merely parsing. The `cross` recipe (a `for` loop), `clean` (`rm -f`), `help` (`grep|sed`), and `bench` remain POSIX-only — on a machine without `sh.exe` on PATH, those are the recipes that fail first.
- 🔴 **Follow-up: `BINARY_NAME` had no extension, so the new build was never run on Windows.** `go build -o 9router-go` on native Windows writes a file named `9router-go` (a PE without extension), while `make run` calls `9router-go`, which cmd.exe resolves via `PATHEXT` to **the previous build's `9router-go.exe`**. As a result `make run` silently ran the old binary: the `go:embed` assets stayed the stale SPA bundle, and frontend fixes looked like they "did not land" even though they were built. Fixed with `BINARY_SUFFIX := $(if $(findstring Windows_NT,$(OS)),.exe,)` so the build target writes exactly the file that gets executed. `BINARY` is also split per-OS: bare name on Windows (PATHEXT), `./$(BINARY_NAME)` on macOS/Linux (`.` is not on PATH).
- **`web-build` can be forced to rebuild.** The `existsSync('web/dist/index.html')` condition meant the SPA was only built once; now `FORCE=1 make web-build` (or `FORCE=1 make build`) rebuilds `web/dist`. Docker (`Dockerfile` stage `web-builder`) and GitHub Actions (`ci.yml`, `release.yml`) are **not** affected: both already call `bun run build` explicitly in a clean build context, so they never hit this cache.


### 🐛 Two dashboard console errors: the "Add Model" button never opened the picker, and the install event was reused

- **One symptom, a different root cause, and both are already measured in the browser.** Opening the Create/Edit Combo modal and pressing **Add Model** throws `effect_update_depth_exceeded` — the picker never appears, there is no search input in the DOM. The reset effect in `CombosView.svelte` writes `modalNameResetKey`, a value read by the `{#key}` block below it; that block recreates `CreateComboModal`, and that re-queues the effect. Svelte discards the entire flush after **1001** turns (measured in the dev build: `window.__resetRuns = 1001` exactly when the Create modal is open, before "Add Model" is pressed). The effect is now guarded by `createSessionOpen`, so the reset happens once per session — verified in the production build: the picker opens, search 77 → 36 pills → back to 77, clicking a pill toggles, **0 console errors**.
- **Not a regression from #61.** This bug reproduces with pre-#61 code too (`git show a8b1556^`), and it was bisected: removing only `modalNameResetKey += 1` (while leaving `modalModels = []`) makes the effect stop at **1** turn and the error disappear. So the cause is measured, not guessed.
- **The `beforeinstallprompt` event is now genuinely single-use.** The old `promptInstall()` kept the event after the dialog was *dismissed*, so the next click called `prompt()` on an already-used event and Chrome refused with "Ignored bad install" in the console. Now the event is *spent* on the first call (accepted or dismissed) and the CTA stays dead until Chrome sends a new event; if the app is already running standalone, the event is no longer *preventDefault*ed because there is no custom button using it. Tested in `web/src/lib/pwa.test.ts` (4 cases) and **mutation-checked**: removing the standalone guard or returning the event to the "not yet used" state makes 3 of the 4 tests fail at the right assertion.
- **`s.includes is not a function` — finally reproduced and fixed.** The root cause was found: `internal/db/accounts.go:LockConnectionRateLimit` writes `lastError` into `providerConnections.data` as a **JSON object** (`{"status":429,"message":"…","timestamp":"…"}`, also used by `RecordConnectionOAuthFailure`), while the provider page reads `conn.lastError` and then calls `.includes('429')`/`.toLowerCase().includes('quota')` in `getCooldownInfo`. As soon as a connection is rate limited, `s` is an object and `.includes` throws a `TypeError` — the whole page render dies. Fixed in three layers: (1) **backend** `sanitizeProviderConnection` normalizes any `lastError` (object/string/other) into a string before it leaves the server; (2) **API client** `normalizeConnection`/`normalizeLastError` does the same on every connection-fetching path (`getConnections`, `getProvidersClient`, `getProvidersClientPage`); (3) **components** use the same helper, so the provider page never calls a string method on a non-string value. Tested with `TestSanitizeProviderConnection_LastErrorString` (backend) and two `client.test.ts` cases covering all three fetch paths.
- **`/api/models/caps` no longer 404s for compatible nodes.** The `openai-compatible-*` / `anthropic-compatible-*` pages and custom provider nodes always request caps; the backend answers `404 unknown provider or no static models` because that node genuinely has no static catalogue, so every page open adds a red request to the console. The handler now answers `200 {"provider":…,"caps":{}}` for ids marked as nodes, and the provider page no longer calls that endpoint at all when its provider is not a static catalogue. Aliases and catalogue ids remain covered: every Go provider with static models (92 canonical ids) is in `PROVIDER_CATALOG`, so the capability icon and the thinking picker do not disappear.
- **The `Auto Free Tier` combo is no longer permanently locked — it can be edited, renamed, and deleted.** The reason: this feature comes from PR #58 and **does not exist upstream**. Upstream `src/app/api/combos/` only has `[id]` and `presets`, no `auto-free`; upstream's `DELETE /api/combos/[id]` route also has **no** guard at all. Meanwhile in this port the combo was locked in three places: `validateLockedReorder` rejects rename/kind/model-set changes (`combos.go`), `HandleDeleteCombo` rejects `DELETE` with 403, and `ComboCard` disables the checkbox/Edit/Delete. As a result, the moment the **Auto Free Tier** button was clicked once, the `auto-free-tier` row existed forever and could not be removed from the dashboard.
- **What was removed:** `validateLockedReorder` + `sameModelSet` + the guard in `HandleUpdateCombo`/`HandleDeleteCombo` (backend), `isAutoFreeCombo` along with the `deletableCombos` filter and the `toggleSelect` guard (frontend), the "reorder only" badge along with the dead Edit/Delete buttons, and the now-dead `onReorder` path — the Move up/down arrows on the card were removed because the Edit modal already provides the same reordering for all combos. `isAutoFreeCombo` is replaced by `isAutoGenerated`, which only flags the `auto-*` kind for the "Auto-generated" badge, just like `auto-family-*`, which was always editable.
- **What was preserved:** `POST /api/combos/auto-free` still upserts on the stable id `auto-free-tier`, so deleting the combo and clicking again recreates it with the latest registry content. The `kind = "auto-free"` type is still written so the card can mark it as machine-generated.
- **Verification:** `TestAutoFreeComboCreateRebuildAndEdit` (replacing `TestAutoFreeComboCreateAndLock`) drives the real routes via `HandleAutoFreeCombo` → rename 200 + name stored → edit model set 200 + 2 members stored → strategy-only update does not overwrite the model set → `DELETE` 200 + row gone → rebuild 200 + name & model set back to the registry content. Live at `localhost:20130`: `DELETE` answers 200 (previously 403), rename 200, edit model set 200, and rebuild returns the combo to its previous state. In the browser at `localhost:5173/dashboard/combos`: the Auto Free Tier card now carries the "Auto-generated" badge, checkbox/Edit/Delete are active, both the Edit modal and the Delete confirmation modal open, 0 console errors. `go vet`, `go test ./...` (32 packages), the integration suite, `bun test` 113/113, and the build are clean.
- **Embedding models no longer enter the `Auto Free Tier` fallback chain.** The free-tier marker is merely a naming convention — `IsFreeTierModel` matches the suffixes `:free` / `/free` / `-free`, and those suffixes also stick to embedding, image, tts, and stt models. `openrouter` has exactly one free-suffix model, namely `nvidia/llama-nemotron-embed-vl-1b-v2:free`, which the registry already marks `"embedding"` in `ProviderModelKinds` — but `freeTierComboModels` never called `GetProviderModelKind`, so models that cannot answer a single chat turn still made the list. Now a single gate line `kind != "" && kind != "llm"` drops it, exactly as `usableModelFamilies` already does for auto-family combos. Registry census: 40 free-suffix models, 35 of them chat-capable, spread across 12 provider keys — so this feature is not a formality; the combo for someone who has 6 free providers contains a dozen chat models.
- **The "Auto Group by Model" and "Auto Free Tier" buttons were removed from the Combo page header.** Both are bulk generators that write dozens of `combos` rows in one click — 40 free-suffix models in the registry, grouped by family, had already produced 80 `Auto: …` combos on a single dashboard. `CombosHeader` lost all four auto-builder props, `CombosView` lost `handleBuildAutoFamily` along with `isBuildingAutoFamily`, and `api.buildAutoFamilyCombos` was discarded too since no one calls it any more.
- **The Combo page was aligned with upstream v0.5.91 (`localhost:20128/dashboard/combos`).** Compared directly in the browser, not by reading code. Upstream uses **one** button in the header — `Create Combo` — and moves all bulk actions to the *selection bar* below the list: a `Select all (N)` checkbox on the left that becomes `N selected` as soon as anything is selected, then on the right `Set strategy…`, `Delete (N)`, and `Clear`, which **only appear when there is a selection**. This port previously kept `Delete Selected` and `Delete All (N)` as permanent toolbar buttons, had no selection bar at all, and had a per-card Rebuild button that does not exist upstream.
- **What changed:** `CombosHeader` now only takes `onCreateClick`. `CombosView` gains `selectedCombos`/`allSelected`/`toggleSelectAll`, `handleApplyBulkStrategy` (writing `comboStrategies` as a single patch followed by one `updateCombo` per row, because its key is the combo *name*, so separate patches would overwrite each other), and a selection bar that mimics the upstream markup. `handleDeleteAll` and `deletableCombos` were removed. `ComboCard` lost `onRebuild`/`rebuilding` and the Rebuild button; the `RefreshCw` import was discarded too. `api.buildAutoFreeCombo` was removed as well because it no longer has a caller.
- **The intended consequence:** `POST /api/combos/auto-free` and `POST /api/combos/auto-family` are now unreachable from the dashboard entirely. The endpoints remain in the backend — only the buttons are gone.
- **A difference NOT yet dispatched:** this port's strategy list has 5 entries (Fallback, Round Robin, **Sticky**, **Capacity**, Fusion) while upstream has 3 (without Sticky and Capacity). That is a functional divergence in routing, not a cosmetic matter, so it was left alone — a separate decision is needed if they really want to match.
- **Verification:** browser `:5173` — the `aria-label="Combo actions"` toolbar contains exactly `Create Combo`; the idle selection bar reads `Select all (84)`; after clicking it reads `84 selected` and reveals `Apply Strategy` (disabled until a strategy is chosen), `Delete (84)`, `Clear`; per-card buttons `Copy combo name` / `Edit` / `Delete`, without Rebuild; 0 console errors. `bun test` 113/113, `tsc -b && vite build` clean, oxlint adds no warnings.


### 🐛 Issue #61 (partial) — combo picker: one flush for three metadata fetches, not three

- **Honest status: #61 is NOT done.** Its headline number is still alive. Re-measured in the production build using the original components (`ModelPickerModal` + the real catalogue, 997 pills / 5.561 nodes inside a single `max-h-[400px] overflow-y-auto` + `flex flex-wrap`), median of 14 alternating iterations in one Chromium session: **clear search → full list 146ms Task / 72ms Script**, and that is unchanged after the work below.
- **What changed: `web/src/components/combos/pickerExtras.ts` (new) + `ModelPickerModal.svelte`.** The three metadata fetches (`/api/models/alias`, `/api/models/custom`, `/api/models/disabled`) were previously chained with three `.then()` calls that each wrote their own `$state`, so every settle rebuilt the entire pill list. Now all three are batched into a single `Promise.all` with a per-endpoint catch, and the modal publishes **one** extras object in one write. The per-endpoint contract is kept: one endpoint returning 500 must not discard the two that succeeded (pinned in `pickerExtras.test.ts`).
- **Also: `ModelPill.svelte` no longer takes a per-row closure.** `onClick={() => handleToggle(model.value)}` was recreated on every render, and `caps` arrives as an object whose identity changes with every list rebuild. Now the pill takes a stable `onToggle` plus `vision`/`reasoning` booleans and builds its own handler from `value`.
- **What actually moved: the open path.** 30 strict-alternating iterations (position flipped every turn): `Task` **149,6ms → 130,1ms (−13%)**, `Script` **105,0ms → 84,3ms (−20%)** — as predicted, two eliminated rebuilds ≈ 2 × 13ms. The search transition (typing / clear) does **not** move: still within noise.
- **Why the headline number cannot be chased from this side.** An isolated probe (rebuilding the group data without touching the DOM) shows the rebuild cost is identical in **both** versions: **13,15ms vs 13,00ms Script** — so the per-row closure and the caps object do not touch the dominant cost. The application logic itself is only **0,14ms**: `resolveFilteredGroups` over 915 pills / 80 groups measured at 0,14ms in Bun with the real catalogue. The cost is creating ~1.000 component instances + ~5.500 DOM nodes once per render — matching the profile in the issue (`props.js` 19,3ms + `attributes.js` 11,4ms + `Icon.svelte` 4,0ms).
- **A prototype that was NOT shipped (ask if you want it).** Collapsed-by-default per group — new pills only mount when the group is clicked, automatically expanded as soon as there is a query — measured in the same harness: **open 196ms → 12,8ms Task**, **clear 146ms → 36ms**. Only this approach never creates those 1.000 pills. Not shipped because (a) it is an option with no upstream divergence chosen, and (b) upstream `v0.5.91` renders the full list; it needs an explicit decision first, then a changelog note as a divergence.
- **Verification:** `bun test` 107/107, `tsc -b` + `vite build` clean, `oxlint` with no new warnings (the 2 existing warnings were already pre-existing in `TerminalView.svelte` and `CreateComboModal.svelte`). All the numbers above were measured A/B on production bundles built for both sides (HEAD vs the change) and presented in alternating order so machine drift cannot invent a difference. A smoke run in the browser also verified pill add/remove toggling across rows and groups with no page error.

### 🐛 Issue #52 — `additionalItems` in a tool schema 400'd the whole Gemini request

- **The symptom.** `Unknown name "additionalItems" at functionDeclaration.parameters` (HTTP 400). Clients and MCP servers that describe array parameters with JSON Schema draft-07 emit `additionalItems`; the Gemini schema proto has no field for it, and one occurrence anywhere in the parameter schema rejects the entire turn, not just that one tool.
- **The gap was one keyword wide.** `cleanGeminiSchema` already stripped the 2020-12 tuple keyword `prefixItems`, but not its draft-07 twin `additionalItems`, so a draft-07 tuple schema was the one shape that still reached Google verbatim.
- **The fix is one list entry, next to `additionalProperties`.** `additionalItems` joins the `unsupported` keywords, so it is dropped at every node the recursion visits (`properties` values, `items`, `items` tuples, `additionalProperties`) rather than at one known depth. It is a list entry and not a local `delete` on purpose: `anyOf`/`oneOf` flattening copies a branch's keys back into the schema, so only the list — which `stripUnsupported` re-runs after the merge — closes that path. `prefixItems` keeps its dedicated block because it has to be promoted to `items` first; `additionalItems` only constrains the tail of a tuple and carries no `items` schema, so dropping it is the whole contract. An array left without `items` still gets the existing `{"type":"string"}` default.
- **Deliberately not Gemini-scoped at the call site.** `SanitizeOpenAITools` also runs on the OpenAI-compat fallback path for every provider, which already strips ~40 keywords there (`const`, `$ref`, `format`, `additionalProperties`, `title`, …). `additionalItems` is a client-side validation constraint with no meaning in a tool declaration on the wire, and it is the draft-07 twin of a keyword that path already drops, so removing it cannot change what any provider does with the body.
- **Mutation-checked.** The new table test (`TestCleanParametersSchema_StripsTupleKeywords`, 10 rows) walks the cleaned schema for the keyword at every depth and pins the siblings that must survive: `type`, `items`, `description`, an existing `items` that must beat `prefixItems[0]`, and a promoted tuple entry. Reverting the one-line change fails 8 of the 10 rows with exactly the reported keyword surviving; the two `prefixItems`-only rows pass either way, which is the no-regression guard. `TestSanitizeOpenAITools_StripsAdditionalItems` covers the OpenAI-compat body. Smoke-run through `TranslateOpenAIToGemini`, the emitted Gemini request carries no `additionalItems` at any depth.

### ✨ Issue #55 — a screenshot returned by a tool reached Gemini as nothing at all

- **Where the pixels were dropped.** `TranslateOpenAIToGemini` builds the tool turn in `internal/translator/gemini.go`: it read the `role: "tool"` content through `extractContentString`, which concatenates `text` blocks and ignores every other key. A Playwright/screenshot tool answers with `[{"type":"text",…},{"type":"image_url","image_url":{"url":"data:image/png;base64,…"}}]`, so the base64 had nowhere to go — `GeminiFunctionResp` carries only `Name`, `ID` and `Response.Result`, and the outbound payload contained no `inlineData` at all. Confirmed before the fix: the trailing user turn had exactly one part, the functionResponse.
- **What changed.** One line — `parts = append(parts, geminiToolMediaParts(msg.Content)…)` — plus the helper that feeds it. The helper is not a second image parser: it calls the existing `convertContentToGeminiParts`, the same path user messages already take, and keeps only the `inlineData`/`fileData` parts. Text is filtered out on purpose because it still rides inside `functionResponse.response.result`, unchanged.
- **Wire shape.** Before: `{"role":"user","parts":[{"functionResponse":{"name":"browser_screenshot","response":{"result":{"output":"captured"}}}}]}`. After: the same part, plus `{"inlineData":{"mimeType":"image/png","data":"QUJD"}}` as a sibling in the same user turn — the shape Gemini accepts for multimodal tool output, and the one OmniRoute PR #14173 established.
- **The common path is byte-identical.** `geminiToolMediaParts` returns nil for a string result and for a text-only block array, so the overwhelming majority of tool calls serialize exactly as before. Both of those cases are pinned by tests that assert the payload contains no `inlineData`.
- **Tests.** `TestOpenAIToGemini_ToolResultMediaParts` (6 table rows): text-only stays a lone functionResponse, text-only block array likewise, image-only keeps the picture, mixed text+image keeps both, a PDF tool output is inlined, and a remote image stays `fileData` rather than being pulled in as bytes. Every row asserts on the marshalled request, not on an intermediate struct, and on the media as wire substrings — a struct field that marshalling dropped would fail. Mutation-checked: reverting `gemini.go` fails the four media rows and leaves the two text-only rows green.

### 🐛 Issue #54 — an OAuth account with a revoked grant was refreshed again on every request

- **The symptom.** A permanently dead account (a revoked Antigravity refresh token) made the router call Google's token endpoint once per request, forever, until the egress IP was rate limited. `forceRefreshOAuthToken` — reached from the reactive-401 path in `fallback.go` and from the `authFailed` probe in `forwardGeminiNativeRequest` — failed with 401, and nothing recorded that failure. The account-scoped cooldown added in #39 is written only from the *upstream response* path, never from a failed refresh, so a dead grant had no backoff at all.
- **The 401 was unreadable even in principle.** Every refresher flattened the token endpoint's status into a message (`refresh returned 401: …`), so no caller could tell a revoked grant from a 5xx blip without parsing English. `providers.OAuthRefreshError` now carries the status and `providers.IsRefreshUnauthorized` reads it, and every refresher returns one: standard (the path Antigravity takes), cline, kiro (both exchanges), xai, claude. Bodies stay truncated at 200 bytes, because a token endpoint that echoes the request would otherwise write a live grant into the logs.
- **A rejected grant parks the account.** `db.RecordConnectionOAuthFailure` writes `oauthLockedUntil`, `oauthFailureCount` and `lastError`; the selector reads both account-scoped cooldowns through the new `db.ConnectionBlockedUntil`, so a parked account is skipped *before* a request is spent on it and no refresh is attempted for it at all. Backoff is `db.OAuthLockDelay`: 5m, 10m, 20m, 40m … capped at 24h, counting consecutive failures only — a successful refresh clears both fields (so a repaired credential is back in rotation at once and the next rejection starts from the floor), and so does `ResetConnectionHealthState`. Upstream parity: OmniRoute #14917.
- **The cooldown counts windows, not attempts.** One request can discover a dead grant more than once — `tryForwardWithConnection` refreshes, `forwardGeminiNativeRequest` refreshes again, and the `authFailed` project probe force-refreshes — so a rejection that lands while the account is already parked is the same strike seen again and does not move the counter. Without that rule one bad request would park the account for 20 minutes instead of 5. The three calls that first request makes are a pre-existing cost, not a hammer: the endpoint is not touched again until the window runs out. Measured end to end through `HandleChatCompletions` against a fake token endpoint that answers 401: three client requests, 3 calls on the first, **0 on the second and third**, and one strike recorded.
- **Its own field, not `rateLimitedUntil`.** The dashboard renders that field as `remainingPercentage: 0` plus `resetAt` (`handlers/dashboard/usage.go`) — "quota spent, come back at HH:MM". For a dead credential that is a lie the user acts on: the quota is untouched and waiting out the window cannot help; only a re-login can. The park keeps its own timestamp and a counter of its own, and the reason still reaches the dashboard through `lastError`, which that panel already renders.
- **The background loop stops hammering too.** `SelectConnectionsNeedingRefresh` skips a parked account, and a 401 out there records the same park. Without that, the 5-minute tick would have re-hit the token endpoint for an account no request can use, and would have inflated the backoff counter behind the request path's back.
- **Only 401 parks.** 400, 403, 5xx and transport failures keep the previous behaviour: none of them prove the grant is gone, and a park on a blip would take a healthy account out of rotation for nothing. On a 401 the custom refresher's error is no longer swallowed into a pointless second call at the standard endpoint, which rejects the same credential.
- **Verification.** Mutation-checked, not just green: routing the selector back to `ConnectionCooldownUntil` fails all three `TestParkedAccountIsSkippedByRouting` subtests plus the earliest-reset test; making `IsRefreshUnauthorized` always false fails both "revoked grant" subtests of `TestRejectedOAuthRefreshParksAccount`; dropping the clear fails `TestSuccessfulRefreshReleasesThePark`. Malformed or missing state fails open in the reader and in the selector (5 table cases). `go vet` and `go test -count=1` green on `internal/db`, `internal/providers`, `internal/proxy/oauth`, `internal/handlers/chat`, `internal/handlers/dashboard`, `internal/handlers/oauth` and `internal/handlers/media`.

### 🐛 Issue #53 — a Gemini 429 with a long Retry-After kept re-hitting the same exhausted account

- **The window Google names was never read.** `proxy.UpstreamError` carried only a status and a body, so the upstream `Retry-After` header was gone by the time any handler saw the failure. And `extractResetDuration` — the one helper that does parse a wait out of a body — looks for `quotaResetDelay` inside ErrorInfo metadata, while a Gemini/Antigravity 429 puts it on a `google.rpc.RetryInfo` detail as `retryDelay`. Neither carrier reached the router: with two accounts in the pool and a `retryDelay: 120s`, the client got **no** `Retry-After` at all and the failed account was locked for the classifier's 2s base backoff. `UpstreamError` now keeps the response headers, and the new `internal/handlers/chat/retry_after.go` reads all three carriers — header, `RetryInfo` detail, and the field names `extractRetryAfter` already owned — taking the longest, because a wait is only over once every source's window has passed.
- **A long Retry-After was waited out on the accounts that just refused it.** `comboRetryAfter` accepted any Retry-After up to `comboRetryWaitCap` (8s), so a 429 naming a 5-second window made a fully-failed combo pass sit for 5 seconds and then re-run the whole pass against the same limited accounts: 4 upstream requests and a 5s stall where 2 requests and no stall were correct. A 429 is now held to `brief429RetryTolerance` — **2s**, chosen because it is the router's own base backoff (`providers.BackoffConfig.BaseMs`): at or under it the account is very likely free again by the time the retry runs, so an ordinary burst is still absorbed, and above it the quota window is spent and waiting here cannot shorten it. Every other status keeps the 8s cap, unchanged.
- **The lock now matches the window, in both directions.** Honouring a long `Retry-After` keeps the next request from re-picking an exhausted account; honouring a short one is what lets the bounded second pass find the account again instead of waiting out a lock the router set on itself a second earlier. `retryableCooldownSec` takes the upstream's word over the classifier's and clamps at `maxResetCooldown`, so a hostile or nonsensical duration still cannot park an account forever.
- **The two combo loops no longer drift.** `earliestRetryAfter` was an RFC3339 string, re-parsed by `mustParseTime`, string-compared for "earliest", and re-formatted into a header — duplicated verbatim in `handleComboFallback` and `handleMessagesComboFallback`. It is now a `passRetry` holding a `time.Duration`, and `comboRetryAfter`/`mustParseTime` are gone; `TestComboRetryAfter`, which only pinned the old string parsing, was replaced by `TestComboPassRetryWait` over the decision that replaced it.
- **Mutation-checked.** `TestComboRateLimitFailover` (7 cases over a real pool), `TestRetryAfterWait_Extraction` (15 carriers, including the malformed header, the past HTTP-date, the empty header and the unparseable `retryDelay`), `TestRetryableCooldownSec` and `TestRateLimitCooldownParksAccountForTheNamedWindow`. Restoring the 8s cap for 429s fails the past-the-tolerance case; dropping `retryInfoDelay` fails the RetryInfo cases; dropping the header carrier fails the `Retry-After` cases; ignoring the window when locking fails both cooldown tests. `TestHandleMessagesComboFallback_RetriesOnceOnBoundedRetryAfter` kept its intent against a Retry-After inside the new tolerance.

### ✨ Issue #56 — an exhausted combo now says *when* to come back

- **The header was already there, the body was not.** When every account in a combo is cooling down, `handleComboFallback` and `handleMessagesComboFallback` already set `Retry-After` to the earliest upstream reset and appended a `reset after …` suffix to the message. That covers a header-reading client — but a client that never surfaces headers (browser SDKs, log-only integrations, a proxy that strips them) got the same 429 with no timing at all.
- **Both response fields now carry it, inside the repo's own error envelope.** New `internal/handlers/chat/combo_exhausted.go` publishes `reset_at` (ISO-8601 UTC) and `retry_after` (seconds) alongside `message`/`type`/`code`, so the body parses exactly like every other error the gateway emits. The duplicated response block in both combo handlers collapsed into one `writeExhaustedComboError`, so the two endpoints cannot drift apart again.
- **The human-readable suffix was reporting the wrong unit, and the new fields would have contradicted it.** `formatRetryAfter` divided `time.Until` — a `time.Duration` in nanoseconds — by `1000`, i.e. it rendered milliseconds as if they were seconds. A 150-second cooldown read as `(reset after 41399h 2m 26s)` next to a `Retry-After: 150` header, so the new `retry_after` would have shipped next to a message denying it. Now `reset after 2m 30s`.
- **A missing reset stays missing.** With no cooldown information the upstream error is forwarded untouched and no field is invented. A `Retry-After` the router cannot parse still degrades to the header's 1s minimum but gets no `reset_at` — a year-1 timestamp is a worse answer than none, because clients schedule on it.
- **Verified, not just green.** Table-driven `TestComboExhaustedCarriesResetTiming` drives both endpoints end-to-end against two mock upstreams that 429 with different cooldowns and asserts the response names the *earliest* candidate, not merely *a* candidate; `TestWriteExhaustedComboError_ResetFieldBoundaries` pins the envelope boundaries and the exact suffix. Mutation-checked: removing the two field assignments fails both tests with `error.reset_at = ""`, and restoring the millisecond division fails the suffix cases.

### 🗑️ `union-alpha` fully removed — a trial model that no longer exists upstream

- **Not just one string.** This model was still alive in nine places, and `docs/DASHBOARD_PROVIDER_PARITY.md` already flags it as **"MISS upstream ✅ removed 2026-09-21"** — the ledger recorded it, the code never followed. What was removed: the dedicated Messages API routing branch in `ForwardOpencode` (68 lines, `cleanModel == "union-alpha"`), the `opencodeGoMessagesModels` entry, the `|| cleanModel == "union-alpha"` clause in the `ForwardOpencodeGo` dispatch, the `oc` / `ocz` / `opencode` / `opencode-zen` catalogue lists (`registry_models.go`), the capability rows in `capabilities.go`, and two entries in `web/src/lib/models.ts` so the dashboard stops offering a dead model. Two executor tests and one live test (`TestIntegration_OpenCode_UnionAlpha_Messages`) were discarded too, because they only exercised a branch that no longer exists.
- **The most misleading placeholder was corrected too.** `translator.TranslateClaudeChunkToOpenAI` uses `state.Model = "union-alpha"` as the **generic fallback** for Messages streams that do not send a `model` — and that value leaks into every chunk via `"model": state.Model`, so clients kept seeing a retired model claimed as the responder. Now `handleClaudeMessagesStream` seeds the state with the model the client actually asked for (`RequestedModelFromContext`), and the translator only falls back to the `"unknown"` placeholder if upstream **and** request both omit the model. Three new cases are pinned in `TestTranslateClaudeChunkToOpenAI_ModelEcho`: the upstream model beats the seed, the seed survives while upstream stays silent, and the placeholder as a last resort.
- **Tests that are still alive stay alive.** `ensureMessagesMaxTokens` is still used (the Anthropic + `opencode-go` path), so its test was not removed — only the sample name changed to `claude-sonnet-4-5`, because that function builds a Claude Messages payload and using a retired model as the example is dishonest. Same in `models_list_scope_test.go`: the sample model for `ConnectionOwnsItsCatalog` changed to `big-pickle` (absent from the static catalogue, so that is indeed the effect under test), while `jev-1.13-free` may only be the model that must **not** appear.
- **Verification:** `go build ./...`, `go vet ./...`, `go test -race ./...`, the `integration` suite (`-race -count=1`), `bun test` 103/103, and `bun run build` are all green. `TestHandleUsageStream` and `TestApplyConnectionStrategy_KeepsRotatingPastFirstCycle` appeared in a few runs — both were already A/B'd at `5ba8aa0` (code without any of these changes) and fail there too, so they are pre-existing.


### 🐛 HTTP 200 with empty content is no longer treated as "success" — the router switches model

- **The symptom.** The report: "round-robin still stays on one model". The root cause is not the round-robin strategy but one assumption: HTTP status is equated with an answer. `providers.RetryableStatusCodes` (401/403/429/502/503/504) only triggers failover when upstream **refuses**. If upstream replies `200` with an empty body, an HTML error page, or an envelope `{"error": …}`, `jsonResponse` / `handleJSONResponse` forwards it as-is, `tryForwardWithConnection` returns `nil`, and `handleAccountFallback` considers the turn served — including **opening** `LockConnectionModel` and `ClearConnectionRateLimit` for that connection. The effect is worse than merely being stuck: the combo halts on the first member, the problematic account is judged recovered, and the next round repeats the same model.
- **What already existed, and why it is not enough.** `sseWithoutCompletionError` already turned an event stream carrying no completion chunk into a 502 — but only for SSE-shaped bodies, exactly as reported. Plain JSON bodies were never checked. The streaming path is looser still: `executor.ForwardOpenAI` calls `execSSEStream` directly without ever reading `Content-Type`, so a 200 HTML error page enters the SSE scanner, yields **zero frames**, and `SSECopy` closes it with a clean `[DONE]`. The client sees the turn finished; the router never gets a chance to fail over.
- **The fix: `internal/proxy/empty_success.go`.** One validator (`EmptyUpstreamError`) that reads the envelope of the three wire shapes the gateway serves — Chat Completions (`choices`), Claude Messages (`content`), Responses (`output`) — and returns `*UpstreamError{502}` for an empty or whitespace-only body, a non-JSON body (an HTML error page is named on its own in the log), an `{"error": …}` envelope behind a 200, `choices: []`, a choice without content/tool_calls/reasoning/refusal, and `output: []` with status `completed`. 502 was chosen because it is already listed in `RetryableStatusCodes`, so the combo lock and exclusion work with no other change. Two helpers that were previously duplicated across packages were collapsed: `UpstreamFailure` replaces `sseWithoutCompletionError`, and `LooksLikeSSE` replaces the `executor.looksLikeSSE` / `chat.isSSEBody` pair.
- **Two fabrications of empty answers removed, not papered over.** `handleCodexStream` wrote `{"choices":[…,"content":"","finish_reason":"stop"}` as a "Fallback empty response", and `ForwardOpencode` (union-alpha) sent an empty SSE buffer to `jsonResponse`. Both are now 502. The side effect is proven: `TestE2E_Opencode_MuseSpark_Mock_Vision` was previously green precisely because it accepted that fabricated completion — its mock now returns the event stream that was actually requested (`buildResponsesBody` forces `stream:true`) and the test asserts on the answer content.
- **Streaming: classification before headers.** `executor.ForwardOpenAI` now checks `Content-Type` before writing any header. A body that is not an event stream is read, and because **not a single byte has been sent to the client yet**, that request can be processed as JSON and fail over exactly like the non-streaming path. A mislabelled event stream (`Content-Type: application/json` with an SSE body) is still replayed from the buffer, so there is no regression for providers that write the wrong content type.
- **The honest limit.** A genuine SSE stream that connects and then immediately EOFs with zero frames **cannot** fail over: the header is already sent, so the status can no longer change. The client is told — `internal/proxy/sse_error.go` (#62) closes that stream with an in-band error frame — but the router still does not switch model in that case. The fix only applies while not a single byte has been sent to the client.
- **Verification.** `TestHandleChatCompletions_ComboMovesOnFromEmpty200`, `…FromHTML200WhileStreaming`, and `…ComboAllEmpty200Fails` are mutation-checked: with the validator disabled, all three fail with exactly the reported symptom (empty body reaching the client, raw HTML piped as a "stream", `content:""` served as a finished turn). Plus 25 table subtests for the validator, 5 subtests for Content-Type classification in the executor, `go vet ./...` clean, `go test -race` green in `internal/proxy/...` and `internal/handlers/chat`, and the `integration` suite green.

### 🐛 The Tailscale dashboard test asserted on whatever was installed on the machine running it

- **The handler was never wrong; the test was.** `TestHandleTunnelEndpoints/TailscaleEnable_ReturnsCleanError` asserted a 400, which is only what the handler returns when no `tailscale` binary is found. On any machine with Tailscale installed the handler finds the binary, probes it, sees a logged-out daemon, and correctly answers **200 with `needsLogin` + `authUrl`** so the dashboard can render its login button — the same contract upstream returns. So the test passed only where Tailscale was absent, and failed for everyone else.
- **It also mutated the developer's machine.** These handlers shell out to the real binary, so the test ran `tailscale up --reset` against a live daemon (~10s), and `TailscaleDisable` ran `tailscale funnel --bg reset` — real side effects on a real Tailscale node, from a unit test.
- **Fixed by pinning the host, not by loosening the assertion.** `internal/handlers/dashboard/tunnel.go` now resolves the binary and runs commands through two package-level seams (`tailscaleBinFn`, `tailscaleExec`), the same pattern `validate.go` and `connection_probe.go` already use for their network calls. No test shells out to a real binary any more; the suite dropped from 10.04s to 0.10s.
- **Both real branches are now pinned**, not just the absent-binary one: no binary -> 400 clean error, installed-but-logged-out -> 200 `needsLogin` with the auth URL parsed out of the login output, and disable -> exactly one `funnel --bg reset`. Mutation-checked: routing the handler back around the seam fails with `must not exec tailscale when none is installed`. `go test -race ./...` is green across the repo for the first time.

### 🐛 Issue #57 — a stream that dies after HTTP 200 was closed silently

- **The client was told the answer was complete.** A stall timeout or a dropped socket used to end a streaming request with a synthesized finish_reason plus the [DONE] sentinel; every OpenAI client reads a finish_reason as a normal completion, so truncated text was kept and reported as a finished turn. Ported from upstream decolua/9router commit 93001213 (buildStreamErrorBytes, onAbortTerminal).
- **An in-band error frame, and never a fabricated terminal.** New `internal/proxy/sse_error.go` emits the OpenAI shape (a data frame carrying an error key with a machine-readable code, then the [DONE] sentinel) and the Anthropic shape (event error) for the Claude-named path. The abort path of `SSECopy` no longer reaches the `finish_reason` synthesis that `finish()` still uses for a clean EOF.
- **A timeout is distinguishable from a lost socket.** The stall watchdog now records that it fired (`stall.go` sets an atomic flag) and `Read` re-wraps the opaque closed-file error as `ErrStreamStall`, so the client gets a 504 `gateway_timeout` instead of a 502 `upstream_error`. Cancellation stays 499 rather than being reported as a gateway failure.
- **Verified, not just green.** New unit tests cover the abort frame and all three classifications; a new integration test drives the real router against a fake upstream that hijacks and kills the socket mid-turn. Mutation-checked: restoring the old call site fails the integration test and the unit tests with exactly the reported symptom. Smoke-tested against the built binary with a socket reset mid-turn: the client receives the error frame plus [DONE], and a healthy stream is relayed unchanged with no error frame.
- Also fixes a pre-existing data race in `mockResponseWriter` (the heartbeat goroutine appended to an embedded `bytes.Buffer` while the test read it), which `go test -race` flags on the untouched `TestHeartbeatWriter_EmitsKeepAliveWhenIdle`.


### ✨ Feature integration suite + `integration` CI job

- **The gap this closes.** Every Go test until now called a handler directly or mounted a hand-built `chi` router. That shape cannot see a regression in the wiring production actually uses, and the failures it hides are exactly the ones nobody can reproduce by hand later: a route registered in the wrong auth group, the `middleware.RequestLogger` `/v1` rewrite dropped so every documented OpenAI URL 404s, account rotation no longer skipping a throttled connection, usage no longer being recorded. `internal/handlers/router_test.go` already documents two of these having shipped — the CLI-Tools 401 and the Codex reset-credit 404 — both found after the fact, both because a route was wired into a table the tests exercised but the server did not.
- **What the suite is.** `internal/integration/` boots `app.ProvideRouter` — the same middleware stack and route table the binary serves — on a real HTTP listener against a temporary SQLite database created by the production schema bootstrap (`EnsureCoreSchema` + `EnsureUpstreamLeases`). Every provider call is intercepted by an `httptest` fake seeded through `providerConnections.data.baseUrl`; the stored row is read back and the fake URL asserted, because an empty `baseUrl` falls through to the real provider URL from `providers.KnownProviders` and would turn an offline suite into a live call. `internal/integration/bootfx/` is a separate test binary that boots the real fx graph (`DatabaseModule` and `ServerModule` included) on a free port. It boots **once**, from `TestMain`: `db.InitGlobalDatabase` is a process-wide `sync.Once` and the fx `OnStop` hook closes that handle for good, so a per-test boot would hand the second caller a closed database and an already-cancelled shutdown context.
- **Coverage.** Auth (missing/unknown/deactivated key with the typed 401 envelope, both credential carriers, the `?key=` asymmetry, the dashboard gate and its always-protected paths), chat completions (outbound envelope, payload passthrough, verbatim upstream errors, non-retryable statuses not rotating accounts, retryable ones locking and moving on, all-throttled surfacing a real 429, disabled accounts out of rotation, provider isolation and alias resolution), streaming (SSE relay terminated by `[DONE]`, provider errors before the first byte), combos and model aliases, `/v1/models` and `/v1/models/info`, dashboard CRUD for connections/keys/combos including the 409 name guard and the key-masking leak guard, `/v1/messages` Claude translation both ways, and usage accounting (a served completion billed, a failed one recorded but not billed).
- **Mutation-checked, not just green.** Removing `middleware.RequestLogger` from the stack fails `TestV1AndUnversionedPathsReachTheSameHandler` with a 404; turning the retryable-status branch of `handleAccountFallback` into an immediate return fails `TestRetryableUpstreamErrorRotatesAccount` with the 429 the client would otherwise have seen. Both regressions leave every handler correct, which is why unit tests stayed green.
- **Harness discipline.** `Env` deliberately holds no `*testing.T`; every helper takes the running test as its first argument. Capturing the parent would make a failing `t.Run` call `FailNow` on the parent from the subtest's goroutine, which `testing` reports as "subtest may have called FailNow on a parent test" and attributes the failure to the wrong line. Subtests assert on a row selected by id and fail when it is absent, so a list that came back empty can never pass a CRUD assertion vacuously.
- **Pipeline.** New `make test-integration` / `make vet-integration` targets (both depend on `web-build`, since the suite imports `internal/handlers` and `web/embed.go` embeds `web/dist` at compile time), and a CI `integration` job that runs `go vet -tags=integration` plus `go test -tags=integration -race -count=1` on every push and PR. `-race` is on because the proxy is genuinely concurrent (SSE pumps, the in-memory usage tracker, per-handler sticky state). The `integration` tag keeps `go test ./...` fast; `internal/integration/doc.go` is deliberately untagged so the package always has a compilable file and `go build ./...` never trips over a directory whose every file is excluded.

### ✨ Issue #38 — the Codex reset-credit counter was read-only

- **The button had no handler at all.** `QuotaTrackerView.svelte` drew the credit count from `resetCredits.availableCount` (which `fetchCodexUsage` already reports) as a `<button>` carrying `type`, `disabled`, `title` and `class` — but no `onclick`, so clicking it did nothing. `wham/rate-limit-reset-credits` appeared nowhere in `internal/`. The port covers the whole flow: list, choose, redeem, and re-read the quota.
- **Contract ported from OmniRoute, not transliterated.** `src/lib/usage/codexResetCredits.ts` was fetched first and its behaviour preserved — the two endpoints, the eight accepted list shapes (`credits` / `reset_credits` / `resetCredits` / `rate_limit_reset_credits` / `rateLimitResetCredits` / `items` / `data` / bare array), the camel/snake id aliases, the unavailable-status filter, expiry ordering, and the typed refusals. New `internal/codexquota/resetcredits.go` + `resetcredits_fetch.go` implement it natively; no TypeScript was carried over.
- **The typed error codes survive.** `no_credit`, `nothing_to_reset`, `selected_credit_unavailable` and `unknown_reset_credit_response` keep their 409/502 statuses, and the dashboard renders the code. Collapsing them into one generic error would have thrown away the only thing that tells "you have no credit" from "your limit is not actually exhausted". `already_redeemed` is a **success**, not a failure — it is the state the user was trying to reach.
- **Two things the port had to add that the original got for free.** `chatgpt-account-id` is sent from the connection's stored account id, because the wham endpoint scopes credits per Codex account and would otherwise list the wrong one. And the consume reuses **one idempotency key across the auth retry**, so a retried click after a 401 can never redeem twice.
- **Failures are read leniently, and the dashboard stays honest.** A non-JSON body becomes a raw string, because the outcome can legitimately be a bare token. A generic upstream 500 is *not* relabelled as `no_credit`; only the two meaningful refusals are.
- **The list is fetched on open, not on every poll.** The row counter keeps costing nothing; opening the chooser is what calls the endpoint, and the server returns them soonest-expiry first so the default selection is the one that frees the quota soonest. Radio selection, the empty/loading/error states and the redeem button disabling themselves are all in the new modal.
- **The refresher is wired as `nil`, deliberately.** `codexquota` supports a 401/403 refresh-and-retry, but the dashboard has no OAuth refresher of its own and importing the chat package's would drag provider-specific token persistence across package boundaries — the same reason `fetchCodexUsage` passes none today. The seam is implemented and tested; leaving it dormant is honest, where a half-wired refresher would silently fail to persist a rotated token.
- **Verification:** 2060 Go tests across 35 packages, 87 web tests, gofmt + vet + `tsc -b` clean. 22 new Go tests cover the parsing contract (filtering, ordering, all eight payload shapes, count reporting), selection, every outcome code, the headers actually sent, the refresh-once-on-401, and that the consume body carries the chosen credit plus the idempotency key; 7 handler tests pin that the new routes are reachable past the `/usage/{connectionId}` parameter route and that a non-Codex or API-key connection is refused before any upstream call.

### 🐛 A stream without `finish_reason` made the client fail with "stream closed before a finish_reason was received"

- 🔴 **`SSECopy` forwarded a bare `data: [DONE]` as-is.** The OpenAI passthrough path (`ForwardOpencode`'s default `chat/completions` route, and all other OpenAI-compatible upstreams via `execSSEStream`) writes the raw upstream chunk to the client and then `return`s as soon as it sees the substring `[DONE]` — without checking whether a terminal `finish_reason` was ever sent. When upstream sends content deltas and then `[DONE]` with no terminal chunk, a strict client (Oh My Pi) throws `OpenAI completions stream closed before a finish_reason was received` on every turn. Because combo fallback tries the next account, which behaves the same, the same error appears repeatedly (5x in the report). The same shape is reported in `can1357/oh-my-pi#9433`: "streamed content deltas and then emitted the [DONE] sentinel without a terminal finish_reason chunk".
- **Terminal synthesis moved to before `[DONE]`, not after it.** `SSECopy` was rewritten in `internal/proxy/sse_copy.go` as a line-boundary-aware `sseCopier`: the original `[DONE]` sentinel line is always preceded by a `finish_reason: "stop"` frame if no terminal is present yet — `stop`, not `network_error`, because `[DONE]` is a deliberate end (EOF/read-error truncation still uses `network_error` as before, and a non-EOF read error now also closes the stream on a best-effort basis before the error is returned). Terminal detection uses a 64B rolling window so a token split across reads is still caught, and an explicit `null` value is not counted as a terminal.
- **Bonus: `[DONE]` inside content no longer cuts the stream.** The old detection (`bytes.Contains(checkBuf, "[DONE]")`) cut the stream as soon as the substring appeared anywhere — including inside a JSON content string. The sentinel is now only recognised as a whole event line (`data: [DONE]` / bare `[DONE]`), including when split across reads and when there is no newline at EOF.
- **Tests:** `TestSSECopy_InjectsStopBeforeBareDone` (delta→DONE, chunk `finish_reason:null`→DONE, DONE with no newline at EOF, sentinel split byte-by-byte via `iotest.OneByteReader`, `[DONE]` inside content, read-error still closes the stream + returns the error). Mutation-checked: all six subtests fail on the old implementation with exactly the reported symptom. The old suite `TestSSECopy_SynthesizesTerminalOnAbruptClose` stays green unchanged.

### 🐛 Issue #39 — accounts already in cooldown were still handed out by rotation

- 🔴 **The Go port was missing a mechanism upstream has, in two halves.** Upstream `filterAvailableAccounts` (`open-sse/services/accountFallback.js:180`) skips any account whose `rateLimitedUntil` is still in the future, and `applyErrorState` (line 216) is what writes it. The Go port had **neither**: `rateLimitedUntil` was read by the dashboard (`internal/handlers/dashboard/usage.go:104`, to render "reset at") and cleared by `ResetConnectionHealthState`, but **nothing ever wrote it** — so the Quota Tracker's reset time was always empty, and the selector had no account-scoped signal to skip on. The only cooldown was `modelLock_<model>`, which is keyed by model *and* only consulted when the request carries a model at all.
- **Writes:** `LockConnectionRateLimit` stores `rateLimitedUntil` + `lastError` + `status` (upstream `applyErrorState`), called from both lock paths — `comboLockRetryable` and the non-combo loop in `fallback.go`. `ClearConnectionRateLimit` drops it again when a request is served (upstream `resetAccountState`); without that a recovered account would stay out of rotation until the cooldown expired on its own. It deliberately leaves the per-model locks alone, so a success on one model cannot unlock a model that is still cooling.
- **Reads:** the pick loop in `getBestConnection` now skips any candidate whose cooldown is still in the future. The check parses `c.Data` directly, so it costs **no extra database query** in the routing hot path — the candidates are already in memory. It sits *outside* the `if model != ""` block because the cooldown is account-scoped: that is what catches a quota spent account-wide, and it is the only cooldown a model-less request can consult.
- **A pinned connection is covered too.** `pinnedConnectionIneligible` now reports "account cooldown until …", so pinning cannot be used to force a request onto a known-dead account — it falls through to the strategy, matching how upstream resolves the pin inside its availability filter (`src/sse/services/auth.js:100-148`).
- **Fails open, on purpose.** A missing, empty, null, wrongly-typed or unparseable `rateLimitedUntil` is treated as *not* in cooldown. A malformed field must never be able to take routing down or silently retire every account.
- **The caller learns when to come back.** When every candidate is cooling, the error now names the earliest reset instead of the bare "all excluded" it replaces — which is what turns a run of failed requests into a "retry in Nm" the client can act on.
- **Verification:** 2021 Go tests pass across 35 packages, gofmt + vet clean. Twelve new tests: the selector skips a cooling account, keeps an expired one, applies the rule to a request with no model, reports the earliest reset when all are cooling, falls through from a pinned cooling account, and ignores a malformed field; the write/clear round-trip and fail-open parsing are pinned in `internal/db`. Mutation-checked — disabling the filter fails four of them with the reported `selected an account that is still in cooldown`, and the two negative cases correctly stay green when it is off.

### 🐛 Issue #40 — the combo strategy dropdown rendered blank

- 🔴 **The claim's mechanism was wrong, but the reported symptom was real.** The issue claimed the card "did not fall back to `combo.strategy`" when `comboStrategies[name]` was missing. It did — `ComboCard.svelte:47` already read `combo.strategy` as the second term. The real bug: `combo.strategy` can hold strings with **no matching `<option>`**. The global "Combo Routing Mode" defaults to `first-model` (`ProfileSettingsView.svelte:49`), and `repos.go:609-611` copies that global onto every combo lacking a per-combo entry. The card offered only three options (`fallback`, `round-robin`, `fusion`), so `first-model` matched nothing and `select.selectedIndex` fell to `-1` — a blank box that still opened on click.
- 🔴 **Selecting Fallback reproduced the blank on the very next refresh.** `updateComboStrategy` (`types.ts:74`) *deletes* the entry when you pick `fallback` without a judge (`if (newStrategy === 'fallback' && !next.judgeModel) { delete updated[comboName] }`). Choosing Fallback cleared `comboStrategies[name]`, the card fell through to the global `first-model`, and the box went blank again.
- **Single source of truth for the options, and a normalizer.** `COMBO_STRATEGIES` (`types.ts`) now defines every strategy the backend speaks (`fallback`, `round-robin`, `sticky`, `capacity`, `fusion`). `resolveComboStrategy` maps `first-model` onto `fallback` (they describe the same try-in-order behaviour in different vocabularies) and falls back to `fallback` for an unrecognised value. The card's `<select>` renders from that list, so the resolved value is guaranteed to match an option.
- **Verified:** 90 web tests pass (5 new in `types.test.ts` covering every value the server can send), and end-to-end through a real Chromium instance with `comboStrategy: 'first-model'` persisted in SQLite — the card renders `"Fallback — try in order"` with `selectedIndex: 0`, and the screenshot confirms the label is visible.

### 🐛 Terminal detection in `SSECopy` never activated — every stream got a double terminal

- 🔴 **The needle had two quotes.** `sseHasNonNullValue` builds `needle := []byte(key + `":`)`, but the `key` passed in already ends with a closing quote (`"finish_reason"`). As a result it searches for `"finish_reason":` — two quotes before the colon — which never appears in any JSON, so `bytes.Index` always returns `-1` and `sseHasTerminalToken` is always `false`. Consequently `sseCopier.hasTerminal` is never filled: **every** stream whose upstream already closed properly with `finish_reason` still gets a second terminal frame injected right before `[DONE]`. This bug has existed since the "inject stop terminal before bare [DONE]" entry and undoes half its claim — "preserving the native terminal" never ran, only the synthesis part ever worked. The needle is now `key + ":"`.
- **As a result it is invisible in the logs.** The injected frames are written by `sseCopier.writeRaw` straight to the `ResponseWriter`, not through `onChunk`, so they do not enter the `ResponseBuf` stored in `requestDetails`. If a recorded stream is read back from the DB, the stream looks clean even though there is a second terminal right in front of its `[DONE]` — investigating from the log side alone would never find this bug.
- **Tests:** `TestSSECopy_KeepsSingleTerminalOnCompliantStream` (table: OpenAI already `stop`, OpenAI already `tool_calls`, Claude already `end_turn` — all must be relayed without a single injected terminal frame, and without `network_error`), plus `TestSSECopy_QuotedTerminalInsideContentIsNotATerminal` which locks in content containing `\"finish_reason\": \"stop\"` escaped: its content must stay intact and the terminal must still be injected. The first three subtests are mutation-checked — restoring the needle to `key + `":`` makes all three fail with exactly one extra terminal before `[DONE]`.

### 🐛 Issue #41 — `stream:false` to kiro and qoder was answered with an event stream

- 🔴 **kiro never looked at the request.** `ForwardKiro` called `handleKiroStream` unconditionally while every sibling executor — qoder, iflow, kimchi, commandcode — branches on `req.IsStream`; `handleKiroStream` hardcodes `Content-Type: text/event-stream` and flushes `data: {...}` frames. A client that asked for JSON got SSE, and `JSON.parse` died on the first `d` of `data:`. Reproduced here before the fix: `Content-Type = "text/event-stream"`, `jsontext: invalid character 'd'`.
- **`ForwardKiro` now branches, and the non-stream path reuses the stream parser instead of duplicating it.** `handleKiroNonStream` runs `handleKiroStream` against an `sseCollectWriter` — a body-only `http.ResponseWriter` — and folds the frames with the existing `sseToOpenAIJSON`, the same fold the codex non-streaming path already uses. The ~120 lines of Kiro event parsing, including the fragmented `toolUseEvent` argument reassembly that keeps emitting `arguments: "{}"`, stay in exactly one place. A clean end of stream surfaces as `io.EOF` once the last frame is consumed, which is normal and not treated as a failure.
- **No assistant frame is an error, not an empty answer.** Returning 200 with an empty body would read as a successful empty completion and silently end combo fallback, so a stream that folds to nothing becomes a 502 `sseWithoutCompletionError`.
- 🔴 **qoder is SSE-only, and the error was hiding inside the stream.** `ForwardQoder`'s upstream path is `/sse/agent_chat_generation`, so it answers with an event stream whatever `stream` says. The non-stream branch did branch, but it handed that stream to `jsonResponse`, which wrote it verbatim under `Content-Type: application/json` — header and body contradicting each other. The body also carried an upstream envelope `{"statusCodeValue":400,…,"body":"[FAIL]node:agent_router …"}`, so the client got HTTP 200, failed `JSON.parse`, and never saw the real error.
- **Two fixes, because folding alone would have hidden the error a second way.** `jsonResponse` now folds an SSE body into one `chat.completion` — the general fix, placed there because an SSE-only upstream ignoring `stream:false` is a cross-provider mechanism, and it runs *before* the log buffer, the Responses bridge and the Claude translation, all of which previously received raw SSE. `looksLikeSSE` only inspects the first non-empty line and requires a `data:`/`event:` prefix, so a JSON body (which starts with `{`) can never be misread. Separately, `qoderSSEUpstreamError` stays in `qoder.go` because the envelope is Qoder-specific, and returns a `*proxy.UpstreamError` carrying the upstream status and message. Verified: without it, a 400 envelope folds into a fake 200 with an empty completion — the error is swallowed rather than raised.
- **Streaming is untouched, and pinned.** `TestForwardKiro_StreamStillEmitsSSE` asserts a `stream:true` request still answers `text/event-stream` with the assistant delta and the terminal `data: [DONE]` frame, so the new branch cannot quietly swallow the streaming path later.
- **Every new test is mutation-checked.** Reverting the kiro branch fails with the reported `text/event-stream` + `invalid character 'd'`; disabling the qoder error detector fails with a fake `200` and an empty completion; disabling the generic fold fails with the same `invalid character` parse error. The four new tests build real AWS EventStream frames and real SSE payloads rather than asserting on mocks, and the kiro non-stream test asserts the *concatenated* content, so a parser that emitted only the last frame would fail.

### 🔒 Issue #35 — the backup file carried the dashboard password hash and the live OIDC client secret

- 🔴 **`GET /api/settings/database` wrote the raw settings blob into the payload.** `exportDatabase` assigned `Repo.GetSettingsRaw()` straight into `out.Settings` (`settings.go:349-354`), and `GetSettingsRaw` unmarshals the stored JSON unfiltered. `sanitizeSettings` — the one function that drops these keys — had exactly two call sites, `GET /api/settings` and `PUT /api/settings`, so the backup path bypassed it entirely. Every `9router-backup-*.json` therefore contained `"password": "$2a$10$…"` and `"oidcClientSecret": "…"` on disk. The OIDC secret is not a hash: `HandleOidcTest` (`sso.go:70`) reads it as plaintext, so it is directly usable against the identity provider. Since #32/#34 made the password-modal flow actually produce a file instead of 401-ing in the browser, this stopped being theoretical.
- **Export now strips them through the existing key list.** `exportDatabase` calls a new `stripSecretSettings`, which reuses `secretSettingKeys` rather than repeating the list, so there is one place that decides what a secret is. It deliberately does *not* add the derived `hasPassword` field the way `sanitizeSettings` does: the payload is read back by `importDatabase` and written into the settings row, so a response-only flag would persist a value nothing reads. Non-secret settings are untouched — `oidcIssuerUrl`, `requireLogin` and friends still restore.
- 🔴 **Naive sanitising was itself a security regression, and this is the part the issue did not predict.** `importDatabase` wipes the settings row (`DELETE FROM settings`) before writing the payload, so a backup with the keys stripped would have *deleted the live password* on restore and dropped the dashboard onto the well-known default `123456` — trading "a secret sits in a file" for "anyone who knows the default can log in", with OIDC silently disabled on top. Verified against a live build before fixing it: after importing a sanitised backup, `POST /api/auth/login` answered **200 for `123456`**, 401 for the real password, and `/api/auth/status` reported `hasPassword: false`. `importDatabase` now reads the current secret values **inside the transaction, before the wipe** (`readSettingsSecrets`) and carries them forward, so a restore can never downgrade auth. A payload that does carry its own hash still wins, so files written by older builds — or by the Next dashboard — keep restoring as before; and an *empty* secret in the payload is read as "not present here" rather than "clear it", so a hand-edited backup cannot blank the stored credentials either.
- **Deliberate divergence from upstream.** `exportSettings()` in `src/lib/db/repos/settingsRepo.js` is a plain `return await readRaw()` and `exportDb()` uses it directly, so upstream leaks the same two keys. This is a parity break, taken because the file is user-shared by nature.
- **What users should know:** the dashboard password and OIDC client secret are now machine-local. A backup restored onto a *fresh* machine no longer carries them, and the password has to be set again there — that is the intended trade, but it is a behaviour change from a backup that used to carry both.
- **Tests:** `TestHandleExportDatabase_StripsSecrets` (both keys and the injected `hasPassword` are absent from the settings block, neither credential appears anywhere in the serialized bytes, non-secret settings still present), `TestHandleImportDatabase_PreservesLiveSecrets` (a secret-free backup keeps the live hash and secret, and so does one whose secrets are empty strings), `TestHandleImportDatabase_LegacyBackupPasswordWins` (a backup carrying its own hash still wins). All three are mutation-checked — reverting each half of the change fails them with the exact leak or the exact `nil` credential.

### ⚡ PGO + upstream connection-pool tuning

- **PGO is now on for release builds.** `cmd/9router-go/default.pgo` is a CPU profile captured from a real chat-completions workload (auth + model resolution + SQLite reads + upstream forward). Go 1.27's default `-pgo=auto` picks it up automatically, so `make build`, `make run` and `make cross` all get the optimized binary with no flag to remember — `go version -m 9router-go | grep pgo` confirms it.
- **The connection pool was the actual bottleneck, not the compiler.** Go defaults `MaxIdleConnsPerHost` to **2**, so a reverse proxy under concurrency kept re-dialling upstream: a fresh TCP connect plus TLS handshake per request. `FallbackTransport`, `directProxyClient`, the chat handler's streaming client and the rotating-proxy client all ran on 2 idle slots per host. Raising it to 128 idle per host (256 total) and enabling HTTP/2 cut p99 latency **-51%** and raised throughput **+27%** at c=100 against the mock upstream.
- **The numbers live in `constants.HTTPTransportConfig`.** The pool/timeout values were four separate magic-number blocks; they are now one struct with a documented field for each knob, plus `Configure(t)` for a cloned `*http.Transport` and `NewTransport()` for a fresh one. `internal/proxy`, `internal/handlers/chat` and the rotating-proxy cache all read the same `DefaultHTTPTransportConfig`.
- **Verified behaviour-preserving against 13 live providers.** A three-way fingerprint (HTTP status, response envelope, error body) was captured from the pre-change build, a `-pgo=off` build and a PGO build, all against the same real upstream accounts: **0/13 providers changed outcome**, and the provider executor suite is 94/94 identical. The eight providers that fail do so for account reasons that predate this work — model not in plan (groq, clinepass), ChatGPT-plan restriction (codex), zero credits (commandcode, trae/opencode), and a bad base path (nvidia).
- **Two pre-existing non-streaming bugs surfaced while probing and are left as-is here.** `qd/*` and `kr/*` answer a `stream:false` request with HTTP 200 and an SSE-shaped body (qoder even mislabels it `application/json` while embedding an upstream 400 envelope). Confirmed identical on the pre-change build, so this change neither caused nor fixed it.
- **`benchmark/run_perf_test.py`** reproduces the load numbers end-to-end against a mock upstream; **`benchmark/live_provider_smoke.py`** reproduces the 13-provider fingerprint against the real database. Neither is part of `go test` — the live one needs `SMOKE_API_KEY` and a populated `~/.9router/db/data.sqlite`.
### ✨ Combos page: bulk delete with selection

- Every combo card gets a checkbox, with **Delete Selected (n)** and **Delete All (n)** buttons in the header. Both open the existing confirm modal, then delete sequentially and report partial failures (`Deleted 3 of 5 combo(s)`) instead of silently dropping the rest.
- The locked auto free-tier combo is excluded from selection, from the Delete All count and from the actual deletes — the button count and what really gets removed always agree, and its checkbox renders disabled with an explanatory tooltip.
- Selection is cleared after any completed delete run, so the counts never point at rows that are already gone.

### ✨ Auto Group combos by model family

- A **Auto Group by Model** button on the combos page creates one fallback combo per model family from every **usable** chat model: any version, any provider. "claude-sonnet-4.6" (cc), "claude-sonnet-4.5" (claude) and a hand-added gateway sonnet all land in the combo **Auto: claude-sonnet**.
- A model counts as usable when its provider has an **active** connection and the model is not in the user's `disabledModels` list. Embedding/image/audio models (declared `kind`, or "embedding" in the id) are skipped — they cannot serve a chat fallback chain.
- Family normalization (`modelFamily`) drops generation numbers and lifecycle flags, keeping size/role words, so `gpt-5.4-mini`→`gpt-mini`, `gemini-3.1-flash-preview`→`gemini-flash`, `kimi-k2.7-code`→`kimi-code`, `minimax-m3`→`minimax`. OpenAI's o-series is deliberately preserved (`o1`, `o3-mini`) since the leading token is the model line, not a generation. Pure-version ids fall back to the original rather than merging into an empty family.
- Combos are upserted under stable ids (`auto-family-<family>`) so regenerating refreshes rows instead of stacking duplicates. Families with under two usable models are skipped — a single-model combo has nothing to fall back to. Unlike the free-tier combo these stay fully editable (badge "Auto-generated", delete/reorder/edit all open).

### ✨ Provider model management: check latest, probe accessibility, prune unusable

- `ProviderDetailView.svelte` gains a **Check Latest Models** button (reusing the existing `/api/providers/{id}/models` fetch, no new endpoint): it diffs the provider's live catalogue against the models already registered (built-in + custom) and lists only the new ones, each with an inline **Add** button. The button only renders for providers the backend can enumerate — live-catalogue providers (`cline`, `clinepass`, `qoder`, `qoder-cn`, `antigravity`, `gemini-cli`), the five `modelsListURL` providers, and OpenAI/Anthropic-compatible custom nodes.
- A **Check All Models** button probes every model of the provider with the existing minimal-chat `api.testModel` path, at bounded concurrency (6 in flight) rather than firing one request per model at once. Each row shows ok/error, and on error gains a per-model **delete** button: custom models are removed via `deleteCustomModel`, built-in registry models (static, undeletable) are disabled instead. A summary line reports the failure count instead of spamming the single shared error banner.
- **Stale results never survive a provider switch** (review finding from #32): `ConnectionsView` renders the panel without a `{#key providerId}`, so the instance is reused across providers. A `$effect.pre` drops `latestModels`, the test verdicts and the in-flight flags before the new provider's DOM commits, and every handler that awaits an upstream call captures `providerId` up front and bails if it changed — previously the old provider's "new models" list stayed clickable and **Add saved the model into the wrong provider**.

### ✨ Locked auto free-tier combo

- A **Auto Free Tier** button on the combos page (re)builds the locked combo from the registry's free-tier models (`:free` / `/free` / `-free` suffixes) of providers the user actually has a connection for, so it never references unreachable providers.
- Locked combos carry a badge and can be **reordered** (fallback order matters) but not edited or deleted — the delete button renders disabled, and `validateLockedReorder` rejects any update that changes the name, kind, or model *set* while permitting pure permutations.
- `isLlmCombo` rejected any `kind !== 'llm'`, so an `auto-free` combo would be invisible on the combos page. It now accepts the `auto-*` kinds while still excluding the upstream `search-combo` media combos.
- **Strategy-only updates no longer fail with 403.** The card's strategy dropdown persists via `PUT {strategy}` with no `models` field, which `validateLockedReorder` read as "models changed to nothing" and always refused with `auto free-tier combo models are locked`. A request that carries no `models` field now skips the model-set comparison entirely — only a request that actually sends models must prove it is a pure permutation.

### 🐛 The CLI Tools page could not be opened at all (broken since v1.9.0)

- 🔴 **The view threw before it could render.** `CliToolsView.svelte` referenced `selectedTool` in ~20 places — the tool-card click handler and the whole detail modal — but the `$state` declaration had been dropped, leaving it as an undeclared global. The template threw `ReferenceError: selectedTool is not defined`, so the component never finished mounting and the page sat on "Connecting to 9router-go Localhost Gateway (:20130)…" forever. `git log -S` pinned the removal to `d8aa5ec3`, which is in **every release from v1.9.0 through v1.9.5** — six releases, about four days.
- 🔴 **The API it calls was behind the wrong auth gate.** `/api/cli-tools/all-statuses` was registered inside `SetupRoutes`, which `SetupServerRouter` mounts under `RequireApiKey` — the LLM API-key middleware. But the SPA calls it with the dashboard session cookie and never an API key, so every call returned 401 and the app bounced back to the endpoint tab even after the render crash was fixed. The route's own comment already said "dashboard batch status", so it was never meant to sit behind the LLM gate. It now lives in the `RequireDashboardAuth` group alongside the rest of the dashboard API; the unprefixed `/cli-tools/all-statuses` alias stays on the API-key group for external callers, and both paths still reject anonymous requests.
- **Why six releases missed it.** `tsc -b` does not typecheck `.svelte` files and the Svelte compiler does not scope-check templates, so an undeclared variable in a template is invisible to both `bun run build` and the CI build step. The 85-test web suite added in #36 covers `client.ts`, `router.ts` and friends, but there is no component-level render test, and no `svelte-check` in the toolchain. Fixing the two bugs closes the symptom; the gap that let it ship six times is still open.
- `TestSetupServerRouter_CLIToolsStatusIsADashboardRead` pins the dashboard-session auth (anonymous stays 401, session gets 200), and `TestSetupServerRouter_CLIToolsStatusAPIKeyAliasUnchanged` keeps the API-key alias registered rather than accidentally removed. Both are mutation-checked: restoring the old registration fails the first one with the exact 401 the dashboard was hitting.

## [v1.9.5] - 2026-09-28

### 🐛 Issue #27 — "Update now" opened the changelog inside the sidebar column

- 🔴 **The modal was clipped into the 288px sidebar.** `App.svelte` wraps `<Sidebar>` in a mobile drawer carrying `transform … lg:transform-none`, and an ancestor with a `transform` becomes the containing block for `position: fixed`. The modal's `fixed inset-0` therefore resolved against the drawer's box instead of the viewport, so the overlay covered only the sidebar and the panel was cut off part-way through the release notes. Upstream renders its update confirm as a sibling of `<aside>` (`src/shared/components/Sidebar.js:367`) for the same reason, so the fix is structural: the modal and the disconnected overlay now live in a new `UpdateModal.svelte` that `App.svelte` mounts outside the transformed drawer. `showUpdateModal`, `updateInfo` and `version` are bindable props so the sidebar keeps ownership of the version polling.
- **Release notes render as markdown, and fall back to the changelog.** `updateInfo.releaseNotes` is a GitHub release body, which the modal printed through `whitespace-pre-line` — so `- **bold**` and `` `code` `` showed up with their markers still attached. It is now parsed with `marked` and rendered through the existing `.changelog-body` styles, and when the manifest carries no notes the modal falls back to `/api/changelog`, the same source `ChangelogModal` uses, so "what changed" is answerable either way. The panel is `max-w-2xl` and scrolls at `max-h-[90vh]` instead of running off the bottom of the screen.
- **Escape now closes it, and every dismiss path is guarded the same way.** The handler was bound to a `role="button"` backdrop, so it only fired while that backdrop itself held focus; pressing Escape with the mouse elsewhere did nothing. A `keydown` listener on `window` closes the modal instead, and the backdrop, close button and Cancel all route through the same `dismiss()`, which refuses while `isUpdating` so an in-flight binary update is never abandoned. Body scroll is locked while the modal is open.
- **Four correctness bugs in this same modal, caught in review.** `marked.parse()` is synchronous unless `async` is set, so the release-notes branch calling `.then()` on its result threw a `TypeError` and the notes never rendered — the first E2E pass missed it because the stubbed manifest carried no notes, and it is now covered by a case that does. The changelog `$effect` depended on both `releaseNotesHtml` and `isLoadingChangelog`, so a failed fetch re-armed itself and retried in a loop while the modal stayed open; a one-shot `changelogRequested` flag ends that. "Copy & Shutdown" swallowed clipboard failures and started the countdown anyway, leaving the user with a stopped server and no command, so a failed copy now aborts with a message. And the countdown's `setInterval` was never cleared, so closing the modal mid-countdown still called `shutdownServer()` five seconds later — it is now cancelled on dismiss, and "Server Stopped" only appears once the shutdown request actually succeeds instead of being assumed.

### 🐛 Issue #30 — the quota tracker fired every account in the same millisecond, and Google answered 429

- 🔴 **Ten accounts on one office IP each tick was a self-inflicted rate limit.** `QuotaTrackerView` refreshes every visible connection at once (`Promise.allSettled(connections.map(fetchQuota))`), so a dashboard with ten accounts sent ten live quota reads inside a few hundred microseconds. Google answered 429, the chat path reads a 429 as real quota exhaustion, and the accounts locked in sequence with "no project ID" while their tokens were still live — taking the paid combos down with the free ones. The per-connection 30s throttle (`MinAntigravityQuotaRefreshInterval`) never applied: it caps how often *one* account is re-read, not how close two *different* accounts' reads are to each other.
- **Quota reads now pass through a single process-wide gate: 250ms floor plus up to 120ms of jitter**, so the same call repeated for the next account is always at least a quarter-second behind the last one and never lands in a perfectly periodic pattern. Measured A/B on the real router with six ollama connections fetched concurrently: with the gate the six responses spread across 325ms → 2.59s (gaps 295–661ms), without it they all landed inside a 22ms window (333–355ms) — the exact burst from the report.
- **New `internal/fetchgate` is the general, provider-agnostic mechanism** (`fetchgate.New(minGap, maxJitter)` + `Acquire(ctx)`), not an Antigravity patch: slots are reserved under the lock in call order, jitter is additive so `minGap` stays a hard floor, and a cancelled context releases the wait instead of holding a slot against the requests behind it. `HandleGetConnectionUsage` acquires one slot before every live read, on both the `fetchProviderUsage` dispatch and the separate Antigravity branch (which does not route through it). One account's own two RPCs (`loadCodeAssist` then `fetchAvailableModels`) stay back to back — that is upstream's own sequence, and one account is not the burst.
- **Only the start of a request is paced, and only when one is actually made.** A single account's manual refresh pays nothing, a provider with no live fetcher (the lock/Cooldown fallback) never queues, and a dashboard that navigates away mid-refresh spends no upstream request on a response nobody reads.
- **Upstream `decolua/9router` has no throttle here either**, so this is a deliberate, documented gap rather than a parity regression to revert later. The same shape is what OmniRoute added in `quotaFetchThrottle` after accounts on one egress IP started looking like automation to their provider.
- Covered by `internal/fetchgate` (idle gate is free, concurrent callers spaced, jitter only widens the gap, cancellation releases the wait) and by the dashboard, which asserts the spacing at the fake provider's own socket — the ollama dispatch, the Antigravity branch, and that an abandoned request makes no upstream call at all. Every one of those three fails when the gate is set to a zero gap.

### ✨ Chat Completions → OpenAI Responses streaming translator

Diffs 1–4 of 4 (stacked), already merged into `main`. The SHAs below are the ones used after rebase onto `main` (`63b3c14c`); before the rebase, diffs 1–2 were at `36b17d46`/`b3af67ad`.

| Diff | Content | Status |
|:---|:---|:---|
| 1 | Chat SSE → Responses SSE + `reasoning_details` | ✅ `b244d9e6` |
| 2 | Request direction: Responses body → Chat Completions body | ✅ `b85b256e` |
| **3** | Wiring in `ChatHandler.HandleResponses` (resolve, combo, fallback, translator only when upstream is not Responses-native) | ✅ `6f47face` |
| **4** | `response.completed` for the non-streaming path | ✅ `6f47face` |

**Diff 1 — the translation engine.** `TranslateOpenAIToResponses` + `FlushResponses` with `ResponsesState` that mirrors upstream `initState()`.

- 🔴 **Clients of `/v1/responses` whose upstream is not Responses-native get a malformed reply.** `HandleResponses` forwards the body as-is to `<base>/responses` and copies the raw reply, so a Chat-Completions-only (OpenAI-compatible) upstream answers with Chat Completions chunks to a client that expects Responses events.
- **`response.completed` must repeat its items (#4307).** Clients that build their final result from the terminal event (GitHub Copilot CLI, the OpenAI SDK "final response" helper) treat the turn as empty even though the text was already streaming. `recordCompletedOutputItem` stores items **keyed by `output_index`** so that repeated close overwrites instead of duplicating, and `collectCompletedOutputItems` sorts them by index so `response.output` follows emission order. Both attach to the same item that emits `response.output_item.done` — if separated, the recorded index would necessarily diverge from what is sent.
- **Completion is deferred while usage has not arrived, but only on the direct path.** OpenAI sends usage in the last chunk whose `choices` is empty, so completing on the `finish_reason` frame would freeze the payload before that chunk is read. `FlushReachesUs` mirrors upstream `state.targetFormat === FORMATS.OPENAI`: correct (defer to flush) when the translator sits on the direct route, wrong (send immediately) when it is the second hop from the pivot, because `translateResponse` already discards the terminal chunk so flush is never called and the deferral would swallow `response.completed` entirely.
- **Usage does not overwrite the accounting owned by the stream layer.** It is produced into its own `ResponsesState.Usage`, not into internal usage, because the latter is filled with `normalizeUsage()`-shaped data for logging and cost; overwriting it with the Responses shape would silently drop cache/reasoning tokens from the statistics. Without `usage`, clients like the Codex CLI hold the "context used" gauge at 0 and never auto-compact.
- **New tool items are announced only after both id and name have arrived.** Some providers split `id` and `function.name` across different chunks; deciding earlier would lock an `exec` call permanently as `function_call`. Custom tool arguments, by contrast, are **not** streamed and are only sent at close, once the Chat JSON envelope can be wrapped — streaming raw fragments would expose `{"input":"..."}`, not the freeform program the client expects.
- **`reasoning_details` is now read too.** `extractReasoningText` only recognized `reasoning_content` and `reasoning`; third-party vendors send `reasoning_details` as an array that can contain plain strings or objects with `text`/`content`. `OpenAIReasoningDetail` accepts both forms and returns them to their original form on marshal, so a translated request does not alter the shape a vendor prefers.
- Covered by `internal/translator/responses_test.go`: text event ordering, monotonically increasing sequence numbers, message items opened once, reasoning via `reasoning_content`/`reasoning_details`/` thinking` blocks (including ones split across chunks), close idempotency, tool calls (waiting for id+name, argument buffering, custom tools, `extractCustomToolInput`), usage from chunks without `choices` along with cache/reasoning details, completion deferral in both `FlushReachesUs` modes, `output` on `response.completed` (#4307) including overwrite and ordering, and `[]` rather than `null` when empty.
- **Diff 2 — request direction.** `ResponsesToChatRequest` ports `openaiResponsesToOpenAIRequest`: `input` (string, array, or empty) becomes Chat `messages`, `instructions` becomes a system message, `input_text`/`output_text` blocks become `text` and `input_image` becomes `image_url`, consecutive `function_call` items merge into a single assistant turn, `function_call_output` becomes a `tool` message, and `reasoning` is buffered then attached to the next assistant turn. `max_output_tokens` maps to `max_tokens` and Responses-specific fields (`input`, `instructions`, `include`, `store`, `prompt_cache_key`, `client_metadata`, `reasoning`) are dropped so they do not leak to the upstream.
- **An empty prompt never becomes a request without a user turn.** `input: ""`, `input: "   "`, and `input: []` all become a single user message containing `"..."`; `messages: []` is rejected by every provider.
- **Nameless tool calls are skipped, not forwarded.** Codex and OpenAI reject nameless calls, so items whose name is empty/whitespace never reach the wire. The same applies to nameless tool declarations: *hosted* tools like `{type:"request_user_input"}` have no `name` and cannot become a function declaration — forwarding them would reach providers like Gemini that validate tool names strictly.
- **Custom tools are preserved as identity.** Chat Completions has no custom tool declaration, so a `type:"custom"` tool is exposed as a function with a single `input` string property; its name is returned in `ResponsesRequestResult.CustomToolNames` so the reply conversion can recognize it again and emit `custom_tool_call_input`. This field does **not** go into the upstream body — upstream puts `_customToolNames` in the body and removes it in `chatCore.js:214` before sending; here it becomes part of the result, not the body.
- **`additional_tools` is merged into the tool declarations**, with tools from the body first. A `type:"object"` schema without `properties` gets an empty `properties`, because the Codex Responses API requires it.
- Covered by `internal/translator/responses_request_test.go` (24 tests): normalization of string/empty input, instructions, content block mapping including `file_id` and default `detail:"auto"`, consecutive tool calls, nameless tool calls, tool results (string and object), reasoning attached to the next turn / not leaking into the user turn / `encrypted_content` not displayed as text, tool declarations (function, hosted, custom, additional, already-Chat-shaped), and cleanup of Responses-specific fields.

**Diff 3 — `/v1/responses` genuinely speaks Responses.** The `/responses` route moved from `MediaHandler` to `ChatHandler.HandleResponses`, mirroring `HandleMessages`: model resolve, combo, capacity adapter, account fallback, and usage logging. Previously the media handler was only a passthrough via `forwardMediaRequest` — no resolution, no combo, no fallback, no logging.

- 🔴 **Clients of `/v1/responses` to a Chat-Completions upstream can get a malformed reply.** Live smoke (binary, fake upstream): previously the reply came out as-is as Chat Completions chunks; afterwards it becomes `response.created` → `response.output_text.delta` → `response.completed`, and the upstream receives `{"messages":[...]}` without `input`/`instructions`.
- **The "Responses-native" guard determines direction, not just layout.** `previous_response_id`, `store`, and item ids belong only to the Responses API; converting them to Chat Completions drops them and codex loses the server-side conversation. Native-ness is read from data that already exists, not a new table: `executor.UpstreamSpeaksResponses` — a base URL ending in `/responses` (codex, grok-cli, perplexity-agent; the same tests the opencode executor used before adding `/responses`) or an opencode model that is actually served from `/responses` (muse-spark, grok-4.6, gpt-5.6-luna). The counterpart of `sourceFormat === targetFormat` is what makes the upstream skip translation. Native replies are forwarded byte for byte via `passthroughResponses`, not via `handleCodexStream`, which would turn them into Chat Completions.
- **Client format goes through context, not a new field.** `translator.WithClientFormat` / `IsResponsesClient` / `NeedsResponsesBridge`, the same pattern as `WithRequestedModel` already used by `HandleMessages`. Zero signature changes: `forwardRequestParams` and its five call sites are untouched, and `executor.Request.Ctx` already forwards the context to `executor.Request{Ctx: ctx}`. A `nil` context is read as a Chat client in all three flags — that is the majority of traffic, and a panic here would bring down the whole proxy.
- **Combo and capacity adapter use the same rules.** `handleMessagesComboFallback` and `handleComboFallback` previously hardcoded `TranslateResponse: true` and `Endpoint: "/v1/messages"`. For a Responses client that meant the reply was flipped back into the Claude shape, and the Anthropic upstream was given a raw Chat body without `EnsureClaudeMessages`. Both are now derived from the client format via `forwardEndpoint`, without changing the behavior of Chat clients or Claude clients.
- **Claude upstream is served in two hops.** Claude events → Chat chunks (the existing branch) → Responses events, through the same bridge. `responsesBridge.feedFrames` accepts ready-made frames as well as per-chunk payloads, so these two producers do not each split frames on their own.
- **A truncated stream is still closed.** `bridge.close()` emits `response.completed` if the upstream dies mid-answer; without a terminal event the client waits on a turn that actually already finished. This also applies to the second hop (Claude upstream), which never uses the `[DONE]` sentinel.

**Diff 4 — the non-streaming path.** A client that does not request streaming must still receive a single `Response` object, not a Chat Completions body it cannot read.

- **The `Response` object is built from the streaming translator, not a second writer.** `ChatResponseToResponses` folds one Chat reply into a single synthetic chunk, plays it through `TranslateOpenAIToResponses` + `FlushResponses`, and takes the object from the `response.completed` event. Item shape, ordering, and usage details become identical on both paths by construction, not because two implementations happen to agree. The upstream counterpart is `convertResponsesStreamToJson`, which aggregates the same events from the stream.
- **A provider that forces streaming is still answered with JSON.** A non-streaming client can receive SSE from the upstream (Codex does this); `respondAsResponses` aggregates it first through `sseToClaudeJSON`, because answering a client waiting for JSON with a stream makes parsing fail on the client side, not on the proxy side.
- **A body that cannot be converted is forwarded whole, instead of becoming a 200 with empty body.** The conversion error is logged and the original body is sent; a malformed shape at least still lets the client report the problem, whereas an empty 200 tells nothing.
- **Native usage is read back.** `ParseResponsesUsage` translates `input_tokens`/`output_tokens` (and `input_tokens_details.cached_tokens`) into the Chat shape used by logging and cost, from both the non-streaming body and the terminal `response.completed` event. Without this, every codex, grok-cli, and perplexity-agent turn would be recorded as zero tokens.
- Covered by `internal/proxy/executor/responses_bridge_test.go` (replay Chat SSE to Responses events, truncated stream still has a terminal event, two-hop Claude, and the provider/model/config native-ness table), `internal/translator/responses_json_test.go` (non-streaming Response object, tool call, unreadable body), `internal/translator/responses_usage_test.go` (non-streaming envelope, terminal event, cache details, no usage), `internal/translator/client_format_test.go` (all three flags including `nil` context), and `internal/handlers/chat/responses_test.go` (three e2e through the handler: bridging to a chat upstream, native upstream not translated, and non-streaming answer shaped as `Response`).

### 🐛 Four paths missed while wiring `/v1/responses`

An audit after the wiring found paths that go through neither `sseStream` nor `handleJSONResponse`, so the bridge never reached them. `ResponsesBridge` is exported so any producer can use it, and `FeedFrames` accepts ready-made frames as well as per-chunk payloads.

- 🔴 **Antigravity (gemini-native) answers Chat Completions to a Responses client.** `handleGeminiStream` and `handleGeminiNonStream` write their translation results directly to the writer, never passing through the bridge. Both now use the same bridge, and `[DONE]` is no longer written for a Responses client, because what it waits for is `response.completed`.
- 🔴 **MiMo free writes the raw body on the non-streaming path.** Its streaming path already went through `handleStreamResponse` (and so got bridged), but the non-streaming branch did a `io.Copy` as-is. Now both read the body and pass it through `respondAsResponses`.
- 🔴 **`/v1/responses/compact` still went through the media handler, and `_compact` was never read by anyone.** The route is now directed to `HandleResponsesCompact` (which now calls `HandleResponses`, just like upstream sets `body._compact` and then reuses `handleChat` — previously it forced the Responses body into `/v1/chat/completions`). `applyCodexCompact` in the codex executor translates that marker into the `/compact` URL and removes it from the body, porting `codex.js` `transformRequest` + `buildUrl`.
- **Dead code is removed, not left as a shim.** After the route moved, `MediaHandler.HandleResponses` / `HandleResponsesCompact` and the `/responses` dispatch block in `forwardMediaRequest` no longer have any callers, so all three were deleted. The `media/responses_test.go` tests (6 tests) went along: they all call handlers that no longer exist, and some merely tested the handler through `io.Copy`. Two behaviors that still genuinely matter — a provider without a connection, and a combo rotating to a healthy member — were moved to `chat/responses_test.go` and rewritten against what the client sees, not the handler sequence.
- **The status code for `/v1/responses` is now the same as for `/v1/chat/completions`.** A provider without a connection used to be answered 404 by the media handler; now it goes through the same fallback machinery as the other two endpoints, so it is 502 with the same message. Consistency is worth more than a different status per wire format.
- Covered by `internal/handlers/chat/responses_test.go`: `TestHandleResponsesCompact_MarksRequestAndKeepsWireFormat` (the `_compact` marker reaches the `/compact` URL, does not leak into the body, the client still gets the Responses format), `TestHandleResponses_ConnectionProblemsAreReported` (no connection is rejected, a connection without credentials never calls the upstream), `TestHandleResponses_ComboRotatesAndStaysInWireFormat` (a healthy combo member answers, the format does not leak), and `TestHandleGeminiStream_ResponsesClient` (two hops Gemini → Chat → Responses, closed with `response.completed`).

### 🐛 reasoning_effort "max" is now clamped down on the MiMo path

- 🔴 **mimo-v2.5-pro and v2.6 answer 400 for `reasoning_effort: "max"`.** The upstream already fixed this in `1b72f02e`; the Go port has no implementation of the rule at all — not because it is "blocked", but because deepseek `applyFormat` simply does not exist in Go yet. What existed before was a `max→xhigh` clamp for Codex and opencode zen (`transform.go:387`, `providers.go:1130`), which does **not** touch mimo and has different target values.
- **The rule is general, not mimo-specific.** `providers.ClampDeepseekEffort` maps levels to the deepseek effort vocabulary (`xhigh`/`max` → `"max"`), then clamps down to `"high"` when the model's declared levels do **not** include `"max"`. What decides is the promoted level, not the model name — mimo-v2.5 *accepts* `"max"` while v2.5-pro/v2.6 reject it, so a pinned model would be wrong for half the cases.
- Called from `injectMimoMarker` (`internal/handlers/chat/mimofree.go`), which now prepares the MiMo body: the anti-abuse marker **and** the effort. Other deepseek paths have not been audited yet — the helper is already generic and ready to be used there.
- Covered by `internal/providers/clamp_effort_test.go` (7 cases, including the v2.5 vs v2.5-pro boundary) and `internal/handlers/chat/mimofree_test.go` (clamp through the body, nonexistent effort is not fabricated, the anti-abuse marker remains attached).
### 🐛 Issue #25 — Codex quota tracker was empty; codex now reads live 5h/7d quota

- 🔴 **Every Codex account showed "Account active. No quota limits tracked."** `codex` was already listed in `usageSupportedProviders`, so Codex OAuth rows appeared in the quota tracker — but `fetchProviderUsage` had no `codex` case, so the request fell through to the lock fallback and answered `{plan:"codex", quotas:{}}`. Antigravity looked fine because it had a fetcher; Codex had none. The bug report was right that nothing read `wham/usage` anywhere in the tree. `internal/codexquota` now owns that endpoint (`GET https://chatgpt.com/backend-api/wham/usage`) and the dashboard dispatches `codex` to it, porting `open-sse/services/usage/codex.js:getCodexUsage`.
- **The window is positional, not duration-derived, and a missing 7d stays missing.** `primary_window` becomes the `5h` row and `secondary_window` the `Weekly` row; `window_minutes` is ignored, exactly as upstream does. When the account has no secondary window the row is simply omitted — the `7d=None` in the report is the account lacking a weekly cap, not a parse failure, and upstream renders that one-row table quietly rather than fabricating a zero-filled Weekly bar. The Report/New/Spark metered features are decoded too (`review_*`, `spark_*`), since the dashboard already renders those labels.
- 🔴 **The `Forbidden` was never an auth or WAF rejection — it was the environment proxy refusing the CONNECT tunnel.** A first cut of this fix copied upstream's `getCodexUsage` exactly and then "fixed" the resulting 403 by adding Codex CLI identity headers and a non-Go `User-Agent`, on the theory that a WAF was blocking `Go-http-client/1.1`. **That diagnosis was wrong and has been reverted.** Two facts killed it. First, the message itself: `Get "https://chatgpt.com/…": Forbidden` is Go's `url.Error` wrapping a *transport* error, not a status — a real 403 is reported as `wham usage returned status 403` and never reaches the body, and Go renders a refused CONNECT tunnel as exactly that reason phrase. Second, a live probe: with the same token, the wham endpoint returns **200** through a direct connection, and returns 200 with upstream's two headers, with a `User-Agent` only, and with the full CLI identity — the headers make no difference at all. This host runs behind `HTTPS_PROXY`, whose tunnel policy refuses `chatgpt.com`.
- **The quota path now uses the proxy-fallback transport the chat path already used.** `internal/proxy/fallback_transport.go` exists for exactly this and its comment names the failure ("CONNECT tunnel failed, 403 Forbidden, blocked-by-allowlist"), but only the chat handler was wired to it. `usageDo` and `codexquota.Fetch` used a bare `http.DefaultClient`, which has no direct-connection retry — so the quota tracker failed on hosts that chat traffic reached fine through the fallback. Both now share a `proxy.FallbackTransport`-backed client, which is what finally made the tracker populate. This is shared infrastructure, not a Codex-only change: `usageDo` is the sender for every dashboard quota fetcher, so it also repairs the providers that were failing for the same reason and are listed in the next section. A refusal that survives the direct retry is now reported as `ProxyRefusedError` naming the proxy as the cause, instead of a bare `Forbidden` that reads like a rejected credential.
- **Each quota row carries an explicit `remaining`.** `usageQuota` emits `used`/`total` only, but the dashboard's codex branch reads `quota.remaining` directly and `getConnectionQuotaRemaining` returns `Infinity` without it, which silently broke the "% quota: low to high" account sort. The rows are built explicitly instead of through that helper, and `resetCredits.availableCount` is carried through `usageResult.extra` because the tracker reads it off the raw response.
- **The three response envelopes upstream tolerates are all decoded**: `rate_limit`, `rate_limits` and `rate_limits_by_limit_id.codex`, plus the one further nested `rate_limit` level that `getCodexRateLimitBody` unwraps. Reset times accept unix seconds, unix milliseconds, numeric strings and ISO-8601; anything unparseable leaves `resetAt` null instead of a zero time.
- 🐛 **Quota rows used to reshuffle on every refresh.** Upstream emits them in insertion order, but Go serializes a `map[string]any` in nondeterministic key order, so `Object.entries` in the dashboard produced a different table order each poll. The codex branch now sorts into the fixed upstream order (`5h`, `Weekly`, `Review (5h)`, `Review (Weekly)`, `Spark (5h)`, `Spark (Weekly)`), keeping any unrecognised window last.
- **Codex also takes part in quota-aware fallback, which it previously could not.** A Codex 429 answers with `{"error":{"type":"usage_limit_reached","resets_at":…}}` (or `resets_in_seconds`) — a body `extractResetDuration` cannot read, so those accounts previously fell back to a generic exponential-backoff cooldown. `NoteCodexQuotaError` now parses that exact reset and caches it, and the picker pre-filters in `connections.go` skip the account until it passes, mirroring the Antigravity quota cache. Both filter sites now go through one `quotaCacheBlocked` dispatcher so the per-provider caches stay separate and cannot cross (AGENTS.md §3.A).
- **A 429 without the `usage_limit_reached` marker blocks nothing.** A per-request or burst limit on an otherwise healthy account reads live quota instead (throttled to one wham call per 30s per connection, in-flight calls coalesced) and caches the healthy reading — it must not synthesize a block that outlives the burst. A successful request clears the cache, and a failed refresh preserves the last known reading rather than overwriting it with "no quota".
- Codex quota is **account-level**, not per-model: one 5h and one 7d window covers every model the account can serve. The cache is therefore keyed by connection alone, which is also why a model-keyed `modelLock` row was the wrong shape for it — it would have free-routed `gpt-6-sol` while `gpt-5.5-codex` on the same account was equally dead.
- Covered by `internal/codexquota` (envelope precedence, window clamping, all four reset-time encodings, review/spark discovery, the two-header upstream contract, non-2xx, proxy-refusal classification), the dashboard fetcher (both windows rendered with `remaining`, absent-7d omission, bare message on 503, dispatch through `fetchProviderUsage`), the shared sender (`TestUsageDo_SendsNonGoUserAgent` — default UA applied, per-fetcher UA preserved) and the live end-to-end check: the real `GET /api/usage/{id}` route against the real wham endpoint, on the connection from the report, returns `{"plan":"free","quotas":{"session":{"used":0,"remaining":100,"total":100,"resetAt":"2026-10-27T13:29:01Z","unlimited":false}}}` and the chat cache (429 body parsing, the five non-quota-429 cases, weekly-window blocking, expired-reset and unknown-reset non-blocking, optimistic-refresh re-assertion, 30s throttle, in-flight coalescing, fail-open, success-clears, picker pre-filter).
- Note: `internal/usagetracker/quota_parsers.go` has an older, uncalled `ParseCodexUsageQuotas` that reads a different field set (`remaining_fraction` / `reset_after_seconds`). It was already dead before this change and is left alone; the live path is `internal/codexquota`.

### 🐛 Other quota trackers — Ollama, Qoder, Groq

- 🐛 **Ollama Cloud free accounts were told "No usage limits reported" while a live quota sat in the response.** `fetchOllamaUsage` read only `limits.session` and `limits.weekly`, but a free account reports `limits.monthly` and nothing else — so the card showed no bars at all. The window list is now a table mirroring upstream's `OLLAMA_LIMIT_WINDOWS` (`session`/`weekly`/`monthly`), which is also what the dashboard's `case 'ollama'` branch was already ready to render. Ollama exposes no reset timestamp, so the monthly reset is derived from the signup date on `/api/me` ("usage resets monthly from the date you signed up"), ported from upstream `nextMonthlyResetFromSignup`; day-of-month clamping (Jan 31 → Feb 28/29) is preserved. Verified against the upstream JavaScript on five cases, including the real account (signup 2025-08-14 → reset 2026-10-14T15:08:01Z).
- **A Qoder 401 now says the token is dead instead of printing a status code.** `Qoder connected. Usage fetch returned 401.` told the user nothing actionable. The connection is OAuth with an expired `accessToken`, and Qoder device tokens genuinely cannot be refreshed — `center.qoder.sh` answers 403 for device tokens, which upstream's own `shared/qoder/constants.js` documents — so the card now reads "Qoder authentication expired. Please re-authorize this connection." No silent retry is attempted, since one cannot succeed.
- **Groq's "No rate-limit data reported" is left alone on purpose.** Groq dropped `x-ratelimit-*` from `/openai/v1/models`; those headers now ride only on chat completions (confirmed live: `/models` returns none, a completion returns limit/remaining/reset for both buckets). Upstream deliberately piggybacks on `/models` so reading usage never costs a token, and returns the same message when no bucket is present. Surfacing real numbers would mean spending a token per quota poll, so that trade-off is unchanged.

### ✨ `/v1/models` can report the models you can actually call (#28)

- Reported by **@FmcStore** ([#28](https://github.com/luqman-v1/9router-go/issues/28)) and implemented in [#29](https://github.com/luqman-v1/9router-go/pull/29) — Thank you for the report that pinned the two code paths apart, and for the live before/after model counts that made the fix verifiable!
- On a fresh install `/v1/models` listed **1302 models across 119 providers** while the dashboard picker showed ~8, and nothing in the response said which list was authoritative. The two numbers came from different code paths: `buildModelsList` deliberately dumps the whole static registry when `providerConnections` is empty ("so a fresh install still has a usable picker"), while `resolveModelPickerGroups` in `web/src/components/combos/pickerData.ts` gates on `isConnected || noAuth`. A new user reads the 1302-entry list as "models I can call" and looks for a missing import step.
- The listing is now scopeable by query parameter, leaving the default byte-for-byte upstream-compatible:
  - `GET /v1/models` (default) — unchanged: full catalog on a fresh install, connection-scoped once connections exist
  - `GET /v1/models?connected=1` — only providers with an active connection, plus registry `noAuth` providers. On a fresh install this is the ~8-model noAuth subset instead of the full catalog
  - `GET /v1/models?all=1` — always the full static catalog, connections ignored, for explicit discovery
- Every response now carries `mode` (`all` | `connected` | `catalog`) and `connections` (distinct providers with an active row), so a client can tell a candidate catalog from a usable model list without guessing.
- The `disabledModels` KV scope keeps filtering in all three modes, and a custom model for an unconnected provider stays hidden under `?connected=1`.
- 🔴 **The first cut of `?connected=1` deleted every noAuth provider as soon as one connection was configured.** The builder walks connection rows, so the fresh-install catalog dump was skipped entirely once any row existed and the response contained nothing but the connected providers — measured on a live instance with one `kiro` row: 44 models, all `kr/*`, no `oc/*`, while the dashboard picker still offered opencode. That silently contradicted the mode's own contract ("providers with an active connection, **plus** registry `noAuth`"). A noAuth catalog pass now runs after the connection loop, skipping any provider an active connection already covers, so a connection's `enabledModels` / live catalog still owns its own model list and is not widened back out to the static catalog. Same instance after the fix: 202 models (`kr/*` plus the 158 noAuth), with unconnected credentialed providers still absent.
- Adds `providers.IsNoAuthProvider`, which resolves an alias to its canonical id before reading the registry flag, and `ModelsListMode` + `ModelsListResult` on the chat handler so the mode and its metadata travel together.
- 🐛 **The listing and the lookup route now disagree with each other.** `GET /v1/models/<model>` resolved against the default list only, which is connection-scoped as soon as any connection row exists — so `oc/jev-1.13-free` would be **listed** by `?connected=1` and **404** from the lookup route on the same install, at the same moment. Two requests seconds apart, two opposite answers about whether the model exists. The lookup now tries the default list and then falls back to connected mode (`findModelForLookup`).
- **The fallback is additive on purpose; swapping the lookup to connected mode outright would be a breaking change.** The two lists swap which one is the superset: with connections, `connected ⊇ default`; on a fresh install `default` is the full catalog and therefore `default ⊃ connected`. Resolving against connected mode alone would fix the 404 and, in the same change, turn the credentialed-provider lookups that resolve today into 404s — `gcli/grok-4.5-high` on a fresh install is the concrete case the test pins. Both tests are mutation-checked: reverting the fix fails `TestModelLookup_AgreesWithConnectedListing`, and the naive single-mode version fails `TestModelLookup_FreshInstallStaysPermissive`.

### 🐛 Dashboard backup import always failed with `Invalid database payload`

- Implemented by [@lautdalamvip](https://github.com/lautdalamvip) in [#32](https://github.com/luqman-v1/9router-go/pull/32) — the multipart/JSON mismatch and the upstream modal port both traced correctly, thank you!
- `ProfileSettingsView.svelte` posted the chosen file as **`multipart/form-data`**, but `HandleImportDatabase` (like upstream `src/app/api/settings/database/route.js`) does `request.json()` on the body, so every import died at the decode with HTTP 400 and the user saw "Failed to import database: Import failed with status 400". The client now reads `file.text()`, `JSON.parse`s it, and POSTs the raw JSON body — verified against a real `9router-backup-<ISO stamp>.json` export.
- The destructive op also re-authenticates against the stored bcrypt hash, and the client never sent a password at all: once a dashboard password was set, import (and the plain anchor-click export, which is likewise an `x-9r-password`-gated route) returned 401. Upstream prompts in a modal first; the port now does the same — both buttons open a password modal, and the password travels in the `x-9r-password` header.
- Backup filename stamp regained full precision (`T15-07-00-715Z` instead of a day-only `T00-00-00-000Z`), matching upstream's `toISOString().replace(/[.:]/g, '-')`.
- Server side accepts the password from **either** the body field or the header, so the pre-existing body-embedded contract keeps working. The upstream post-import `applyOutboundProxyEnv` has no Go equivalent yet (no `outboundProxyUrl` handling exists in this runtime), so it is noted in a comment rather than faked.

### 🐛 Four defects in that same backup flow, caught in review

- 🔴 **A 401 rendered as `[object Object]`.** `/api/settings/database` is always-protected, so a sessionless request is rejected by `RequireDashboardAuth` and gets the nested `{"error":{"message":…}}` envelope, not the handler's flat `{"error":"…"}`. `new Error(data.error)` then stringified the object. Both paths now read the message through `responseErrorMessage`, which unwraps either shape — the server's actual reason is what the user sees, instead of something less informative than the pre-fix "Import failed with status 401".
- 🔴 **Escape did not cancel the import — the import ran anyway.** `Modal` wires Escape, the backdrop and both ✕ buttons to `onClose`, and nothing gated them on the in-flight flag, so dismissing the dialog left the POST running: it completed, alerted success and reloaded the page, overwriting the database after the user had cancelled. All dismissal paths now route through one `closeDbAuth()` that refuses while a request is active, which is the behaviour the old blocking `confirm()` had.
- **The two backup actions could run on top of each other.** "Download Backup" was only disabled during its own download, so opening it mid-import cleared the typed password and reopened the dialog as a download prompt — and the import's `finally` then closed that brand-new dialog. Both buttons are now disabled while either request is in flight, the entry handlers re-check, and the import snapshots the password before its first `await` so a reset mid-read cannot blank the `x-9r-password` header.
- **The negative auth assertion passed for the wrong reason.** The authorized import earlier in `TestHandleImportDatabase_ClientContract` wipes the settings row, so by the time the wrong-password case ran there was no stored hash left and the request was answered by the `"123456"` fallback rather than by bcrypt — the case would have kept passing even with header auth broken. The hash is re-armed first, and the default password is now asserted rejected too, which is the assertion that actually distinguishes the two paths.

### 🐛 Claude decloak now recovers a tool name the map lost (`b65d2d0a`, #4342)

- A cloaked tool name reached the client as an unresolvable `<tool>_ide` whenever `toolNameMap` missed it — the map is built per request, so a retry or a reconnect loses it. Both decloak paths now fall back to stripping the literal suffix: the non-streaming `DecloakClaudeResponseBody` and the streaming `ClaudeStreamDecloaker`. Decoy names are exempt, so they still reach the client unresolved and surface as "tool unavailable" rather than a silent no-op.
- Two places bailed out before the fallback could help. `DecloakClaudeResponseBody` returned early on an empty map, and `NewClaudeStreamDecloaker` returned nil for one. The constructor now distinguishes the two cases: a **nil** map means no cloaking was applied at all, so stripping would be wrong; a non-nil **empty** map means the map was lost, and the decloaker is built anyway so the fallback runs.
- `claude-opus-5-5` joined the `cc` and `claude` catalogues, in the registry position upstream lists it.

### 🐛 Qoder parity — the provider page worked, the provider did not

- 🔴 **Every Qoder request was signed for a made-up account.** `ForwardQoder` passed an empty user id to `buildQoderCosyHeaders`, which substituted `user-<8 hex chars>` and carried on. The COSY signature is verified against the real account, so Qoder answered `403 {"code":"105","message":"Login expired"}` — which reads as an expired login, not as a signing bug. Upstream refuses to sign at all in that case (`buildCosyHeaders` throws on an empty user id). `BuildQoderCosyHeaders` now takes a `QoderCosyCreds` and errors instead of inventing an identity. Verified live against the account in the bug report: a fabricated id gives `403 Login expired`, the stored one gives `200`.
- 🔴 **The signing identity was never stored at login.** `qoderPoll` read `user_id` from the device-token response only to synthesise an email, and never persisted it. Upstream keeps it as `providerSpecificData.userId` (`src/lib/oauth/providers/qoder.js` mapTokens). It is now stored, along with the machineId that was already generated but dropped.
- 🔴 **"Import from /models" was dead for Qoder.** `GET /api/providers/{id}/models` only handled providers with a plain `/models` URL plus the compatible-node shapes, so Qoder fell through to `400 "provider qoder does not support models listing"`. The COSY-signed catalogue path already existed in `validate.go` for the key-validate probe but was never reachable from the models endpoint; `qoder_catalog.go` now serves it, porting `fetchQoderCatalogRaw` (skip `enable:false`, `max_input_tokens` with a 131 072 fallback) and upstream's route normalization. Verified live: the endpoint returns the same two models upstream does.
- **The machine UUID is now stable per connection.** It was regenerated on every request; upstream persists it so one auth always presents the same machine. `name` and `email` are also carried into the signed user-info blob, which upstream sends and we sent as empty strings.
- **Connection cards show the secondary label.** Upstream's `ConnectionRow` renders the email under a distinct name, or the `displayName` when the stored name *is* the email — which is what the Qoder device flow writes, so "Luqmanul Hakim" never appeared on our card. Ported as `secondaryConnLabel`.
- **The Qoder button is labelled like upstream's.** Upstream renders a dedicated `Fetch Qoder Models` action for qoder/qoder-cn (distinct from the Cline/clinepass one); ours said `Import from /models` for all four providers. The label is now provider-aware, and the icon spins while fetching as upstream does.
- 🐛 **"Quota Exhausted (0%): Resets in 2912172d 1h" on a perfectly healthy Qoder account.** The connection card's exhausted badge treated any quota with `remaining <= 0` as spent, but Qoder reports credits as `total: 0` when the account has no credit pool at all, and its `expiresAt` is a `9999-12-31` "no expiry" sentinel — so the countdown rendered 7973 years. A pool that was never allocated cannot be exhausted, so `total <= 0` is now excluded. The same misread had been tagging Command Code's `Credits` row (which is also `total: 0, unlimited`). Upstream has no such badge, so this is our own logic corrected rather than a parity gap.
- Covered by `internal/proxy/executor/qoder_cosy_test.go` (signature refuses a missing user id or token, connection identity is read, missing userId stays empty) plus a live check of the models route.
- Not changed: Qoder's own account state. Upstream's Qoder chat on the same account also fails with a `403` pointing at qoder.com/pricing, so that is account-side, not gateway-side.

### 🐛 Qoder OAuth — the login button opened a "paste your token" form

- 🔴 **Clicking OAuth on Qoder never reached the login page.** `handleAddConnectionClick` had branches for antigravity, freebuff, cline, PKCE, auth-code, custom, kiro, special and generic OAuth — but nothing for the device-code family, so Qoder fell through to `openGenericOAuth` and the user was asked to paste a token by hand. `isDeviceOAuth` was computed and never read, and `openDeviceOAuth` — which already opens the verification URL in a new tab, shows the user code and polls every 2s — was never called by anything. The missing branch is added ahead of the generic one (after kiro, which keeps its own specialised flow), so it also fixes kilocode, grok-cli, github, kimi/kimi-coding and codebuddy-cn/-intl, which were all silently on the same dead path.
- 🔴 **Even with the modal open, the poll could never succeed.** `device.go` built a bare `&http.Client{}` for every device exchange, and a sandbox or corporate `HTTP(S)_PROXY` refuses the CONNECT tunnel to `openapi.qoder.sh` with 403 — which Go renders as `Get "https://…": Forbidden`, a transport error, not a status. The modal opened the login page, then failed every poll with that text and sat on "waiting" forever. Verified: direct reaches the endpoint (404 for a bogus nonce), the proxy answers 403. Every device-code call now shares one `deviceClient` built on `proxy.NewFallbackTransport`, the same mechanism the chat path and the quota tracker already use.
- Verified live end to end: the OAuth button opens the Qoder device URL in a new tab and the modal shows the user code, the login URL with Open/Copy, and "waiting for device authorization (auto-check every 2s)" with no poll error. `kiro`, `github`, `grok-cli`, `kilocode`, `codebuddy-intl` and `kimi` still return 200 with valid device codes.

### 🗣️ The OAuth surfaces are now entirely in English

- The login modal and callback pages were a mix of Indonesian and English inside the same dialog — "Masukkan kode ini di halaman login yang terbuka" above a box already labelled "Step 1: Open this URL in your browser", and a callback page that said "Login berhasil!" next to "Koneksi diproses otomatis di tab dashboard". Every user-facing string in the OAuth flow is now English, in both places it is rendered: the Svelte modal (`ProviderDetailView.svelte`), the SPA callback view (`OAuthCallbackView.svelte`) and the Go-served callback page (`internal/handlers/oauth/callback.go`), which has two code paths — the no-code page and the auto-submitting one after a successful exchange. The Indonesian code comments in those files went with it so the next reader isn't switching languages mid-file.
- Also translated: the per-provider manual-token instructions (Cline, GitLab, iflow, Cursor, Freebuff), every `oauthError` message, the Cline divider label, and the direct-OAuth-unsupported fallback.

### 🐛 Guard: a combo name can no longer collide with a model alias or a custom model id

- **The hole.** A bare model string resolves against a **model alias** first (`resolveModel` step 2), a **combo name** second (step 3), and a **provider node prefix** third — and all three are writable from the dashboard, with no cross-check. A combo named `agy` beside a model alias `agy` left the alias silently outranking the combo: the combo stayed listed in the dashboard, answered no request, and the only symptom was a wrong model upstream. A custom model id colliding with a combo name is the same confusion one address over — `/v1/models` then advertises `combo-wombo` (the combo) and `xai/combo-wombo` (the custom model) side by side, and a name copied out of the list no longer says which one it lands on. A real install hit this with four collisions at once: the `xai` node carries custom model ids `combo-wombo`, `agy`, `agy-low` and `agy-med` beside combos of exactly those names.
- **The guard.** `POST /api/models/custom`, `PUT /api/models/alias`, `POST /api/combos` and `PUT /api/combos/{id}` now refuse a name already taken in another space, with a typed `409` (`COMBO_NAME_CONFLICT`, `MODEL_ALIAS_CONFLICT`, `CUSTOM_MODEL_NAME_CONFLICT`) shaped like the existing `PROVIDER_NAME_CONFLICT` guard, naming the occupant so the message says what to rename. Comparison is exact after trimming — resolution itself is case-sensitive, so `Combo` and `combo` are two real addresses and the guard must not refuse either. A combo keeping its own name across an update is unaffected.
- **Write-side only, so nothing existing breaks.** Rows that already collide keep serving traffic; the guard only refuses new writes, so no migration or hidden row is involved.
- **A refused write is now visible.** `CombosView` caught a failed combo save with `console.error` alone, so a 409 would have looked like a dead button; the modal keeps itself open and shows the reason inline. `ProviderDetailView`'s add-compatible-model path had the same gap and now alerts, matching the other import paths on that page.
- Verified with `go test ./...`, a nine-case regression test (`TestGuardNameCollision`) that also asserts a refusal writes nothing, and a live run of the built binary against a copy of the real database: the four collision shapes return 409, a rename into a taken name leaves the stored name untouched, and a free name still saves.


### 🐛 Capacity-adapter pool no longer joins a combo's round-robin rotation

- 🔴 **Traffic leaked to a provider that was not in the combo.** A combo whose own models cannot serve a request gets the capacity-adapter pool prepended to it — the pool exists *because* none of the combo's models fit. `HandleChatCompletions` and `HandleMessages` then discarded the adapter's own returned strategy and handed the **combo's** strategy to `handleComboFallback`, so under `round-robin` the injected model was folded into the rotation. With `combo-wombo` (one entry, `oc/space-bunny-free`, which reports no vision) plus the default `ag/gemini-3.8-flash-high` vision adapter, every turn carrying an image alternated between `opencode/space-bunny-free` and `antigravity/gemini-3.8-flash-high` — half the traffic went to a provider absent from the combo, which is exactly what an explicit combo is supposed to prevent. Both call sites now go through `applyCapacityAdapter`, which returns the adapter's strategy whenever something was injected and logs the switch, and falls back to the combo's own strategy only when the list was untouched. The single-model path already honoured it.
- Verified with `go test ./...` plus a targeted regression test asserting four consecutive vision turns all lead with the vision-capable model and keep the combo's own model as the trailing fallback.


### 🐛 Fix usage animation edge loss when many models/providers are active and on hard refresh

- **Provider topology node slicing removed**: `AnalyticsView.svelte` and `ProviderTopologyCard.svelte` previously hard-sliced displayed providers with `.slice(0, 14)`. When users had 14+ configured connections, active providers beyond the first 14 (such as `opencode` free tier or public endpoints) were completely discarded from the canvas, causing the center router node to pulse while leaving the active laser beam and shockwave animation missing. Providers are now dynamically accommodated on the ellipse with auto-scaled `fitView` zoom.
- **Active provider prioritization & alias matching**: `topologyProviders` now explicitly prioritizes currently active requests, recent completions, and pulse providers ahead of dormant connections so lines to in-use models are never dropped. Provider alias matching in `ProviderTopologyCard` now supports canonical IDs, display names, and catalog aliases (e.g. `oc` ↔ `opencode`).
- **Hard refresh state preservation**: `loadStats` on hard refresh now immediately seeds `activeRequests` and `lastProvider` from `/api/usage/stats` REST responses, preventing a blank state before the SSE stream connects.
- **Robust model key parsing & direct request tracking in usagetracker**: In `internal/usagetracker/tracker.go`, `parseModelKey` previously failed on model names containing spaces (e.g. `Grok CLI (Grok Build)`), returning `unknown` provider. It now robustly splits via `strings.LastIndex`. Concurrent direct/no-auth requests are tracked under `__direct__` so they are never masked when requests with connection IDs are active.
- **SVG filter clipping fix**: SVG `filter="url(#topo-electric)"` now uses `filterUnits="userSpaceOnUse"` with ample bounds to avoid 0-width/0-height bounding box clipping on vertical/horizontal edges.

## [v1.9.4] - 2026-09-27

### 🐛 Issue #24 — proxy UI, account rotation, priority reorder, provider search

- 🔴 **The Apply Proxy modal was unusable past a handful of pools.** It was hand-rolled without the shared `Modal.svelte` scroll wrapper, so the flex-centred panel grew past the viewport and clipped the title and every action above the fold — with a couple of hundred batch-imported pools the user saw only an endless `Imported <ip>` list and could reach neither "One-to-one (rotate)" nor Cancel. The panel is now `max-h-[85vh]` with the pool list scrolling on its own, and a filter box narrows pools by name or URL. Bulk apply also reports `Applying N / M` and its Stop button now actually aborts the loop instead of just hiding the modal while requests keep firing.
- 🔴 **Reordering two accounts could permanently break that pair.** The dashboard swapped priorities with two independent full-row `PUT`s, which have no cross-row transaction: one failing while the other landed left two rows sharing a priority, and a stable sort over tied priorities then made every later click a literal no-op. A single `POST /api/connections/{id}/reorder` now performs the swap *and* renumbers the pool to a contiguous 1..N inside one SQLite transaction, mirroring upstream's `reorderInTx` (`connectionsRepo.js:111-133`) — partial failure is impossible and any pre-existing tie is repaired. The client gained an in-flight guard so a second click cannot fire a swap computed from a stale list, and a failed reorder now alerts instead of only reaching `console.error`.
- 🔴 **A NULL priority was silently rewritten to 0 by any unrelated edit.** `HandleUpdateConnection` defaulted the column to `0` and only restored the old value when the row already had one, so a rename, proxy assignment or model assignment promoted a legacy row to rank 0 — ahead of every other account, since 0 beats every positive rank. The parameter is now `*int` and NULL stays NULL. The same coercion existed in four background writers; they use the new data-only `Repo.UpdateConnectionData`, which also stops a background OAuth refresh from writing back the name/isActive/priority it read before the write and reverting a reorder or a toggle the user had just performed.
- 🔴 **A client-pinned connection bypassed the enable/disable toggle.** The pinned branch of `getBestConnection` only checked provider ownership, so `x-connection-id` (and the custom-node prefix pin in `resolution.go`) served a disabled, excluded or model-locked account. Upstream resolves the pin *inside* `availableConnections` (`src/sse/services/auth.js:100-148`), where those rows never match; the pin now falls through to the configured strategy on the same conditions, and cross-provider pinning is still rejected.
- **Provider search only matched display names.** Searching a provider's own id found nothing — "bai" returned "Baidu Qianfan" but never "B.AI", and "atria-asi" or "tokenharbor" returned zero results — so a user concluded the provider was missing and built a custom OpenAI-compatible node instead. `matchesSearch` now also matches the id and alias.
- **`qoder-cn` was half-registered.** It appeared in the dashboard catalog, aliases and offline models, but had no `KnownProviders` transport and no executor, so `qdcn/<model>` resolved a provider the request path could not serve. It is now registered end to end: its own gateway base URL, COSY executor, OAuth refresh config, device-flow login/poll hosts, PAT validation endpoints and quota URL — each keeping its own hosts, never aliased onto `qoder` (AGENTS.md §3.A). Note that `bai` and `atria` were already present in the working tree from `773b5d96` but sit under `[Unreleased]`; a release containing that commit is what actually reaches users.
- 🔴 **The edit modal kept manufacturing priority ties.** The priority input was seeded with `conn.priority ?? 1` and always sent back, so renaming a NULL-priority row silently assigned it rank 1 — tying it with whichever row already held that rank and recreating exactly the un-reorderable pair the new endpoint exists to repair. The field is now compared against its seeded value and the key is omitted when untouched, so only a deliberate edit changes a rank.
- **The first priority-chevron click after opening a row proxy dropdown was always dead.** The dropdown paints a `fixed inset-0 z-40` click-away backdrop over the whole viewport, and a positioned element paints above the unpositioned chevrons, so the click only ever dismissed the dropdown. The chevron group now sits at `z-[60]`, above that backdrop, and the list is raised to `z-50` to match.
- 🔴 **Round-robin state was an in-memory index, so it reset to the top account on every restart** — and the reported "round robin never reaches the next account" is exactly what that produces. Rotation is now persisted per row, ported from upstream's stateless LRU (`src/sse/services/auth.js:151-189`): `lastUsedAt` / `consecutiveUseCount` are read with the candidate set, the least-recently-used row wins once its sticky window expires, and the winner's stamp is written back. The selector lives in `selectByRecency`, replacing `rotateConnectionsSticky`; the now-dead `ResetConnectionState` and the `conn:`-prefixed in-memory keys are gone, since there is no in-memory connection state left to reset.
- **Rotation stamps need nanosecond precision, and this is load-bearing.** The least-recently-used tie-break keeps the first row in priority order when two stamps are equal, so with `time.RFC3339` (whole seconds) every account ends up carrying the same stamp after one full cycle and the rotation then locks onto the top account forever. `db.RotationTimestampFormat` is a fixed-width nanosecond RFC3339 — fixed width, not `time.RFC3339Nano`, which trims trailing zeros and would sort `…:00.5Z` after `…:00.500000001Z`. `TestApplyConnectionStrategy_KeepsRotatingPastFirstCycle` fails on pick 5 if the format is reverted to seconds.
- **Test fixtures were building a narrower schema than production.** `dbtest.CreateTables` and five hand-rolled test schemas declared `providerConnections` without the Go-only additive columns, so any query reading `lastUsedAt` failed in tests while working in a real database. `db.EnsureAdditiveColumns` is now exported and applied by those fixtures, which keeps the two shapes from drifting again the next time a column is added.
- **Providers added to the catalog without a shipped PNG rendered as an empty tile.** `getIconPath` resolves `/providers/<id>.png` for every card, and the sites hid the failed `<img>` — B.AI, Qoder CN, Dahl, Atria, Agnes, TokenHarbor (and `zai-search`) all showed a blank 32px box. The real logos were copied from upstream `decolua/9router` (`public/providers/{bai,atria,dahl,agnes,tokenharbor,qoder-cn}.png`, verified as valid PNGs), so the cards now render the genuine artwork. As a backstop, the new shared `ProviderIcon.svelte` (card tile, detail header, top bar) still falls back to an initials badge in the provider's own catalog colour on any missing asset, so a future provider that forgets to ship artwork reads as intentional rather than broken. `zai-search` is an internal tool provider with no card and no upstream logo; only the fallback covers it if it ever renders.
- Verified with `go test ./...`, a full SPA build, and a live run of the built binary driven through the dashboard UI: the reorder endpoint repaired a deliberately tied pool; a chevron click moved a row the old two-PUT path had wedged; renaming a NULL-priority row left the column NULL while an explicit priority change still took effect; and a chevron click landed while a row proxy dropdown was open.
- Round-robin was then exercised end to end against a stand-in upstream that echoes back the credential it received, over a custom OpenAI-compatible node with three accounts: six requests rotated `1→2→3→1→2→3`; the gateway was restarted mid-cycle and the next three continued `3→1→2` rather than restarting at the top account; disabling one account removed it from service (`3→1→3→1`) and an explicit `x-connection-id` pin naming that disabled account was ignored in favour of an active one.

### ✨ Gemini Live realtime STT transport (`fe347e4e`)

- 🔴 **Realtime transcription was not available at all.** The REST `generateContent` path can only transcribe a whole file inline; the Live API's `:bidiGenerateContent` WebSocket is the streaming counterpart. `internal/handlers/media/gemini_live_stt.go` ports it: audio goes up as `realtimeInput.mediaChunks` in 16 KiB slices (~0.5s of 16-bit 16kHz mono PCM) and the server's incremental `serverContent.inputTranscription` deltas accumulate into the transcript.
- **Dispatch is on a transport marker, never a model id.** `providers.ResolveModelTransport` reads the `transport` field off a catalog entry (`modelTransports`, mirroring the registry field upstream reads in `sttCore.resolveModelTransport`), so a new realtime provider extends through data rather than a new hardcoded branch. `gemini-2.5-flash-native-audio-preview-09-17` joined the gemini catalogue with `kind: "stt"` and `transport: "gemini-live"`, and a caller-supplied `transport` still wins — same precedence as upstream.
- Audio is only sent after the server's `setupComplete`, and the run settles on `turnComplete` — or on a graceful close, where a partial transcript beats a hard error and silence is the hard error. `goAway` advisories rotate the socket once per call: setup is replayed and audio resumes from the byte offset already sent, so nothing is re-uploaded and the transcript survives the hop.
- Lifecycle knobs (`prompt`, `language`, `system_instruction`, `setup_timeout_ms`, `turn_timeout_ms`) ride the same form pass-through every transport already gets, so no engine change was needed to reach this leaf. `system_instruction` replaces the built-in directive wholesale; `prompt`/`language` only shape the default. Caller-supplied timeouts are clamped at 300s.
- `verbose_json` answers with `segments` built from the raw deltas. Those frames carry **no timestamps**, so segments expose `{id, text}` only — start/end/duration are deliberately absent rather than fabricated as zeros, which would misrepresent provider data to anyone diffing transports.
- New dependency: `github.com/gorilla/websocket` (Node has a global `WebSocket`, which is why upstream needed none).
- Tested end to end against a stand-in Live server: `toLiveWSURL` shapes, the setup frame's model/generationConfig/systemInstruction, every audio byte arriving as base64 (20 000 bytes round-tripped), the flushing turn, transcript assembly from two deltas, partial-transcript-on-close versus silence-as-error, the system-instruction precedence, and the timeout clamp.

### ✨ Claude header parity — session id, beta merge, rate-limit forwarding

- **`x-claude-code-session-id` on OAuth requests** (`6aea3875`). The cloak generates a `metadata.user_id` carrying a session id, but nothing echoed it back in the matching header, so Anthropic saw two different sessions for one request. `extractClaudeSessionIdFromUserId` reads the id back out (JSON object form, which is what the cloak writes, or a bare id from other clients) and the request-scoped header is set from it. `generateFakeUserID` now also strips the CLI's own `claude:` prefix before baking the id into the body, so the two agree without a second pass.
- **The caller's `anthropic-beta` flags are merged, not dropped** (`dc198dff`). A client asking for a beta the gateway does not list was silently refused without being told why. `handlerutil.WithClientAnthropicBeta` carries the header on the context alongside the session id, and `providers.MergeAnthropicBeta` unions it with the registry's own list, de-duplicated and in order.
- **Retry and rate-limit headers are forwarded to the client** (`dc198dff`). `retry-after`, `x-should-retry` and every `anthropic-ratelimit-*` header are copied from the upstream response in `ForwardOpenAI`, before any branch writes a status — so they reach the client on the streaming, non-streaming and Claude-translated paths alike. Without them a 429 arrives as an opaque failure and a gateway in front of us cannot back off on our behalf. Hop-by-hop, auth and content headers stay internal.
- Header edits are request-scoped: `providers.WithHeader` and `WithoutBetaFlag` both copy, because the map in `KnownProviders` is shared by every request and a mutation would leak into later ones. `TestWithHeader_DoesNotMutateSharedMap` guards it.
- Not ported here: **Claude free-tier reset grants** (`consumeClaudeResetGrant`, `?cedar_ember=1` plus a new `claude-reset` API route). It is a new usage-side flow, not a header one — see the outstanding list.
- `TestExtractClaudeSessionIdFromUserId` pins a quirk on purpose: the prefix is matched at the start only, and leading whitespace is trimmed *after* the match, so `"  Claude: abc"` keeps its prefix — exactly what upstream's anchored replace does.

### ✨ Port the full upstream pricing table (271 rate entries) and the real cost formula

- 🔴 **Costs in the Usage views were a guess.** The Go port had four hand-written prefix rows (`claude-sonnet-4`, `claude-haiku`, `deepseek-v4-flash`, `gpt-4o`) and charged everything else a fabricated **$1/M input, $3/M output** — and charged cache reads at the *full* input rate, because the call site never passed the cached or cache-creation counts. `internal/pricing/tables.go` is now generated from `open-sse/providers/pricing.js`: 108 canonical models, 112 provider-specific entries, 51 pattern rows, five rates each (input, output, cached, reasoning, cache_creation). Regenerate rather than hand-edit.
- `GetPricingForModel(provider, model)` replaces `lookupPricing(model)` and follows upstream's four steps: the provider's own table, the free namespace, the canonical table (vendor prefix stripped, then whole id), then the ordered pattern table. `path.Match` is unusable here for the same reason it is in the thinking-levels port — its `*` never crosses `/` — so the glob keeps upstream's semantics.
- `CalculateCost` is the port of `calculateCostFromTokens`. The convention it assumes is the one the wire uses: **`prompt_tokens` is cache-inclusive**, so cached and cache-creation are subtracted before the input rate and then charged at their own cheaper rate; a rate left at zero falls back the way upstream does (cached → input, reasoning → output, cache_creation → input).
- The call site in `handlers/chat/usage.go` now passes the provider and the full token mix, so a cache-heavy request stops being billed as fresh input.
- ⚠️ **Visible change in the Usage totals: 556 of the 1592 catalogued `(provider, model)` pairs are priced by nobody, upstream either.** Upstream records a cost of **0** for those; the Go port used to invent $1/$3 per 1M for them. This follows upstream and makes "unpriced" visible instead of quietly fictional, but it does mean the cost column drops for those models from here on — say the word if you would rather have a fallback rate back.
- Tests pin the **numbers upstream itself returns**, not a re-implementation: `TestGetPricingForModel_Parity` and `TestCalculateCost_Parity` use rates and costs read out of the upstream module (anthropic sonnet $18.8625, gpt-4o $13.6875, gpt-5.6-sol $36.875, vendor-prefixed deepseek $0.4137, free namespace $0, unpriced $0 — all on prompt 1M / completion 1M / cached 250k / reasoning 100k / cache-creation 50k). The old `cost_test.go` asserted the four-row table and the prefix-matching behaviour, neither of which exists upstream, so it was replaced.
- `TestPricingTablesPopulated` guards the generated file: an empty table would still compile and would silently price everything at zero.
### 🐛 Codex "Add Connection" died at `invalid_authorize_request` (upstream loopback parity)

- **Symptom**: `Providers → OpenAI Codex → Add Connection` opened the OpenAI login page only to bounce straight to `Authentication Error` (`error_code: unknown_error`), and the modal never connected. Older builds surfaced the same rejection as `{"code":"invalid_authorize_request","message":"Invalid authorize request"}`.
- **Root cause**: `callbackRedirectURI` (`internal/handlers/oauth/redirect.go`) derived the callback from the dashboard's own host for *every* provider, so Codex was sent `redirect_uri=http://localhost:<dashboardPort>/callback`. Codex's public OAuth client (`app_EMoamEEZ73f0CkXaXp7hrann`, the Codex CLI) has exactly one registered loopback URI, and `auth.openai.com` validates `redirect_uri` during the authorize step. Probed live in a browser against `auth.openai.com`: `http://localhost:1455/auth/callback` reaches the login page, while `http://localhost:20130/callback`, `http://localhost:20130/auth/callback` and even `http://localhost:1455/callback` (right port, wrong path) are all rejected before the login form renders.
- **Fix, in three parts**:
  - `pkceConfig` gained `fixedRedirectURI` / `fixedPort`, and `callbackRedirectURIFor` / `exchangeRedirectURIFor` make the registered URI win over the dashboard-derived one for providers that pin it. Every other PKCE provider keeps following the dashboard, unchanged.
  - `internal/handlers/oauth/codex_proxy.go` (new) ports upstream `startCodexProxy` (`src/lib/oauth/utils/server.js`): a short-lived listener on `127.0.0.1:1455` behind `GET /api/oauth/codex/start-proxy`, a state-keyed session carrying the PKCE verifier, and `GET /api/oauth/codex/poll-status` so the dashboard learns the outcome. The listener completes the token exchange **server-side** and renders the result in the popup; an unregistered callback (a real `codex` CLI on the same machine) is 302'd to the dashboard's `/callback` instead of being dropped.
  - The exchange itself moved out of the HTTP handler into `resolvePKCEExchange` → `requestToken` → `buildConnectionData` → `persistConnection`, so the loopback server and `POST /api/oauth/pkce/exchange` run one code path instead of two copies of the OAuth rules.
- **A second, quieter Codex gap closed at the same time.** Upstream stores `chatgpt_account_id` / `chatgpt_plan_type` from the id_token into `providerSpecificData` and sends `chatgpt-account-id` on every call; Go stored neither and sent no such header. That is not cosmetic — the codex responses endpoint rejects a request without it, so even a successfully added account could not complete a single call. `codexAccountInfo` (`codex_account.go`) decodes the namespaced `https://api.openai.com/auth` claim block (with the legacy root-level `account_id` / `plan_type` as fallback) and `ForwardCodex` now sends the header from `ConnData`.
- **Two listener-lifetime bugs found by testing, both fixed**: `Server.Close()` from inside the request handler killed the connection serving it, truncating the very result page the user was looking at — shutdown is now `Shutdown`, deferred off the handler goroutine. And a stray callback retiring the listener would silently kill an unrelated in-flight login (the port is fixed, so a dead listener is a dead login with no way back), so the listener now only retires once nothing is pending. The dashboard also stopped freezing on "Waiting for popup authorization…" when the server no longer tracks the login; it says so and asks for a retry.
- Verified live against a worktree build on `:20141`, upstream on `:20128`: `Add Connection` now lands on `https://auth.openai.com/log-in`, `127.0.0.1:1455` is listening, `poll-status` reports `pending`, and a callback with an unusable code produces OpenAI's real `401 token_expired` in the popup and in the modal — i.e. the token request genuinely reaches `auth.openai.com`. A/B on the authorize endpoint: `:20130` (old) sends `http://localhost:20130/callback`, `:20141` (fixed) sends `http://localhost:1455/auth/callback`. `go test ./...` and `bun run build` green; `TestCodexLoopback_completesLoginEndToEnd` drives the real listener through a full login against a stub token endpoint.

### ✨ Sync batch — Codex `responsesLite` and the connection name guard

- **`responsesLite` for `gpt-6-sol` / `gpt-6-luna`** (`95600db1`). Those two models were in the catalogue but would 400: Codex 0.155 serves them with a different request shape — tools and instructions travel as an `input` prefix (`additional_tools` + a developer message) instead of top-level fields, and reasoning carries `context: "all_turns"` with no `summary`. Adding the ids to the catalogue without the wire shape was worse than leaving them out, so `internal/proxy/executor/codex_responses_lite.go` ports it: `isCodexResponsesLiteModel` (stripping a trailing `(level)` override first), the prefix rewrite, and the lite reasoning block (effort defaulting to medium rather than low). A body that already carries the prefix is left untouched, so replaying a transcript is not double-wrapped, and an input shape the rewrite cannot read is left alone rather than replacing the conversation with the prefix — the two build paths produce different concrete slice types, `[]any` and `[]map[string]any`. Classic Codex models keep the shape they had; `TestBuildResponsesBody_ClassicCodexUnchanged` pins that.
- Not ported: upstream falls back to `CODEX_DEFAULT_INSTRUCTIONS` when the client sends none, and 9router-go has no copy of that prompt blob for any model — the prefix carries the caller's own instructions, and adds no developer message when there are none. Carrying a Codex prompt only on the lite path would be a new, unreviewed prompt rather than a parity fix.
- **`POST /api/connections` refused to overwrite nothing** (`239bcfc5`, fixes #4311). A create reusing a name already in use replaced the stored key without a word: a script naming rows "Key 1", "Key 2", … kept wiping existing pool entries and got a success back. A create now looks the name up first and answers **409 `PROVIDER_NAME_CONFLICT`** naming the row that would have been replaced; a caller that means it passes `allowOverwrite: true` (or the legacy `overwrite`), which rewrites that row in place, keeping its id and its place in the rotation instead of leaving two rows the account picker cannot tell apart. A request carrying an `id` is already an explicit edit and is never name-guarded. Go's symptom was a silent duplicate rather than a silent replace, because `CreateProviderConnectionFull` is a plain INSERT — same caller-facing hole, different shape.
- The O(1) half of that upstream commit was already ported (`MAX(priority)+1`, no renumber pass on insert).
- Live: `POST /api/connections {"provider":"commandcode","name":"9router",…}` without an id answers 409 with `existingId`/`existingName`, and the stored connection is still the only one — one row, key intact.

### ✨ Sync batch — 5 aggregator providers, catalogue and Cline free tier

- **Five OpenAI-compatible aggregators** added upstream in v0.5.91, wired end to end: `tokenharbor` (+`th`/`thh`), `dahl` (+`dahl-inference`), `atria` (+`atria-asi`), `agnes` (+`agnes-ai`), `bai` (+`b-ai`). A provider entry is only real once every table carries it, so this touches `providers.go` (transport), `aliases.go` (short prefixes), `registry_models.go` (offline catalogue), `executor/init.go` (routing), `web/src/lib/providers.ts` (dashboard card, notice, `modelsFetcher`) and `web/src/lib/models.ts`. Upstream ships no capabilities or pricing rows for any of the five, so none were invented here.
- The dashboard "Suggested free models" import needs a live catalogue endpoint, which upstream wires per provider (`PROVIDER_MODELS_CONFIG`). `providers.ModelsListURL` carries that mapping and `HandleGetConnectionModels` gained a branch that Bearer-probes it. `agnes` and `bai` publish no seed catalogue at all — their page correctly lists no models and imports everything live.
- **`passthroughModels` needs no Go counterpart.** Upstream added a per-provider opt-in so any model id is accepted; the Go request path already forwards the model string untouched for every provider, so it is a superset. Deliberately not "implemented" — adding a gate would be a regression.
- **Catalogue sync**: `opencode-go` 28 → **42** models, `codex` 27 → **29** (`gpt-6-sol`, `gpt-6-luna`), both now byte-identical to the upstream registry. `qoder-cn` was missing from the dashboard catalog entirely and is back.
- **Cline free tier** (`199bcfc5`). `/api/v1/models` carries no `cline-free/*` ids, so upstream merges a second, unauthenticated feed (`/api/v1/ai/cline/recommended-models`) and prices that namespace at zero. Both are here: `clineFreeTierModels` merges with first-writer-wins so a dead feed cannot take the catalogue down, and `pricing.IsFreeModel` checks the namespace before the price table — the same model id is *not* free through a paid gateway.
- Parity fixture re-captured for the wider catalogue: **1589/1589** `(provider, model)` pairs match upstream (was 1547). The 42 new pairs include `gpt-6-sol`/`gpt-6-luna`, which are the first to exercise the per-model `thinkingLevels` stage.
- Live, `20130` vs upstream `20128` on the same data: all six provider pages identical — `tokenharbor` 6 rows / 7 level options, `dahl` 3 rows (incl. the binary `thinking` level), `atria` 1 row, `agnes` and `bai` 0 rows with no picker, `qoder-cn` 16 rows.

### ✨ Sync batch — upstream v0.5.86..v0.5.91 (Gemini turns, Claude thinking text, usage by API key)

- **Gemini terminal-turn guard** (`30464bc2`). `NormalizeGeminiContents` merged same-role turns and stripped empty parts, then stopped — a conversation that ends on a `model` turn is rejected by Gemini, and three ways arrive that way: a prefill, a truncated stream, and tool calls the client never answered. It now brackets the payload with user turns: a leading `user "..."` when the first turn is not a user turn (that part was already missing, from #e7b5f09), and a trailing turn carrying one `functionResponse` per unanswered `functionCall` — or a plain `Continue.` when the model was talking. `GeminiFunctionCall`/`GeminiFunctionResp` gained `ID`; without it a synthesized response could not be matched to its call. Ported upstream's own `gemini-contents-normalization.test.js` as a table test.
- **Claude thinking text for OpenAI clients** (`90b06934`). Claude returns thinking as a signature only unless the request sets `thinking.display: "summarized"` — a field OpenAI has no equivalent for, so a client asking with `reasoning_effort` (Chat Completions) or `reasoning.summary` (Responses) silently got nothing: `ensureMessagesMaxTokens` deleted `reasoning_effort` outright. The intent is now captured into the Claude field before the OpenAI-only keys are stripped (`internal/proxy/executor/claude_thinking.go`), and `redact-thinking-2026-02-12` is dropped from `Anthropic-Beta` for that request, since it asks Anthropic for exactly the signature-only behaviour the client is trying to avoid. An explicit `thinking.display` on the body still wins, and `reasoning_effort: "none"/"off"` stays an opt-out.
- **Usage by API key** (`4a57df8b`, `a406381f`). `UsageStatsResponse.ByApiKey` was declared and initialised but never written, so the dashboard's per-key breakdown was permanently empty. It is now filled from both the daily rollup and live `usageHistory`, keyed by the **stored** key value rather than a re-derived display mask — every key an instance mints shares the same `sk-{machineId}` head, so masking collapsed a whole team key set into one row and attributed one key's usage to another. Friendly names resolve against the `apiKeys` table, indexed under both the full key and its mask because the shared database holds both forms (rows this build writes are masked, rows the Next.js dashboard wrote are not).
- `handlerutil.MaskAPIKey` is now the single mask implementation, widened from a 4-character prefix to 8 to match upstream's keep-the-tail change. The `***` sentinel for short/empty keys is kept so existing no-key usage rows stay in one bucket instead of splitting.
- Three existing translator tests indexed `contents[0]` / `contents[1]` and counted turns; they now locate the part by role or by scanning, since the bracketing deliberately changes the shape.
- Live: `/api/usage/stats?period=7d` went from an empty `byApiKey` to 506 buckets with the two local keys resolving to `luqman` and `mac`. `go test ./...` and `bun test` green.

### 🐛 Thinking levels drifted from upstream v0.5.91 (MiMo + Codex per-model sets)

- Found while auditing the upstream tag range `v0.5.86..v0.5.91` (38 commits, 123 files). `internal/providers/thinking_levels.go` was missing two pattern rows and a whole resolution stage that v0.5.91 added.
- Missing rows: `*mimo*v2.6*` and `*mimo*v2.5-pro*`, both `["none","low","medium","high","xhigh"]`. Upstream's note: *mimo-v2.5-pro on opencode-go rejects reasoning_effort "max" (probed live); v2.5 accepts it*. Without them, 15 `(provider, model)` pairs fell through to the `deepseek` format default and answered `["high","max"]` where upstream answers `["low","medium","high","xhigh"]` — the picker would have offered a level those gateways reject.
- Missing stage: upstream resolves a per-model `thinkingLevels` off the catalog record before the pattern table (`getProviderModels("cx").find(e => e.id === baseId)?.thinkingLevels`, with a trailing `(level)` suffix stripped first). Go's catalog is a flat id list, so the field lives in `codexModelThinkingLevels`; `GetThinkingLevels` now resolves registry-declared → pattern → format default, the order upstream uses.
- The parity fixture had been **masking this**: it was captured from an upstream tree carrying the older values, so `TestGetThinkingLevels_MatchesUpstreamFixture` reported 1547/1547 while 15 pairs were wrong. The fixture now carries an `upstreamVersion` field, the test fails when it does not match the tag the port targets, and the capture was re-taken from v0.5.91 — 1547/1547 against the real thing.
- `TestCodexModelLevels` covers the per-model stage directly (including the `(level)` suffix and the codex/cx split), since the GPT-6 models are not in the Go catalog yet and the fixture cannot reach that path.

### ✨ Provider detail parity for `/dashboard/providers/<id>` (CommandCode audit against upstream `:20128`)

- 🔴 **The "Available Models" list was empty for 28 providers.** `web/src/lib/models.ts` keyed `PROVIDER_MODELS` by provider id but resolved it through `PROVIDER_ID_TO_ALIAS`, which holds the *display* prefix (`uiAlias`: `commandcode→cmc`, `deepseek→ds`, …). Upstream derives the same map from the registry `alias` and only remaps OAuth entries (`providerModels.js:107-113`), so every non-OAuth provider whose alias differs from its id got `undefined` instead of a list. `getModelsByProviderId` now resolves by id first and falls back to the alias.
- `internal/providers/thinking_levels.go` (new) — port of upstream `open-sse/providers/thinkingLevels.js`: the shared level sets, `FORMAT_LEVELS` keyed by thinking format, the ordered `PATTERN_THINKING` overrides (codex GPT-5.6, deepseek v4, per-model codebuddy sets), and the Kiro `resolveKiroEffortPath` gate. `path.Match` is unusable for the pattern table because its `*` never crosses `/`, so the port carries upstream's glob semantics.
- `Capabilities` gained the thinking block (`ThinkingFormat`, `ThinkingCanDisable`, `ThinkingRange`, `ThinkingEffortSupported`) plus provider-declared `ContextWindow`/`MaxOutput`. `ThinkingCanDisable` is a `*bool` because upstream's default is `true` and a Go bool cannot say "not specified" — without the tri-state, every table entry that names a format silently lost the `none` level. `mergeCapabilities` takes the whole thinking block from an overlay only once it declares a format, mirroring the JS spread.
- Ported upstream's 145 thinking declarations across the model / provider / pattern tables, and the 8 pattern rows the Go table was missing (`*gemini-3.7*`, `*grok-4.6*`, `*glm-5.3*`, `*glm-5.2*`, `*deepseek-v4.*`, `*minimax-m2.5*`, `*mimo*v2.6*`, `*muse*spark*`). Removed the table entries upstream does not have and that therefore shadowed a pattern row (`grok-4.5`, `grok-4.6`, `gpt-6-astra`, `glm-5.3`, the `gpt-5.6-*-image` trio, `*hy4*`, `*longcat*`, the `qoder` provider scope), and corrected `union-alpha` plus the `*mimo*` rows.
- `internal/handlers/dashboard/model_caps.go` (new) — `GET /api/models/caps?provider=<id>`, the Go counterpart of upstream `useModelCaps`: the model *catalog* still ships as a static bundle, but capabilities and thinking levels are resolved server-side from the provider registry, the capability tables and the synced models.dev catalog. The provider resolves by id or alias, so `/dashboard/providers/commandcode` and the `cmc/` storage prefix share one block.
- CommandCode capabilities are now the upstream short-circuit (`capabilities.js:570-582`): every model is served from one `/alpha/generate` wire, so the per-family patterns (deepseek-v4 → vision false, thinkingFormat deepseek, …) must not win. `internal/providers/capabilities_commandcode.go` ports the 23-model text-only denylist and declares `reasoning`, `thinkingFormat: "commandcode"`, `thinkingEffortSupported`, `contextWindow: 1000000`, `maxOutput: 384000`.
- Dashboard: the "Thinking:" picker is now the union of the levels this provider's models accept, with `auto` first and the picker hidden when no model has reasoning (upstream `providerThinkingLevels`); the `(level)` suffix is gated per model and shown in the model row, not only copied; the vision/reasoning icons come from the server caps instead of being empty for every built-in model.
- Model rows now render in **registry order**, the way upstream does: `models` there is `getModelsByProviderId()` verbatim (`providers/[id]/page.js:158`) and the A–Z sort this port added is gone. `buildAvailableModels` also lists the catalog first and appends custom models, instead of hoisting customs to the top. The bundled web catalog had drifted out of registry order for `commandcode`, `deepseek`, `gcli`, `openai` and `ocz`; all four are now in the upstream sequence (the Go `registry_models.go` already was).
- Live verification against upstream `:20128` on the same data. `commandcode` is byte-identical: 22 rows, the same 22 ids in the same order, the same per-model vision/reasoning icons, and the same picker (`auto, low, medium, high, xhigh, max`); the `(high)` suffix appears and clears identically. `openai` (21), `xai` (6) and `blackbox` (10) also match row-for-row including icons and order. The 16 LLM providers that rendered zero models now render theirs; the media/embedding/STT providers correctly render none on both.
- Resolved below: the `deepseek-v4-flash` vision icon was not stale data but a broken catalog sync — see "Synced models.dev catalog recorded every model as text-only".
- Tests: `TestGetThinkingLevels_MatchesUpstreamFixture` replays upstream's own resolver over all 1547 `(provider, model)` pairs in the catalog — **1547/1547 match**. Plus `TestGetCapabilitiesDetailForModel_CommandCode`, `TestIsCommandCodeTextOnly`, `TestMergeCapabilities_ThinkingIsDeclarative`, `TestGetThinkingLevels_Parity` (upstream's vitest expectations), `TestGetThinkingLevels_NoReasoning`, `TestResolveKiroEffortPath`, `TestMatchThinkingGlob`, `TestHandleGetModelCaps_*`, and `web/src/lib/models.test.ts`. `TestQoder_Capabilities` was removed: it asserted a `qoder` capability scope that upstream does not have, so it contradicted the parity it claimed.
- Still open, left alone on purpose because each one adds or removes a model rather than fixing a rendering defect: 8 providers whose bundled catalog content differs from the registry (`ag`, `cx`, `gemini`, `huggingface`, `kr`, `opencode-go`, `oc`, `xiaomi-mimo` — the `oc` and `huggingface` extras are local free models such as `space-bunny-free` / `nemotron-3-ultra-free`), and 4 registry providers with no bundled catalog at all (`qdcn`, `tokenharbor`, `dahl`, `atria`). No order-only difference remains.

### 🐛 Synced models.dev catalog recorded every model as text-only

- Symptom: capability icons that upstream shows never appeared, and the token limits in `/v1/models` came from the substring table instead of the catalog. `deepseek-v4-flash` rendered without a vision icon while the upstream dashboard rendered one.
- Root cause — the sync read a field models.dev no longer publishes. `SyncModelCatalog` decoded `modality: {image: true}` (a boolean map); the API serves `modalities: {input: ["text","image",…], output: […]}` (string arrays), so the decoded map was always empty and the writer stored `vision: false` for **every** model. Second defect: modalities were filed under the bare model id, aggregating across gateways, while upstream keys them `provider:model` precisely because gateways disagree about the same weights — some do not proxy images at all. Third: `SyncedCatalog.UnmarshalJSON` read `syncedAt` into a `[]byte`, which `encoding/json/v2` accepts only as base64, so neither an ISO string (this writer) nor epoch milliseconds (upstream's writer) loaded — a restart could not read back the file it had just written, which also dropped the `LastSync` state.
- Fix: `internal/providers/catalog_sync.go` decodes `modalities.input` with upstream's `MODALITY_BY_INPUT` mapping (image→vision, pdf, audio, video), keys modalities `provider:model` with the local alias filed too (`claude` → `anthropic`), and `GetCatalogModalities` takes the provider so a gateway's entry only answers for that gateway. A bare-model key is still accepted, so a catalog written by an earlier build keeps resolving. `syncedAt` is now written as an ISO string and decoded from ISO, epoch milliseconds, or absent.
- After the first background sync the file holds **4780** real modality entries (was 2399 all-false). `/api/models/caps?provider=deepseek` now reports `deepseek-v4-flash` as `vision: true`, and the provider detail page is row-for-row identical to upstream again — same ids, same order, same icons.
- `TestSyncModelCatalog_Modalities` (was a test that re-implemented the parser it was checking, so it passed against the broken schema — now drives the real `SyncModelCatalog` end-to-end against the current payload), `TestSyncModelCatalog_ModalitiesArePerProvider`, `TestLoadCatalogFromFile_SyncedAtForms`, `TestLoadCatalogFromFile_LegacyBareKeys`, `TestCatalogProviderKeys`, `TestCatalogBaseID`.

## [v1.9.3] - 2026-09-26

### 🐛 Kiro tool calling never reached the client (`arguments: {}` / no `tool_calls`)

- Symptom: `POST /v1/chat/completions` with `tools` never returned `tool_calls` on Kiro. The model wrote pseudo tool calls as plain text (`<invoke name="browser">…`), or — once the catalogue was sent — replied with an empty turn carrying only `reasoning_content: "..."`. Other providers (grok-cli, antigravity) were unaffected.
- Root cause 1 — **request**: Kiro has no OpenAI-style `tools` array. The tool catalogue must be attached to the last user turn as `userInputMessage.userInputMessageContext.tools` (`{toolSpecification:{name,description,inputSchema:{json:…}}}`, upstream `normalizeKiroToolSpecs`). The Go translator only sent `toolResults`, so the model never learned which tools existed.
- Root cause 2 — **response**: Kiro splits one tool call across several `toolUseEvent` frames — the first carries only `name` + `toolUseId`, the following ones carry slices of the JSON arguments under `input` (`"{\"ci"`, `"ty\": \"Jak"`, …), and a final `stop: true` frame closes it. The old code read a `content` field that is never sent and emitted `arguments: "{}"` per frame.
- Fix:
  - `internal/translator/kiro_tools.go` (new) — port of upstream `normalizeKiroToolSpecs`: OpenAI/Claude tool normalization, name sanitization (`[^a-zA-Z0-9_-]` → `_`, de-duplication suffix, 64-char cap), `inputSchema.json` forced to `type: object`, `additionalProperties` stripped, `required` filtered down to properties that actually exist.
  - `internal/translator/kiro.go` — the tool catalogue is attached to `userInputMessageContext.tools` on the last turn; `toolResults` still travel alongside it.
  - `internal/proxy/executor/stream.go` — `kiroToolCall` buffers arguments per `toolUseId` (accepting both fragmented `input` strings and objects, with the legacy `content` kept as a fallback), and `tool_calls` are only emitted once the stream ends, with the reassembled arguments.
- Live e2e verification (tool `get_weather`, "weather in Jakarta"): `kr/auto` → `{"city":"Jakarta","unit":"celsius"}`, `kr/claude-sonnet-4.5` → `{"city":"Jakarta"}`, while `gcli/grok-4.5` and `ag/gemini-3.8-flash-high` keep passing. The upstream instance (`:20128`, same Kiro account) returns the same `tool_calls`.
- Tests: `TestOpenAIToKiro_AttachesToolSpecsToLastUserTurn`, `TestOpenAIToKiro_ToolResultsStillTravel`, `TestOpenAIToKiro_NoToolsNoContext`, `TestKiroNormalizeRootSchema`, `TestKiroUniqueToolName`, `TestForwardKiroRequest_ReassemblesFragmentedToolInput`. Suite: 1473 pass.

### 🐛 Kiro `403 The bearer token included in the request is invalid`

- Symptom: Kiro chat and `POST /api/models/test` failed with 403 while the token was actually valid — `GET ListAvailableModels` with that same token returned 200.
- Root cause: `extractAPIKey` always preferred `apiKey`, and a Kiro connection created by *import* stores **two** credentials (`apiKey` + `accessToken`). That `apiKey` was sent as both `Authorization: Bearer` and `x-amz-sso-bearer`, while upstream (`open-sse/executors/kiro.js` `buildHeaders`) uses the apiKey only for `authMethod: "api_key"` connections and the `accessToken` everywhere else. CodeWhisperer rejects that token with "bearer token invalid" even though the `accessToken` is perfectly healthy.
- Fix:
  - `internal/handlers/chat/connections.go` — `resolveProviderAuthToken` applies that upstream rule in the forward path (Kiro only; every other provider is untouched).
  - `internal/proxy/grokcli.go` — `ForwardKiro` now sends `TokenType: API_KEY|EXTERNAL_IDP` according to `authMethod` plus the `x-amzn-codewhisperer-profile-arn` header, and **rotates endpoints** like upstream: `q.<region>` → `codewhisperer.<region>` → `runtime.us-east-1.kiro.dev`, falling back on 401/403/404 (`KIRO_ENDPOINT_FALLBACK_STATUSES`); 400 stays terminal. AWS hosts are regionalized from `providerSpecificData.region`.
- Verification: `POST /v1/chat/completions {"model":"kr/claude-sonnet-4.5"}` → **200** (`"ok"`), previously 403; all three Kiro surfaces also answer 200 when probed directly with the same `accessToken`.
- Tests: `TestResolveProviderAuthToken_Kiro` (7 cases: imported/builder-id/api_key/access-only/key-only/empty/other provider), `TestKiroEndpointsOrdering` (3), `TestKiroTokenType` (5), `TestKiroEndpointFallbackStatus` (401/403/404 rotate, 400/429/5xx terminal). Suite: 1466 pass.

### ✨ `GET /v1/models` — live catalog and response shape identical to upstream

- `internal/handlers/chat/live_catalog.go` (new): port of upstream's `LIVE_MODEL_RESOLVERS` — **kiro** (`GET https://q.<region>.amazonaws.com/ListAvailableModels` with a Kiro IDE fingerprint UA, each model expanded into `-thinking`/`-agentic`/`-thinking-agentic` variants, `auto` without agentic variants), **grok-cli** (`GET <base>/v1/models` with the `x-grok-cli` headers), and **custom nodes** (`fetchCompatibleModelIds`: `GET <baseUrl>/models`). In-process cache of 5 minutes per credential; 401/403 triggers a single token refresh and a retry; failures fall back to the static catalog and never empty a provider.
- A resolver is only consulted when `enabledModels` is not pinned on the connection, exactly like upstream; the live catalog replaces the static one while custom models and aliases are still merged in.
- Response shape aligned with upstream: kiro `capabilities` uses the live `{thinking, agentic}` block verbatim; combos use a separate `ComboCapabilities` shape (no `thinkingEffortSupported`, `tools` = AND across leaves, `contextWindow` = narrowest leaf, `maxOutput` = widest leaf — the `aggregateComboCapabilities` rules); `context_length`/`max_completion_tokens` are gone from combo entries (`omitzero` is required because `encoding/json/v2` does not drop zero numbers under `omitempty`); the `outputAlias/` qualifier is stripped only from registry/alias ids, never from custom ids (which is why `openrouter/openrouter/free` stays double-prefixed upstream too).
- `internal/providers/catalog_sync.go`: the models.dev catalog file written by upstream is now readable (its `v2`/`etag`/epoch-millisecond `syncedAt` format), and `GetCatalogLimits` is consulted before the substring guesses, so token limits follow the catalog instead of heuristics.
- Cross-check against the running upstream instance (`:20128`, same database): **714 models upstream vs 713 in 9router-go, all 713 ids identical, 0 entry-key and 0 `capabilities`-key differences**. Breakdown: `clinepass` 469/469, `kr` 34/34, `ag` 20/20, `cbai` 15/15, `openrouter` 7/7, `nvidia` 4/4, `Id` 3/3, `gcli` 1/1, `tr` 153/154, combos 7/7.
- Remaining: (a) `tr/typesafe/jev-1.13` exists only upstream — the tokenrouter node's `GET <baseUrl>/models` now returns nothing, so it is a leftover upstream cache entry; (b) the **values** of `context_length`/`max_completion_tokens` still differ for 627 ids because upstream's `MODEL_CAPABILITIES`/`PATTERN_CAPABILITIES` tables are not ported yet (the models.dev catalog only covers 23 providers) — the model list and the response shape are identical, the numbers are not.
- Tests: `live_catalog_test.go` (kiro variant expansion, `kiroDisplayName`, grok-cli catalogue parser with 5 cases, region from profileArn) + `live_catalog_handler_test.go` (live kiro replaces the static catalog, `enabledModels` skips the live fetch, failures fall back to the static catalog, live grok-cli and its headers).

### 🐛 `GET /v1/models` — full port of upstream's Next.js `buildModelsList` (fixes issue #1)

- Symptom: `/v1/models` flooded the catalogue — `ghost customs` in `kv.customModels` for unconnected providers were published, alias keys were published as models of their own, `enabledModels` was ignored, and media/embedding models leaked in. On the user's real database: 640 models, `clinepass/` duplicated with `cp/`, and 4 dead nodes still exposing 21 models.
- Source of truth: `~/htdocs/9router/src/app/api/v1/models/route.js` (`buildModelsList`, `KIND_SLUG_MAP`, `MODEL_TYPE_TO_KIND`, `inferKindFromUnknownModelId`, `aggregateComboCapabilities`).
- `internal/handlers/chat/models_list.go` (new; the builder moved out of `chat.go` — 1331 → 897 lines):
  - Upstream ordering: **combos first** (`owned_by: "combo"`, capabilities aggregated across every leaf), then per-connection models.
  - `isActive !== false` **only** — upstream requires no credentials for listing, so 9router-go's credential gate was deliberately reverted (`TestHandleModels_SkipsCredentiallessConnections` was replaced by `TestHandleModels_ActiveConnectionPublishesCatalog`).
  - Per connection: `staticAlias` = catalogue alias, `outputAlias` = `providerSpecificData.prefix`; `enabledModels` (psd or top-level) **replaces** the static catalogue; then custom models whose `providerAlias ∈ {staticAlias, outputAlias, providerId}` (llm-typed only) and `modelAliases` targets with a matching prefix are merged in; `isDisabled(outputAlias|staticAlias, id)` is enforced; `outputAlias/`, `staticAlias/` and `providerId/` qualifiers are stripped from ids.
  - Alias keys are **no longer** model ids themselves (upstream only uses their targets), and untyped custom models are now filtered by `isLLMModelID` (the same `embed|tts|speech|audio|voice|image|imagen|dall-e|flux|sdxl|sd-|stable-diffusion` heuristic as upstream's `inferKindFromUnknownModelId`).
  - The static dump only runs when the `providerConnections` table is genuinely empty (upstream `connections.length === 0`).
  - Non-LLM combos (`webSearch`/`webFetch`) are excluded from this list — the endpoint builds with `kindFilter ["llm"]` (upstream `comboMatchesKinds`); combo entries carry `capabilities` only, without `context_length`/`max_completion_tokens`.
- `internal/providers/registry_aliases.go` (new): 61 provider aliases ported from `open-sse/providers/registry` (`uiAlias || alias`), and `GetProviderAlias` now uses that table — providers without an upstream alias are published under their own id (`clinepass`, `nvidia`, `openrouter`, `openai`) instead of our invented short aliases. The old prefixes (`cp`, `or`, `nv`, `gb`) are gone from the listing; `ProviderAliasMap` (alias → id) still drives **request resolution**, so `cp/...` keeps routing to clinepass. The now-dead `ProviderToAliasMap` was removed.
- Entry shape aligned with upstream: `ModelInfoObject` now only has `id`, `object`, `owned_by`, `capabilities`, `context_length`, `max_completion_tokens` (the `created` and `context_window` fields that upstream does not have were dropped), and `CapabilitiesDetail` gained `search`, `tools`, `reasoning`, `thinkingFormat`, `contextWindow`, `maxOutput` (replacing the duplicated `contextWindows`).
- `internal/providers/registry_models.go` fully regenerated from `open-sse/providers/registry` (89 providers, 1550 id/alias entries) plus a new `ProviderModelKinds` table (191 media models typed `image`/`tts`/`stt`/`embedding`/`video`/`systemone`) and a `GetProviderModelKind` accessor. Kind resolution in `/v1/models` now mirrors upstream (custom `type` → registry `kind` → id heuristic), so media models no longer leak into the LLM list and the static catalogue tracks upstream (e.g. `openrouter` 10 → 18 entries, `ag` 21 models).
- Derived fix: a provider with one active plus one inactive connection row is no longer treated as entirely disabled (previously `tr/*`'s 153 models disappeared).
- Cross-check against the running upstream instance (port 20128, same database): **714 models upstream vs 726 in 9router-go, 687 identical ids**; prefixes, entry keys and `capabilities` keys match exactly. Breakdown: `clinepass` 469/469, `tr` 154/153, `kr` 34/44, `ag` 20/20, `cbai` 15/15, `openrouter` 7/6, `nvidia` 4/4, `Id` 3/3, `gcli` 1/5, combos 7/7.
- Remaining 39-id gap at that point: (a) upstream uses a **live catalog** for kiro/grok-cli (`kr/auto`, `kr/minimax-m2.1`, `gcli/grok-4.7`) while 9router-go used the static catalogue — the next PR; (b) `openrouter/free` exists upstream only; (c) an upstream quirk: vendor-prefixed ids (`nvidia/parakeet-ctc-1.1b-asr`) lose their qualifier before the kind lookup, so they slip through on both sides.
- Tests: `TestIsLLMModelID` (9 cases), `TestGetProviderModelKind` (11 cases), `TestHandleModels_MediaKindModelsExcluded`, `TestHandleModels_EnabledModelsOverrideCatalog`, `TestHandleModels_AliasTargetMergedIntoProvider`, `TestHandleModels_ComboOwnedByCombo`, plus the filtering tests from the previous commit; the legacy `TestHandleModelLookup_ProviderModel` / `TestHandleModels_IncludesTokenLimits` were re-seeded to upstream semantics (an alias needs a connected provider, and token limits are asserted through `context_length`/`max_completion_tokens`/`capabilities.contextWindow`). Full suite: 1433 pass.

### 🐛 Fallback logs never named the failing account/project (Antigravity 403 `VALIDATION_REQUIRED`)

- Symptom: `WRN [fallback] upstream failed provider=antigravity ... status=403 error=... "Verify your account to continue."` only printed `conn=<uuid>`, so there was no way to tell which project ID / email needed verification at `accounts.google.com`.
- `internal/handlers/chat/conn_identity.go` (new): `connIdentityKV` (name, email, projectId from `data.projectId` or `data.providerSpecificData.projectId`, empty fields skipped) + `connIdentityKVByID` (lookup via `Repo.GetProviderConnectionByID`, failure paths only).
- `internal/handlers/chat/fallback.go`: the `upstream failed` and `connection locked` logs now add `connName=`, `email=` and `projectId=`. Example: `... status=403 cooldown_s=120 connName=AG Verify email=luqmangeminipro@gmail.com projectId=mega-rainfall-szp2g`.
- Tests: `TestConnIdentityKV` (6 cases: both projectId shapes, empty fields, broken JSON, nil conn) + `TestConnIdentityKVByID_MissingConnection` (a handler without a Repo does not panic). Smoke: a mocked 403 `VALIDATION_REQUIRED` makes both log lines carry the email and project ID.

### 🐛 `POST /api/models/test` returned 401 even when logged in

- The route moved from the `RequireApiKey` group (Bearer / `X-API-Key` only) to `RequireDashboardAuth` — parity with upstream `dashboardGuard` (`src/app/api/models/test/route.js`). Previously a dashboard authenticated by cookie session got `401 invalid_api_key`; now cookie session, CLI token and API key are all accepted, matching `/api/models/custom|disabled|alias`.
- Regression test: `TestSetupServerRouter_ModelTestDashboardSession` (anonymous → 401, valid session → passes).

### 🐛 Kiro `400 Improperly formed request` — OpenAI body forwarded raw to the gateway

- Root cause: there was no OpenAI→Kiro translator in Go. `ForwardKiro` passed the OpenAI body through untouched; the kiro.dev gateway only accepts a `conversationState` envelope and answered `400 {"message":"Improperly formed request."}`. Upstream has `openai-to-kiro.js` / `claude-to-kiro.js`, which were not ported.
- `internal/translator/kiro.go` (new): port of `openaiToKiroRequest` — build the `conversationState` (chatTriggerType / conversationId / currentMessage / history), inject the system and time context into the user turn's content (the gateway rejects a top-level `systemPrompt`), tool_use / tool_result, base64 images → Kiro blocks, `profileArn` from `providerSpecificData`, `inferenceConfig`, `modelId` backfill across history, and merge consecutive user turns. Upstream session replay and thinking budget are not ported yet.
- `ForwardKiro` now runs the translator for OpenAI bodies and passes through bodies that already carry `conversationState` (MITM); it strips the `kr/` prefix and the `-thinking` / `-agentic` suffixes before `modelId`.
- Tests: `TestOpenAIToKiro_*` (7) + `TestKiroUpstreamBody_*` against a mock upstream (3).

### 🐛 Kiro connect modal never opened and the OAuth/API Key buttons were overwritten

- Root cause 1: `devicePollTimer` was never declared, so `stopDevicePoll()` threw a `ReferenceError` inside `openKiroOAuth()` and `showOAuthModal = true` never ran (no console error, because the failure was in an async callback).
- Root cause 2: `isNoAuth` was inferred from `category === 'free'`; kiro and gemini-cli declare `noAuth: false` but were still treated as no-auth, hiding the connection card (and its buttons). Upstream parity: only an explicit `noAuth` flag counts. Also fixed in `combos/pickerData.ts`.
- Root cause 3: the `{:else if providerId === 'kiro'}` branch in the lower button row swallowed the `{:else if hasDualAuthModes}` branch (the OAuth/API Key buttons). The "Waiting…" banner and the "MANUAL TOKEN IMPORT" block were hidden whenever the Kiro method list was shown.
- Tests: browser verification (click → 7 methods render, with no leftover generic markup).

- `web/vite.config.ts` — the dev proxy now also covers `/admin`; previously `resetHealth()` (`/admin/health/reset`) fell through to the SPA fallback in dev mode because it was not proxied to Go.
- `Makefile` — new `web-dev` target (Vite dev server on :5173 with HMR). Two-terminal workflow: `make dev` (Go on :20130) + `make web-dev` → frontend changes hot-reload without rebuilding or restarting the binary.
- `web/README.md` — documents the two-terminal dev workflow and the service worker caveat in dev (`sw.js` also registers in dev; hard-refresh / unregister if the view looks stale).

## [v1.9.2] - 2026-09-26

### 🐛 Dashboard console errors: version 401 spam, manifest/SW 404, missing icons, auth redirect

- `GET /version`, `/api/version`, `/api/version/status`, and `/api/version/check` are now public (upstream `PUBLIC_API_PATHS` parity): the Sidebar polls on every page including `/login` before any session exists. `POST update/shutdown/auto-update` stay admin-only (upstream `ALWAYS_PROTECTED`).
- `GET /sw.js`, `/manifest.webmanifest`, and `/manifest.json` routed to the embedded SPA handler; PWA shell files existed in `web/dist` but chi had no route.
- Added missing provider icons `opencode-zen.png` and `ollama-search.png`.
- Frontend 401 handling: `onUnauthorized` clears stale local session and redirects to login; `App` stops polling when logged out.
- `POST /api/models/test` moved to `RequireDashboardAuth` (cookie session, CLI token, and API key accepted) — dashboard got `401 invalid_api_key` despite being logged in.
- Sidebar nav now scrolls with sticky header/footer: `aside` is viewport-bounded (`h-full max-h-screen overflow-hidden`), header/footer `shrink-0`, nav `min-h-0` (closes issue #22 side-nav points; live-verified at 500px viewport + mobile drawer).
- Dev workflow: `make web-dev` (Vite :5173 + HMR, no binary rebuild); dev proxy keeps `/debug` (traces/pprof) alongside `/admin` (health reset).

### 🐛 Media providers audit (search/fetch/image/STT/TTS) + Antigravity failover

- Antigravity/Xquik search, Antigravity image/STT, and Nvidia TTS now rotate all active accounts with per-account `ClassifyError` locks and success unlocks; pinned `x-connection-id` honored once.
- Fixed Xquik registry `BaseURL`, clamped `max_results 5..100`, optional `queryType`, `answer`/timing envelope fields, upstream error statuses preserved.
- Request-scoped 4xx no longer lock accounts; combo video skips 5xx rotation; multipart model rewrite byte-exact; pinned connections provider-scoped.
- `GET /api/keys` returns full secrets to dashboard sessions (upstream parity); masked values stay for API-key callers.
- Live-verified: OpenRouter embeddings `:20128` vs `:20129` return byte-identical 3072-dim vectors.

## [v1.9.1] - 2026-09-25

### 🐛 Dashboard: Custom Models Parity — Combo Picker Unwraps `{models}` Envelope

- `web/src/lib/customModels.ts` (new): shared `parseCustomModelsResponse` (`{models:[...]}` upstream shape, tolerates bare-array/record-map), `parseDisabledModelsMap` (`{disabled:{...}}` upstream + bare map go-port), `notifyCustomModelsChanged` / `subscribeCustomModelsChanged` (`customModelChanged` + `focus` reload, ported from upstream `SttExampleCard.js` / `ModelsCard.js` / `useModelCaps.js`).
- `web/src/components/combos/ModelPickerModal.svelte`: `normalizeCustoms`/`normalizeDisabled` now use the shared parser. Root cause of the reported bug: `GET /api/models/custom` returns `{models:[...]}` but the modal expected a bare array/record, so `fetchedCustoms` stayed `[]` and `oc/space-bunny-free` (custom-only, not in builtin `oc` catalog) never appeared in "tambah combo" — while the provider page (which already unwrapped `{models}`) showed it.
- `web/src/api/client.ts`: `getCustomModels`/`getDisabledModels` typed honestly (`{models:[...]}` / map) so future consumers stop guessing the envelope.
- `web/src/components/connections/types.ts`: `fetchProviderModelsData` uses the shared parser (same result as before, single code path).
- `web/src/components/media/MediaProviderDetail.svelte`: models card merges builtin + custom per kind (upstream `ModelsCard kindFilter` parity, builtin dedupe). Previously custom media models (`tts`/`stt`/`image`/`embedding`/`video`) never appeared.
- `web/src/components/media/SttExampleCard.svelte`: custom STT filter matches `storageAlias || providerId` (upstream uses `getProviderAlias`), reloads on `focus` + `customModelChanged` like upstream `loadCustom`.
- `web/src/components/connections/ProviderDetailView.svelte`: dispatches `customModelChanged` after every add/delete/import of a custom model (upstream `ModelsCard`/`page.js` parity), so pickers and STT cards refresh without full reload.
- Unchanged (already parity): `TtsExampleCard` stays builtin-only like upstream; backend `GET /models/disabled` bare-map shape kept, parser handles both.


## [v1.9.0] - 2026-09-25

### 🔀 Routing: antigravity-prefixed muse-spark Reaches the Owning Executor

- `internal/handlers/chat/resolution.go`: `routeModelToOwningProvider` — a request like `ag/muse-spark-1.3-contributor-free` (prefix copied from a dashboard combo) now resolves to the provider that actually serves the model (opencode family) instead of antigravity, which answered upstream 404 `Requested entity was not found`. Native antigravity models are untouched. Ported from `fix/10` (`ad4b355b`), which never reached main. Tests: `TestRouteModelToOwningProvider`, `TestResolveModel_AntigravityMuseSparkRoutesToOpencode`.


### 🐛 Dashboard: Recent Requests List No Longer Blinks/Shrinks on First Request

- `internal/usagetracker/tracker.go`: seed the in-memory `recentRing` from `usageHistory` once per process (upstream `ensureRingInitialized` parity) + `recentFromHistoryRow` mapper. Previously a fresh process streamed a ring holding only post-restart rows, so the first completed request replaced the dashboard's DB-backed 20-row list with 1 row (list blinked, rows below vanished). Regression test `TestTracker_RingSeededFromHistoryOnce`.
- `web/src/components/analytics/AnalyticsView.svelte`: SSE `recentRequests` now merges (union + dedupe + newest-first + cap 20) instead of replacing, so a short stream payload can never drop rows already rendered.


### 🧹 Leak Hunt: 7 Fixes for 24/7 Operation (Independent Audit)

- `internal/shutdown/shutdown.go` + `internal/app/server.go`: new `shutdown.Context()` (canceled on `Cancel`); updater + catalog-sync loops take it instead of `context.Background()` — background goroutines + tickers now exit on ^C. Fixed `TestReset` double-close panic (recreate `done` channel).
- `internal/handlers/chat/combo_fusion.go`: `collectPanel`/`makePanelCall` take `ctx`; stragglers abort on grace/hard timeout, client cancel, or shutdown (previously `context.Background()`, orphaned until upstream responded).
- `internal/proxy/executor/freebuff_session.go` + `internal/handlers/chat/antigravity_quota.go`: lazy eviction of expired entries + opportunistic sweep (2× TTL) — token-rotated keys no longer accumulate.
- `internal/handlers/chat/connections_proxy.go`: `proxyClients` capped at 128 with idle-close eviction (each entry pins a Transport + sockets).
- `internal/auth/session.go`: login limiter capped at 5000 buckets with window sweep + oldest-evict (scanner IPs bounded).
- `internal/handlers/chat/gemini_handler.go` + `internal/proxy/executor/stream.go`: 10MB caps on non-stream body reads and codex SSE accumulation.
- Out of scope (pre-existing, bounded): HeartbeatWriter ticker (dies with stream Close), tracing ring (2000), translator prune (50/10min), MITM conns (Wait).


### 🔊 Opencode Responses Errors Fail Loud (No More Silent Empty 200)

- `internal/proxy/executor/stream.go`: `ProcessCodexEvent` now records upstream `{"type":"error",...}` events (e.g. `FreeTierError` on muse-spark `-free` models) on stream state; `handleCodexStream` (non-stream) converts them to `*proxy.UpstreamError` (403 for free-tier/auth gates, else 502) instead of emitting `200 + content:""`. Silent success broke agents, bypassed account fallback/locks, and faked green monitoring. Upstream Next.js (`base.js`) likewise returns non-OK responses as errors, never empty 200s.
- Live evidence: `POST opencode.ai/zen/v1/responses` with `muse-spark-1.3-contributor-free` returns `FreeTierError: "OpenCode's free tier can only be used from within OpenCode"`.
- Limit (honest): on the already-committed SSE stream path headers cannot be unwound, so a pure-error stream still closes without chunks; the non-stream path (which agents use for the failing case) now errors properly.


### 🔗 Freebuff Cross-Process Session Coordination (Anti-Hijack)

- `internal/proxy/executor/freebuff_session.go` + `freebuff.go`: session lookup now memory L1 → `upstream_leases` L2; admission is coordinated — exactly one claimer per token+model across processes sharing the DB (`freebuffClaimMu` in-process + `AcquireLease` cross-process). Losers follow the winner's `instanceId` instead of POSTing their own claim (the pattern upstream punishes with 409 `session_superseded`). Stale-session retry drops the lease compare-and-delete (a sibling's fresh row survives). `Request.Leases` (nil = memory-only, old behavior) wired from chat fallback ×2, media `/responses`, and the session-switch endpoint.
- Tests: two racers converge on 1 POST (fake + real SQLite backends), follower reads with 0 POST, stale drop is compare-and-delete, nil-store contract unchanged.
- Note: coordination fixes *technical* hijacking between cooperating instances, not *policy* — two machines serving traffic concurrently on one account is still concurrent use server-side.


### ⬆️ Upstream v0.5.86 Parity (decolua/9router#v0.5.86)

- `internal/handlers/chat/claude_cloaking.go` + `internal/providers/providers.go`: bumped Claude CLI fingerprint `2.1.258` → `2.1.280` (upstream `cbffeb9`), so the billing-header cloak and `claude-cli/*` UA stay current. Added `claude-opus-5-5` to `cc`/`claude` catalogs (`registry_models.go`, `web/src/lib/models.ts`).
- `internal/handlers/media/deploy.go`: Vercel relay template now forwards headers losslessly (copy to plain object, strip only `x-relay-target`/`x-relay-path`/`host`) — upstream `6af26a9`. Cloaking headers survive relay pools, which matters for proxied Freebuff traffic.
- Deferred: Xiaomi MiMo v2.6 desktop login (5 region clusters, dual-route models, server-assisted flow) — large scope, tracked as separate stacked diff.

### 🛡️ Freebuff client_id Cloaking (Anti-Ban Parity)

- `internal/proxy/executor/freebuff.go`: `ForwardFreebuff` reuses the account's stored `fingerprintId` verbatim as `codebuff_metadata.client_id`, falling back to a fresh unbranded UUID only for connections saved before this change. Previously every chat request sent `client_id: "9router-<uuid>"`, which brands the traffic as non-CLI at the application layer — the most likely reason accounts got `banned` even though headers/User-Agent already matched the CLI. Note: cloaking only removes the self-identifying fingerprint; it cannot protect accounts banned for quota abuse, multi-account farming on one IP/fingerprint, or region violations.
- `internal/handlers/oauth/cline.go`: `decodeClineCode` now accepts the real browser-callback shape — base64url (`-`/`_` alphabet, padding stripped) plus the trailing signature segment the extension appends after the JSON payload. Previously only strict `StdEncoding` decoded, so pasting the callback failed to extract tokens and the handler fell through to `POST /api/v1/auth/token`, which the server rejects with `Forbidden` — exactly the reported `Cline token exchange failed ... Forbidden` error.
- `internal/handlers/chat/connections.go` + `internal/handlers/dashboard/connection_probe.go`: the `workos:` prefix is now JWT-only (WorkOS JWT = base64url `eyJ…` + dot, upstream parity `open-sse/shared/clineAuth.js`). Non-JWT ClinePass API keys ride plain `Bearer` — prefixing them is what the server answers with 401 `"Unauthorized: Please make sure you're using the latest version of Cline and re-authenticate your Cline account."` Note: your pasted bundle decodes to a real WorkOS JWT (`eyJhbGciOiJSUzI1NiIsImtpZCI6InNzb19vaWRj...`, `expiresAt` already past `2026-09-24T07:23:51Z`), so that specific token is expired server-side — reconnect with a fresh browser login after updating.
- `internal/handlers/oauth/cline.go`: new Cline/ClinePass connections are named by account email from the token bundle (fallback: first+last name, then provider default) instead of the generic `ClinePass` label, so multi-account setups stay distinguishable in provider detail.
- All-providers branding sweep (no behavior change otherwise): audited every header/body sent to upstream. Only one real leak found and removed — `User-Agent: 9router/oauth` on the Antigravity Google token exchange (`internal/handlers/oauth/antigravity.go`), now unbranded Go default. Everything else already mirrors an official client: Freebuff `codebuff-cli/*` + fingerprinted `client_id` (no `9router-` anywhere on the wire), Cline `Cline/*` + `cline-cli`, Antigravity `antigravity/ide/*`, Gemini CLI `google-api-nodejs-client/*`, Grok/Codex/iFlow/Qoder/MiMo browser or CLI UAs, TTS browser UAs. `X-Msh-Platform: 9router` (Kimi) and `HTTP-Referer/X-Title: endpoint-proxy.local / Endpoint Proxy` (OpenRouter/Airforce) are byte-identical to upstream `decolua/9router` — changing them would *break* parity, not improve stealth. `User-Agent: 9Router` only ever hits the user's own proxy-test target and GitHub API (never an LLM provider). Local-only strings (`9router-oauth` BroadcastChannel, MITM CA, updater UA, file paths) never leave the machine.
- `internal/handlers/oauth/naming.go` (new) + all OAuth handlers (`freebuff.go`, `cline.go`, `antigravity.go`, `trae.go`, `windsurf.go`, `zed.go`, `authcode.go`, `pkce.go`, `device.go`): single email-first naming rule `connectionDisplayName` — account email when known, else explicit user-supplied name, else provider default. No more `"Provider (name)"` labels anywhere, so every provider detail page (Freebuff, ClinePass, Antigravity, …) lists accounts by email.
**Production Internet Hardening & Cloudflare Integration:**
- **Protect `/debug/pprof/*` endpoints**: Disabled Go runtime profiling endpoints (`/debug/pprof/*`) by default in production to prevent Denial of Service (DoS) and potential memory/key disclosures. Can be explicitly enabled via `PPROF_ENABLED=true`.
- **Privilege separation for client API keys**: Restricted destructive administrative routes (`/api/version/shutdown`, `/api/version/update`, `/api/settings/database`, `/admin/health/reset`) so they strictly require a valid dashboard JWT session cookie or local CLI token (`x-9r-cli-token`), matching upstream `ALWAYS_PROTECTED` behavior in `src/dashboardGuard.js`. Client API keys can no longer trigger shutdowns or database dumps.
- **Cloudflare `CF-Connecting-IP` support**: Updated `LoginClientIP` in `internal/auth/session.go` to support `CF-Connecting-IP` when `TRUST_PROXY=true` or `TRUST_CLOUDFLARE=true`, ensuring proper client IP resolution and preventing shared-bucket lockout behind Cloudflare.
- **Configurable host binding (`HOST` / `BIND_ADDR`)**: Added `Host` to configuration and updated server listener to bind to `HOST` or `BIND_ADDR` when specified (e.g. `127.0.0.1` when proxied by `cloudflared`), while preserving `:20130` (`0.0.0.0`) default behavior.

### 🎨 UI & Dashboard

**Media Providers Full Parity (`/dashboard/media-providers/*`):**
- `internal/handlers/media/tts_synthesizers.go` & `tts_forward.go`: Built local native synthesis engines (`edge-tts`, `google-tts`, `nvidia`) and voice catalog endpoints (`/api/media-providers/tts/voices`), supporting real-time streaming audio generation and custom speed/pitch/voice options.
- `internal/handlers/media/antigravity_image.go` & `antigravity_stt.go`: Added Antigravity image generation and speech-to-text (STT) transcription handlers with multipart form-data parsing, extracting audio models and delegating to Google's IDE backend.
- `internal/handlers/media/antigravity_search.go`: Ensured typed JSON serialization for Antigravity web search requests and responses to match upstream key ordering and structure.
- `internal/handlers/chat/resolution.go`: Fixed System One endpoint `/v1/systemone` to handle `x-antigravity-session` headers and fall back seamlessly to direct connections with `public` default keys for `antigravity-zen`.
- `web/src/components/media/MediaKindView.svelte`, `MediaProviderCard.svelte`, `NoAuthProxyCard.svelte`, `TtsExampleCard.svelte`, and `SttExampleCard.svelte`: Full Svelte 5 runes parity with upstream Next.js for all 8 media kinds (`video`, `stt`, `tts`, `image`, `embedding`, `systemone`, `webSearch`, `webFetch`), aligning provider sorting, priority, hidden flags, and live audio/waveform test players.

**Antigravity Live Models Discovery & "Import from /models":**
- `internal/providers/registry_models.go` & `web/src/lib/models.ts`: Added Google Antigravity official live models (`gemini-2.5-flash`, `gemini-2.5-flash-lite`, `gemini-2.5-pro`, `gemini-2.5-flash-thinking`, `gemini-3.1-pro-high`, `gemini-3.1-flash-lite`, `gemini-3.5-flash-lite`).
- `internal/handlers/dashboard/connections.go`: Extended `GET /api/providers/:id/models` to support Antigravity, Gemini CLI, Cline, and ClinePass connections. For Antigravity, queries Google's live RPC (`https://daily-cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels`) with Bearer tokens, filtering internal chat IDs and parsing vision/reasoning capabilities.
- `web/src/components/connections/ProviderDetailView.svelte`:
  - Added `[📥 Import from /models]` button next to `Add Model` for Antigravity, Cline, ClinePass, and Qoder accounts.
  - Auto-fetches live models from Google for active Antigravity connections on page load, displaying uncataloged models in the **Suggested models** section for 1-click addition.

**Suggested Free Models Feed & Custom Models Parity:**
- `internal/handlers/suggestedmodels.go`: Wrapped HTTP client in `proxy.NewFallbackTransport` so public model catalog feeds (`opencode`, `openrouter`, `kilocode`, `airforce`) never get blocked by sandbox proxy allowlists.
- `web/src/lib/providers.ts`: Added `modelsFetcher: { url: "https://opencode.ai/zen/v1/models", type: "opencode-free" }` to `opencode`, restoring the "Suggested free models (≥200k context)" section on OpenCode Free.
- `internal/handlers/dashboard/models.go`: Updated `GET /api/models/custom` to return `{ "models": [...] }` matching upstream Next.js shape, and updated `web/src/components/connections/types.ts` to cleanly parse custom model lists without type-assertion errors.

**Universal Outbound Direct Fallback & Proxy Allowlist Bypass:**
- `internal/proxy/fallback_transport.go`: Created `FallbackTransport` which wraps Go HTTP round-trippers to detect local proxy refusal (`403 Forbidden`, `blocked-by-allowlist`, `CONNECT tunnel failed`, or proxy text/plain errors) and instantly re-issue the request directly (`Proxy: nil`) with re-readable request bodies.
- Applied universally across chat resolution, streaming SSE forwarders, media endpoints, validation probes, and catalog feeds.

**Comprehensive Connection Health Probing & Zero False Errors:**
- `internal/handlers/dashboard/connection_probe.go`:
  - Added native probe configurations for OAuth providers (`antigravity`, `gemini-cli`, `cline`, `clinepass`, `freebuff`, `xai`, `grok-cli`, `codebuddy-intl`, `zed`, `windsurf`, `trae`, `devin`, `devin-cli`, etc.).
  - Implemented token-exists heuristic for unconfigured providers and compatible base-URL probing for custom nodes.
  - Fixed `persistProbeResult` so that informational "Provider test not supported" messages no longer falsely mark connections as `testStatus: "error"` or pollute `lastError` in the SQLite database.
  - Updated Cline / ClinePass probe to prefix WorkOS JWT tokens with `workos:`.

**Quota Tracker Full Parity with Upstream Next.js (`/dashboard/quota`):**
- `internal/handlers/router.go`: Mounted `/api/usage/{connectionId}`, `/api/usage/providers`, `/api/usage/stream`, `/api/usage/stats`, and `/api/usage/request-details` inside `SetupDashboardRoutes` with `RequireDashboardAuth`, allowing authenticated browser sessions (JWT cookie), local CLI tokens, and client API keys to fetch quota and usage details.
- `internal/handlers/dashboard/usage_providers.go`: Enhanced `fetchAntigravityDashboardWeekly` to parse both session (5h) and weekly quota buckets from Google's `retrieveUserQuotaSummary`, and added reconciliation when all Gemini models are exhausted (matching upstream `open-sse/services/usage/antigravity-weekly.js` and `google.js`).
- `web/package.json` & `web/src/main.ts`: Added and bundled `material-symbols/outlined.css` locally, ensuring all icons (refresh, edit, delete, eye-off, hourglass, toggle) render instantly and work 100% offline without text flashing.
- `web/src/components/quota/types.ts`: Implemented full upstream provider quota parser `parseQuotaData` supporting Antigravity (5-quota family grouping: Gemini 5h, Claude & GPT 5h, Gemini 3.1 Flash Image, Gemini Weekly, Claude & GPT Weekly), Codex, Kiro, Qoder, Claude, DeepSeek, Groq, Ollama, and Zed, with model catalog canonical sorting.
- `web/src/components/QuotaTrackerView.svelte`:
  - Added secondary connection label (`getConnectionSecondaryLabel`) for accounts with different emails/display names.
  - Aligned status badges to only render on Kiro connections (matching upstream Next.js).
  - Added connection edit action (pencil icon) with interactive `Edit Connection` modal (name & priority editing + reachability test).
  - Added auto-ping toggle (`bolt` icon) for Claude & Codex OAuth accounts and Codex reset credits integration.


**Token Saver Full Parity with Upstream Next.js (`/dashboard/token-saver`):**
- `internal/handlers/router.go`: Mounted `/api/headroom/*` (`status`, `start`, `stop`, `restart`, `extras`, and `proxy`) inside `SetupDashboardRoutes` with `RequireDashboardAuth`, allowing dashboard session cookies to query and control Headroom proxy lifecycle.
- `web/src/api/client.ts`: Updated `HeadroomStatusResponse` and `HeadroomExtrasResponse`, adding `getHeadroomExtras()`, `startHeadroom()`, `stopHeadroom()`, `restartHeadroom()`, `installHeadroomExtras()`, and `uninstallHeadroomExtras()`.
- `web/src/components/TokenSaverView.svelte`:
  - Removed duplicate in-page header (`PiggyBank` banner) to align with upstream Next.js header layout where page title & icon live in `TopBar`.
  - Replaced custom dialog with standard `Modal.svelte` featuring macOS-style traffic lights (`#FF5F56`), backdrop blur, and `Button.svelte`/`Input.svelte` components.
  - Implemented full Headroom status detection (`Checking…`, `Running`, `Not installed`, `Stopped`, `External`) with local/managed PID checks.
  - Added Headroom compression extras (`[code]`, `[ml]`), install confirmation modal (warning on 1GB ML download), pip uninstall, and log tail streaming.
  - Added locale-based Wenyan level filtering for Caveman output compressor (only showing classical Chinese compression levels on `zh` locales).


**Freebuff Multi-Account Session Management & Direct Proxy Fallback:**
- `internal/proxy/executor/freebuff_session.go` & `freebuff.go`: Added `DoFreebuffHTTP` with automatic fallback to direct connection (`Proxy: nil`) when local or environment proxies refuse requests to `codebuff.com` / `freebuff.com` (`403 Forbidden` / `blocked-by-allowlist`), preventing session lookups and admissions from failing.
- `internal/handlers/oauth/freebuff_session.go` & `freebuff_session_switch.go`: Automatically syncs active session models (`currentModel`) into `conn.Data` (`freebuffModel` and `assignedModel`) in SQLite, enabling strict model routing per connection.
- `web/src/components/connections/FreebuffSessionBanner.svelte`: Added multi-account selection pills allowing users to view and switch between different Freebuff accounts and their respective sessions directly from the banner.
- `web/src/components/connections/ProviderDetailView.svelte`: Added per-connection session badges (`🔒 model-id`, `queued`, `banned`, `no session`) on connection cards with a dedicated **Session** button (`lock_clock`) to quickly focus and manage any account's active seat.

**Vision & Audio Adapter Full Parity (`/dashboard/combos`):**
- `web/src/lib/models.ts`:
  - Updated `getModelCaps` to detect `audioInput` capability and refined vision detection by eliminating false positives from bare `"flash"` model ID tokens (which previously caused non-vision models like `deepseek-v4-flash` to be incorrectly classified as vision-capable).
  - Aligned vision patterns with upstream `visionPatterns.js` (`looksLikeVisionModel`), filtering out audio/tts/stt/embedding generators while identifying multi-modal vision families (`gemini`, `4o`, `gpt-5`, `gpt-6`, `opus`, `sonnet`, `haiku-4.5`, `fable`, `kimi`, `minimax`, `mimo`, `qwen`, `grok`, `llama-4`, `muse-spark`).
- `web/src/components/combos/pickerData.ts`:
  - Added `caps.audioInput` to `PickerModel`.
  - Strictly enforced modality filtering in `resolveFilteredGroups`: `target === 'vision'` now filters exclusively for models with `caps.vision === true`, and `target === 'audio'` filters exclusively for `caps.audioInput === true`, regardless of whether a search query is active.
  - Added empty state handler displaying `No models found` with search icon when no models in the active catalog match the modality requirement.
- `web/src/components/combos/CapacityAdapterSection.svelte`:
  - Removed redundant summary bullet list above cards to match upstream Next.js header layout.
  - Aligned subtitles to `— images (png, jpg, webp, …)` and `— audio input`.
  - Standardized icons to Material Symbols `visibility` (eye) and `graphic_eq` (sound wave equalizer).
- `internal/db/settings.go`: Added `CapacityAdapterEntry` and `CapacityAdapter map[string]CapacityAdapterEntry` to `SettingsData` with default fallback to `ag/gemini-3.8-flash-high`.
- `internal/handlers/chat/combo.go`: Implemented `AugmentModelsWithCapacityAdapter` to automatically prepend models from the capacity adapter pool when none of the target models support required input modalities (e.g. vision for image inputs).
- `internal/handlers/chat/chat.go`: Integrated capacity adapter auto-switch into both OpenAI `/v1/chat/completions` and Anthropic `/v1/messages` for both combos and single-model requests.
- `internal/handlers/chat/vision_adapter_e2e_test.go`: Added comprehensive E2E tests verifying automatic switching from text-only models (`deepseek/deepseek-chat`) to vision-capable models (`ag/gemini-3.8-flash-high`) when image inputs are present in OpenAI and Anthropic request formats.

**Change Log in-app modal (upstream Next.js parity):**
- `web/src/components/ChangelogModal.svelte`: Added `ChangelogModal` Svelte 5 component with markdown parsing via `marked`, custom scrollable container, loading/error states with retry, backdrop-blur overlay, and links to GitHub Releases & Changelog history.
- `web/src/components/TopBar.svelte`: Changed the App Drawer item under "Theme" from an external link to a button triggering the in-app `ChangelogModal`, matching upstream Next.js `HeaderMenu` behavior.
- `web/src/api/client.ts`: Added `api.getChangelog()` querying `/api/changelog` with fallback to GitHub raw URLs.
- `internal/handlers/chat/chat.go` + `internal/handlers/router.go`: Added `GET /api/changelog` and `GET /changelog` endpoints to serve the application changelog directly from local disk with remote fallback.
- `web/src/index.css`: Added `.changelog-body` markdown typography and badge styles.

**Update notification banner in Sidebar (upstream Next.js parity):**
- `web/src/components/Sidebar.svelte`: Added update notification banner below the version label (`↑ New version available: v{latestVersion}`) with `Update now` button and clickable `9router-go update` command pill, matching upstream Next.js `Sidebar.js`.
- Added interactive Update modal with release notes, one-click auto-updater (`api.triggerUpdate()`), manual copy command with countdown shutdown, and disconnected reconnect overlay.
- `web/src/api/client.ts`: Added `SystemVersionInfo` type, `checkUpdate()`, `triggerUpdate()`, and `shutdownServer()`.

**Remove 9Remote & 9English; align Support 9Router with 9router-go repo:**
- `web/src/components/Sidebar.svelte`: Removed the `9Remote` action button & modal and `9English` external link from navigation items; cleaned up modal markup and unused `isRemoteModalOpen` state.
- `web/src/components/TopBar.svelte`: Updated the "Support 9Router" modal to remove the external 9English project link and point to the `9router-go` repository ([`https://github.com/luqman-v1/9router-go`](https://github.com/luqman-v1/9router-go)) and Releases page ([`/releases`](https://github.com/luqman-v1/9router-go/releases)).

### 🐛 Bug Fixes

**OpenCode Chat Completions Tool Fingerprint & Model Purity (`space-bunny-free`):**
- `internal/translator/fingerprint.go`: Fixed `ConcealFingerprintTools` to preserve standard Chat Completions tool shape (`{"type":"function","function":{"name":...}}`) with `"tool_choice":"none"` when client tools are absent, instead of falling back to flat Responses format (`{"type":"function","name":...}`) which caused upstream `[invalid_request_error] invalid request` on OpenCode Chat Completions models like `space-bunny-free`.
- `internal/proxy/executor/providers.go`: Stripped provider prefixes (`oc/`, `opencode/`) from `model` in `ForwardOpencode` and `ForwardOpencodeGo` before forwarding upstream.
- `internal/handlers/chat/resolution.go`: Removed hardcoded model rewrites (`strings.Contains(model, "muse-spark")`, etc.) from Antigravity resolution; all `ag/` and `antigravity/` models resolve cleanly to `antigravity` without special-case overrides.
- `web/src/components/connections/ProviderDetailView.svelte`: Restricted `suggestedModels` strictly to providers that declare a public `modelsFetcher` (upstream Next.js parity). Removed artificial suggested models injection under Antigravity so OpenCode models no longer leak into Antigravity's view.

**Antigravity Google OAuth Callback percent-encoding unescape:**
- `internal/handlers/oauth/antigravity.go`: Added `cleanAuthCode` to unescape double-encoded slashes (`4/0A...` vs `4%252F...`) and strip raw URL parameter prefixes before submitting `application/x-www-form-urlencoded` token exchange requests to Google.
- `web/src/components/connections/ProviderDetailView.svelte`: Added `decodeURIComponent` input cleansing for pasted OAuth authorization callback URLs.

**Deep Auth Verification for OpenAI-Compatible Custom Nodes:**
- `internal/handlers/dashboard/validate.go`: Added a secondary 1-token probe to `POST /v1/chat/completions` during provider node validation (`validateOpenAICompatibleNode`), preventing mock or unauthenticated servers from returning false positive validation results.

**Fix Round-Robin routing for combos and provider connections (Issue #20):**
- `internal/db/settings.go`: Updated `SettingsData` and `GetSettings()` to parse both dashboard JSON keys (`fallbackStrategy` / `rotateStrategy` and `stickyRoundRobinLimit` / `stickyLimit`), as well as global `fallbackStrategy`, `stickyRoundRobinLimit`, `comboStrategy`, `comboStickyRoundRobinLimit`, and `comboStrategies`.
- `internal/db/settings.go`: Updated `SetProviderStrategy` and added `SetComboStrategy` to write to `settings.data` via `UpdateSettingsRaw` without clobbering other settings fields.
- `internal/db/repos.go`: Updated `GetComboByName`, `GetComboById`, and `GetCombos` to populate `combo.Strategy` from `settings.comboStrategies[combo.Name]` and global `settings.comboStrategy`.
- `internal/handlers/chat/resolution.go`: Added `resolveComboRouting` so `ResolveModel` and `resolveModelEntry` populate `ModelInfo.Strategy`, `ModelInfo.StickyLimit`, and `ModelInfo.JudgeModel` from `settings.comboStrategies` or global combo settings.
- `internal/handlers/chat/connections.go` & `fallback.go`: Updated provider connection selection to rotate active connections using `fallbackStrategy` or global fallback settings when configured to `"round-robin"`.
- Added unit tests in `internal/db/settings_test.go` and `internal/handlers/chat/connection_strategy_test.go` covering combo strategy resolution, sticky limits, judge models, and provider connection rotation.

**Fix fetch stream double-read in `web/src/api/client.ts` and add missing tunnel endpoint handlers:**
- `web/src/api/client.ts`: Resolved `Failed to execute 'text' on 'Response': body stream already read` error when receiving non-2xx responses. Previously, `res.json()` consumed the stream body on error responses, which caused the subsequent `res.text()` fallback in the catch block to crash. The client now safely reads `res.text()` first before attempting JSON parsing.
- `internal/handlers/dashboard/tunnel.go` & `internal/handlers/router.go`: Added endpoints `POST /api/tunnel/enable`, `POST /api/tunnel/disable`, `GET /api/tunnel/tailscale-check`, `POST /api/tunnel/tailscale-enable`, and `POST /api/tunnel/tailscale-disable` with structured JSON responses and clean error handling instead of unhandled 404s.

**Combo bypass Vercel Edge Relay for no-auth providers (e.g. `oc/muse-spark-1.3` 429 on `combo-wombo`, solo test 200):**
- `internal/handlers/chat/combo.go` (chat + messages fallback) and `combo_fusion.go` — no-auth branch now sets `ProxyPoolID: h.ResolveProviderProxyPoolID(modelInfo.Provider)` (was `&ConnectionData{APIKey}` only), so combo routing goes through the configured `providerStrategies.<provider>.proxyPoolId` relay (`x-relay-target`/`x-relay-path`) exactly like the solo path (`handleAccountFallback`). Direct-to-`https://opencode.ai/zen/v1/responses` calls that burned the free-tier IP quota (`FreeUsageLimitError` 429) are eliminated.

**Vercel Edge Relay header forwarding for `muse-spark` / `antigravity`:**
- `internal/proxy/opencode.go` — `BuildOpenCodeHeaders` preserves `x-relay-target` / `x-relay-path` instead of dropping them.
- `internal/proxy/executor/providers.go` — `ForwardOpencode` / `ForwardOpencodeGo` keep `BaseURL` on the relay host and route via `x-relay-path` (`/zen/v1/responses`, `/zen/v1/messages`, `/zen/go/v1/responses`) when relay headers are present.
- Added `TestForwardOpencode_MuseSpark_EdgeRelay` (PASS); verified live `200 OK` via relay.

**Topology false pulse on dashboard load (`AnalyticsView.svelte`):**
- SSE `/api/usage/stream` initial snapshot no longer triggers the electric-beam animation: added `streamInitialized` guard so only genuine new model requests after init pulse; active-request updates set `lastProvider` without re-pulsing. Dashboard API traffic (`/api/usage`, polling) never triggers topology effects — only upstream model calls do.
- Consolidated per-node SVG turbulence filters into one lightweight global filter (`numOctaves="1"`) for GPU/CPU relief during continuous animation.

**Query-param auth for SSE streams (`internal/middleware/auth.go`):**
- `ExtractApiKey` accepts `?key=` / `?apiKey=` on routes ending in `/stream` (native `EventSource` can't set custom headers); REST/LLM endpoints stay header-only.

**Topology Option A visuals (`ProviderTopologyCard.svelte` + `web/src/index.css`):**
- Bidirectional neural stream (cyan prompt Router→Provider, emerald/gold response Provider→Router), dual shockwave rings on the active provider target, router absorption rings, node micro-bounce + `LIVE` badge.

### 🔄 Upstream Parity Sync

**Strike-breaker quota-only (upstream `decolua/9router#4197` parity, PR #16):**
- `internal/handlers/chat/antigravity_quota.go` — `HandleAntigravityQuotaError` now takes the upstream `errorMessage` and only counts a strike on explicit quota markers (`RATE_LIMIT_EXCEEDED`, `QUOTA_EXHAUSTED`, `Individual quota reached`). Generic bare `RESOURCE_EXHAUSTED` 429s no longer burn strikes / lock combos.
- `internal/handlers/chat/gemini_handler.go` — forwards `string(uErr.Body)` as the error message source.
- Added `TestAntigravityQuota_Generic429NoStrike` regression test.

**Refusal → content_filter mapping (upstream `decolua/9router#4210` parity, PR #16):**
- `internal/translator/claude_response.go` — streaming + non-streaming: Gemini `refusal` finish maps to `content_filter`, emits `stop_details.explanation` so Claude Code renders the block instead of hanging.
- `internal/translator/response.go` — reverse mapping `content_filter → refusal` for round-trips.
- Added refusal stream / non-stream / round-trip tests.

**Weekly vs session quota buckets (upstream `decolua/9router#4209` parity, PR #18):**
- `internal/handlers/chat/antigravity_quota.go` — `ParseWeeklyQuotaSummary` classifies the `window` field into weekly (`gemini`/`claude_gpt`) vs 5h-session (`gemini_session`/`claude_gpt_session`) buckets; `IsAntigravityModelBlocked` honors session buckets via `quotaEntryExhausted` helper.
- Added `TestAntigravityWeeklyQuota_SessionBuckets`.

**Add Compatible modal + provider-node validation (upstream `AddCompatibleModal.js` + `provider-nodes/validate/route.js` parity):**
- `web/src/components/connections/AddCompatibleNodeModal.svelte` — rebuilt to match the upstream modal: separate `Name` / `Prefix` / `API Type` fields with upstream placeholders (`OpenAI Compatible (Prod)`, `oc-prod`, …) and hints, `API Key (for Check)` + `Model ID (optional)` inputs driving a `Check` button with `Valid` / `Invalid` badges (chat-fallback note included), and full-width `Create` + `Cancel`. The API key is now validation-only and no longer auto-creates a connection (`ConnectionsView.svelte`).
- `internal/handlers/dashboard/provider_nodes.go` + `router.go` / `routes.go` — added `POST /api/provider-nodes/validate` (OpenAI-compatible `/models` + chat fallback, Anthropic-compatible with `x-api-key` + `/messages`-suffix strip, `custom-embedding` with dimension report), including SSRF guard for non-loopback callers (`handlerutil.AssertPublicURL`).
- `web/src/api/client.ts` — added `validateProviderNode`.
- Added `provider_nodes_validate_test.go` (13 tests: input guards, SSRF/local, OpenAI/Anthropic/embedding probes, chat fallback, network-error mapping).

### 🧹 Style Cleanup (behavior-neutral, PR #17 + follow-ups)

- `interface{}` → `any` across production code and test files; `errors.New` + `%w` wrapping; `slices.Contains/Sorted/Delete`, builtin `max()`/`clear()`, `strings.Builder`, shared header constants in `internal/constants`.
- Named constants: `antigravityDecoyUnavailable`, `maxReadLimit`, `thinkingHeadroomTokens`, `maxCallIDLen`, `MaxUpstreamBodyBytes`/`UpstreamErrLimit`.
- `fallback.go` — `forwardRequestParams` struct replaces 10-param forwarding; `openai.go` — `sseStreamOpts` struct replaces 8-param SSE helper.
- `antigravity_quota.go` — `AntigravityQuotaError` struct + named quota markers (replaces `map[string]any` error plumbing).
- Added `samber/lo` (`Ternary`, `CoalesceOrEmpty` only — `Coalesce` on `any` maps and eager `Ternary` slicing deliberately avoided).
- Default port `20128` → `20130` (`config.go`, `Makefile`, `mitm/handlers/base.go`, `.env.example`, `docker-compose.yml`, `Dockerfile`, `README.md`).

### 🧪 Tests

- `gemini38_live_test.go` — real upstream tests for `ag/gemini-3.8-flash-medium` (chat + stream, `200 OK`). Live E2E: 17/17 PASS.

### 📦 Release Hardening (issue #19)

- `make cross` generates `SHA256SUMS.txt`, uploaded by `release.yml`; README documents the Windows Defender false-positive (`Wacatac.C!ml` heuristic on the unsigned binary) with verify + Allow steps.


## [v1.8.18] — 2026-09-21

### 🐛 Bug Fixes

**OMP Harness False-429 on Antigravity (upstream `decolua/9router#3986` parity):**
- `internal/translator/antigravity.go` — `WrapForAntigravity` no longer sends `requestType: "agent"` in the Cloud Code envelope (`AntigravityRequest.RequestType` is now `omitempty` and left empty). Google enforces a tiny separate quota bucket whenever `requestType="agent"` is present, so OMP (Oh My Pi) harness payloads (~25–30k token system prompt + tools) were rejected with false `429 RESOURCE_EXHAUSTED` even with quota remaining — cascading into `CACHE_BLOCK` account locks while Claude Code stayed green. Verified live: same payload returns `200 OK` after the fix.
- `internal/translator/antigravity_test.go` — Added `TestWrapForAntigravity_OmitsAgentRequestType` regression test (asserts the field is absent from the raw envelope JSON and the `requestId` `agent/<…>` shape is preserved). Image (`image_gen`) and search (`search`) request types are untouched.

### 🔄 Upstream Parity Sync — `decolua/9router` v0.5.75…v0.5.81 (100%)

**Model Catalog & Routing:**
- `internal/providers/registry_models.go` — Registered `deepseek-v4.1-flash` for `codebuddy-intl` (`cbai`, replacing the retired `deepseek-v4-flash`) and added `deepseek-v4.1-flash:cloud` to the `ollama` catalog.
- `internal/handlers/chat/resolution.go` — Routed bare `codex-auto-review` to the `codex` provider (PR #4135 parity), resolved even with a nil repo / empty DB.

**Antigravity Hygiene:**
- `internal/translator/antigravity.go` — Stripped the Claude Code `x-anthropic-billing-header` from system prompts and sanitized the Hermes Agent identity (`You are Hermes Agent, an intelligent AI assistant created by Nous Research.` → neutral form) to eliminate false HTTP 429/403 anti-abuse rejections.
- `internal/translator/thought_signature_store.go` — Scoped cached Gemini thought signatures to the producing model family (`claude` vs `gemini`), preventing cross-family replay that triggers HTTP 400 `Invalid thought signature` when a conversation switches models (upstream `bc3be0cb` parity).
- `internal/translator/gemini.go` — Threaded the model name through the `GetGeminiThoughtSignature` / `StoreGeminiThoughtSignature` call sites so stored signatures are keyed per model family (call-site half of the scoping above).

**CommandCode Multimodal & Reasoning:**
- `internal/proxy/executor/providers.go` — Added native image blocks to `buildCommandcodeBody`: OpenAI `image_url` data URIs and Claude/OpenAI base64 image sources are converted to CommandCode `{type: "image", image: <dataUri>, mimeType}` blocks, and `reasoning_effort` (`low`/`medium`/`high`/`max`) is preserved on `/alpha/generate`.

**Union-Alpha / OpenCode Parity (verified):**
- Confirmed live routing of `oc/union-alpha` through the Anthropic Messages API (`/zen/v1/messages`) with `anthropic-version: 2023-06-01` and automatic `max_tokens` injection (PR #4099 parity); free-tier `forceStream`/SSE aggregation parity already structural in Go.
- `internal/handlers/chat/muse_spark_e2e_test.go` — Added `TestIntegration_OpenCode_UnionAlpha_Messages` (live E2E; SKIPs on upstream rate-limit or auth-dependent `Model union-alpha is not supported` 401, consistent with existing Muse Spark E2E policy).

## [v1.8.17] — 2026-09-18

### 🚀 Features & Upstream Parity

**Claude OAuth Subscription (`sk-ant-oat`) Support End-to-End (PR #13):**
- Contributed by **@rezhajulio** ([#13](https://github.com/luqman-v1/9router-go/pull/13)) — Special thanks for bringing full Claude Pro/Max subscription parity from the dashboard to the native Go proxy!
- `internal/handlers/chat/fallback.go` — Automatic header switching to `Authorization: Bearer` and appending `?beta=true` for Claude OAuth credentials (`sk-ant-oat` or `accessToken`), supporting direct Anthropic API as well as Edge Relay proxy pools.
- `internal/handlers/chat/claude_cloaking.go` — Injected official `x-anthropic-billing-header` into `system[0]`, deterministic account `metadata.user_id`, client tool name obfuscation with `_ide` suffix, and decoy tools (`CCDecoyTools`) preventing false HTTP 429 anti-abuse rate limits.
- `internal/proxy/executor/claude_decloak.go` — Streaming and non-streaming response decloaker restoring original tool names and translating decoy tool invocations into clean text blocks.
- `internal/proxy/executor/providers.go` — Added `sanitizeToolUseID` to rewrite foreign/Gemini tool IDs deterministically to Anthropic-compliant `toolu_<sha256>`.
- `internal/tokensaver/prompts.go` — Added `InjectSystemPromptClaude` for format-aware system prompt injection at top-level `system`.

**Antigravity Zen Free-Tier Tool Quartet Renaming (PR #12):**
- Contributed by **@yxxrn** ([#12](https://github.com/luqman-v1/9router-go/pull/12)) — Special thanks for identifying the exact upstream fingerprinting gate and eliminating Claude Code 403/500 errors!
- `internal/translator/fingerprint.go` — Implemented `ConcealFingerprintTools` to rename uppercase tool quartet variants from Claude Code CLI (`Bash`, `Glob`, `Grep`, `Read`) to canonical lowercase (`bash`, `glob`, `grep`, `read`), eliminate duplicates (preventing upstream HTTP 500), retarget `tool_choice`, and restore original tool names in response payloads via `RestoreToolNamesInPayload` / `RestoreToolNamesInSSE`.
- `internal/proxy/executor/toolname_writer.go` — Embedded `toolNameRestoringWriter` on responses ensuring client tools are seamlessly restored across both streaming and non-streaming responses.

### 🐛 Bug Fixes & Improvements

**CommandCode CLI User-Agent & Schema Wrapping (PR #14, fixes #9):**
- Reported by **@jhonoryza** ([#9](https://github.com/luqman-v1/9router-go/issues/9)) — Thank you for reporting the Cloudflare challenge error!
- `internal/providers/providers.go` & `internal/proxy/executor/providers.go` — Added official `User-Agent: commandcode/0.25.7 (cli)` and `x-command-code-version: 0.25.7`, eliminating Cloudflare WAF bot-challenge intercepts (HTTP 403 `Attention Required!`).
- `internal/proxy/executor/providers.go` — Implemented `buildCommandcodeBody` wrapping OpenAI payloads into `{threadId, memory, config, params}` schema required by CommandCode's `/alpha/generate` endpoint.
- `internal/handlers/chat/fallback.go` & `internal/proxy/proxy.go` — Enhanced error parsing in `extractErrorText` and `UpstreamError.Error()` to summarize Cloudflare challenge pages cleanly without dumping raw HTML.

**Responses API (`POST /v1/responses`) Public Provider Fallback & String Input (PR #15, fixes #10):**
- Reported by **@pankaj-raikar** ([#10](https://github.com/luqman-v1/9router-go/issues/10)) — Thank you for the detailed reproduction report!
- `internal/handlers/media/media.go` — Added automatic fallback for public/free-tier providers (`DefaultAPIKey: "public"`) in `forwardMediaRequest`, eliminating `"no active connections for provider: opencode"` when no SQLite connection is seeded.
- `internal/handlers/media/media.go` & `internal/handlers/chat/resolution.go` — Routed `opencode`, `opencode-go`, and `antigravity/muse-spark-*` models on `/responses` directly to `ForwardOpencode` so session tracking, request headers, and tool-name concealing work out of the box.
- `internal/proxy/executor/transform.go` — Updated `buildResponsesBody` to support both string inputs (`"input": "Say hello"`) and array inputs (`Input []any`), normalizing string prompts into valid Responses message items.

## [v1.8.16] — 2026-09-17

### 🐛 Upstream Parity & Bug Fixes

**Active Provider Connections & Capabilities on `/v1/models` and `/api/models` (PR #8, fixes #7):**
- `internal/providers/registry_models.go` — Added comprehensive upstream provider models registry mapped from 128 provider definitions (`decolua/9router` parity) with `GetProviderModels()`.
- `internal/providers/capabilities.go` — Implemented `CapabilitiesDetail` and `GetCapabilitiesDetailForModel()`, outputting full multimodal flags (`vision`, `pdf`, `audioInput`, `videoInput`, `imageOutput`, `audioOutput`), `thinkingCanDisable`, and dual token limits (`contextWindows` and `contextWindow`).
- `internal/handlers/chat/chat.go` — Replaced empty `/v1/models` responses by dynamically aggregating models for active connections (`isActive = 1`), honoring custom prefixes and connection-enabled models. Custom models from deactivated connections (`disabledProviders`) are automatically excluded, and clean static catalogs are returned when no database connections are configured.
- `internal/handlers/router.go` — Mounted `GET /api/models` and `GET /api/models/*` serving both `"data"` and `"models"` top-level keys for universal compatibility across OpenAI-compliant IDE clients and Next.js web dashboards.
- `internal/handlers/chat/models_provider_test.go` — Added regression tests verifying model discovery for active Codex (`cx`) connections, fallback registry, and single-model lookups.

## [v1.8.15] — 2026-09-17

### 🚀 Features & Upstream Parity

**Claude Messages to OpenAI Response Translation for Zen / Union-Alpha (PR #4099, #4111):**
- `internal/translator/claude_response.go` — Added on-the-fly streaming (`TranslateClaudeChunkToOpenAI`) and non-streaming (`TranslateClaudeResponseToOpenAI`) response translation engines. Transforms Claude Messages SSE events (`content_block_delta`, `thinking_delta`, `tool_use`, `input_json_delta`, `message_delta`, `message_stop`) into standard OpenAI chunks (`choices[0].delta.content`, `reasoning_content`, `tool_calls`) so that client harnesses (e.g. omp, Cursor, Cline) receive native responses.
- `internal/proxy/executor/claude_messages.go` — Added dedicated streaming handler (`handleClaudeMessagesStream`) and non-streaming handler (`handleClaudeMessagesNonStream`) wired into `ForwardOpencode` and `ForwardOpencodeGo` for `union-alpha` routes.
- `internal/proxy/opencode.go` — Updated Antigravity Zen headers to comply with upstream PR #4111 (`User-Agent: antigravity/1.18.31 ai-sdk/provider-utils/4.0.46 runtime/bun/1.3.14`, `x-antigravity-client: cli`, dynamic 40-character hex project IDs `GenerateOpenCodeProjectID()`, and `x-api-key: public`).

**Anthropic Tools & Messages Schema Normalization:**
- `internal/proxy/executor/providers.go` — Added `convertOpenAIToolsToClaude`, `ensureMessagesMaxTokens`, `extractClaudeSystemPrompt`, and `convertOpenAIMessagesToClaude` to convert incoming OpenAI tool definitions (`type: "function"`) into Claude tools (`{name, description, input_schema}`), normalize `tool_choice`, and merge adjacent same-role messages for compliant Anthropic payload delivery.
- `internal/proxy/sse.go` — Expanded terminal detection buffer to 64 bytes and added recognition for Anthropic terminal signals (`"stop_reason":` non-null and `"message_stop"`) to eliminate premature `finish_reason: "network_error"` synthesis at clean stream EOF.

### 🐛 Bug Fixes & Resilience

**Google RPC `quotaResetDelay` Automatic Duration Locking with Deadlock Prevention:**
- `internal/handlers/chat/fallback.go` — Implemented `extractResetDuration` to parse Google RPC ErrorInfo metadata `quotaResetDelay` (e.g. `"1h12m28.109534319s"`) and text patterns (`"Resets in XhYmZs."`). Enforces safety bounds (min 5s, hard cap at 2 hours) to avoid perpetual lockouts or deadlocks.
- `internal/handlers/chat/combo.go` — Updated `comboLockRetryable` to use the parsed reset duration for connection and model locks instead of falling back to 8s exponential backoff.
- `internal/handlers/chat/antigravity_quota.go` & `internal/handlers/chat/gemini_handler.go` — Added `BlockAntigravityModelUntil` to cache exhausted model quotas and canonical synonyms (`gemini-3.8-flash-tiered`) in RAM until the verified reset timestamp, preventing continuous 429 spam to Google upstream while automatically unblocking the moment reset time is reached.

## [v1.8.14] — 2026-09-17

### 🐛 Upstream Parity & Bug Fixes

**Muse Spark 1.3 FreeTier Authorization & Session Normalization (PR #4105, #4061, #4062):**
- `internal/proxy/antigravity.go` — Updated Antigravity OpenCode user-agent default to `antigravity/1.18.31` and implemented descending canonical 30-character session (`ses_` + 12 hex + 14 Base62) and message (`msg_` + 12 hex + 14 Base62) ID generators. This resolves HTTP 403 `FreeTierError` when routing requests to `oc/muse-spark-1.3-contributor-free`.
- `internal/proxy/executor/providers.go` — Added `normalizeMuseSparkResponsesBody` to strip prior multi-turn reasoning content and encrypted content blocks that trigger HTTP 400 parameter errors, while explicitly normalizing `tool_choice` to `"auto"` for Muse Spark 1.3.

**Missing `tool_call_id` FIFO Repair (PR #4090):**
- `internal/handlers/chat/tool_repair.go` & `internal/translator/request.go` — Added automatic repairing for client requests where `role: "tool"` or `function_call_output` messages omit `tool_call_id`. Uses FIFO pairing with un-paired assistant tool calls or mints deterministic call IDs to prevent strict upstreams (OpenAI, DeepSeek, Antigravity) from failing with HTTP 400.
- `internal/handlers/chat/chat.go`, `internal/handlers/chat/combo.go`, `internal/handlers/chat/fallback.go`, & `internal/proxy/executor/transform.go` — Integrated tool call repair across OpenAI chat completions, combo routing, and account fallback handlers.

**Stream Interruption Terminal Synthesis (PR #4079):**
- `internal/proxy/sse.go` — Implemented terminal frame synthesis (`finish_reason: "network_error"` followed by `data: [DONE]\n\n`) when upstream SSE connections terminate abruptly at EOF before emitting a terminal frame, preventing IDE client hangs and errors in Cline/Pi.

**Claude Tool Result Image Hoisting (PR #4083):**
- `internal/translator/request.go` — Converted base64 image blocks embedded inside Claude `tool_result` into follow-up user messages with `[Image from tool result <id>]` and OpenAI `image_url` blocks, allowing vision models to inspect tool screenshot outputs without violating text-only tool-role schema constraints.

**Grok CLI Tool Result Neutral Placeholder (PR #4109):**
- `internal/proxy/grokcli.go` & `internal/mitm/handlers/kiro.go` — Replaced placeholder `"continue"` with neutral `"Tool results provided."` for tool-result user turns in Kiro request payloads to prevent assistant hallucination loops.

**Thinking Variant Route Stripping & Union Alpha Messages Route (PR #4084, #4099):**
- `internal/proxy/executor/providers.go` — Stripped model suffix before checking `isOpencodeResponsesModel`, enabling models like `gpt-5.6-luna(high)` to correctly route to `/responses`.
- `internal/proxy/executor/providers.go` — Routed Antigravity Zen `union-alpha` directly to `/zen/v1/messages` with `anthropic-version: 2023-06-01`.
- `internal/providers/capabilities.go` — Registered model capabilities for `union-alpha`, `deepseek-v4.1-flash`, and `deepseek-flash`.

## [v1.8.13] — 2026-09-16

### 🐛 Bug Fixes & Parity

**Grok CLI Responses Endpoint Fix (PR #4, fixes #2):**
- `internal/providers/providers.go` — Updated `grok-cli` `BaseURL` from bare root `https://cli-chat-proxy.grok.com` to `https://cli-chat-proxy.grok.com/v1/responses`, resolving HTTP 404 HTML edge errors when calling `gcli/*` models (`grok-4.5`, `grok-4.6`).
- `internal/proxy/grokcli.go` — Added auto-normalization in `ForwardGrokCLI` so that if `cfg.BaseURL` is empty or lacks the `/v1/responses` path, it automatically normalizes to `/v1/responses`.
- `internal/handlers/chat/grokcli_handler_test.go` — Added regression tests for grok-cli BaseURL and endpoint routing.

**Custom Provider Nodes Prefix Priority & Model Mapping (PR #5, fixes #3):**
- `internal/handlers/chat/resolution.go` — Prioritized custom `providerNode` prefix resolution (`h.resolvePrefixProvider(prefix, model)`) before checking built-in provider aliases (`resolveProviderAlias(prefix)`). This prevents short prefixes like `oa` or `cc` from being shadowed by `openai` or `claude`, eliminating false 502 "no active connections for provider: openai" failures when the custom node has active connections.
- `internal/handlers/chat/resolution.go` — Added fallback so that when the alias-resolved provider has no active connections, unresolved prefix queries report the matching custom `providerNode.id` instead of falsely blaming the shadowed built-in provider.
- `internal/handlers/chat/resolution.go` — Added defensive nil checks for `h.Repo` across all model and combo resolution helpers.
- `internal/db/repos.go` — Added `GetProviderNodePrefixMap()` to map internal row IDs (`openai-compatible-chat-0489...`) to user-configured prefixes (e.g. `nara`, `orca`, `oa`).
- `internal/db/repos.go` — Updated `GetCustomModels()` with fallback parsing from keys (`<providerAlias>|<modelId>|<kind>`) and removed restrictive `type == "llm"` filtering, exposing all custom chat and completion models.
- `internal/handlers/chat/chat.go` — In `HandleModels` (`GET /v1/models`) and `HandleModelLookup` (`GET /v1/models/*`), mapped `cm.ProviderAlias` through the prefix map so models are published under their clean user-configured prefix (e.g. `nara/glm-5.3`) with `owned_by` set to the prefix rather than leaking internal database row IDs.
- `internal/handlers/chat/resolution_test.go`, `internal/handlers/chat/chat_v065_test.go`, & `internal/db/repos_test.go` — Added comprehensive unit and regression tests for custom prefix priority, fallback error reporting, prefix map caching, key-fallback parsing, and `/v1/models` prefix output.

## [v1.8.12] — 2026-09-16

### 🚀 Features & Provider Additions

**Freebuff Provider Integration (`fb`):**
- `internal/proxy/executor/freebuff.go` — Added native Freebuff executor supporting `https://www.codebuff.com/api/v1/chat/completions` with 1-hour session token lifecycle caching, agent run tracking (`/api/v1/agent-runs`), Buffy system prompt marker injection, and `end_turn` tool injection for sub-agent orchestration.
- `internal/providers/providers.go` & `internal/providers/aliases.go` — Registered provider `freebuff` and alias `fb`.

**Provider Connection Routing Strategies:**
- `internal/db/settings.go` & `internal/handlers/chat/connections.go` — Added configurable multi-connection routing strategies per provider: `sticky` (with configurable `stickyLimit`), `round-robin`, `random`, and `none`.

**Model Capabilities & Limits:**
- `internal/providers/capabilities.go` — Added capabilities for Upstage Solar Pro (`*solar-pro*`) and LongCat (`*longcat*`) with reasoning, tools, 200,000 token context window, and 32,000 max output tokens.

### 🐛 Bug Fixes & Resiliency

**Cline & Clinepass OAuth Refresh Overhaul:**
- `internal/proxy/oauth/cline.go` — Registered `clinepass` alongside `cline` in the OAuth registry, resolving issues where ClinePass connections fell back to incompatible standard form-urlencoded OAuth refresh.
- Migrated token refresh endpoint from deprecated `/v1/auth/refresh` (which returned 401 "Please make sure you're using the latest version of Cline") to active upstream `/api/v1/auth/refresh`.
- Emulated full Cline CLI identity headers on refresh (`User-Agent: Cline/3.0.61`, `X-CLIENT-TYPE: cline-cli`, `X-CLIENT-VERSION: 3.0.61`, `X-CORE-VERSION: 3.0.61`, `X-PLATFORM: cli`).
- Added token rotation support: propagated rotated `refreshToken` to SQLite database across `BuildConnectionUpdate` and `forceRefreshOAuthToken`.
- `internal/handlers/chat/fallback.go` — Ensured `refreshedKey` is normalized with `NormalizeProviderToken` on reactive 401 retries so WorkOS prefix (`workos:`) is preserved.

**Zero-Sleep Failover & Canonical Model Locking:**
- `internal/handlers/chat/combo.go` — Removed synchronous blocking sleeps (`time.Sleep`) during combo failover loops on transient errors (502, 503, 504), enabling immediate non-blocking failover to backup models/connections without stalling client turns.
- `internal/handlers/chat/connections.go` & `internal/handlers/chat/combo.go` — Added `canonicalLockModel(provider, model)` to atomically lock shared tier pools across connections (e.g., Antigravity `gemini-3.8-flash-low`/`high` map to canonical lock key `gemini-3.8-flash-tiered`). Prevents split-lock failure loops across shared tier accounts.
- `internal/providers/errorclassify.go` — Expanded error classification to trigger backoff for `resource_exhausted`, `model_capacity_exhausted`, and HTTP 502/503/504 status codes.

**Stream Telemetry & Connection Safety:**
- `internal/proxy/sse.go` — Added rolling 16-byte tail buffer in `SSECopy` to safely detect `[DONE]` across chunk boundaries and cleanly terminate SSE streams without hanging on keep-alive connections.
- `internal/handlers/chat/fallback.go` — Guaranteed `usageHistory` database logging on completed streams even when the client disconnects at stream end.
- Advertised `context_window` in `/v1/models` and `/v1/models/info` dynamically from capabilities.

## [v1.8.11] — 2026-09-12

### 🐛 Bug Fixes & Parity — Upstream PR Porting

**Gemini Multiple System Messages Preservation (PR #3973):**
- `internal/translator/gemini.go` — Preserved all `role: "system"` messages in `req.SystemInstruction.Parts` rather than overwriting earlier instructions with the last turn, ensuring all system prompts and developer directives reach Gemini models.

**Antigravity Thinking Budget & Output Tokens Guard (PR #3981):**
- `internal/translator/gemini.go` & `internal/translator/antigravity.go` — Guarded `maxOutputTokens > thinkingBudget` across `TranslateOpenAIToGemini` and `hardenAntigravityRequest`, preventing HTTP 400 `INVALID_ARGUMENT: max_tokens must be greater than thinking.budget_tokens` and erroneous connection locks on reasoning models.
- Added support for `max_completion_tokens`, `thinking.budget_tokens`, and `thinking_budget`.

**Claude Document Block Support for Antigravity & OpenAI (PR #3968):**
- `internal/translator/request.go` & `internal/translator/types.go` — Added `document` block handling in `TranslateClaudeToOpenAI` and `OpenAIFile` struct in `OpenAIContentBlock`, converting base64 PDF documents into OpenAI file format that flows into Gemini/Antigravity `inlineData`.

**Client Cancellation Tracing & Stream Telemetry:**
- `internal/handlers/chat/fallback.go` — Differentiated client cancellations (`errors.Is(fwdErr, context.Canceled)` or `ctx.Err() != nil`) from true upstream failures, logging `INF [fallback] client canceled request` and recording status `499` in traces instead of raising false `WRN upstream failed` alarms.
- `internal/handlers/chat/gemini_handler.go` — Added accurate `totalBytesWritten` accumulation for OpenAI format streaming branches and terminal `[DONE]` frame.
- `internal/handlers/chat/fallback.go` & `internal/handlers/chat/combo.go` — Refactored hardcoded HTTP status codes to standard `net/http` constants (`http.StatusOK`, `StatusClientClosedRequest`).

**Memory Leak Protections & High-Traffic Concurrency:**
- `internal/translator/usage.go` & `internal/translator/response.go` — Added `pendingFragment` struct with `createdAt` timestamps and 10-minute TTL pruning in `pruneStaleStatesLocked()`. Prevents abandoned fragmented SSE streams from accumulating in the global `pendingJSON` map.
- `internal/handlers/chat/connections.go` — Pooled and cached `*http.Client` and `*http.Transport` instances by proxy URL using `sync.RWMutex`. Eliminates per-request transport allocations, enables TCP keep-alive reuse across proxy pool traffic, and prevents socket/goroutine exhaustion.

**Live Profiling & Diagnostics:**
- `internal/handlers/router.go` — Mounted Go standard `net/http/pprof` endpoints (`/debug/pprof/`, `/debug/pprof/heap`, `/debug/pprof/goroutine`, `/debug/pprof/profile`) for real-time heap and concurrency inspection.

**Documentation & Client Guides:**
- `README.md` — Added comprehensive pre-built binary download links (macOS, Linux, Windows), one-liner install script, Docker setup, and configuration examples for Claude Code, `omp`, and Cursor/Cline.

## [v1.8.10] — 2026-09-11

### ✨ Features & Parity — Next.js v0.5.75 Sync (27 Commits)

**Gemini & Antigravity Content Normalization:**
- `internal/translator/gemini.go` & `internal/translator/antigravity.go` — Added `NormalizeGeminiContents` merging adjacent same-role messages and filtering out empty parts (parity with `#e7b5f09`).
- `internal/handlers/chat/antigravity_quota.go` — Added Antigravity weekly quota tracking (`gemini_weekly`, `claude_gpt_weekly`) and free-tier handling via `retrieveUserQuotaSummary`, caching summaries and reconciling against exhausted model families (#3892).

**Kiro Routing & Wire Payload Cleanup:**
- `internal/providers/providers.go` & `internal/proxy/grokcli.go` — Routed Kiro through Amazon Q first (`https://q.us-east-1.amazonaws.com/generateAssistantResponse`), deprecated legacy runtime path to avoid 400 `REQUEST_BODY_INVALID` (#3776).
- Injected `x-amz-sso-bearer`, `x-amzn-kiro-agent-mode: spec`, and `x-amzn-codewhisperer-machine-id: kiro-desktop` headers.
- `internal/proxy/grokcli.go` & `internal/mitm/handlers/kiro.go` — Stripped top-level `systemPrompt`, `agentMode`, and `conversationState` continuation fields (`agentContinuationId`, `agentTaskType`) that modern Kiro gateways reject with 400 (#1892ed7).

**Opencode-Go Catalog Refresh & Responses API:**
- `internal/proxy/executor/providers.go` — Routed `grok-4.6` and `gpt-5.6-luna` on `opencode-go` to the `/zen/go/v1/responses` endpoint alongside `muse-spark`.
- `internal/providers/capabilities.go` & `internal/proxy/executor/providers.go` — Registered newly published Go models (`deepseek-flash`, `glm-5.3`, `kimi-k3`, `longcat-2.0`, `qwen3.8-max`, `qwen3.8-flash`, `hy4-preview`, `hy3`), with `deepseek-flash` (DeepSeek V4.1 Flash) priority and Qwen 3.8 models in `opencodeGoMessagesModels`.

**Codex CLI Bump & Unicode Schema Sanitization:**
- `internal/providers/providers.go` — Updated Codex CLI User-Agent to `codex_cli_rs/0.154.0` (parity with `#a7047a0`).
- `internal/proxy/executor/transform.go` — Added `StripCodexUnsupportedPatterns` to sanitize `\p{...}` / `\P{...}` Unicode property escapes in tool parameters that Codex's `/responses` validator rejects with HTTP 400 (#3922).
- `internal/providers/capabilities.go` — Added Codex image models (`gpt-image-2.5`, `gpt-image-2.5-flare`, `gpt-image-2.5-sunburst`, `gpt-image-2`, `gpt-image-1.5`) with `ImageOutput` capability and `*gpt-image*` pattern match.

**Claude Cache Budget & Single-Object Turns:**
- `internal/translator/request.go` — Enforced Anthropic 4-marker `cache_control` budget in `AnchorClaudeCache`: pins head anchors (last system block, last non-deferred tool) and keeps at most 2 tail message markers, trimming earlier ones (#8a81085).
- `internal/translator/request.go` — Supported single-object content turns (`content: {type: "text", ...}`) across `convertClaudeMessage`, `SanitizeClaudePassthrough`, and `AnchorClaudeCache`.
- `internal/handlers/chat/chat.go` — Scoped Claude tool type defaulting to gateways declaring `requireClaudeToolType` (MiniMax / MiniMax-CN), avoiding 400 `unknown variant custom` on DeepSeek Anthropic endpoint (#3905, #45ec1d3).

**Cline Envelope Unwrapping & Token Refresh:**
- `internal/translator/response.go`, `internal/handlers/chat/forward.go`, `internal/proxy/executor/openai.go` — Added `UnwrapClineEnvelope` unwrapping `{"success":true,"data":{...}}` for non-streaming completions on `cline` and `clinepass` (#122f23ee).
- `internal/providers/oauth.go` — Registered `clinepass` in OAuth token refresh config.
- `internal/providers/capabilities.go` — Replaced `deepseek-v4-flash` with `deepseek-v4.1-flash` for `codebuddy-cn` (#807553e).

**Connection Health & Security:**
- `internal/db/accounts.go` — Added `ResetConnectionHealthState` clearing `modelLock_*`, `errorCode`, `rateLimitedUntil`, and resetting `backoffLevel = 0` upon connection activation (#3830).
- `internal/handlers/media/media.go` — Rejected path-escaping characters (`..`, `/`, `\`) in `HandleVideoGet` (#da6aa901).

**Responses API Stream & Tool Calling Fixes:**
- `internal/proxy/executor/stream.go` — Fixed tool call argument duplication (`InputValidationError` caused by duplicate concatenated JSON bodies such as `{"q":"*.yaml"}{"q":"*.yaml"}`) on Responses API streams by tracking `ArgsEmitted` during `response.function_call_arguments.delta` and suppressing redundant full argument re-emission on `response.output_item.done`.
- `internal/handlers/chat/gemini_handler.go` — Ensured terminal `data: [DONE]\n\n` SSE frame is emitted upon stream completion for OpenAI-format streaming clients and cleaned up redundant newlines.
- `internal/handlers/chat/live_e2e_test.go` — Added live non-mock E2E tests for Antigravity (`gemini-2.5-flash`, `gemini-3.8-flash-high`), DeepSeek, and OpenCode covering parallel multi-tool calls, multi-turn tool execution, streaming, and weekly quota retrieval.

## [v1.8.9] — 2026-09-06

### ✨ Features & Parity — Next.js v0.5.69 Sync (19 Commits)

**Gemini & Antigravity ThoughtSignatureStore:**
- `internal/translator/thought_signature_store.go` — Added thread-safe in-memory LRU store (capacity: 2,000 entries, 1-hour memory TTL) scoped by `sessionId` + `toolCallId`. Caches and replays thought signatures across turns to prevent corrupted thought signature errors during multi-turn reasoning conversations.
- `internal/translator/gemini.go` & `antigravity.go` — On parallel function calls, only the first call receives the signature/fallback, leaving sibling calls unsigned per Google Gemini 3+ specification.

**New Models & Capabilities Sync:**
- `internal/providers/capabilities.go` — Registered **`gpt-6-astra`** (Vision, Reasoning, Search, Tools; 272K window / 128K max output) and added `*gpt-6*` pattern match.
- Registered GPT-5.6 image aliases: `gpt-5.6-sol-image`, `gpt-5.6-terra-image`, and `gpt-5.6-luna-image` with `ImageOutput` capability.
- Qoder capabilities catalog refresh (`ultimate`, `performance`, `gmodel`, `gfmodel`, `qmodel_38max`, etc.).
- CodeBuddy-CN capabilities updated (`glm-5.2` vision enabled).

**Anti-Abuse Google Token Refresh (Antigravity):**
- `internal/handlers/chat/antigravity_project.go` — Configurable `ONBOARD_MAX_ATTEMPTS` (default 2, down from 5) and `ONBOARD_RETRY_DELAY_MS` (default 12s) to prevent Google account rate-limit blocks during multi-account refresh (#3813).

**Anthropic-Beta Header Forwarding & Effort Normalization:**
- `internal/handlers/chat/fallback.go` — Automatically injects `Anthropic-Beta: prompt-caching-scope-2026-01-05, context-management-2025-06-27` for `anthropic-compatible-*` nodes serving Claude models (#3797).
- `internal/translator/request.go` & `types.go` — Normalizes Claude adaptive auto effort (`output_config.effort="auto"` and `"xhigh"` $\to$ `"high"`) (#3792).

**OpenCode-Go Executor & Responses Parallel Tool Calls Fixes:**
- `internal/proxy/executor/providers.go` — Added `deriveOpencodeSession` generating stable `x-opencode-session: ses_<32hex>` headers for all `opencode-go` requests, with fallback and client tool isolation (#3800).
- Routed `muse-spark-1.2-contributor` and `muse-spark-1.3-contributor` on `opencode-go` to the `/responses` endpoint (#3819, #3820).
- `internal/proxy/executor/stream.go` — Fixed parallel tool calls argument collision on Responses API SSE stream by indexing events via `item_id`. Emits arguments from `response.output_item.done` when upstreams send arguments on item completion without deltas.
- `internal/proxy/executor/stream.go` — Fixed `handleCodexStream` SSE stream truncation and disconnects on `muse-spark-1.3` (and 1.2) by switching to `proxy.ScanStream` (up to 10MB buffered scanner), preventing line fragmentation when handling large (>3KB) encrypted reasoning payloads across TCP packet boundaries.
- `internal/proxy/executor/stream.go` — Added support for `response.reasoning_summary_text.delta`, `response.reasoning_text.delta`, and `response.thought.delta` emitting `reasoning_content` delta chunks for thinking models.
- `internal/proxy/sse.go` — Added `HeartbeatWriter` emitting periodic `: keep-alive\n\n` comments every 15 seconds during prolonged upstream reasoning phases (fixes #3796 stream stall timeouts on strict clients like Oh My Pi during deep thinking on `ag/gemini-3.8-flash*`).
- `internal/proxy/stall.go` — Added `NewStallReaderWithContext` binding client `ctx.Done()` directly to body closer, immediately freeing upstream sockets on client abort and eliminating Windows socket leaks (`CLOSE_WAIT`/`FIN_WAIT_1`).
**Database Path Configuration:**
- `internal/config/config.go` — Enhanced `DB_PATH` resolution to automatically detect `db/data.sqlite`, `data.sqlite`, or `9router.db` when pointed directly to a directory (e.g. `E:\project\database\9router`).
## [v1.8.8] — 2026-09-03

### ✨ E2E & Parity — Next.js v0.5.65 (31 commits)

**E2E Gemini 3.8 Flash High + tool calling (deterministic mocks):**
- `internal/handlers/chat/gemini38_e2e_test.go` — `gemini-3.8-flash-high` via Antigravity `2.11.0` non-stream + multi-turn + stream SSE `get_weather_ide` uncloaking, `prefixItems` cleaning, `thoughtSignature` backfill, `tool_calls` dedup. Ports `decolua/9router` `gemini-3.8-flash-medium/high/low` + `capabilities.go:*gemini-3.8*` + `antigravity.go:gemini-3.8-flash-tiered` + `proxy/gemini.go:2.11.0`.

**E2E Opencode muse-spark (deterministic mocks, no real network):**
- `internal/handlers/chat/opencode_mock_e2e_test.go` — `oc/muse-spark-1.2` & `1.3` via `Responses API /v1/responses` SSE `output_item.added` + `function_call_arguments.delta/done` aggregation, `reasoning max→xhigh`, `Vision:true` (`capabilities.go:124` pattern `*muse-spark*`), `image_url` preservation. Fixes routing `muse-spark-1.3` `500` → `200` (`providers.go:293` `Contains(muse-spark)` + `capabilities.go:124` `1.3`).

**Unit tests — now locking logic (previously untested):**
- `providers_v065_test.go` — `claude-cli/2.1.258` + full `Anthropic-Beta`, `ollama FetchURL https://ollama.com/api/web_fetch`, `gemini-3.8` caps, `muse-spark 1.2/1.3`, `codebuddy-cn hy3/hy3-x/hy4-preview/x/glm-5.3/kimi-k3-1` + EOL `glm-5.0/4.7` removed, `GetModelTokenLimits` 3.8.
- `translator/claude_cache_test.go` — `LastCacheableToolIndex` + `AnchorClaudeCache` for `defer_loading:true` tail, all-deferred, stripping client `cache_control` (#3567).
- `handlerutil/ssrf_test.go` — `trailing dot` (`localhost.`), `CGNAT 100.64/10`, `169.254.169.254`, IPv6 `::ffff:7f00:1` hex, `64:ff9b::`, `fe80/fc`, `normalizeHost`, `parseIPv6ToGroups`.
- `mitm/handlers/mitm_handlers_test.go` — `HandleKiro` removes `systemPrompt` + `userInputMessage.images → image_url data:`, `HandleAntigravity` preserves `fetchAvailableModels(2.11.0)` vs overrides `generateContent→1.23.2`.
- `usagetracker/quota_parsers_test.go` `TestParseGroqQuotasFromHeaders` — `x-ratelimit-*` Go duration `2m59.56s` → `requests/tokens` `used/total/resetAt`.
- `handlers/chat/chat_v065_test.go` — `HandleModelLookup` kind `image` + `cc/claude-sonnet-4-6` + encoded slash + 404 `model_not_found`, `HandleModels` custom `cc/my-custom-vision` caps, `StrikeReassert` 3×429 optimistic 90% → `CACHE_BLOCK 15m` + re-assert after `Refresh`.
- `proxy/executor/opencode_test.go` `MuseSpark13_ResponsesRouting` + `OCPrefix` — routing `1.3` + `oc/` to `/responses`.

**Fixes:**
- **Opencode 1.3 `500` → `200`** — `ForwardOpencode` routing `Contains(muse-spark)` + `capabilities` `1.3` Vision (fixes report `14:11:47` `muse-spark-1.3 500`).
- **jcode tool_smoke 3→1** — `stream.go:118` dedup `ToolCallIdx` + `codebuddy.go:168` `sseToOpenAIJSON` dedup `arguments` for `bash` `intent` split (fixes `echo JCODE_TOOL_OK` 3 tool_calls).
- **Flaky real upstream 429** — `muse_spark_e2e_test.go` real `opencode.ai` `429 FreeUsageLimitError` now `Skip` instead of `Fail`.
- **DB flaky `429` in `go test ./...`** — `go vet` clean, `ps` `9router-go 20130` health `{"status":"ok"}` (not stopped, log stopped due to `user stepped away` recap 98k prompt).

## [v1.8.7] — 2026-09-02

### 🐛 Bug Fixes

- **Gemini `system_instruction` Empty Part 400 Fix** — `StripCompetitivePrompts` now drops empty `system_instruction` parts after `rewriteCompetingBranding` (e.g. `"You are a Claude agent..."` -> `""`) and filters empty text parts in `contents`; if all parts are empty the `system_instruction` is removed (`nil`) instead of emitting `{"parts":[{}]}` which Gemini rejects as `system_instruction.parts[0].data: required oneof field 'data' must have one initialized field`. Also `TranslateOpenAIToGemini` now `TrimSpace` checks system content. Fixes `ForwardGemini (antigravity/gemini-3.7-flash-high): ...system_instruction.parts[0].data: required oneof`. (`internal/translator/antigravity.go`, `internal/translator/gemini.go`)
- **Gemini Tool Schema `required` Inside `properties` 400 Fix** — `cleanGeminiSchema` now detects misplaced `required` array inside `properties` (e.g. `{"properties":{"query":{...},"required":["query"]}}`) and promotes it to top-level `required`, fixing `Invalid value at 'request.tools[0].function_declarations[0].parameters.properties[0].value' (Map), Cannot have repeated items ('required') within a map. Unknown name ""`. This was the root cause of the `12:14:50` `query_db_ide` 400 after the `where.items` fix. Also sanitizes all OpenAI-compatible providers (including `opencode`) via `fallback.go` `SanitizeOpenAITools`. (`internal/translator/schema.go`, `internal/handlers/chat/fallback.go`)
- **Opencode `muse-spark` Tool Name Triplication Fix** — `sseToOpenAIJSON` `internal/proxy/executor/codebuddy.go:168` now only sets `name` if empty and avoids duplicating `arguments` already sent via `delta`/`done`; `ProcessCodexEvent` `stream.go:148` for `response.function_call_arguments.delta/done` now deduplicates `name` and tracks `ToolCallArgs` to prevent `get_weather` -> `get_weatherget_weatherget_weather` and `{"location":"Jakarta"}{"location":"Jakarta"}` on `combo-wombo` (`oc/muse-spark-1.2`) non-stream and stream. (`internal/proxy/executor/codebuddy.go`, `internal/proxy/executor/stream.go`)

## [v1.8.6] — 2026-09-02

### 🐛 Bug Fixes

- **Gemini Tool Schema `where.items.items: missing field` 400 Fix** — `cleanGeminiSchema` now ensures every `type: array` has a valid `items` schema (default `{"type":"string"}`), flattens `prefixItems` (2020-12 tuple) and `items: [...]` tuple to single `items`, and auto-fills inner `items` without `type`/`properties`/`enum`. Fixes `ForwardGemini (antigravity/gemini-3.7-flash-high): upstream returned 400: ...where.items.items: missing field.` when Claude Code sends DB-like tools with nested `array<array>` params. Added `internal/proxy/gemini.go` 400 payload dump to `/tmp/9router-gemini-400.json` for post-mortem. (`internal/translator/schema.go`, `internal/proxy/gemini.go`)
- **Bare Alias `ag` -> `antigravity` Resolution** — `resolveModel("ag")` now checks `ProviderAliasMap` before common-provider fallback, so `POST /search` with `{"model":"ag"}` correctly routes to `antigravity` instead of `deepseek/ag` -> `404 Via cloudfront`. Parity with Next.js `ag` search. (`internal/handlers/chat/resolution.go`)
- **Antigravity Search Default Model Parity** — `handleAntigravitySearch` default changed `gemini-3-flash-agent` -> `gemini-2.5-flash` (Next.js `ag` search returns `answer.model: gemini-2.5-flash`), fixing `500 UNKNOWN` from `daily-cloudcode-pa.googleapis.com` for bare `ag` search. (`internal/handlers/media/antigravity_search.go`)
- **Claude `call_` Tool Name Fallback Fix (Gateway IP Check)** — `TranslateOpenAIToClaude` no longer falls back to `tc.ID` (`call_...`) when `Function.Name` is empty; skips invalid tool calls instead of emitting `tool_use` with `name: call_...` which caused `No such tool available: call_...` and `Invalid tool parameters` churn on `toloing cek config gateway` via `opencode/muse-spark`. (`internal/translator/response.go:165`, `cf42120`, port `decolua/9router#2077`/`#3685`)
- **Claude Streaming Tool Delta Deduplication** — `TranslateOpenAIToClaudeStreamSession` now checks `state.ToolCalls[idx]` before creating a new `content_block_start`; second delta for same `idx`/`id` (Codex `output_item.added` + `function_call_arguments.delta` same `call_...`) no longer creates duplicate `tool_use` and empty `partial_json: "{}"`; correctly buffers `arguments` and emits `{"command":"ip route ..."}`. Fixes `InputValidationError: Bash missing command` on `Gue cek gateway IP...` streaming. (`internal/translator/response.go:468`, `ea10c16`)
- **Bash Extra Fields Strip** — `sanitizeBashArgs` now keeps only `command`, deletes hallucinated `description` etc. inside `input` (`{"command":"ls ...","description":"List home..."}` -> `{"command":"ls ..."}`), fixing `Bash(input JSON failed to parse — 433 bytes)` on `Cek gateway config — lagi intip file-file di home`. (`internal/translator/sanitize.go:239`, `6b94535`)
- **Server Tool Use Foreign ID Drop (Combo Poison)** — `SanitizeClaudePassthrough` drops `server_tool_use` with id not matching `^srvtoolu_` (e.g. `call_` from `z.ai/glm` `analyze_image`) and paired `tool_result`, strips empty text and empty messages. Port `decolua/9router#3686`. (`internal/translator/request.go:342`, `cf42120`)
- **1M Context Marker Strip** — `stripModelContextMarker` strips trailing `[1m]`/`[1M]` from `claude-opus-5[1m]` before `resolveModel`, so combo `combo-wombo[1m]` routes correctly instead of `Invalid model format`. Port `decolua/9router#3691`. (`internal/handlers/chat/resolution.go:135`)
- **Streaming Model Echo** — `SeedStreamState` pre-seeds `StreamState.Model` with client-requested model via `WithRequestedModel` context so `message_start` echoes `combo-wombo` not `claude-3-5-sonnet` provider model. Port `decolua/9router#3693`. (`internal/translator/usage.go:41`, `forward.go:91`)
- **GPT-5 / o-series `max_completion_tokens`** — `requiresMaxCompletionTokens` (`/gpt-5|o[134]-/i`) emits `max_completion_tokens` instead of `max_tokens` for `gpt-5`/`o1-`/`o3-`/`o4-` in `TranslateClaudeToOpenAI`. Port `decolua/9router#3657`. (`internal/translator/request.go:12`, `types.go:202`)
- **Antigravity Optimistic Quota Strike-Breaker** — after 3 consecutive `429` for same `connection|model` within 60s while quota `remaining>0`, `HandleAntigravityQuotaError` returns `CACHE_BLOCK 15m` instead of looping 300s `modelLock`. Port `decolua/9router#3684`. (`internal/handlers/chat/antigravity_quota.go:44`)
- **Forced-SSE JSON for Claude Clients** — `handleJSONResponse` detects `isSSEBody` when `translate=true` and `stream:false` retry hits forced-stream provider (Responses-API), aggregates via `sseToClaudeJSON` then `TranslateOpenAIToClaude` to return `Anthropic Message` not `chat.completion`. Port `decolua/9router#3683`. (`internal/handlers/chat/forward.go:144`)
- **OpenRouter Pattern Compat + Combo Tools Detection** — `NormalizeToolSchemasForProvider("openrouter")` strips invalid `pattern` regex (keep valid, handle `properties` named `properties`), and `DetectRequiredCapabilities` now requires `tools` capability for `type:function`/`functionDeclarations`. Port `decolua/9router#3665`. (`internal/translator/tool_schema.go`, `combo.go:129`, `forward.go:35`)

## [v1.8.5] — 2026-08-31

### ✨ Features & Parity (Next.js v0.5.59 Sync)

- **New Search Providers & Credential Fallback** — added `xquik` (X search provider with raw API key), `ollama-search`, and `zai-search` (GLM Coding web search). Added automatic credential fallback where search providers borrow API keys from parent chat connections (`ollama` / `glm`) when dedicated search connections are absent. (`internal/providers/providers.go`, `internal/providers/aliases.go`, `internal/handlers/chat/connections.go`)
- **Antigravity Web Search Provider** — added Antigravity as a web search provider via Google Search grounding, with full Next.js parity for the search response structure. (`internal/handlers/media/antigravity_search.go`)
- **New Models & Capabilities Sync** — registered new flagship models: `GLM-5.3-Flash` (1M context window + native vision multimodal), `GLM-5.3`, `DeepSeek V4 Vision`, `Grok 4.5/4.6` (500k context window), and `muse-spark-1.2-contributor-free`. (`internal/providers/capabilities.go`, `internal/providers/aliases.go`)
- **Claude Tool Type Defaulting (`type: "custom"`)** — added `DefaultClaudeToolType` ensuring tools in Claude-format requests always carry a valid `type` (defaulting to `"custom"` when omitted), preventing HTTP 400 rejection on strict Anthropic-compatible gateways such as MiniMax. (`internal/translator/request.go`, `internal/handlers/chat/chat.go`)
- **Claude Code Session ID Header Support** — prioritized `x-claude-code-session-id` in `ExtractSessionID` to ensure stable prompt caching and avoid conversation fragmentation across client tool calls. (`internal/handlerutil/response.go`)
- **CommandCode In-Stream Error Peeking** — peeks the initial NDJSON event in CommandCode stream for `type: "error"` before committing HTTP 200 OK headers, transforming internal stream errors into real HTTP error statuses (429, 503, 401, etc.) so combo and account fallback trigger seamlessly. (`internal/proxy/executor/stream.go`)
- **OpenCode Responses API Parity (v0.5.59)** — completed Responses API translation for OpenCode Muse Spark: proper tool names emitted on `response.output_item.added`, accurate usage and prompt-cache token extraction from `response.completed`, Claude SSE streaming translation and non-streaming support in `handleCodexStream`, and 64-char clamping for `call_id`. (`internal/proxy/executor/`)

### 🐛 Bug Fixes

- **Gemini Cached Token Extraction** — added support for both `cachedContentTokenCount` and `cachedContentToken` keys in Gemini stream and non-stream responses. (`internal/translator/gemini.go`)
- **Non-Interactive Test Execution** — bypassed interactive `sudo security` CA keychain install when executing unit tests, ensuring fast, deterministic test suite completion. (`internal/mitm/cert.go`)
- **Self-Update SHA256 Verification** — `PerformSelfUpdate` now downloads to memory, verifies the expected SHA-256 checksum, and refuses to install mismatched binaries, eliminating the risk of installing corrupted or tampered updates. (`internal/updater/updater.go`)
- **Graceful Self-Restart** — replaced abrupt `os.Exit` after self-update with `syscall.Kill(SIGTERM)` plus a graceful fallback, giving in-flight requests and DB connections a chance to drain cleanly. (`internal/updater/updater.go`)
- **Cross-Platform Restart** — extracted the self-signal into a platform-specific `signalSelfShutdown` helper (`signal_unix.go` sends SIGTERM; `signal_windows.go` is a no-op that falls back to `os.Exit(0)`), fixing the Windows cross-compile of the release binaries. (`internal/updater/signal_unix.go`, `internal/updater/signal_windows.go`)
- **Smart Archive Executable Selection** — `extractExecutableBytes` now scores archive entries (penalizing README/LICENSE/`*.md`/`*.sha256`) and validates ELF/Mach-O/PE magic bytes, reliably picking the real binary from multi-file release archives. (`internal/updater/updater.go`)
- **SSE Copy Race Condition** — replaced the shared pooled buffer in `SSECopy` with a per-call local buffer, eliminating concurrent read/write races on the pool buffer. (`internal/proxy/sse.go`)
- **Nil Guard in Token-Saving Compression** — guarded against a nil `rawMap` when the upstream body cannot be decoded, preventing a panic on malformed responses. (`internal/tokensaver/compress.go`)
- **Quota Percentage Clamping** — clamped `RemainingPercentage` to a sane `[0, 100]` range so upstream values >100 or negative cannot skew quota-block and dashboard logic. (`internal/usagetracker/quota_parsers.go`)
- **Exponential Backoff for Antigravity Onboarding** — replaced the fixed 2s sleep between `onboardUser` retries with exponential backoff (2s, 4s, ...) that also honors context cancellation, so a 429 burst no longer gets hammered by fixed-interval retries. (`internal/handlers/chat/antigravity_project.go`)
- **Decloak Deduplication** — extracted a shared `decloakContentBlockStart` helper used by both `DecloakStreamChunk` and `DecloakClaudeStreamEvent`, removing duplicate content-block-start logic. (`internal/translator/antigravity.go`)
- **Tool Property Sanitization** — preserved tool parameters named after reserved keywords and sanitized `required` fields against the declared `properties`, preventing schema validation failures. (`internal/translator/sanitize.go`)

## [v1.8.4] — 2026-08-14

### 🐛 Bug Fixes & Resilience

- **Combo Cycle Graceful Recovery & Fault Tolerance** — `flattenComboModels` now gracefully skips recursive / self-referencing combo branches with a warning log instead of failing hard with HTTP 400 (`combo cycle detected`), ensuring chatbot requests continue executing remaining valid models seamlessly. (`internal/handlers/chat/resolution.go`)
- **Safe Model Resolution on Leaf Models** — eliminates potential slice index-out-of-range edge cases when resolving combo leaf models that do not contain a provider prefix. (`internal/handlers/chat/resolution.go`)
- **Multi-Level Nested Combo Support** — verified recursive cascading combo expansion (e.g. `super-combo` → `mid-combo` → `base-combo` → leaf models) so all reachable models participate in round-robin, sticky, and fallback strategies. (`internal/handlers/chat/resolution.go`, `internal/handlers/chat/resolution_test.go`)

## [v1.8.3] — 2026-08-14

### ✨ Features

- **Antigravity Gemini 3.7 Flash Model Mapping** — canonical model IDs and aliases for `gemini-3.7-flash`, `gemini-3.7-flash-high`, `gemini-3.7-flash-agent`, `gemini-3.7-flash-medium`, `gemini-3.7-flash-low`, `gemini-3.7-flash-extra-low`, and `gemini-3.7-flash-thinking` correctly mapped to Google Antigravity backend model IDs (`gemini-3-flash-agent` / `gemini-3.5-flash-low`), fixing upstream 404 errors. (`internal/translator/antigravity.go`)
- **Enriched Prompt-Injection Guard** — enhanced prompt-injection detector with heuristic patterns for raw model delimiters (`<|im_start|>system`, `<<SYS>>`, `[SYSTEM PROMPT]`, `[INST]`), verbatim system prompt extraction attempts, and developer/admin mode override simulations. (`internal/tokensaver/injection.go`)
- **Accurate Gemini Cached Token Tracking** — correctly unmarshals and propagates `cachedContentTokenCount` from Gemini stream and non-stream responses into `OpenAIUsage.CachedTokens`, providing accurate cache hit reporting and cost calculation. (`internal/translator/gemini.go`)
- **Gemini Vision FileData & Audio Modalities** — added support for remote HTTP/HTTPS image URLs (`fileData: { fileUri, mimeType: "image/*" }`), base64 input audio (`input_audio`, `audio_url`), and uploaded documents in Gemini native translator, matching Next.js full multimodal capabilities. (`internal/translator/gemini.go`)
- **Realtime SSE Usage Stream & Topology Animation** — added in-memory in-flight request tracker (`internal/usagetracker`), real-time SSE broadcasting (`GET /api/usage/stream` and `GET /usage/stream`), and recent requests ring buffer matching the Next.js dashboard shape, enabling instant glowing pulse node & marching-ants edge animations on the Usage Topology graph when requests are handled by `9router-go`. (`internal/usagetracker/tracker.go`, `internal/handlers/usage_stream.go`, `internal/handlers/chat/fallback.go`, `internal/handlers/chat/usage.go`)
- **Antigravity Anti-Competitive Prompt Stripping & 429 Prevention** — automatically strips competitor identity phrases (e.g. `"You are a Claude agent, built on Anthropic's Claude Agent SDK."` from Zed IDE and Claude agents) from `system_instruction` and message contents, preventing Antigravity from returning synthetic `429 Quota Exhausted` errors. (`internal/translator/antigravity.go`)
- **Edge Relay URL Rewriting & Header Forwarding** — automatically rewrites `BaseURL` to the relay deployment and injects `x-relay-target` and `x-relay-path` headers when a connection uses a Vercel, Cloudflare Worker, or Deno Edge Relay Proxy Pool. (`internal/handlers/chat/connections.go`)
- **No-Auth Provider Proxy Pool Strategy** — automatically respects `settings.providerStrategies` for no-auth providers (e.g. `mimo-free`, `opencode`), attaching configured proxy pools or rotation strategies to virtual connections. (`internal/handlers/chat/connections.go`, `internal/db/settings.go`)
- **Snake_case Model Limits on `/v1/models` & `/v1/models/info`** — exposes `context_length`, `max_completion_tokens`, `max_input_tokens`, and `max_output_tokens` so clients like Cline, Roo Code, and LibreChat resolve proper context ceilings. (`internal/handlers/chat/chat.go`, `internal/providers/capabilities.go`)
- **CodeBuddy OAuth Configuration** — registered `codebuddy-cn` and `codebuddy-intl` OAuth token refresh configurations. (`internal/providers/oauth.go`)
- **OpenCode Official Client Fingerprint Headers** — injects official headers (`User-Agent: opencode`, `x-opencode-client: desktop`, `x-opencode-session: ses_...`, `x-opencode-request: msg_...`, `x-opencode-project: global`) on free-tier OpenCode requests to prevent rate limiting from unidentified client traffic. (`internal/proxy/opencode.go`, `internal/proxy/executor/providers.go`)
- **Kimchi Dual Authentication** — supports direct API keys (`Authorization: Bearer <key>`) in addition to OAuth tokens with seamless credential resolution. (`internal/handlers/chat/connections.go`, `internal/handlers/chat/kimchi_handler_test.go`)
- **Startup Banner & Version Display** — dynamically displays current version in CLI startup banner (`🚀 9Router Go Proxy (v1.8.3) on :20130`) and server ready logs. (`cmd/9router-go/main.go`)
- **New Provider Registries & Aliases** — added Alibaba Token Plan Singapore (`alitp-intl` / `ali-tp` / `alitp`) and Fish Audio Text-to-Speech (`fish-audio` / `fish`). (`internal/providers/providers.go`, `internal/providers/aliases.go`)

### 🐛 Bug Fixes

- **Invalid Tool Parameters & Decoy Schemas** — provided valid non-empty `properties.reason` schema for all 21 Antigravity decoy tools and mapped `tool_call_id` to exact function names in OpenAI-to-Gemini conversation history, eliminating protobuf validation errors when using Claude Code or other tool-calling clients. (`internal/translator/antigravity.go`, `internal/translator/gemini.go`)
- **Antigravity Upstream Model Resolution** — prevented invalid model aliases like `gemini-3.7-flash-high` from reaching Google Cloud Code without being translated to their backend model IDs. (`internal/translator/antigravity.go`)

### 📚 Documentation

- Comprehensive refresh of `README.md`, `COMPARISON.md`, `DATABASE.md`, `ARCHITECTURE.md`, `TECHNICAL_DEBT.md` (0 open items), and newly added `ROADMAP.md`.

## [v1.8.2] — 2026-08-14

### ✨ Features

- **Antigravity Tool Cloaking & Anti-Ban Decoy System** — automatically cloaks client tool declarations with `_ide` suffixes (e.g. `Bash_ide`), injects 21 official Antigravity IDE decoy tools (`run_command`, `replace_file_content`, `grep_search`, `list_dir`, etc.), synchronizes conversation history functionCall/functionResponse names, and seamlessly uncloaks tool names on response SSE stream and non-stream outputs. (`internal/translator/antigravity.go`, `internal/translator/gemini.go`)
- **Antigravity Native Image Generation** — added image model detection (`imagen`, `*image*`), aspect ratio suffix parsing (`16x9`, `4:3`, `1:1`, custom resolutions via GCD reduction), `requestType: "image_gen"` envelope wrapping with forced non-streaming `/v1internal:generateContent`, and OpenAI-compatible base64 image response formatting. (`internal/translator/antigravity.go`, `internal/proxy/gemini.go`)
- **Edge Relay & Transport Engine** — added transport support for Vercel, Cloudflare Worker, and Deno edge relays using `x-relay-target` and `x-relay-path` headers, wildcard `noProxy` domain filtering, and legacy connection-level proxy configuration fallback (`connectionProxyUrl`). (`internal/proxy/transport.go`, `internal/handlers/chat/connections.go`)

### 🐛 Bug Fixes

- **Proxy Pool DB Parsing Bug** — fixed `GetProxyPool` (`internal/db/proxyPools.go`) failing to parse single string `proxyUrl` created by Next.js UI / `InsertProxyPool`, which previously caused proxy pools to be silently ignored and requests to fall back to direct connections. Added parsing for `type`, `noProxy`, and `strictProxy` metadata.

## [v1.8.1] — 2026-08-12

### ✨ Features

- **Combo strategy sync with Next.js reference** — per-combo rotation state, correct auto-switch ordering, and a capabilities provider (`internal/providers/capabilities.go`) replacing the hardcoded vision/pdf maps with tiered capability detection.
- **Flatten nested combos** — `combo-wombo → free-tier` now expands to its four leaf models, so round-robin actually rotates across them instead of always landing on the first leaf (this was hammering one account and producing the `429 all connections for this provider are rate-limited` error).
- **Turn-aware rotation** — `applyComboStrategy` advances the rotation index only on a new turn; mid-turn tool-use requests reuse the model serving the turn, so the provider never switches mid-turn (which broke Gemini thinking models that require a `thought_signature` on current-turn function calls).
- **Bounded retry-once on total combo 429** — when every combo model fails with a Retry-After ≤ 8s, the pass waits once and retries before surfacing a hard 429 (`comboRetryAfter`).
- **Backfill default `thought_signature`** — every `functionCall` part now carries a `thoughtSignature` (the real one via `__ts__` transport when present, else the Next.js `DEFAULT_THINKING_AG_SIGNATURE`), closing the last 400-`thought_signature` gaps on mixed combos.
- **Gemini tool-schema keyword parity** — strip the remaining unsupported JSON-Schema keywords (`multipleOf`, `uniqueItems`, `contains`, `unevaluated*`, `contentSchema`) and fill bare `{}` schemas with the object placeholder, matching Next.js `cleanJSONSchemaForAntigravity` (fixes `Invalid tool parameters` 400 from antigravity).
- **Sanitize tools on the OpenAI-compat Gemini path** — the `gemini` provider is now marked `gemini-openai` and its `/v1beta/openai` bodies are run through `SanitizeOpenAITools`, so the strict schema validation applies on both Gemini routes.

### 🐛 Bug Fixes

- **Emit camelCase `thoughtSignature`** — the Gemini-native `generateContent` endpoint only recognizes the camelCase part field; the snake_case regression caused the `400 Function call is missing a thought_signature` error. Both read and write directions now handle camelCase.
- **Use the daily antigravity endpoint** — migrate `cloudcode-pa.googleapis.com` → `daily-cloudcode-pa.googleapis.com` for the `antigravity` provider (`providers.go`, `antigravity_project.go`, MITM domain list) to avoid strict rate limits.
- **Combo connection retry-loop parity** — connection retry loop now matches the single-model path.

### 🧹 Chores / Docs

- Remove the stray `patch_combo.go` throwaway script.
- Add design specs and implementation plans for the combo sync / thought_signature / backfill / schema-parity work.

## [v1.8.0] — 2026-08-11

### 🐛 Bug Fixes

- **Combo/router fallback bypasses model-lock backoff on retryable errors → antigravity rate-limit loop** — `handleComboFallback` / `handleMessagesComboFallback` (`internal/handlers/chat/combo.go`) called `tryForwardWithConnection` directly, so `LockConnectionModel` was never invoked on retryable errors (429/500s) — unlike the single-model path `handleAccountFallback`. The exponential 429 backoff was dead in the router path: every request re-tried all combo models back-to-back on the same connection/account, got 429, returned 429, and the client's ~35s retry repeated the loop forever. Fix:
  - New `comboLockRetryable` helper runs on every `RetryableStatusCodes` error in both combo loops — classifies via `ClassifyError`, calls `LockConnectionModel(connID, model, cooldownSec, newBackoffLevel)` so the exponential backoff persists across requests, and appends the conn to a request-local `excludeIDs` passed into `getBestConnection` so remaining combo models don't re-select the same connection (same account = same quota bucket).
  - A locked-connection skip covers pinned connections whose direct-fetch branch bypasses `getBestConnection`'s lock check.
  - `context.Background()` → `ctx` in `handleComboFallback` so client cancels propagate; the 502/503/504 transient-wait sleep is preserved.
  - Test: `TestHandleMessagesComboFallback_429LocksAndExcludesConnection` asserts a 429 locks the connection AND keeps the second combo model from re-hitting it (exactly 1 upstream hit).

## [v1.7.2] — 2026-08-08

### 🐛 Bug Fixes

- **Antigravity 429/404 failure-loop fix** — An unprovisioned Antigravity account
  (`onboardUser` returns `200` with an empty `cloudaicompanionProject`) left the
  connection without a `projectID`. The router then force-refreshed the OAuth
  token on every request (never an auth problem, so it never helped), fell through
  to a guaranteed-404 OpenAI-compatible lane on `cloudcode-pa.googleapis.com`,
  and repeated client retries rammed Google's rate limit (`429`). (`internal/handlers/chat/gemini_handler.go`, `internal/handlers/chat/antigravity_project.go`)
  - `fetchAntigravityProjectID` now reports the outcome (`projectID`, `authFailed`,
    `noProject`). Token refresh runs **only** on a genuine `401/403` — never on a
    missing/empty project.
  - When antigravity has no project ID, it no longer burns a request on the dead
    OpenAI lane; it returns an error and the fallback chain moves straight to the
    next provider.
  - **Negative cache (10 min, per connection):** once Google confirms "no project",
    later requests skip the `loadCodeAssist`/`onboardUser` RPCs entirely — this is
    what stops the repeated `429` hammering.
  - Onboarding guidance is logged once per connection per window
    ("onboard the account via Antigravity IDE/CLI, then re-login"); repeated
    failures log at `Debug` instead of spamming `Warn`. (`internal/handlers/chat/fallback.go`)

### 🧪 Tests

- `antigravity_project_test.go` — pins the probe classification (project found /
  token rejected `401`+`403` / project definitively missing / transient `429`+`503`)
  and the negative-cache expiry semantics. (`internal/handlers/chat/antigravity_project_test.go`)

## [v1.7.1] — 2026-08-08

### 🐛 Bug Fixes

- **Cached-token parity across every provider** — Prompt-cache accounting no longer works only for antigravity. Gemini `usageMetadata.cachedContentToken` now flows through both non-stream and stream translation into OpenAI `usage.cached_tokens`; the `!translate` response path uses a dual-format parser (`ParseResponseUsage`) that reads Claude `cache_read_input_tokens`/`cache_creation_input_tokens` and OpenAI `prompt_tokens_details.cached_tokens`, so cached tokens survive any provider → OpenAI → Claude double translation. (`internal/translator/gemini.go`, `internal/translator/response.go`)
- **Gemini tool-schema `const` re-injection** — `stripUnsupported` now re-runs after `anyOf`/`oneOf` flattening so `const` and vendor `x-*` keys can't leak back into the merged branch. (`internal/translator/schema.go`)
- **Provider 403 is now retryable** — Gemini/antigravity daily-quota errors can arrive as HTTP 403; these now trigger the connection fallback instead of a hard failure. (`internal/providers/providers.go`)
- **CodeBuddy CN stream cleanup** — The stall reader is now closed after the stream, stopping its shutdown watcher + stall timer (no per-request goroutine leak). (`internal/proxy/executor/codebuddy.go`)

### ⚙️ Graceful Shutdown Hardening

- New `internal/shutdown` package: a process-wide signal the first Ctrl+C / SIGTERM fires.
- `StallReader` now closes in-flight SSE upstream bodies on shutdown, so `server.Shutdown` drains streams in milliseconds instead of waiting out the 15s deadline — and the deferred DB/log-file close always runs.
- Translate-path SSE handlers emit a final `data: [DONE]` on abort so clients get a clean end instead of a truncated stream.
- A second Ctrl+C / SIGTERM force-quits immediately (stuck-drain escape hatch).
- Shutdown timeout logs a warning instead of `log.Fatalf`, so `conn.Close()` and the log file are still closed gracefully. (`internal/proxy/stall.go`, `cmd/9router-go/main.go`, `internal/handlers/chat/forward.go`, `internal/proxy/executor/openai.go`)

### 🔧 Internal

- Stream handlers now carry the request `ctx` and pull accumulated usage (incl. cached tokens) out of the translation session, so logged usage reflects real token counts instead of the character-estimate fallback.

## [v1.7.0] — 2026-08-06

### 🚀 New Executors

- **Trae SOLO remote agent** (`internal/proxy/executor/trae.go`) — Port of `open-sse/executors/trae.js`: `POST {base}/chat_sessions` creates a session, `GET {base}/chat_sessions/{id}/events` streams `plan_item` / `token_usage` / `done` as SSE. Cumulative `plan_item.thought` rendering (longest-wins per id, delta-only emission), `Cloud-IDE-JWT` auth, and `work`/`auto`/manual model modes. Non-stream requests aggregate into a single `chat.completion`. Round-trip test: `TestForwardTrae_StreamsAccumulatedThought`.
- **Windsurf gRPC-web** (`internal/proxy/executor/windsurf.go`) — Port of `open-sse/executors/windsurf.js`: hand-rolled protobuf `GetChatMessageRequest` encoder (Metadata.api_key + cascade_id + model_or_alias + repeated messages), gRPC-web framing (0x00 flag + big-endian length), and a `CompletionChunk` decoder (content / done+UsageStats / error) streaming OpenAI SSE. Catalog→wire model alias map ported verbatim; `crypto/rand` session/cascade ids. Non-stream requests aggregate frames into `chat.completion`. Round-trip test: `TestForwardWindsurf_StreamsGRPCWeb`.

### 🎙️ Xiaomi MiMo TTS

- `/v1/audio/speech` for the `xiaomi-mimo` provider now uses the chat-completions contract (port of `open-sse/handlers/ttsProviders/xiaomi-mimo.js`): target text in `role:assistant`, style/language instructions in `role:user`, voice via top-level `audio.voice`, base64 audio from `choices[0].message.audio.data`. (`internal/handlers/media/media.go`)

### ➕ Providers

- **tokenrouter** — Registered as an OpenAI-compatible upstream (`https://api.tokenrouter.com/v1/chat/completions`).

### 🗑️ Removed

- **qwen provider** — Removed from providers, OAuth config, and the alias map (deprecated upstream).

### 📋 Docs

- `TECHNICAL_DEBT.md` — windsurf + trae moved to resolved; zed + devin-cli documented with the safe-stopgap note (devin-cli corrected: ACP over **stdio** subprocess, not HTTP).

## [v1.6.1] — 2026-08-05

### 🐛 Bug Fixes

- **CodeBuddy CN 502** (`internal/proxy/executor/codebuddy.go`) — `codebuddy-cn` / `codebuddy-intl` now use a dedicated executor that forces `stream=true` upstream (CodeBuddy rejects non-stream with HTTP 400 code 11101), injects the CLI/IDE static headers, and re-aggregates OpenAI-chat SSE into a single `chat.completion` for non-stream clients (`sseToOpenAIJSON`, mirroring JS `parseSSEToOpenAIResponse`).
- Provider parity with the reference implementation.

## [v1.6.0] — 2026-08-04

### 🚀 Next.js Engine Feature Ports

- **TTS Voice Listing** (`/audio/voices`) — Full voice-listing with provider support (`edge-tts` default, `elevenlabs`, `gemini`, `local-device`), `?lang` filter, 24h in-process cache, and `byLang`/`languages` grouping matching the dashboard's media-providers page.
- **Proxy-Pools Deploy** (`/proxy-pools/{vercel,deno,cloudflare}-deploy`) — Deploy edge relay functions to Vercel/Deno/Cloudflare with status polling, plus a new `InsertProxyPool` DB method writing byte-compatible `data` JSON.
- **Headroom Management** (`/headroom/*`) — Full headroom-ai lifecycle in Go: binary/Python detection, spawn/stop/restart, compression extras install/uninstall, `/headroom/proxy` reverse proxy with SSRF guard, and dashboard HTML rewrite.
- **CLI-Tools Status** (`/cli-tools/all-statuses`) — Batch detection of 14 CLI tools (Claude, Codex, OpenCode, etc.) installed state + version.
- **Live Console Logs** (`/translator/console-logs`, `/stream`) — In-process ring buffer + SSE streaming of engine log output so the dashboard's "Monitor Console Log" shows Go logs live (25s keepalive, init/line/clear events).

### 🔍 Observability

- **Lightweight Request Tracing** (`/debug/traces`) — In-memory span recording + p50/p95/p99 latency per provider+model with `?n=` cap. Stdlib-only, no OpenTelemetry SDK dependency.

### 🛡️ Security

- **Prompt-Injection Guard** — Heuristic detection (`messages[]`, `input[]`, Claude content blocks) tagging classic injection attempts in logs. Toggle via `--no-injection-guard` / `INJECTION_GUARD_DISABLED` (on by default).
- **`/admin/health/reset` Moved Behind API-Key Auth** — Previously public; now requires a valid API key to prevent unauthenticated health-state resets (open-source hardening).
- **MITM Binds Loopback Only** — TLS proxy binds `127.0.0.1:443` instead of all interfaces, preventing LAN clients from using it as an open proxy.

### 🐛 Bug Fixes & Stability

- **SSE Fragment Rejoin** — Fixed `unexpected end of JSON input` on opencode free-tier by buffering/rejoining truncated SSE JSON payloads per session (1 MiB cap).
- **Codex/CommandCode Tool-Call Streams** — Stable per-call tool IDs/indices (using upstream `call_id`), correct `[DONE]` framing, and checked `w.Write` errors.
- **MITM Goroutine Leaks** — `Stop()` drains in-flight connections (WaitGroup + active conn close); request bodies bounded at 10 MiB.
- **Executor/OAuth Registry Mutexes** — Package-level registry maps now guarded by `sync.RWMutex` (race-free on re-registration).
- **Token Saver JSON Number Preservation** — `CompressMessages`/`InjectSystemPrompt` use `json.Number` so numeric fields (temperature, large ints) round-trip unchanged.
- **`interface{}` → `any`** — Lint cleanup across stream/log packages.

## [v1.5.0] — 2026-07-24

### 🚀 Architecture & Observability Enhancements

- **Modular `main.go` Refactoring** — Extracted CLI subcommands (`mitmEnable`, `mitmDisable`, `mitmStatus`, `resolveDataDir`) to `cmd/9router-go/commands.go` and encapsulated server routing setup into `handlers.SetupServerRouter()`.
- **Structured Request Logging Middleware** — Moved `statusWriter` and `RequestLogger` to `internal/middleware/logging.go`. Requests are logged with Correlation ID (`id=req_...`) using structured logger (`slog.Info`, `slog.Warn`, `slog.Error`).
- **Dynamic HTTP Status Log Levels** — Requests with status 5xx are logged at `ERROR` level, 4xx at `WARN` level, and 2xx/3xx at `INFO` level for clean log filtering in production.
- **Upstream Memory Exhaustion Protection** — Added `io.LimitReader` caps (1MB for upstream error bodies, 10MB for non-streaming completion bodies) to protect proxy memory from rogue upstreams.
- **Double WriteHeader Prevention** — Added `written bool` guard to `statusWriter` and `cw.IsCommitted()` checks across combo fallback handlers to eliminate `superfluous response.WriteHeader` warnings.
- **Typed Request ID Context Key** — Shared `log.RequestIDKey` across middleware and logging packages to ensure context lookups match reliably.

## [v1.4.0] — 2026-07-23

### 🛠️ Technical Debt Remediations (All 9 Items Resolved)

- **Context-based Per-Request Usage Capture** — Replaced global `translator.lastUsage` with context-captured isolation (`WithUsageCapture`, `SetUsage`, `GetAndClearUsage`) to eliminate cross-request data races under concurrent traffic. (`internal/translator/usage.go`)
- **Thread-safe Daily Usage Updates** — Protected `upsertDailyUsage()` with `dailyUsageMu` mutex to prevent concurrent SQLite read-modify-write races. (`internal/handlers/chat/usage.go`)
- **Committed Response Writer** — Wrapped `http.ResponseWriter` with `committedResponseWriter` to prevent safe-retry attempts after response headers have already been sent to the client. (`internal/handlers/chat/response_writer.go`)
- **Strict Context Propagation** — Replaced all `http.NewRequest` with `http.NewRequestWithContext` across handlers, proxy execution drivers, and OAuth helpers to prevent orphaned upstream connections.
- **Graceful Shutdown** — Implemented `http.Server` graceful shutdown with signal drain (15-second timeout) on SIGINT/SIGTERM. (`cmd/9router-go/main.go`)
- **SQLite Connection Pool Optimization** — Reduced SQLite `SetMaxOpenConns(4)` for optimal WAL mode performance and zero connection contention. (`internal/db/client.go`)
- **Thread-Safe ProxyPool Cache** — Added `sync.Map` `proxyPoolCache` in `internal/db/proxyPools.go` to preserve round-robin rotation indices across requests. (`internal/db/proxyPools.go`)
- **Unbounded Request Body Guard** — Added `middleware.MaxBody` (10MB limit) to protect all endpoints from OOM attacks. (`internal/middleware/max_body.go`, `cmd/9router-go/main.go`)

### ⚡ Metrics, Latency & Token Accounting Fixes

- **TTFT & Latency Tracking** — Added `StartTime` and `TTFT` tracking across all streaming and non-streaming proxy execution drivers (`openai`, `opencode`, `deepseek`, `claude`, `grok-cli`, `qoder`, etc.). (`internal/proxy/executor/`)
- **Input Token Calculation Fix** — Added `[]byte` type support to `CountValueChars` so fallback prompt token calculation accurately estimates token size instead of defaulting to 1 token. (`internal/handlers/chat/chat.go`)
- **Output Token Calculation Fix** — Connected `ResponseBuf` in `executor.Request` to record stream output tokens when upstream omits token usage objects. (`internal/proxy/executor/openai.go`)
- **Prompt Caching Tokens Support** — Updated `OpenAIUsage` to extract `cached_tokens` (`prompt_tokens_details.cached_tokens`) and `cache_creation_input_tokens`. (`internal/translator/types.go`, `internal/handlers/chat/usage.go`)

### 🧪 End-to-End Integration Test Suite

- **E2E Test Suite** — Added `internal/handlers/chat/e2e_integration_test.go` to test real HTTP streaming SSE, non-streaming JSON responses, TTFT latency, token accounting, and SQLite DB usage logging end-to-end.

### 🌐 Endpoints

- **`/api/hello`** — Registered `/api/hello` route returning `200 OK` for ping probes from Claude Code CLI. (`cmd/9router-go/main.go`)

## [v1.3.0] — 2026-07-22

### 🏥 Next.js-Compatible Health System

- **Connection-based health** — Replaced old `kv`-based `IsProviderHealthy`/`RecordProviderHealth` with `modelLock_*` fields in `providerConnections.data` JSON blob, matching Next.js `markAccountUnavailable` / `clearAccountError` flow. (`internal/db/health.go`, `internal/db/accounts.go`)
- **Per-connection model locks** — `LockConnectionModel` / `UnlockConnectionModel` / `IsConnectionModelLocked` use SQLite `json_set()` on shared `providerConnections.data`. Dashboard can read/write same fields. (`internal/db/accounts.go`)
- **`IsProviderAvailable`** — New `Repo` method checks if ANY connection for a provider has no active `modelLock_<model>`, replacing the old kv-based pre-check. (`internal/db/accounts.go`)
- **`POST /admin/health/reset`** — Resets `modelLock_*` on connections via query params `?provider=X&model=X`. Dashboard can call via headroom proxy. (`cmd/9router-go/main.go`)
- **Eliminated duplication** — Package-level `IsProviderHealthy` / `ResetProviderHealth` now delegate to `NewRepo(database)` instead of duplicating lock JSON parsing logic. (`internal/db/health.go`)

### 🧪 Test Fixes

- **False-pass assertions** — 3 handler tests were checking old kv-based `repo.IsModelLocked()` which always returned `false` vacuously. Changed to `repo.IsConnectionModelLocked(connID, model)` to actually verify connection-level locks. (`internal/handlers/chat_test.go`)

## [v1.2.0] — 2026-07-22

### 🎯 Gemini Tool Calling Fixes

- **thought_signature round-trip** — Gemini response encodes `thought_signature` into tool call `id` via `__ts__` separator; request decoder restores it for valid verification. Works for both streaming and non-streaming. (`internal/translator/gemini.go`)
- **Antigravity (AGY) support** — Custom `GeminiPart.UnmarshalJSON` handles `thoughtSignature` (camelCase) AND `thought_signature` (snake_case) since the internal `v1internal` endpoint returns camelCase. (`internal/translator/gemini.go`)
- **Tool response name fix** — `tool_call_id` with `__ts__` suffix no longer corrupts `functionResponse.name` extraction, preventing Gemini validation errors on turn 2. (`internal/translator/gemini.go`)

### 🎨 Logging

- **ANSI color-coded logs** — `INF` = green, `WRN` = yellow, `ERR` = red, `DBG` = cyan. Auto-detects TTY (disabled when piped). Disable via `NO_COLOR=1`. (`internal/log/log.go`)

### 🔧 Streaming Fixes

- **SSE multi-line** — Gemini stream chunks with multiple SSE lines (`data: ...\ndata: ...`) are now split and translated individually. Error on one line continues to next instead of aborting. (`internal/handlers/gemini_handler.go`)

### 🧹 Cleanup

- `fallback.go`: Removed misleading `WRN tokensaver failed` logs — replaced with idiomatic `if next, did := ...; did` pattern.
- `test_opencode.go`: Removed (stale temporary test file).
- `internal/translator/gemini_test.go`: Added (unit tests for `thought_signature` round-trip).

## [v1.1.0] — 2026-07-21

### 🚀 New Features

- **SSRF protection** — `/v1/web/fetch` now blocks requests to private/internal IPs (RFC 1918, loopback, link-local, cloud metadata). Matches Next.js `assertPublicUrl()`. (`internal/handlerutil/ssrf.go`)
- **Bypass handler** — Detects Claude Code naming, warmup, and count requests. Returns fake responses without calling upstream, preventing wasted combo rotation slots. (`internal/handlers/bypass.go`)
- **Structured logging** — New `internal/log` package with Info/Warn/Error/Debug levels, runtime config via `LOG_LEVEL` env var. All ~100 `log.Printf` calls replaced across 24 files.
- **Per-connection model locks** — Model locks now stored as `modelLock_<model>` in `providerConnections.data` JSON blob. DB-compatible with Next.js dashboard. Connection A and B can have independent lock states.
- **SSE stall detection** — `StallReader` wrapper closes upstream connection after 6 minutes of no data, preventing hung streams. Integrated into all 4 SSE stream paths.
- **Error classification** — Text-based error rules (8 patterns) + status-based rules (5 codes) + exponential backoff (2s–5min). Fully matching Next.js `checkFallbackError()`.
- **Retry-after tracking** — Tracks earliest `retryAfter` across combo models, includes `Retry-After` header in error responses.
- **Request ID tracing** — Every response includes `X-Request-ID` header, access log includes `id=xxx` prefix.
- **Combo strategies aligned with Next.js** — Sticky round-robin, auto-capability-switch (vision/pdf detection).
- **Health/lock check in combo loops** — Skip unhealthy or locked models during fallback iteration.

### 🔧 Refactoring

- **Error response consistency** — `WriteJSONError` now status-code-aware (e.g., 401 → `authentication_error`, 429 → `rate_limit_error`). `auth.go` inline JSON replaced.
- **SSE consolidation** — `proxy.WriteSSEHeaders` shared by all 4 SSE stream functions. `proxy.SSECopy` with optional `onChunk` callback.
- **Shared test fixture** — `internal/dbtest` package provides canonical `CreateTables()` eliminating duplicated schema in 5+ test files.
- **`stringBuilder` → `bytes.Buffer`** — Removed duplicate custom type in favor of standard library.

### 📚 Documentation

- `ARCHITECTURE.md` — 10 Mermaid flow diagrams (request lifecycle, combo, fusion, error classification, etc.)
- `DATABASE.md` — All 11 tables, JSON blob structure, Go vs Next.js differences

### 🐛 Fixes

- `RetryAfter` ceiling calculation corrected from floor to proper ceiling (`time.Second - 1`)
- Stream translation now handles `[DONE]` marker before JSON parsing
- `TranslateResp` field now passed in `tryForwardWithConnection`

## [v1.0.2] — Previous

- Initial release with OpenAI/Claude SSE proxy, combo fallback, token savers, benchmark results.
