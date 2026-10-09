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