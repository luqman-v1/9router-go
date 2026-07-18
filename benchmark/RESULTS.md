# 9Router Benchmark: Go Proxy vs Legacy Next.js

> **Date:** 2026-07-18
> **Machine:** macOS Darwin 25.5.0, Apple Silicon
> **Go version:** 1.26.0
> **Node version:** 22.x (Next.js 16.2.10)
> **Tool:** `hey` HTTP benchmarking tool
> **Upstream:** Mock OpenAI-compatible server (5ms simulated latency)
> **DB:** SQLite (WAL mode), shared between Go and Next.js

## Headline Results

| Metric | Go Proxy | Legacy Next.js | Speedup |
|---|---|---|---|
| **Peak RPS (non-stream)** | 5,920 | 505 | **11.7x** |
| **Peak RPS (stream)** | 5,437 | 429 | **12.6x** |
| **Avg latency c=100** | 11ms | 108ms | **9.8x** |
| **Memory (RSS)** | 42.5 MB | 270.9 MB | **6.4x lighter** |
| **Binary size** | 16 MB | ~200 MB (node_modules) | **12.5x** |
| **Startup time** | <100ms | ~3-5s | **30-50x** |

## Non-Streaming (`POST /v1/chat/completions`, `stream: false`)

| c | Go RPS | Go Avg | JS RPS | JS Avg | Speedup |
|---|---|---|---|---|---|
| 1 | 150 | 6.7ms | 104 | 9.6ms | 1.4x |
| 5 | 811 | 6.1ms | 405 | 12.2ms | 2.0x |
| 10 | 1,590 | 6.1ms | 413 | 23.3ms | 3.8x |
| 25 | 3,192 | 6.6ms | 461 | 43.2ms | 6.9x |
| 50 | 3,541 | 9.9ms | 499 | 61.2ms | 7.0x |
| 100 | 5,920 | 11.2ms | 505 | 107.8ms | 11.7x |

## Streaming (`POST /v1/chat/completions`, `stream: true`)

| c | Go RPS | Go Avg | JS RPS | JS Avg | Speedup |
|---|---|---|---|---|---|
| 1 | 145 | 6.9ms | 105 | 9.5ms | 1.3x |
| 5 | 748 | 6.7ms | 417 | 11.8ms | 1.7x |
| 10 | 1,492 | 6.7ms | 438 | 22.0ms | 3.4x |
| 25 | 3,500 | 6.5ms | 467 | 42.2ms | 7.4x |
| 50 | 3,521 | 9.8ms | 424 | 71.3ms | 8.3x |
| 100 | 5,437 | 14.2ms | 429 | 133.3ms | 12.6x |

## Memory Usage

| Component | RSS |
|---|---|
| Go Proxy | **42.5 MB** |
| Next.js (Node.js) | **270.9 MB** |
| Ratio | **6.4x** (Next.js uses 6.4x more RAM) |

## Key Observations

1. **Go proxy scales linearly** with concurrency — RPS increases as concurrency grows until mock upstream saturation.
2. **Next.js caps at ~500 RPS** regardless of concurrency — Node.js single-threaded event loop bottleneck.
3. **Latency stays flat for Go** (6-14ms) while Next.js climbs from 10ms to 133ms.
4. **Memory efficiency**: Go uses 42MB vs Next.js 271MB — 6.4x less RAM.
5. **Startup**: Go proxy starts in <100ms, Next.js takes 3-5 seconds.

## Notes

- Mock upstream has 5ms simulated latency to be realistic.
- Both use same SQLite DB (WAL mode) for auth + model resolution.
- Go proxy reads from SQLite per request (same as Next.js).
- Go proxy: 82 unit tests passing, 7 packages.
- Binary size: 16MB CGO-free, cross-compiles to any platform.

## How to Reproduce

```bash
cd go-proxy

# Run comparison benchmark (includes mock upstream, temp DB, both servers)
bash benchmark/run_comparison.sh

# Or manually:
go run benchmark/mock_upstream.go &                    # mock upstream on :20199
go build -o /tmp/go-proxy ./cmd/9router-go/        # build Go proxy
DATA_DIR=<db_dir> PORT=20131 /tmp/go-proxy &          # start Go proxy
PORT=20132 NODE_ENV=production node ../.next/standalone/server.js &  # start Next.js

# Benchmark Go proxy
hey -n 200 -c 10 -m POST \
  -H "Authorization: Bearer sk-benchmark-test-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"mock/mock-model","messages":[{"role":"user","content":"Hello"}],"stream":false,"max_tokens":10}' \
  http://127.0.0.1:20131/v1/chat/completions

# Benchmark Next.js
hey -n 200 -c 10 -m POST \
  -H "Authorization: Bearer sk-benchmark-test-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"mock/mock-model","messages":[{"role":"user","content":"Hello"}],"stream":false,"max_tokens":10}' \
  http://127.0.0.1:20132/v1/chat/completions
```
