---
title: New-API Ports
order: 22
section: Under the Hood
since: 1.9.17
---

# New-API Ports

Features ported from [QuantumNous/new-api](https://github.com/QuantumNous/new-api), a
separate AI gateway project (Go + React, 51k stars). 9router-go is not a fork of it —
the two share no code — but new-api solves several of the same problems, and where its
solution fits this gateway's single-binary model, the feature is ported.

Each entry says what the feature does, how to turn it on, and the PR that landed it.

---

## Budget enforcement — per-key USD and token ceilings

**Since:** v1.9.10 · **PR:** [#848](https://github.com/luqman-v1/9router-go/pull/848)

### What it does

Every API key can carry a spending ceiling in USD and in tokens, over a period. When
the ceiling is reached the key is refused — or warned, if you prefer — instead of
running an unbounded bill.

### How to use it

1. Open **API Keys**, click a key, and set **Budget (USD)**, **Tokens**, **Period**
   (`daily` / `weekly` / `monthly` / `total`), **Alert %** and **Hard cutoff**.
2. Or set it once on a [Plan](governance.html) and attach the plan to many keys.
3. **Hard cutoff on** → a request past the cap is rejected with **`402 budget_exhausted`**
   (not a token-bucket `429`, which would point the client at the wrong limit).
4. **Hard cutoff off** → the request is served with `X-Budget-Status: approaching` and
   the spend is still recorded.

In-flight requests are reserved before the check, so N parallel requests cannot all
slip under the limit. A key with no budget armed costs no database read at all, and a
failed read fails open — a spend ceiling is not an access grant.

Money is stored as integer micro-dollars, never a float, so the comparison that refuses
a request never rounds.

---

## `/v1/rerank` — rerank API lane

**Since:** v1.9.10 · **PR:** [#848](https://github.com/luqman-v1/9router-go/pull/848)

### What it does

Proxies the rerank API (`/v1/rerank`) for providers that serve it, alongside the
existing embeddings lane.

### How to use it

1. Point a client at `POST /v1/rerank` with the same shape the provider documents.
2. `GET /v1/models/rerank` lists the providers that serve the lane.

The lane follows the embeddings handler's structure: model resolution, per-key access
gate, connection pick, provider auth, proxy-pool egress and usage logging. `buildRerankURL`
derives the lane from whichever spelling the connection was saved with, so a base naming
`/chat/completions` or `/embeddings` still resolves.

---

## Usage log export — CSV and JSON

**Since:** v1.9.10 · **PR:** [#848](https://github.com/luqman-v1/9router-go/pull/848)

### What it does

Streams the `requestDetails` ledger as a downloadable file, so an operator can analyse
spend outside the dashboard.

### How to use it

```text
GET /api/usage/export?format=csv|json&period=<window>
```

The export walks the same paged reader and period resolver the dashboard uses, so it
cannot disagree with what the UI showed. It streams, so a large window never has to fit
in memory. Truncation at 100k rows is stated in the file rather than silent.

---

## Uptime heartbeat — push to an uptime monitor

**Since:** v1.9.10 · **PR:** [#848](https://github.com/luqman-v1/9router-go/pull/848)

### What it does

Pushes a heartbeat to any Push-Monitor-compatible URL (Uptime Kuma and similar) on a
timer, so an operator learns the gateway is down from their monitor rather than from a
CLI tool that stopped answering.

### How to use it

The three keys live on the settings blob; there is no dedicated form yet, so set them
with a `PUT /api/settings` call (the dashboard's own settings editor uses the same
endpoint, and unknown-to-the-UI keys survive the round-trip):

```bash
curl -X PUT http://localhost:20130/api/settings \
  -H "x-9r-cli-token: $CLI_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"uptimeKumaEnabled": true, "uptimeKumaPushUrl": "https://kuma.example/api/push/abc123", "uptimeKumaInterval": 60}'
```

From a logged-in browser session the `auth_token` cookie already carries the
credential, so the same call works from the dashboard's developer console without the
header.

The configuration is read on every tick, so a URL change takes effect without a restart.
A tick with no configuration is a no-op, and a failed push is logged and dropped rather
than stalling the loop on a monitor that is already gone.

---

## Vertex `v1beta1` — per-connection API version

**Since:** v1.9.17 · **PR:** [#1392](https://github.com/luqman-v1/9router-go/pull/1392)

### What it does

Vertex serves some surfaces — Imagen, the newer Gemini features — only on `/v1beta1/`.
The executor used to hardcode `/v1/` in all three URL forms, so a connection aimed at one
of those models answered 404 no matter how the credential was shaped.

### How to use it

On a Vertex connection, add `vertexApiVersion` to its provider-specific data:

```json
{ "projectId": "my-proj", "location": "us-central1", "vertexApiVersion": "v1beta1" }
```

The default is `v1`, so every existing connection keeps sending traffic to the path it
always did. The change is opt-in per connection.

---

## HTTP/2 PING — health check for idle upstream connections

**Since:** v1.9.17 · **PR:** [#1409](https://github.com/luqman-v1/9router-go/pull/1409)

### What it does

An intermediary that drops an idle connection without `FIN`/`RST` leaves the socket in
the transport's idle pool. A request that arrives inside that window is handed the dead
socket and hangs until the client gives up — a stall that looks like the provider being
slow rather than the connection being gone. The PING probe moves detection to the idle
boundary.

### How to use it

```bash
export RELAY_HTTP2_PING_IDLE_TIMEOUT=30   # seconds without a frame before a probe (default 0 = off)
export RELAY_HTTP2_PING_TIMEOUT=15        # seconds to wait for an acknowledgement (default 15)
```

**Off by default.** The probe adds frames to every idle connection, and the gateway runs
on a 1 vCPU box that is already spending its CPU on streaming. An operator who does not
see the stall should not pay for it.

`Zero` disables the probe entirely, rather than falling back to a default interval — the
operator who wants the extra frames has to ask for them. A `PingTimeout` of `0` leaves
Go's own 15-second default in place.

---

## Per-key model restriction — reselling a key safely

**Since:** v1.9.14 (restriction) · v1.9.17 (presets + route proof) · **PRs:** [#1412](https://github.com/luqman-v1/9router-go/pull/1412)

### What it does

An API key can be restricted to a set of models. A buyer's key only reaches the models
it was sold for, and the restriction is enforced before dispatch — a denied model never
reaches a provider.

### How to use it

1. Open **API Keys**, click a key, open the policy dialog, and set the **model allowlist**
   (`deepseek-*`, `gpt-4*` — wildcards work).
2. Set the budget, period and hard cutoff in the same dialog.
3. Or pick a **buyer preset** in the create dialog — three resale shapes that stamp the
   cap, period, cutoff, 30-day expiry, resale metadata and allowlist in one choice.

Every field stays editable afterwards, so a preset is a starting point, not a lock.

The dashboard route answers `404` for a key that does not exist (it used to answer `200`
and silently restrict nothing), and the `/v1/models` listing reports exactly what
dispatch allows — a client cannot list a model its key may not call.

---

## Playground — a conversation with any routed model

**Since:** v1.9.17 · **PR:** [#1466](https://github.com/luqman-v1/9router-go/pull/1466)

### What it does

A **Playground** tab holds a conversation with any model the gateway routes, without
an engine key. It is the fastest way to answer "does this connection, combo or model
actually answer?" before wiring a client.

Each turn is a synthetic request through the engine's own `HandleChatCompletions` —
the same in-process path the Gemini surface uses — so routing, combo fallback,
persona injection, the limiter and usage accounting are the production code, not a
second implementation. The reply **streams** in as the provider produces it, and the
turn's header shows what a client would receive: the model the gateway resolved, the
token count and the finish reason.

### How to use it

1. Open **Playground** in the sidebar.
2. Optionally pick an **API key**. Every turn is then attributed to it: its usage rows
   name it as the client, its budget is spent, and its model allowlist is enforced by
   the same gate the public lane uses. No key picked is the global scope.
3. Type a **model** (`provider/model` or a combo name) and a prompt, then **Send**
   (or Enter; Shift+Enter adds a line).
4. Keep typing to continue the conversation — each turn carries the earlier ones, so
   context, tool use and multi-step prompts behave as they do for a real client.
   **Stop** ends a stream mid-answer and keeps the text that already arrived.
   **New** clears the conversation.

### What it deliberately does not do

**Nothing is persisted.** The conversation lives in the page's own state: no
`localStorage`, no `sessionStorage`, no database row. Reloading the page starts a
fresh conversation, and a closed tab leaves nothing behind.

The only server-side write a turn makes is the usage row its own attribution
requires (and the budget it spends) — the same accounting the public lane performs
for a real client request. Pick **Global scope** to send a turn that is billed to no
key.

The body is rebuilt deterministically for the same input, so an identical turn
produces identical bytes and can hit the prompt cache.

**Related:** the page was inspired by new-api's Playground, but it is not a parity
port — the mechanism here is a synthetic request through this gateway's own engine.

---

## What was considered and not ported

- **Provider/request plugin marketplace** (300 files, one commit) — an architecture
  overhaul, and it executes user JavaScript, which is an attack surface this gateway
  does not want.
- **Topup, redemption, check-in, subscription (Stripe/ePay), 2FA, email binding,
  wechat** — SaaS multi-tenant features; this gateway has no paid-user model.
- **Task plugins for video vendors (Kling, Hailuo, Vidu, Veo)** — rides on the plugin
  system above.
- **Per-key IP whitelist and per-key self-service quota read** — 24 files, but they
  presume a user model this gateway does not have.
- **Expression-based pricing** — the largest genuine gap (upstream's `billingexpr`
  powers tiered settle, ratio sync and every advanced billing shape), but it needs a
  design decision first: a full expression language or a minimal per-model price map.
  Not ported for want of that decision, not because it is unwanted.
- **OpenAI 12-hour prompt cache billing** — 50 files. Correct to port for an
  OpenAI-heavy deployment; not ported here because the operator does not run OpenAI
  traffic at volume.

---

## Further reading

- [Governance](governance.html) — the budget, plan and per-key limit reference
- [Parity](parity.html) — how 9router-go compares with upstream 9Router
- [Advanced Features](advanced-features.html) — features ported from KeiRouter and
  other projects
- Upstream new-api: [QuantumNous/new-api](https://github.com/QuantumNous/new-api)