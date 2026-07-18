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
go build -o 9router-proxy ./cmd/9router-proxy/

# Run (standalone, no dashboard needed)
PORT=20128 ./9router-proxy

# Health check
curl http://localhost:20128/health
```

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `20128` | Server port |
| `DATA_DIR` | `~/.9router/` | Data directory (DB, JWT secret) |
| `DB_PATH` | `DATA_DIR/db/data.sqlite` | Custom SQLite DB path (overrides DATA_DIR) |

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
docker build -t 9router-proxy .
docker run -d -p 20128:20128 -v 9router-data:/data --name 9router-proxy 9router-proxy

# With custom DB path
docker run -d -p 20128:20128 \
  -v /path/to/your/data.sqlite:/db/data.sqlite \
  -e DB_PATH=/db/data.sqlite \
  --name 9router-proxy 9router-proxy
```

## API Endpoints

```
POST /v1/chat/completions  # OpenAI format
POST /v1/messages           # Claude format
GET  /v1/models             # List models
GET  /health                # Health check
```

## Cross-Compile

```bash
GOOS=linux GOARCH=amd64 go build -o 9router-proxy-linux ./cmd/9router-proxy/
GOOS=darwin GOARCH=arm64 go build -o 9router-proxy-mac ./cmd/9router-proxy/
GOOS=windows GOARCH=amd64 go build -o 9router-proxy.exe ./cmd/9router-proxy/
```

## Test

```bash
go test ./... -v
```

## Benchmark

```bash
bash benchmark/run_comparison.sh
```

## Credits

- [9Router](https://github.com/decolua/9router) — Original Next.js LLM routing gateway + dashboard
