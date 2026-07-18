# 9router-go

High-performance Go proxy gateway for [9Router](https://github.com/decolua/9router) LLM routing.

> **9Router** is a local AI routing gateway + dashboard. This Go proxy replaces the Next.js `/v1/*` routes for high-throughput LLM traffic, while the [9Router dashboard](https://github.com/decolua/9router) handles management UI (providers, API keys, combos, usage tracking).

## Features

- **32K+ RPS** peak throughput (Go vs Next.js ~500 RPS)
- **42 MB** memory footprint
- SQLite WAL mode (shared with [9Router dashboard](https://github.com/decolua/9router))
- OpenAI & Claude format support
- SSE streaming with real-time translation
- Combo fallback (multi-model retry)
- API key auth middleware
- **Token savers**: RTK input compression, Caveman terse output, Ponytail minimal-code bias
- CGO-free, cross-compile to any platform

## Architecture

```
┌─────────────────┐     ┌──────────────────┐     ┌─────────────────┐
│   CLI Client    │────▶│   Go Proxy       │────▶│  Upstream LLM   │
│  (Claude Code,  │     │  • Auth (SQLite) │     │  (OpenAI, etc.) │
│   Codex, etc.)  │     │  • Model resolve │     │                 │
└─────────────────┘     │  • Translation   │     └─────────────────┘
                        │  • Combo fallback│
                        │  • SSE streaming │
                        └───────┬──────────┘
                                │
┌─────────────────┐     ┌───────▼──────────┐
│   Dashboard     │────▶│  SQLite (WAL)    │
│  [9Router](https://github.com/decolua/9router) │     └──────────────────┘
│  • Providers    │
│  • API Keys     │
│  • Usage        │
└─────────────────┘
```

## Quick Start

```bash
# Build
go build -o 9router-go ./cmd/9router-proxy/

# Run (standalone, no dashboard needed)
PORT=20128 ./9router-go

# Run with token savers enabled
RTK_ENABLED=true CAVEMAN_ENABLED=true ./9router-go

# Health check
curl http://localhost:20128/health
```

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

# Only RTK (default), others off
RTK_ENABLED=true CAVEMAN_ENABLED=false PONYTAIL_ENABLED=false ./9router-go
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

Uses same SQLite DB as [9Router dashboard](https://github.com/decolua/9router) (`~/.9router/db/data.sqlite`) with WAL mode.

Tables: `apiKeys`, `providerConnections`, `providerNodes`, `combos`, `modelAliases`

### Custom DB Location

```bash
# Use custom SQLite path
DB_PATH=/mnt/shared/9router/data.sqlite PORT=20128 ./9router-proxy
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

## API Endpoints

```
POST /v1/chat/completions      # OpenAI format
POST /v1/messages              # Claude format
POST /v1/embeddings            # Embeddings
POST /v1/responses             # Responses API
POST /v1/images/generations    # Image generation
POST /v1/audio/speech          # Text-to-speech (TTS)
POST /v1/audio/transcriptions  # Speech-to-text (STT)
POST /v1/search                 # Web search (provider-selected)
POST /v1/scrape                 # Web fetch (provider-selected)
GET  /v1/models                 # List models
GET  /health                    # Health check
```

## Cross-Compile

```bash
GOOS=linux GOARCH=amd64 go build -o 9router-go-linux ./cmd/9router-proxy/
GOOS=darwin GOARCH=arm64 go build -o 9router-go-mac ./cmd/9router-proxy/
GOOS=windows GOARCH=amd64 go build -o 9router-go.exe ./cmd/9router-proxy/
```

## Test

```bash
go test ./... -v
```

## Benchmark

The Go proxy was benchmarked against the legacy Next.js router using `hey`
against a mock upstream. Headline results:

| Metric | Go Proxy | Legacy Next.js | Speedup |
|---|---|---|---|
| Peak RPS (non-stream) | 5,920 | 505 | **11.7x** |
| Peak RPS (stream) | 5,437 | 429 | **12.6x** |
| Avg latency (c=100) | 11ms | 108ms | **9.8x** |
| Memory (RSS) | 42.5 MB | 270.9 MB | **6.4x lighter** |
| Startup | <100ms | 3–5s | **30–50x** |

Full methodology, per-concurrency tables, and reproduction steps:
see [`benchmark/RESULTS.md`](benchmark/RESULTS.md).

```bash
bash benchmark/run_comparison.sh
```

## TODO / Known Gaps

Tracked items not yet implemented:

- [ ] **Distribution via package managers** — add npm wrapper (`npm i -g 9router-go`,
      prebuilt binaries per platform) and/or Homebrew formula for `brew install`.
      Currently only Docker Hub + GitHub Releases binaries are available.
- [ ] **Docker Hub publish** — `release.yml` pushes to `luqmanv1/9router-go`, but
      GitHub Secrets `DOCKERHUB_USERNAME` / `DOCKERHUB_TOKEN` must be set before
      the first tagged release.
- [ ] **Search & scrape provider dispatch** — `/v1/search` and `/v1/scrape` are
      simple forward endpoints (provider selected via the `model` field, path
      appended to the connection base URL). Unlike the 9Router JS reference, there
      is **no multi-provider dispatch or response normalizer** (Tavily/Exa/Brave,
      firecrawl/jina). Register a connection with the search/fetch base URL + key
      to use them.
- [ ] **Source directory name** — the binary is `9router-go` but the Go package
      dir is still `cmd/9router-proxy/`. Cosmetic; only affects the build path.

## Credits

- [9Router](https://github.com/decolua/9router) — Original Next.js LLM routing gateway + dashboard
