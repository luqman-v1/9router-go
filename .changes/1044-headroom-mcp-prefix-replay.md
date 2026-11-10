### The built-in headroom engine over MCP, and a conversation that stops re-folding its seen history

Builds on the in-process headroom engine (#979). Two additions: an MCP surface
for the engine, and a prefix-replay cache that keeps a long agent run from
re-folding already-seen history every turn.

- **`POST /mcp` exposes the built-in engine over MCP** (Model Context
  Protocol, JSON-RPC 2.0), so an MCP-capable client can drive compression
  without the dashboard. The JSON-RPC envelope is hand-rolled rather than
  pulled from an SDK on purpose: the gateway runs with no external
  dependencies, and MCP's wire format is small enough that a focused
  implementation is easier to audit than a dependency. It exposes two tools —
  `headroom_compress` (fold a conversation in-process, fail-open when nothing
  is compressible) and `headroom_stats` (compression counters) — plus a
  `headroom://stats` resource mirroring the stats tool, and answers
  `initialize` / `tools/list` / `tools/call` / `resources/*` with the standard
  JSON-RPC error codes (parse −32700, invalid request −32600, method not found
  −32601, invalid params −32602, internal −32603, unknown resource −32002).
  The endpoint needs no external process, so it works whenever the built-in
  engine is selected.
- **A long conversation no longer re-folds its already-seen history every
  turn.** The in-process engine keeps a session-scoped prefix-replay cache: a
  request whose leading messages match a turn already compressed replays that
  folded prefix and runs the RTK pipeline over only the new tail. The replay is
  sound because the fold is per message and the engine does no cross-message
  work, so a cached prefix's folded form is byte-identical to a full re-fold and
  the spliced output is exactly what a from-scratch fold would produce — and the
  caller's structural gate and byte-gain check still run on it, so a divergence
  fails open. The cache is keyed by session id and bounded (500 sessions, 50
  prefixes per session, 24h TTL) under a mutex, so it cannot grow without bound
  on the small deployment target. It lives at process scope because the gateway
  builds a fresh engine per request in its only production call path
  (`internal/handlers/chat/fallback.go`); a store owned by the engine would be
  rebuilt empty every turn and never learn a prefix.
- **Sessions are ordered for eviction by a monotonic access stamp, not
  wall-clock time.** Sessions touched within one clock tick share an idle
  timestamp, so ordering the LRU eviction on it let Go's random map iteration
  pick the victim — which made the bounded-session test fail intermittently.
  The monotonic stamp is a total order, so the eviction is deterministic
  regardless of timing or map ordering.
- **The engine has no retrieval endpoint, and says so.** It never emits a
  context-compression marker it cannot later expand: the gateway is a stateless
  hop, so storing a blob behind a hash and handing the model a marker to call
  back for would either need a retrieval surface this binary deliberately does
  not run, or leave the model reading a marker with nothing behind it. The
  guards that reject such markers on a compressed response stay active on the
  built-in path too.

Verified: `go build ./...`, `go vet` and `gofmt -l` clean. Unit tests in
`internal/headroom` cover the MCP surface (tools/list, compress, compress
fail-open, stats + `headroom://stats` resource, parse error and the full
JSON-RPC error-code matrix) and the prefix-replay path — a second turn that
reuses the first turn's messages replays the cached prefix, and its output is
compared message-for-message against a from-scratch fold of the same
conversation; a further test pins that an empty session id populates no cache
and no result leaks across sessions. The bounded-session LRU test, flaky before
the access-stamp fix, runs clean across 30 repeated runs. The
`internal/handlers/chat` (141s) and `internal/handlers/media` suites pass
against the rebased base.
