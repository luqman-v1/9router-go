### Tier 1 upstream-gap ports: budget enforcement, `/v1/rerank`, usage export, uptime heartbeat

Four features taken from `QuantumNous/new-api` and the F-2 budget work that
`docs/keirouter-port-plan.md` had specified but never implemented. Each is
additive: no existing route, column or behaviour changed.

#### Budget engine (F-2) — the plan budget is now enforced

The `plans` table and the `apiKeys` budget columns have carried
`LimitMicros`, `LimitTokens`, `Period`, `AlertPct` and `HardCutoff` since F-3
landed, and `policy.Resolve` has resolved them since the same commit, but
nothing read them. This closes that gap.

- `internal/budget/` is the engine: `ResolveWindow` maps a period
  (`daily|weekly|monthly|total`) onto a timestamp floor, `Check` measures one
  key's spend and returns a `Decision`, and `decide` applies the ceilings.
- `internal/db/budget_spend.go` adds `SpendForKeySince`, which sums cost and
  tokens over `usageHistory` inside the window. Cost is converted to integer
  micros exactly once, in SQL, so no float reaches the comparison that refuses
  a request.
- `internal/middleware/ratelimit.go` enforces it after the lifetime token
  quota and before TPM: an over-budget key is answered with **402** rather than
  a token-bucket 429 that would send the client looking at the wrong limit.
  A soft ceiling (hardCutoff off) serves the request with
  `X-Budget-Status: approaching` instead of refusing.
- The key's own budget columns override its plan's, the same precedence
  `rateLimitRPM` already uses.
- A key with no budget armed costs no database read at all, and a failed read
  fails open — the same choice the lifetime quota makes, because a spend
  ceiling is not an access grant.
- The budget is configurable end to end: `PUT /api/keys/{id}` accepts
  `limitMicros`, `limitTokens`, `budgetPeriod`, `alertPct` and `hardCutoff`
  (an omitted field keeps the stored value, an explicit zero clears that
  axis), `GET /api/keys` publishes them per row, and they survive a backup
  round-trip — a restore that dropped them would bring every key back with
  its ceiling silently cleared.
- A database whose `apiKeys` table predates the columns reads every key with
  its budget axes at zero rather than failing the listing, the same
  degradation `quotaResetId` already gets.

#### `/v1/rerank`

- `MediaHandler.HandleRerank` forwards to the provider's `/rerank` sibling,
  following the embeddings handler's structure: model resolution, per-key
  access gate, connection pick, provider auth and static headers, proxy-pool
  egress, usage logging.
- `buildRerankURL` derives the lane from whichever spelling the connection was
  saved with, so a base naming `/chat/completions` or `/embeddings` still
  resolves rather than producing a doubled path.
- `GET /v1/models/rerank` lists the providers that serve the lane.

#### Usage log export

- `GET /api/usage/export?format=csv|json&period=<window>` streams the
  `requestDetails` ledger as a downloadable file. It walks the same paged
  reader the dashboard uses, so the export cannot disagree with what the UI
  showed, and it is streamed so a large window never has to fit in memory.
- The period resolver is the dashboard's own, so `24h` here means what `24h`
  means there.

#### Uptime Kuma heartbeat

- `internal/uptime/` pushes a heartbeat to any Push-Monitor-compatible URL on
  a timer, so an operator learns the gateway is down from their monitor rather
  than from a CLI tool that stopped answering.
- The configuration is read from settings on every tick, so a URL saved in the
  dashboard takes effect without a restart. A tick with no configuration is a
  no-op, and a failed push is logged and dropped rather than stalling the loop
  on a monitor that is already gone.
- Settings gains `uptimeKumaEnabled`, `uptimeKumaPushUrl` and
  `uptimeKumaInterval`, all off by default.

### Verified

- `go vet ./...` clean; `go vet -tags=integration ./internal/integration/...`
  clean.
- `golangci-lint run --new-from-rev="$(git merge-base luqman/main HEAD)" ./...`
  reports 0 issues.
- `go test -count=1` green on `internal/budget`, `internal/db`,
  `internal/middleware`, `internal/uptime`, `internal/handlers/media` and
  `internal/handlers/dashboard`, including new table-driven tests for the
  window resolver, the decision thresholds, the spend reader's micros rounding
  and window floor, the middleware's 402/alert/fail-open paths and
  plan-vs-key precedence, the rerank forward and URL derivation, the CSV and
  JSON exports, the heartbeat loop's lifecycle, the backup round-trip of the
  budget columns, and the legacy-schema degradation.
- `go test -tags=integration -count=1 ./internal/integration/` green, with five
  new cases driving the real router: an exhausted key is refused 402 without
  reaching the upstream, an under-ceiling key is served, a soft ceiling alerts,
  a monthly window excludes older spend, and a plan's budget governs a key with
  no columns of its own.
