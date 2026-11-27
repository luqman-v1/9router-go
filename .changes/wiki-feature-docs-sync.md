### docs(wiki): document the features that shipped undocumented, and fix the site's cross-links

The wiki had drifted a full release behind the binary: whole subsystems were
absent from it, so an operator reading the docs could not find a setting the
dashboard offered, and a route list that omitted a page read as proof the
endpoint did not exist.

- `wiki/governance.md`: the per-key ceilings that came with the workload gate —
  RPM/TPM/RPH/RPD plus concurrency and the FIFO queue that holds a request until
  a slot frees, the per-client-IP gate checked before every key window, and the
  guardrail policy model (`global`/`provider`/`model`/`chain`/`apikey`, the five
  actions, and the `400` a policy with an uncompilable pattern gets at save time).
- `wiki/providers.md`: the transient-retry policy (`502`/`503`/`504` only — a
  `429` belongs to the account-fallback path) and the seat attempt budget that
  bounds the whole retry run, resolved from `seatAttemptBudgetMs`, a provider's
  own `seatBudgetMs`, or `SEAT_ATTEMPT_BUDGET_MS`; and the sparse per-model
  capability override at
  `GET|PUT /api/providers/{id}/models/{model}/caps` — eleven pinnable fields, an
  untouched field left to detection, the `1…10,000,000` ceiling, the five-second
  cache and the fail-open path.
- `wiki/settings.md`: the capacity limits that hang off a custom provider node
  (RPM/TPM/concurrency, queue timeout, lifetime token quota, contract expiry) and
  the `totalRequestTimeoutMs` timeout, including why a supervisor-managed gateway
  asks the supervisor to stop it rather than replacing itself.
- `wiki/usage-analytics.md` and `wiki/api.md`: month-to-date as a distinct window
  from a rolling 20-day one, the hour/day bucket caps, the Claude/Codex seat tier,
  and the analytics, guardrail-log, capability and models endpoints.
- `wiki/combos.md`: the member attempt cap — what it bounds, that `0` is off and
  an explicit per-provider `0` disables it even with a global cap set — and
  least-busy as a fourth connection strategy beside failover, round-robin and
  sticky.
- `wiki/token-saver.md`, `wiki/cli.md`, `wiki/installation.md`: the
  release-error-advisor path, the `doctor` command, and the container/runtime
  facts an install page is expected to state.

`tools/sitegen` also stopped rewriting the wiki's own cross-references into
`github.com/.../blob/main/<page>.html` URLs: a relative `X.html` naming a page the
site publishes now stays relative, and only an `.html` destination that is not a
wiki page falls through to the repository. Every cross-reference on the site was
a 404 on GitHub while the build stayed green, which is why nothing caught it.
Two links pointed at `advanced-features.html#model-breakers`, an anchor goldmark
never publishes; they now name the heading that exists.

Verified: `go test ./tools/sitegen/` (new
`TestRenderWikiPageKeepsIntraWikiHTMLLinksRelative` pins both halves of the link
rule), a full site build with zero `blob/main/*.html` anchors and zero unpublished
`#fragment` targets across all 23 pages, and the `pages.yml` assertions run
locally (sidebar, table wrappers, lazy+sized images, `search-index.json` anchors).
