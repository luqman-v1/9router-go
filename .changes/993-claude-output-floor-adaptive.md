### fix(translator): raise an output floor for claude-adaptive thinking so a small max_tokens is not eaten whole by reasoning

A model whose thinking can never be switched off — MiniMax-M3.1-Flash
on the minimax-code credits lane (`claude-adaptive`, `ThinkingCanDisable:
false`), and the permanently-adaptive Fable lane — spends its reasoning
inside `max_tokens`. When a client sent a small `max_tokens` (OMP's
compaction snapshot, at a small context window), the whole budget went
to reasoning, the answer never started, and the client read
`finish_reason: "length"` with no text — OMP retried three times and
gave up, so auto-compact appeared not to work even at a small context.

The claude-budget path already raised `max_tokens` to `budget + 1024`
headroom; the adaptive path set the effort and returned, leaving the
client's `max_tokens` untouched. `writeClaudeAdaptive` now ensures the
same level-sized output floor (level budget + headroom, capped at the
model's own output limit) across `max_tokens` / `max_completion_tokens` /
`max_output_tokens`, so an explicit small answer budget is no longer
spent entirely inside reasoning.

Verified: `go test ./internal/thinking/... ./internal/proxy/executor/...`
passing, including a new table-driven regression test pinning the floor.

### fix(clientconfig): OMP writes a context window for every connect so its auto-compact trigger always has a boundary

The OMP adapter only emitted the per-model entry when the gateway
resolved the model's capacity. When it resolved nothing — a discovered or
custom model such as `omv/mmg/MiniMax-M3.1-Flash` — OMP got a provider
entry with no `contextWindow`, its context percentage stayed empty, and
auto-compact never fired, even as the conversation approached the real
limit.

Like crush and pi, the OMP adapter now always writes the entry, and
`piLimitsFor` fills `contextWindow: 128000` / `maxTokens: 16384` defaults for
whatever the gateway did not resolve. A model the gateway did resolve
keeps its real values.

The previous behavior was pinned by `TestOmpWithoutLimitsKeepsPreviousShape`;
it now expects the default entry, renamed
`TestOmpWithoutLimitsFallsBackToDefaults`.

Verified: `go test ./internal/clientconfig/...` passing.
