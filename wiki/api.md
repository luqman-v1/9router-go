---
title: API
order: 4
section: Getting Started
since: 1.9.14
---

# API

9router-go speaks the OpenAI, Claude, Gemini and Ollama dialects, and adds media,
batching, and a dashboard REST API. This page is the full surface, taken from the
route table.

## Authentication tiers

| Tier | Protects |
|---|---|
| Public | `/health`, `/version`, `/api/hello`, `/changelog`, OAuth callbacks, dashboard assets |
| Client API key (`Authorization: Bearer` / `X-API-Key`) | every `/v1/*` proxy route |
| Dashboard session / CLI token / API key | every `/api/*` dashboard route |
| Console-log session / CLI token | `/api/translator/console-logs*` (client API keys are rejected) |
| Admin session / CLI token | update, auto-update toggle, shutdown, `/debug/pprof/*` |

## Chat and models

```text
POST /v1/chat/completions       OpenAI Chat Completions
POST /messages                  Claude Messages
POST /messages/count_tokens     Claude token counting
POST /responses                 Responses API
POST /responses/compact         Compact a Responses conversation
POST /api/chat                  Ollama-compatible chat
GET  /v1/models                 Model catalog
GET  /v1/models/{id}            One model's detail
GET  /v1/models/{kind}          Models by kind (llm, embedding, image, tts, stt, video)
GET  /api/models/catalog-sync   Catalog sync status
POST /api/models/catalog-sync   Trigger a catalog sync
```

## Media

```text
POST /embeddings                 Embeddings
POST /images/generations         Text to image
POST /images/edits               Image editing
POST /audio/speech               Text to speech
POST /audio/music                Music generation
POST /audio/sound-generation     Sound effect generation
POST /audio/transcriptions       Speech to text
GET  /audio/voices               Available voices
GET  /api/media-providers/tts/voices            TTS voices
GET  /api/media-providers/tts/{provider}/voices  Voices for one TTS provider
POST /videos/generations         Text to video
POST /videos/edits               Video editing
POST /videos/extensions          Video extension
GET  /videos/{id}                Video job status
POST /search                     Web search
POST /scrape                     Page scraping
POST /web/fetch                  Fetch a URL
POST /systemone                  System One
```

## Batching

```text
POST /v1/batches                  Create a batch
GET  /v1/batches                  List batches
GET  /v1/batches/{batch_id}       Batch status
POST /v1/batches/{batch_id}/cancel  Cancel a batch
```

The same four exist unprefixed under `/batches`.

## Dashboard REST

Grouped by the dashboard page that drives them.

### Connections and providers

```text
GET    /api/connections
POST   /api/connections
PUT    /api/connections/{id}
DELETE /api/connections/{id}
POST   /api/connections/{id}/test          Probe a connection
POST   /api/connections/{id}/reorder       Change rotation order
GET    /api/providers/{id}/models          Models on a provider
GET    /api/providers/{id}/export-keys     Export provider keys
DELETE /api/providers/{id}/models          Bulk model removal
GET    /api/providers/{id}/overrides       Per-provider header overrides
PUT    /api/providers/{id}/overrides
POST   /api/providers/validate             Validate an endpoint before saving
```

### Custom provider nodes

```text
GET    /api/provider-nodes
POST   /api/provider-nodes
PUT    /api/provider-nodes/{id}
DELETE /api/provider-nodes/{id}
POST   /api/provider-nodes/validate
PUT    /api/provider-nodes/{id}/limits        Capacity limits
POST   /api/provider-nodes/{id}/quota/reset   Lift a spent token quota
```

### Combos

```text
GET    /api/combos
POST   /api/combos
POST   /api/combos/auto-free          Build a free-tier chain
POST   /api/combos/auto-family        Build per-family chains
GET    /api/combos/presets            Cursor / Claude preset lists
POST   /api/combos/presets            Create from presets
PUT    /api/combos/{id}
DELETE /api/combos/{id}
POST   /api/combos/reconcile                  Apply every combo's smart rules
POST   /api/combos/{id}/smart/preview         Dry-run one combo's rules
POST   /api/combos/{id}/smart/load            Apply one combo's rules
```

### API keys, plans, and model access

```text
GET    /api/keys
POST   /api/keys
PUT    /api/keys/{id}
DELETE /api/keys/{id}
PUT    /api/keys/{id}/toggle
POST   /api/keys/{id}/rotate
POST   /api/keys/{id}/reset-quota
GET    /api/keys/{id}/models          Per-key model allowlist
PUT    /api/keys/{id}/models

GET    /api/plans
POST   /api/plans
GET    /api/plans/{id}
PUT    /api/plans/{id}
DELETE /api/plans/{id}
GET    /api/plans/{id}/keys           Keys inheriting a plan
```

### Models

```text
GET    /api/models/custom
POST   /api/models/custom
DELETE /api/models/custom/{key}
GET    /api/models/caps               Model capabilities and limits
GET    /api/models/meta
GET    /api/models/disabled
PUT    /api/models/disabled/{provider}
POST   /api/models/auto-disable
GET    /api/models/health
PUT    /api/models/health
GET    /api/models/alias
PUT    /api/models/alias
DELETE /api/models/alias
GET    /api/models/deprecations
POST   /api/models/sync
```

