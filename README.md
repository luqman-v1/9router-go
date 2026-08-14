# 9router-go

[![CI](https://github.com/luqman-v1/9router-go/actions/workflows/ci.yml/badge.svg)](https://github.com/luqman-v1/9router-go/actions/workflows/ci.yml)
[![Release](https://github.com/luqman-v1/9router-go/actions/workflows/release.yml/badge.svg)](https://github.com/luqman-v1/9router-go/actions/workflows/release.yml)

High-performance Go proxy gateway for [9Router](https://github.com/decolua/9router) LLM routing.

> **9Router** is a local AI routing gateway + dashboard. This Go proxy replaces the Next.js `/v1/*` routes for high-throughput LLM traffic, while the [9Router dashboard](https://github.com/decolua/9router) handles management UI (providers, API keys, combos, usage tracking).

### Features

- **32K+ RPS** peak throughput (Go vs Next.js ~500 RPS)
- **42 MB** memory footprint
- **SQLite WAL mode** with non-blocking concurrency (shared with [9Router dashboard](https://github.com/decolua/9router))
- **OpenAI, Claude, and Gemini native format support** with bidirectional SSE translation
- **Antigravity Tool Cloaking & Anti-Ban Decoy System**: 21 official IDE decoy tools (`run_command`, `replace_file_content`, etc.) with `_ide` suffix cloaking and protobuf validation safeguards
- **Antigravity Anti-Competitive Prompt Stripping**: strips competitor identity prompts to prevent synthetic 429 quota exhaustion errors
- **Dynamic Egress Proxy Pools & Edge Relays**: round-robin IP rotation via active HTTP/HTTPS/SOCKS5 pools + Vercel/Cloudflare/Deno edge relays (`x-relay-target` / `x-relay-path`)
- **No-Auth Provider Proxy Strategies**: automatic proxy pool routing & rotation for free-tier/public providers (`settings.providerStrategies`)
- **Realtime SSE Usage Stream (`/api/usage/stream`)**: in-memory in-flight request tracker powering live glowing pulse & marching-ants animations on the Next.js Usage Topology graph
- **Snake_case Token Limits (`/v1/models` & `/v1/models/info`)**: exposes `context_length`, `max_completion_tokens`, `max_input_tokens`, and `max_output_tokens`
- **Gemini 3.7 Flash Model Family**: complete alias and capability mapping for Gemini 3.7 Flash models
- **Gemini Multimodal Vision & Audio**: support for base64 inline images, remote HTTP/HTTPS `fileData` URLs, and `input_audio`
- **OpenCode Desktop Fingerprint**: official client headers (`User-Agent: opencode`, `x-opencode-client: desktop`, session/request IDs)
- **Kimchi Dual Authentication**: seamless API key + OAuth token resolution
- **Dedicated High-Performance Executors**: Qoder COSY signing (RSA-2048 + AES-128 + MD5), CodeBuddy CN/INTL streaming, Trae SOLO remote agent, Windsurf gRPC-web
- **Combo strategies**: sticky round-robin, round-robin, fallback, fusion (multi-panel + judge), weight
- **Auto-capability-switch**: floats vision/pdf/audio-capable models to front based on request content
- **Turn & Tool-Calling Stickiness**: locks multi-turn tool calling to the same provider/model to preserve thought signatures
- **Error classification**: text-based error rules + exponential backoff matching Next.js
- **Per-connection model locks**: DB-compatible with Next.js dashboard
- **SSE stall detection**: 6-minute timeout with per-chunk reset
- **Reactive 401 Unauthorized Auto-Refresh**: auto-refreshes OAuth tokens on 401 and retries once before fallback
- **Token savers**: RTK input compression, Caveman terse output (`lite`, `full`, `ultra`, `wenyan-ultra`), Ponytail minimal-code bias (`lite`, `full`, `ultra`), auto-synced from SQLite `settings` table
- **Headroom Lifecycle Proxy**: token compression proxy management & dashboard reverse proxy
- **Live Console Logs**: in-process ring buffer + SSE streaming for dashboard console monitoring
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
# Core Chat & Completion Endpoints
POST /v1/chat/completions      # OpenAI format
POST /v1/messages              # Claude format
POST /v1/messages/count_tokens # Claude token counter
POST /v1/embeddings            # Embeddings
POST /v1/responses             # Responses API
POST /v1/responses/compact     # Compact responses API
POST /api/chat                 # Ollama compatible format

# Media & Multimodal Endpoints
POST /v1/images/generations    # Text-to-image generation
POST /v1/images/understanding  # Image understanding (vision)
POST /v1/videos/generations    # Video generation
POST /v1/videos/edits          # Video edits
POST /v1/videos/extensions     # Video extension
GET  /v1/videos/{id}           # Video status lookup
POST /v1/audio/speech          # Text-to-speech (TTS)
POST /v1/audio/transcriptions  # Speech-to-text (STT)
POST /v1/audio/music           # Music generation
GET  /v1/audio/voices          # TTS voices list

# Search, Scrape & Web Fetch
POST /v1/search                # Web search (provider-selected)
POST /v1/scrape                # Web scrape
POST /v1/web/fetch             # Web URL extraction (Jina Reader / Firecrawl)

# Models & Token Limits
GET  /v1/models                # List models (with context_length, token limits)
GET  /v1/models/info           # Model capability & limits metadata
GET  /v1/models/{kind}         # Models filtered by kind (e.g. tts, image)

# Usage & Realtime SSE Stream
GET  /api/usage/stream         # Live SSE in-flight request tracking & topology animation
GET  /api/usage/stats          # Realtime usage stats & active concurrency

# OAuth & Authentication
POST /v1/oauth/authorize       # OAuth authorize
POST /v1/oauth/refresh         # OAuth refresh

# System & Monitoring
GET  /health                   # Health check
GET  /api/version              # Proxy version & update check
GET  /api/translator/stream    # Dashboard live console log SSE stream
```

## Docker

### Pull from Docker Hub

```bash
docker pull luqmenul/9router-go:latest
```

### Docker Compose (`docker-compose.yml`)

#### With Outbound Egress Proxy (Microwarp SOCKS5)

```yaml
services:
  microwarp:
    image: ghcr.io/ccbkkb/microwarp:latest
    container_name: microwarp
    restart: always
    ports:
      - "1080:1080"
    cap_add:
      - NET_ADMIN
      - SYS_MODULE
    sysctls:
      - net.ipv4.conf.all.src_valid_mark=1
    volumes:
      - ./warp:/etc/wireguard

  9router-go:
    image: luqmenul/9router-go:latest
    container_name: 9router-go
    ports:
      - "20130:20128"
    environment:
      - PORT=20128
      - DATA_DIR=/data
      - RTK_ENABLED=true
      - CAVEMAN_ENABLED=false
      - PONYTAIL_ENABLED=true
      - HTTP_PROXY=socks5://microwarp:1080
      - HTTPS_PROXY=socks5://microwarp:1080
    volumes:
      - ./data:/data
    depends_on:
      - microwarp
    restart: unless-stopped
```

#### Standalone Deployment

```yaml
services:
  9router-go:
    image: luqmenul/9router-go:latest
    container_name: 9router-go
    ports:
      - "20128:20128"
    environment:
      - PORT=20128
      - DATA_DIR=/data
      - RTK_ENABLED=true
      - CAVEMAN_ENABLED=false
      - PONYTAIL_ENABLED=true
    volumes:
      - ./data:/data
    restart: unless-stopped
```

```bash
# Start container
docker compose up -d
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

## Roadmap

See [`ROADMAP.md`](ROADMAP.md) for planned future features, cost-aware model routing, semantic caching, and alerting proposals.

## Credits

- [9Router](https://github.com/decolua/9router) — Original Next.js LLM routing gateway + dashboard
