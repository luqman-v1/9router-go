# 9router-go

[![CI](https://github.com/luqman-v1/9router-go/actions/workflows/ci.yml/badge.svg)](https://github.com/luqman-v1/9router-go/actions/workflows/ci.yml)
[![Release](https://github.com/luqman-v1/9router-go/actions/workflows/release.yml/badge.svg)](https://github.com/luqman-v1/9router-go/actions/workflows/release.yml)

High-performance Go proxy gateway for [9Router](https://github.com/decolua/9router) LLM routing.

> **9Router** is a local AI routing gateway + dashboard. This Go proxy replaces the Next.js `/v1/*` routes for high-throughput LLM traffic, while the [9Router dashboard](https://github.com/decolua/9router) handles management UI (providers, API keys, combos, usage tracking).

## Features

- **32K+ RPS** peak throughput (Go vs Next.js ~500 RPS)
- **42 MB** memory footprint
- **SQLite WAL mode** (shared with [9Router dashboard](https://github.com/decolua/9router))
- **OpenAI & Claude format support** + real-time SSE translation
- **Dynamic Egress Proxy Pools**: round-robin IP rotation via active HTTP/HTTPS/SOCKS5 proxy pools
- **Reactive 401 Unauthorized Auto-Refresh**: auto-refreshes OAuth tokens on 401 and retries once before fallback
- **Qoder COSY Signing Executor**: full RSA-2048 + AES-128-CBC + MD5 signed header payload support
- **Combo strategies**: sticky round-robin, round-robin, fallback, fusion (multi-panel + judge)
- **Auto-capability-switch**: floats vision/pdf-capable models to front based on request content
- **Error classification**: text-based error rules + exponential backoff (matching Next.js)
- **Per-connection model locks**: DB-compatible with Next.js dashboard
- **SSE stall detection**: 6-minute timeout with per-chunk reset
- **Retry-after tracking**: earliest retry time across combo models
- **Fusion**: parallel panel fan-out + quorum-grace collection + anonymized judge synthesis
- **Health tracking**: per-model consecutive error counter
- API key auth middleware
- **Token savers**: RTK input compression, Caveman terse output (`lite`, `full`, `ultra`, `wenyan-ultra`), Ponytail minimal-code bias (`lite`, `full`, `ultra`), auto-synced from SQLite `settings` table
- Gemini-native provider support (antigravity)
- CGO-free, cross-compile to any platform

## Architecture

```
┌─────────────────┐     ┌──────────────────────┐     ┌─────────────────┐
│   CLI Client    │────▶│    Go Proxy           │────▶│  Upstream LLM   │
│  (Claude Code,  │     │                       │     │  (OpenAI, etc.) │
│   Codex, etc.)  │     │  • Auth (SQLite)      │     └─────────────────┘
│                 │     │  • Model resolution   │
│                 │     │  • Combo strategies   │
│                 │     │    - sticky           │
│                 │     │    - round-robin      │
│                 │     │    - fallback         │
│                 │     │    - fusion           │
│                 │     │  • Auto-capability    │
│                 │     │  • SSE streaming      │
│                 │     │  • Stall detection    │
│                 │     │  • Error klasifikasi  │
│                 │     │  • Translation        │
│                 │     └───────┬──────────┘
│                             │
└─────────────────────┐     ┌─▼──────────────────┐
  │   Dashboard     │────▶│  SQLite (WAL)    │
  │  [9Router]      │     └────────────────────┘
  │  • Providers    │
  │  • API Keys     │
  │  • Usage        │
  └─────────────────┘
```

### Request Flow

```
Client → Auth → resolveModel() → [Combo?]
    │                              │
    │ Yes                          │ No
    ▼                              ▼
Combo Handler                 Single Model
    │                              │
    ├─ sticky/round-robin          │
    ├─ fallback                    │
    └─ fusion (parallel panel)     │
    │                              │
    ▼                              ▼
detectRequiredCapabilities()
    │
    ▼
tryForwardWithConnection()
    │
    ├─ Success → unlockModel + logUsage
    └─ Error  → classifyError() → lockConnectionModel()
                                       │
                                  Fallback model?
                                       │ Yes → retry next model
                                       │ No  → error response
```

See [ARCHITECTURE.md](ARCHITECTURE.md) for detailed flow diagrams (combo, fusion, error classification, locking, SSE stall, etc.).

## Quick Start

```bash
# Build
go build -o 9router-go ./cmd/9router-go/

# Run (standalone, no dashboard needed)
PORT=20128 ./9router-go

# Health check
curl http://localhost:20128/health
```

## Combo Strategies

Combo models support multiple routing strategies, configurable per combo:

| Strategy | Description |
|----------|-------------|
| **fallback** | Try models in order, skip on error (default) |
| **round-robin** | Rotate starting index per request |
| **sticky** | Round-robin with consecutive-use pinning; rotate after `stickyLimit` requests |
| **fusion** | Fire all panel models in parallel → collect with quorum grace → judge synthesizes final answer |

All strategies support **auto-capability-switch**: if the request body contains images or PDFs, capable models (OpenAI, Anthropic, Gemini, etc.) are floated to the front automatically.

### Fusion

Fusion runs multiple models as a panel in parallel:

1. **Fan-out**: Send request to all panel models simultaneously (non-streaming)
2. **CollectPanel**: Wait for quorum (`minPanel=2`), apply `stragglerGraceMs=8s`, hard timeout at `panelHardTimeoutMs=90s`
3. **Degrade gracefully**: If 0 answers → 503; if 1 answer → answer directly
4. **Judge synthesis**: Build anonymized panel responses → send to judge model → final answer streamed to client

## Error Classification

Errors are classified using the same rule system as Next.js:

| Rule | Type | Action |
|------|------|--------|
| `"no credentials"` | Text | Cooldown 120s |
| `"request not allowed"` | Text | Cooldown 5s |
| `"rate limit"` | Text | Exponential backoff |
| `"too many requests"` | Text | Exponential backoff |
| `"quota exceeded"` | Text | Exponential backoff |
| `"capacity"` / `"overloaded"` | Text | Exponential backoff |
| 401 / 402 / 403 / 404 | Status | Cooldown 120s |
| 429 | Status | Exponential backoff |
| Default (unmatched) | — | Cooldown 30s |

**Exponential backoff**: 2s base, doubled per level, max 5 minutes, 15 levels max.
Backoff level is tracked per-connection in `providerConnections.data.backoffLevel` (DB-compatible with Next.js dashboard).

## Model Locking

**Per-connection model locks** — stored as `modelLock_<model>` fields in `providerConnections.data` JSON blob.
Same format as Next.js, readable by the shared dashboard.

- Failed connection → `LockConnectionModel(id, model, duration)` → `data.modelLock_gpt-4 = "ISO timestamp"`
- Successful request → `UnlockConnectionModel(id, model)` → `data.modelLock_gpt-4 = null`, `backoffLevel = 0`
- Connection selection → skips connections with active model lock

## SSE Stall Detection

Each SSE stream is wrapped with a `StallReader` (6-minute timeout by default).

- Timer resets on each received chunk
- If timer fires (no data for 6 minutes) → underlying connection is closed → `Read` unblocks with error → stream terminated
- No goroutine leak on clean stream close (timer stopped)

## Token Savers

Reduce token usage on routed LLM traffic. Each saver is independently toggleable
via CLI flag or environment variable (CLI flag overrides env).

| Saver | CLI flag | Env var | Default | Effect |
|-------|----------|---------|---------|--------|
| RTK | `--rtk` | `RTK_ENABLED` | **on** | Content-aware compression of tool/tool_result messages (git diff, logs, grep, tree) |
| Caveman | `--caveman` | `CAVEMAN_ENABLED` | off | Injects terse-output system prompt (~65% fewer output tokens) |
| Ponytail | `--ponytail` | `PONYTAIL_ENABLED` | off | Injects lazy-senior-dev prompt biasing minimal code |

```bash
# All savers on
./9router-go --rtk --caveman --ponytail
```

> RTK is on by default. Disable with `RTK_ENABLED=false` or `--rtk=false`.

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `20128` | Server port |
| `DATA_DIR` | `~/.9router/` | Data directory (DB, JWT secret) |
| `DB_PATH` | `DATA_DIR/db/data.sqlite` | Custom SQLite DB path (overrides DATA_DIR) |
| `LOG_FILE` | stderr | Log output file (defaults to stderr when unset) |
| `RTK_ENABLED` | `true` | Enable RTK input compression |
| `CAVEMAN_ENABLED` | `false` | Enable Caveman terse output style |
| `PONYTAIL_ENABLED` | `false` | Enable Ponytail minimal-code bias |

## Database

Uses the same SQLite DB as [9Router dashboard](https://github.com/decolua/9router) (`~/.9router/db/data.sqlite`) with WAL mode.

**Tables:** `apiKeys`, `providerConnections`, `providerNodes`, `combos`, `kv`, `settings`, `usageHistory`, `usageDaily`, `requestDetails`, `proxyPools`, `_meta`

See [DATABASE.md](DATABASE.md) for full schema documentation, JSON blob structure, and Go vs Next.js differences.

### Custom DB Location

```bash
# Use custom SQLite path
DB_PATH=/mnt/shared/9router/data.sqlite PORT=20128 ./9router-go
```

## API Endpoints

```
POST /v1/chat/completions      # OpenAI format
POST /v1/messages              # Claude format
POST /v1/embeddings            # Embeddings
POST /v1/responses             # Responses API
POST /v1/images/generations    # Image generation
POST /v1/video/generations     # Video generation
POST /v1/video/extend          # Video extend
POST /v1/video/edit            # Video edit
POST /v1/audio/speech          # Text-to-speech (TTS)
POST /v1/audio/transcriptions  # Speech-to-text (STT)
POST /v1/web/fetch             # Web URL extraction (Jina Reader / Firecrawl)
POST /v1/search                # Web search (provider-selected)
POST /v1/scrape                # Web fetch (provider-selected)
GET  /v1/models                # List models
POST /v1/oauth/authorize       # OAuth authorize
POST /v1/oauth/refresh         # OAuth refresh
GET  /health                   # Health check
```

## Docker

```bash
# Build & run
docker compose up -d

# Or manually
docker build -t 9router-go .
docker run -d -p 20128:20128 -v 9router-data:/data --name 9router-go 9router-go

# With custom DB path
docker run -d -p 20128:20128 \
  -v /path/to/your/data.sqlite:/db/data.sqlite \
  -e DB_PATH=/db/data.sqlite \
  --name 9router-go 9router-go
```

## Cross-Compile

```bash
GOOS=linux GOARCH=amd64 go build -o 9router-go-linux ./cmd/9router-go/
GOOS=darwin GOARCH=arm64 go build -o 9router-go-mac ./cmd/9router-go/
GOOS=windows GOARCH=amd64 go build -o 9router-go.exe ./cmd/9router-go/
```

## Test

```bash
go test ./... -v
```

All **655 tests** pass (with `-count=1` to bypass test caching).

## Benchmark

Run the native self-contained Go benchmark runner (zero external dependencies):

```bash
go run ./benchmark/runner.go
```

| Metric | Go Proxy | Legacy Next.js | Speedup |
|---|---|---|---|
| Peak RPS (non-stream) | 5,920 (up to 13,216 native) | 505 | **11.7x – 26x** |
| Peak RPS (stream) | 5,437 | 429 | **12.6x** |
| Avg latency (c=100) | 6.0ms | 108ms | **18x** |
| Memory (RSS) | 42.5 MB | 270.9 MB | **6.4x lighter** |
| Startup | <100ms | 3–5s | **30–50x** |

See [`benchmark/RESULTS.md`](benchmark/RESULTS.md) for full methodology and reproduction steps.

## Credits

- [9Router](https://github.com/decolua/9router) — Original Next.js LLM routing gateway + dashboard