### Proxy pools

```text
GET    /api/proxy-pools
POST   /api/proxy-pools
PUT    /api/proxy-pools/{id}
DELETE /api/proxy-pools/{id}
POST   /api/proxy-pools/{id}/test
POST   /api/proxy-pools/vercel-deploy      Deploy a relay
POST   /api/proxy-pools/deno-deploy
POST   /api/proxy-pools/cloudflare-deploy
POST   /api/proxy-pools/netlify-deploy
```

### Usage, quota, and analytics

```text
GET /api/usage/stream                        Live usage SSE
GET /api/usage/stats
GET /api/usage/chart
GET /api/usage/dashboard
GET /api/usage/request-details
GET /api/usage/request-details/{id}
GET /api/usage/providers
GET /api/usage/{connectionId}
GET /api/usage/{connectionId}/reset-credits          Codex reset credits
POST /api/usage/{connectionId}/reset-credits/consume
GET /api/usage/{connectionId}/glm-reset-cards        GLM reset cards
POST /api/usage/{connectionId}/glm-reset-cards
GET /api/analytics/compression                      Token-saver analytics
```

### Cache

```text
GET    /api/cache
DELETE /api/cache
GET    /api/cache/entries
DELETE /api/cache/entries
```

### Settings and maintenance

```text
GET    /api/settings
PUT    /api/settings
GET    /api/settings/database          Export the database
POST   /api/settings/database          Import a database
GET    /api/settings/database/stats
POST   /api/settings/database/prune
POST   /api/settings/database/prune-usage
POST   /api/settings/database/vacuum
POST   /api/settings/proxy-test
POST   /api/settings/logo              White-label logo upload
POST   /api/settings/currency/refresh  Refresh exchange rates
GET    /api/tokensaver/rtk/filters
POST   /api/tokensaver/rtk/test
POST   /api/tokensaver/caveman/test
```

### Tunnels

```text
GET  /api/tunnel/status
POST /api/tunnel/enable
POST /api/tunnel/disable
GET  /api/tunnel/tailscale-check
POST /api/tunnel/tailscale-enable
POST /api/tunnel/tailscale-disable
```

### Security

```text
GET    /api/vault/status
POST   /api/vault/rotate
GET    /api/guardrails/policies
POST   /api/guardrails/policies
PUT    /api/guardrails/policies/{id}
DELETE /api/guardrails/policies/{id}
GET    /api/guardrails/logs
GET    /api/metrics                     Prometheus
```

### OAuth

Import and exchange endpoints per provider. Kiro, Codex, Grok CLI, Freebuff,
Antigravity, Cline, PKCE, device-code, auth-code, Trae, Windsurf, Zed, Cursor,
Kimchi, GitLab PAT, iFlow cookie, and Xiaomi MiMo all have their own
authorize/exchange pair under `/api/oauth/{provider}/…`. Several providers add
bulk-import and auto-import variants.

### Console log

```text
GET    /api/translator/console-logs
DELETE /api/translator/console-logs
GET    /api/translator/console-logs/stream
GET    /api/translator/console-logs/level
PUT    /api/translator/console-logs/level
```

### CLI tools and version

```text
GET  /api/cli-tools/all-statuses
GET  /api/cli-tools/{tool}-settings

GET  /version
GET  /api/version
GET  /api/version/status
GET  /api/version/check
POST /api/version/update           admin
POST /api/version/auto-update      admin
```

### Headroom

```text
POST   /headroom/start | stop | restart
GET    /headroom/status
GET    /headroom/extras
POST   /headroom/extras
DELETE /headroom/extras
GET    /headroom/proxy
```

The same set is also mounted under `/api/headroom/…`.

## Finding an endpoint

Use the search box at the top left to jump straight to an endpoint. Shareable
links you can build:

- `?q=/v1/chat/completions` — every page mentioning that endpoint
- `?q=RTK_ENABLED` — the environment page and any page using that variable
- `api.html#batching` — a specific section inside this page

## Model response timeout in combos

For sequential combo routing, set **Model response timeout** in seconds (decimals
such as `5.3` are fine) in the combo's Edit dialog, or from the compact field on
the combo card. Empty or `0` disables it. The timer covers each model candidate
except the last, including connection selection and waiting for a usable
response. If it expires before output is committed, the router cancels that
candidate and tries the next. The last candidate keeps the gateway's normal
deadline, so the setting never makes a combo fail sooner than it would without
it. An active stream is not cut off by this setting. For non-streaming requests,
the timer covers the wait for the complete response.

Fusion panel timing is separate. A model that times out is left out of that combo
for a grace period (default 5 minutes, set per combo under the timeout in the Edit
dialog; `0` re-includes it on the next request) and then rejoins the chain. If
every model in the chain is inside its grace period, the full chain is used. The
pause is held in memory and resets on restart. Stored as `firstTokenGraceMs` next
to the timeout. The dashboard shows seconds; the setting is stored in milliseconds
as `comboStrategies[comboName].firstTokenTimeoutMs`.
