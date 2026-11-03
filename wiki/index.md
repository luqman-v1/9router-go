---
title: Home
order: 1
section: Getting Started
since: 1.9.14
---

# 9router-go

**Never stop coding. Save 20-40% tokens with RTK + auto-fallback to FREE & cheap AI models.**

9router-go is a single Go binary that serves both the proxy APIs and the dashboard — no Node.js at runtime. This page is the entry point to the whole wiki.

## Start here

| You want to… | Open |
|---|---|
| Install and run | [Installation](installation.html) |
| Use it from a CLI tool (Claude Code, Codex, Cursor…) | [Client Setup](client-setup.html) |
| See every endpoint | [API](api.html) |
| Get to know the dashboard | [Dashboard](dashboard.html) |
| Connect AI providers | [Providers](providers.html) |
| Build fallback chains | [Combos](combos.html) |
| Cut token spend | [Token Saver](token-saver.html) |
| See what RTK actually saved | [Usage & Analytics](usage-analytics.html) |
| Limit who spends what | [Governance](governance.html) |
| Protect credentials and prompts | [Security](security.html) |
| Understand routing internals | [Architecture](architecture.html) |
| See how data is stored | [Database](database.html) |
| Run it in production | [Operations](operations.html) |
| Configure everything in the dashboard | [Settings](settings.html) |
| Compare with upstream 9Router | [Parity](parity.html) |
| See what was ported, and from where | [Advanced Features](advanced-features.html) |
| Every CLI command and env var | [CLI](cli.html) |
| Contribute | [Contributing](contributing.html) |
| Browse screenshots | [Screenshots](screenshots.html) |

## How it compares with 9Router
[9Router](https://github.com/decolua/9router) is the upstream project this is built against: a Node.js + Next.js gateway. 9router-go is a reimplementation of the same contract in Go, and the difference is not the API — it is what you have to run.

| | 9router-go | 9Router (upstream) |
|---|---|---|
| Runtime | Single Go binary | Node.js + Next.js |
| Dashboard | Embedded Svelte 5 | React 19 |
| Database | SQLite (modernc, pure Go) | SQLite |
| Schema | Same, additive | Same |
| Docker | Single binary | Node |
| Port | `20130` | `20128` |

**What is identical:** request and response schemas for `/v1/*` (Chat Completions, Messages, Embeddings, Audio, Models), dashboard endpoints `/api/*` (Connections, Combos, ProxyPools, Settings, ProviderNodes, Usage, Auth), combo expansion, model fallback, account rotation, and strike-breaker quota handling. Both read the same `~/.9router/db/data.sqlite`, so a connection you add in either dashboard shows up in the other — no import, no export, no migration step.

**What 9router-go adds:** per-custom-provider limits (RPM/TPM/concurrency/quota/expiry), per-API-key rate limiting with a FIFO queue, USD and token budgets with hard cutoff, an AES-GCM credential vault, Argon2id API key hashing, PII and injection guardrails, a semantic response cache, and Prometheus metrics.

**What differs by design:** SSE streams flow through `io.Reader`/`io.Writer` and are never buffered whole, and SQLite reads are batched — one query per page, never N+1.

**What upstream still has and 9router-go does not** — the other half of the comparison, from the repository's [comparison document](https://github.com/luqman-v1/9router-go/blob/main/COMPARISON.md):
- **Public route aliases are incomplete.** 9router-go is root-native (`POST /chat/completions`, not `/v1/chat/completions`); upstream's blanket `/api/v1/*` and `/v1/*` rewrites are not implemented, so a client that depends on them must use the routes the Go server actually exposes.
- **Provider and model coverage is partial.** Route parity does not imply provider parity — executors, aliases, capabilities, cloaking and OAuth behaviour can differ per release, and only a path-specific comparison classifies a feature as parity.
- **No cloud sync and no npm tray.** The dashboard handles local configuration only; upstream's cloud synchronisation and the npm tray launcher are not ported.
- **OIDC/SAML is not a working login flow.** The configuration and metadata routes exist, but the login links target start/callback routes the Go router does not mount — the UI advertises a login path that does not run.
- **Dynamic CLI-tool configuration, dashboard MITM management and the Translator workbench are not ported.** 9router-go has a MITM engine and a console stream, not upstream's management surfaces.
- **A blank database is not production-ready for 9router-go alone.** Bootstrap with a compatible upstream database first; migration-free fresh installation is not claimed.
- **Credentials are not encrypted at rest.** The database is upstream-compatible plaintext SQLite; filesystem controls reduce exposure but do not replace encryption.
- **One active writer is the supported model.** WAL avoids immediate lock errors, but settings merges, daily aggregates and caches are not process-coherent.

Full detail, including what is still unported: [Parity](parity.html) and the repository's [comparison document](https://github.com/luqman-v1/9router-go/blob/main/COMPARISON.md).

## Running in 30 seconds

```bash
9router-go
# Dashboard: http://localhost:20130 (initial password: 123456)
```

Dashboard → Providers → connect **Kiro AI** (≈50 free credits per month) or
**OpenCode Free** (no auth), then point your CLI tool at
`http://localhost:20130/v1`.

## Which version am I on?

```bash
9router-go version
```

Some features on this site arrived in a specific release. Where that matters, the
page says so — for example the [Advanced Features](advanced-features.html) page
lists which release each port landed in, and flags the ones that are still on a
branch rather than in a release you can download.

The version in the sidebar footer is the release these pages document, not the
binary the site was built from. The two differ while experimental work is in
flight: the build being developed is `1.9.15-exp.16`, while the release
`9router-go update` offers is still `1.9.14` — so the footer reads `1.9.14`,
because that is the release whose behaviour the pages describe. A page whose
`since` frontmatter is newer than that release is marked **new** in the sidebar:
the docs for two releases sit at the same URL, and without the mark a reader
takes an experimental flag as the shipped contract.

## What is in the box

| Area | What you get |
|---|---|
| Proxy APIs | OpenAI Chat, Claude Messages, Responses, embeddings, images, video, audio, search, batches |
| Routing | Combos with fallback, round-robin, sticky, capacity, and fusion |
| Token savers | RTK compression plus Caveman, Ponytail, and ADHD output styles |
| Governance | Per-key rate limits, USD/token budgets, plans, model allowlists |
| Security | AES-GCM credential vault, Argon2id key hashing, PII and injection guardrails, SSRF protection |
| Providers | 40+ providers, OAuth for most, custom OpenAI/Anthropic-compatible nodes |
| Operations | Docker, tunnels, self-update, background loops, Prometheus metrics |

## Search

The search box at the top left searches **titles, headings, and endpoints** across
every wiki page. Results are also shareable: the URL becomes `?q=...` (query) and
`#...` (a specific section inside a page), so whoever you send it to opens
exactly the part you meant.
