### Added

- `agy-acp` provider: independent provider that drives the locally installed `agy` CLI over the Agent Client Protocol (JSON-RPC NDJSON over stdio) instead of calling Google's HTTP API. Registered with its own executor, alias `agcpy`, model catalogue, capabilities, and a ToS risk notice; no cross-provider aliasing with `antigravity`.
- Multi-account support: each `agy-acp` connection gets an isolated 0700 account dir and writes `agy`'s file-backed OAuth token (0600) at turn time, cleared afterwards. Concurrency is bounded by a capped worker manager (queue + timeout, no unbounded growth). Failover stays off by default.
- Shared credentials with `antigravity`: `providerSpecificData.sourceConnection` references an existing Antigravity connection so the same OAuth token is refreshed and reused; `accessToken`/`refreshToken` on the connection itself support the copy/manual-import mode. Both modes are resolved and refreshed by the chat handler without exposing tokens to logs.
- Loopback HTTP MCP tool bridge: OpenAI `tools` are exposed to agy on `127.0.0.1:<ephemeral>` (no `nc` dependency), tool calls are emitted as OpenAI `tool_calls` deltas, and prior tool results are seeded for multi-turn calls. Permission auto-approval is limited to `accept-edits`/`auto`/`bypass` modes.

### Verification

- `go test ./internal/acp/agy/...` passes with a fake ACP subprocess (handshake, streaming, credential file mode 0600, model mapping, error classification).
- Provider artwork `web/public/providers/agy-acp.png` added so the dashboard's provider-artwork parity test passes for the new registry entry.
- Lint clean: checked pipe/credential `Close` return values, wrapped sentinel errors with `%w`, and used `net.ListenConfig.Listen` with context for the MCP loopback listener.
- `go build ./...` passes.
