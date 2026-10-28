### feat(dashboard): live catalogue discovery for five more public endpoints

Command Code's catalogue was the first provider whose registry snapshot froze
the dashboard (`#559`). The same audit across all 159 providers found five more
whose catalogue is reachable without a valid credential, and none of them were
wired: `GET /api/providers/{id}/models` fell through to the
`openai-compatible-*` branch and answered `400`, and `ModelsListURLs` omitted
them so "Check Latest Models" never rendered.

| Provider | Live models | Static rows | Note |
|---|---|---|---|
| `vercel-ai-gateway` | 484 | **none** | no static catalog at all |
| `openrouter` | 468 | 18 | already had a free-tier fetcher, no backend entry |
| `nvidia` | 84 | 11 | |
| `ollama` | 23 | 8 | `{models:[{name, model}]}`, not OpenAI shape |
| `chutes` | 14 | **none** | no static catalog at all |

`vercel-ai-gateway` and `chutes` are the sharpest cases: both exist in
`KnownProviders` and in the dashboard catalog, and neither has a single model
listed in `ProviderModels` or `web/src/lib/models.ts` — the provider could be
routed to but never discovered.

**What changed**

- `internal/providers/providers.go`: five entries in `modelsListURL`, the one
  map `writeConnectionModels` and `fetchLiveModelIDs` both read.
- `internal/handlers/suggestedmodels.go`: a `vercel-free` filter. Vercel spells
  its price keys `input`/`output` rather than openrouter's
  `prompt`/`completion` and its window `context_window` rather than
  `context_length`, so `filterOpenRouterFree` cannot be reused. The 200k floor
  is what separates the free tier from the free *evaluation* tier — Vercel also
  zero-prices four baseten/convai models at 8k–16k context.
- `web/src/lib/providers.ts`: `modelsFetcher` for `vercel-ai-gateway` only.
- `web/src/components/connections/ProviderDetailView.svelte`: the five added to
  `ModelsListURLs`, and the "Import from /models" gate widened.

**Why only one new `modelsFetcher`.** That section is a free-tier surface, and
probing each feed shows the other four have no zero-priced tier to suggest:
`chutes` prices every model, `nvidia` and `ollama` carry no pricing block at
all, and `openrouter` already had its `openrouter-free` fetcher. A fetcher that
returns nothing renders an empty section the operator has to interpret.

**A distinction worth recording.** All five answer 200 with no `Authorization`
header, but only `nvidia`, `ollama` and `openrouter` still answer 200 once the
handler sends the bearer it always sends. `vercel-ai-gateway` and `chutes` 401
on any bearer, so a revoked key there reports a failed fetch exactly like the
keyed aggregators — correct, but not the "survives a revoked credential"
property the other three have. Either way a connection must exist first:
`writeConnectionModels` refuses a provider with no stored key before dialing.

`ollama-local` is deliberately excluded. It is loopback by design, and pointing
the shared discovery path at it would make "Check Latest Models" dial the
operator's own machine — a failure they cannot act on.

**Verification**

- Probed live through the real handler: `nvidia` 84 models, `ollama` 23,
  `openrouter` 468, all through `GET /api/providers/{id}/models` with a bogus
  bearer; `vercel-ai-gateway` and `chutes` 401 on that same bogus bearer.
- The `vercel-free` filter against the live feed returns exactly
  `[nvidia/nemotron-3.5-lightning-free, poolside/laguna-s-2.1-free,
  reflection/beam, reflection/beam-free, stealth/glyph-cluster]` and excludes
  all four evaluation-tier models.
- `go test ./internal/providers/ ./internal/handlers/ ./internal/handlers/dashboard/`
  pass; `bun test src/lib/models.test.ts src/lib/providers.test.ts` 24 pass;
  `bun run build` and the svelte-check ratchet (0 unresolved identifiers, 78
  against the pinned baseline of 78).
