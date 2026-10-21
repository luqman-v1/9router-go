### Fixed

- Per-provider model testing no longer judges a provider by a single model.
  The provider sweep used to probe only the first chat model
  (`FirstChatModelForProvider`), so a provider whose first model was down
  read as failed even while its other models answered. `probeProviderAll`
  now walks every chat model the provider's active connections serve
  (`ChatHandler.ChatModelsForProvider`, the plural of the first-model
  lookup, over the same connected list `/v1/models` serves) and the
  provider passes when at least one model answers. When none do, the row
  keeps the first failure's wording (or the first block, when nothing
  outright failed); a partial sweep notes "N of M models answered".
- Provider rows now carry the per-model breakdown (`models[]` in the run
  snapshot: model, state, latency, status, error), and the Model Testing
  page renders it: a segmented bar under the provider row (one segment per
  model — pass / fail / blocked / running), an "N of M models answered"
  line, and a chip per model with its state and latency. The Failures list
  also reports the dead models of a provider that passed overall, instead
  of hiding them behind the provider's pass.

### Page layout (follow-up, same day)

- At the operator's request the Model Testing page was restructured to the
  JRouters testing page's layout, keeping this dashboard's theme: provider
  chips as the scope filter (with per-provider health dots), five stat
  cards (Selected / Healthy / Slow / Error / Tested), a progress bar, the
  latency legend (Healthy <8000ms, Slow 8000-15000ms, Error above that or
  failed), and one table row per model (Status / Model / Provider / Total /
  HTTP / Error / Retry). The per-model / per-provider mode toggle is gone:
  like JRouters, health is read per model, so a provider can no longer look
  failed because one model is down — the chips and the report group by
  provider instead. Statuses band a pass by latency (slow/error) on top of
  the server verdicts; blocked rows stay distinct (amber, "account parked").
- Verdicts accumulate in a per-model map persisted to localStorage
  (`9router-model-test-verdicts`), mirroring the JRouters testing store, so
  a single-row Retry no longer replaces the whole table and results survive
  reloads. A Markdown report (summary + results by provider) can be
  downloaded, as in JRouters. The sweep itself remains the server-side run;
  the per-provider sweep above stays available through the runs API.

### Verified

- `go test ./internal/handlers/media/` — all pass, including new tests:
  `TestAggregateProviderResults` (all pass, mixed pass, all fail keeping
  the first failure's wording, all blocked, nothing probed),
  `TestModelBreakdown`, and `TestModelTestRunner_ProviderBreakdown`
  (a mixed provider passes as a row and keeps both model verdicts).
- Web build (`npm run build`) passes; binary rebuilt and installed;
  gateway healthy on :20130. A live provider run still needs a dashboard
  session, so the final click-through is the operator's.
