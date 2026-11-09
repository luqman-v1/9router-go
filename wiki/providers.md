---
title: Providers
order: 6
section: Using the Gateway
since: 1.9.14
---

# Providers

9router-go supports 40+ providers and 100+ models. Every provider is **isolated
and independent** — no provider is merged, cross-aliased, or redirected into
another.

## Connecting

Dashboard → Providers → pick a provider → Connect.

No signup needed:

- **Kiro AI** — around 50 free credits per month
- **OpenCode Free** — no auth

## OAuth flows

Different providers authenticate differently, and the dashboard offers the right
one per provider:

| Flow | Providers |
|---|---|
| Social / authorize-exchange | Kiro, Antigravity, Cline, Trae, Windsurf, Zed, Kimchi, Xiaomi MiMo |
| Device code | Any provider exposing a device flow |
| Local proxy callback | Codex |
| Bulk import | Codex, Grok CLI |
| API key paste | Kiro |
| Cookie / PAT paste | iFlow (cookie), GitLab (PAT) |
| Session polling | Freebuff — initiate, poll, inspect, and switch between sessions |

Built-in public client credentials exist for antigravity, gemini-cli, xai, codex,
iflow and others, so most operators never set an OAuth client ID. Override per
provider with `{PROVIDER}_OAUTH_CLIENT_ID` / `{PROVIDER}_OAUTH_CLIENT_SECRET`
when you run your own OAuth app.

Tokens are refreshed automatically, and a request that fails with `401` is retried
once after a refresh.

## Browser-based login

Some web providers have no OAuth endpoint at all. Two paths:

- **Gemini Web** (`/api/providers/gemini-web/browser-login`) and
  **DeepSeek Web** (`/api/providers/deepseek-web/browser-login`) open a browser,
  wait for the sign-in, and read the session out.
- **Import from a signed-in browser** (`/api/providers/{gemini,deepseek}-web/import-chrome`)
  reads the browser's own cookie store. **Close the browser first** — a browser
  that is still open rotates the same session from the other side and the two
  renewals cancel each other out, which signs the account out.

Renew a DeepSeek session with
`POST /api/connections/{id}/renew-deepseek`.

## Custom providers

One shared dialog adds an OpenAI- or Anthropic-compatible endpoint. One custom
provider is one `providerNodes` row, and a node can have many connections; a
connection points at its node through `provider = node.id`.

| Field | Purpose |
|---|---|
| Name | Display name |
| Prefix | Wire syntax prefix (`provider/model`) |
| Base URL | Upstream endpoint |
| Headers | Your own request headers |

Validate an endpoint before saving with `POST /api/providers/validate`.

## Per-provider limits

Each custom node can carry a capacity budget:

| # | Limit | Unit | Meaning |
|---|---|---|---|
| L1 | RPM | requests/minute | Request rate this endpoint tolerates |
| L2 | TPM | tokens/minute | Input (prompt) token rate this endpoint tolerates |
| L3 | Concurrency | parallel requests | Number of simultaneous requests |
| L4 | Lifetime quota | total tokens | Cumulative cap before the node is declared exhausted |
| L5 | Queue timeout | ms | How long a request may wait in the FIFO queue for L1/L3 |
| L6 | Contract expiry | RFC3339 UTC date | Validity period of the node's contract |

`0` means unlimited; an empty string means no expiry — the same convention as
API keys.

**Behaviour when exceeded**: congestion (RPM/TPM/concurrency) marks the candidate
as full for that attempt only, and the router tries the next candidate; `429` is
returned only when every candidate is exhausted. Node state (quota exhausted,
contract ended) skips all of that node's connections and returns a typed `403`.

**A congestion burst writes no cooldown.** One request over a limit must not
globally park a healthy node — only a debug trace is written. The `lastError` row
the dashboard's node-health table reads is still written, so you can see the
cause without the node being locked.

Set limits with `PUT /api/provider-nodes/{id}/limits` and lift a spent quota with
`POST /api/provider-nodes/{id}/quota/reset` (it moves the watermark, it does not
delete history).

## Models

Browse a provider's models with `GET /api/providers/{id}/models`, export keys
with `GET /api/providers/{id}/export-keys`, and remove many at once with
`DELETE /api/providers/{id}/models`.

**Custom models** are stored in the `customModels` key-value scope, not as columns
on `providerNodes`. Add one at a time or paste many (separated by spaces, commas
or newlines) — one request, atomic, per-row results.

**Aliases** (`/api/models/alias`) let a client write a short name for a long
model id. **Deprecations** (`/api/models/deprecations`) surface models upstream
has retired, and **auto-disable** (`/api/models/auto-disable`) turns off the ones
that stopped working.

## Important notes

- **No cross-provider aliasing.** `antigravity` is not `opencode`; a model
  belonging to one provider is never redirected to another.
- **No substring-based model rewriting.** Routing decisions are driven by catalog
  registration, database aliases, custom node prefixes, or standard
  `provider/model` wire syntax — never by ad-hoc string inspection.
- **The Import from /models button** only appears for providers that genuinely
  support dynamic catalog discovery upstream.
