# 9router-go

High-performance Go proxy gateway for 9Router LLM routing.

## Features

- **32K+ RPS** peak throughput (Go vs Next.js ~500 RPS)
- **42 MB** memory footprint
- SQLite WAL mode (shared with Next.js dashboard)
- OpenAI & Claude format support
- SSE streaming with real-time translation
- Combo fallback (multi-model retry)
- API key auth middleware
- CGO-free, cross-compile to any platform

## Quick Start

```bash
# Build
go build -o 9router-proxy ./cmd/9router-proxy/

# Run
PORT=20128 ./9router-proxy

# Health check
curl http://localhost:20128/health
```

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `20128` | Server port |
| `DATA_DIR` | `~/.9router/` | SQLite DB directory |

## Database

Uses same SQLite DB as Next.js dashboard (`~/.9router/db/data.sqlite`) with WAL mode.

Tables: `apiKeys`, `providerConnections`, `providerNodes`, `combos`, `modelAliases`

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
