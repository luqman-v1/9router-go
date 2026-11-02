---
title: Home
order: 1
section: Getting Started
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
