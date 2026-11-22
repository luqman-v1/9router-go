### agy-acp: warm worker pool cuts per-turn latency

- `agy-acp` previously spawned a fresh `agy acp` subprocess per turn and paid
  process start + `initialize` + `session/new` before the first prompt byte —
  measured ~1.0–1.7 s on agy 1.5.14 (Windows, warm FS cache). The new warm
  pool (`internal/acp/agy/pool.go`) keeps one pre-initialized worker per
  account config alive between turns and pre-creates the next session in the
  background after each turn, so a steady-state text turn skips spawn,
  handshake and `session/new` entirely. Measured `session/new` drops from
  ~1.3 s cold to ~10–80 ms warm; the refill runs off the request path.
- Bounds (§4.G): max 4 workers process-wide, 5-minute idle TTL, 30 s bounded
  handshake; stale workers are killed and their credential files cleared.
  `AGY_ACP_POOL=0` restores the exact old spawn-per-turn behavior.
- Credential rotation replaces the worker instead of reusing a cached stale
  token; tool turns always create a fresh session (the MCP bridge URL is
  per-turn); agent faults/banners discard the worker so failover still works.
- Client disconnect now sends `session/cancel` (new `Client.Notification`)
  and keeps the healthy worker pooled instead of killing it; a cancelled
  turn never reports success from a racing prompt result.
- `Client.Close` waits for `cmd.Wait` so discarded workers fully exit before
  their directory is cleared (Windows holds the exe image until exit).
- Verified: `go test ./internal/acp/agy/ ./internal/proxy/executor/` (incl.
  new `pool_test.go`: reuse, rotation, kill-switch, cancellation), `-race`
  clean, `golangci-lint --new-from-rev=HEAD` 0 issues. Real-CLI probe: warm
  turn ≈ 380–390 ms vs cold ≈ 370–480 ms on the auth-banner path; the win is
  the removed ~1.3 s `session/new` on authenticated turns (measured directly
  against agy 1.5.14: cold session/new 1.3–1.7 s, warm refill 13–80 ms).
- agy 1.5.x sometimes ends a turn with only a "checking model/config in the
  background" status line (also seen localized: "Cek model dan konfigurasi
  sedang berjalan di latar belakang...") and no answer, leaving the client
  with a silent stream. `streamPrompt` now holds that line, then re-prompts
  on the same session with an explicit nudge (up to 2 times) to finish the
  answer in the current turn. If agy keeps deferring, the turn returns
  `ErrStall` (mapped to 502) instead of an empty 200, so the router can fail
  over. A gateway preamble in `BuildPrompt` asks agy not to defer in the
  first place. Regression tests cover both the nudge-then-answer and the
  repeated-deferral paths, in English and Indonesian.

### shutdown cleanup & context lifetime

- `DefaultPool().Close()` is now wired into the gateway's Fx `OnStop` hook
  (`internal/app/server.go`), after HTTP requests drain. It kills idle workers
  and clears their on-disk OAuth token files; previously the OS reaped the
  child process on exit but the token files were left behind.
- `Pool.spawn` now honours the caller's context: it aborts before spawning a
  worker for an already-cancelled turn and bounds `initialize` by the caller's
  deadline as well as the 30 s handshake TTL. The worker process itself still
  runs under a background context so it can outlive the turn and be pooled —
  `exec.CommandContext` would kill it when the request context ended.
- Regression tests: `TestSpawnRespectsCancelledCallerContext`,
  `TestPoolCloseClearsCredentialsAndWorker`, and an updated
  `TestPoolCancelledTurnKeepsWorker` (cancels mid-prompt on an already-pooled
  worker, which is the scenario the contract actually covers).
