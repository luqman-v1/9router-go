# MITM Proxy — Full TLS Intercept for CLI Tools

> Design for MITM proxy in 9router-go. Matches JS reference at `src/mitm/`.

## Goal

Intercept Antigravity, Codex, Copilot, Cursor, and Kiro CLI traffic at the network level. Redirect tool domains to localhost, terminate TLS via SNI, then forward to 9router proxy.

## Architecture

```
Client CLI → antigravity/codex domain (443)
                  │
            DNS /etc/hosts → 127.0.0.1
                  │
            net.Listen("tcp", ":443")
                  │
            tls.Config{GetCertificate}
            SNI → generate per-domain leaf cert
            signed by local root CA
                  │
            ┌─────┼──────┬──────┬──────┐
      antigravity codex copilot cursor  kiro
         handler  handler handler handler handler
            │        │       │       │      │
            └────────┼───────┼───────┼──────┘
               9router proxy (localhost:20128)
```

## Components

### 1. DNS (`internal/mitm/dns.go`)
- `/etc/hosts` entries via sudo:
  - `cloudcode-pa.googleapis.com` / `daily-cloudcode-pa.googleapis.com` → antigravity
  - `chatgpt.com` / `api.chatgpt.com` → codex
  - `runtime.us-east-1.kiro.dev` → kiro
  - `api.githubcopilot.com` → copilot
  - `api2.cursor.com` / `api.cursor.com` → cursor
- Add on start, remove on stop
- macOS: `echo "..." | sudo tee -a /etc/hosts`
- Linux: same
- Health check: resolve domain → 127.0.0.1

### 2. CA + Certificates (`internal/mitm/cert.go`)
- Generate root CA on first run (`~/.9router/mitm/rootCA.pem`, `rootCA-key.pem`)
- `crypto/x509` + `crypto/rand`
- Install root CA to system trust store:
  - macOS: `security add-trusted-cert -d -r trustRoot`
  - Linux: `update-ca-certificates`
- Per-domain leaf certs generated on-demand in SNI callback
- Cached in `~/.9router/mitm/certs/{domain}.pem`
- Re-generate if expired (leaf TTL: 1 year)

### 3. SNI Server (`internal/mitm/server.go`)
- `net.Listen("tcp", ":443")` → `tls.Config{GetCertificate: sniCallback}`
- Accept both HTTP/1.1 and HTTP/2
- Parse `Host` header → select handler
- Default: passthrough to real upstream
- Per-request: read body → handler → write response

### 4. Handlers (`internal/mitm/handlers/`)

| Handler | Protocol | Upstream | Complexity |
|---------|----------|----------|------------|
| antigravity | Gemini-native | `/v1/chat/completions` | Simple — body passthrough, SSE pipe |
| codex | OpenAI Responses API | `/v1/responses` | Medium — format transform |
| copilot | GitHub Copilot API | `/v1/chat/completions` | Simple — body rewrite |
| cursor | Cursor API | `/v1/chat/completions` | Simple — body rewrite |
| kiro | AWS EventStream | executor | Complex — binary protocol, existing in executor |

All handlers:
1. Parse request body
2. Rewrite model/URL to 9router format
3. POST to `localhost:20128/v1/chat/completions` (or responses)
4. Pipe response (SSE or JSON) back to client

### 5. Manager (`internal/mitm/manager.go`)
- Start: verify CA → setup DNS → start server → track PID
- Stop: remove DNS entries → stop server (cleanup)
- Status: check DNS + server health
- Config via `~/.9router/config.yaml`:
  - `mitm.enabled`, `mitm.port` (default 443)

### 6. CLI Integration
- `cmd/9router-go/main.go`: `--mitm` flag group
- `--mitm enable` — start MITM proxy
- `--mitm disable` — stop MITM proxy
- `--mitm status` — check running state

## Files

| File | Lines | Description |
|------|-------|-------------|
| `internal/mitm/dns.go` | ~100 | Hosts management (add/remove/status) |
| `internal/mitm/cert.go` | ~150 | Root CA + leaf cert generation |
| `internal/mitm/server.go` | ~200 | TLS SNI listener + request dispatch |
| `internal/mitm/manager.go` | ~150 | Start/stop/status lifecycle |
| `internal/mitm/handlers/antigravity.go` | ~80 | Antigravity handler |
| `internal/mitm/handlers/codex.go` | ~80 | Codex handler |
| `internal/mitm/handlers/copilot.go` | ~60 | Copilot handler |
| `internal/mitm/handlers/cursor.go` | ~60 | Cursor handler |
| `internal/mitm/handlers/kiro.go` | ~80 | Kiro handler |
| `internal/mitm/handlers/base.go` | ~60 | Shared (fetchRouter, pipeSSE) |
| `cmd/9router-go/main.go` | +30 | MITM CLI flags |
| Tests | ~200 | Per-handler + integration |
| **Total** | **~1250** | |

## Error Handling

- Port 443 in use → clear error with sudo hint
- DNS add fails (no sudo) → warn, continue
- Handshake error → close connection
- Upstream 9router down → 502 response to client
- Stream error → send SSE error chunk
- Root CA generation fails → clear instructions

## Security

- Root CA private key stored at `~/.9router/mitm/rootCA-key.pem` (0600 perms)
- Only intercepts configured domains — all other traffic passes through
- CA cert only installed on this machine
- No persistent network-level interception

## Testing

- DNS: add/remove entries (requires sudo), verify resolve
- Cert: generate root CA, sign leaf cert, verify chain
- Server: `httptest` with mocked upstream
- Handlers: each with test server confirming body transform + response pipe
- Integration: start → curl intercepted domain → verify response
