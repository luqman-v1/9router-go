### feat(connect): write each client's real context window so auto-compact can fire

Auto-compact has two inputs: the usage each response reports, and the context
window the client believes the model has. The first is handled by the streaming
usage fix; the second never came through the API at all — it lives in each
client's own config, and the connect flow either wrote a hardcoded guess
(crush and pi: 128000) or nothing at all (claude, codex, opencode, omp). The
gateway already knows the real numbers — `/v1/models` publishes
`context_length` and `max_completion_tokens` per model from declared custom
limits, the capability tables and combo seat minimums — so the connect handler
now resolves them through that same chain and hands them to the adapters.

Claude Code gets `CLAUDE_CODE_AUTO_COMPACT_WINDOW` (the documented way to
state a proxied model's window; the narrowest across the model and all tiers,
clamped to the CLI's 100k–1M bounds). Codex gets `model_context_window`,
`model_auto_compact_token_limit` at 90% of the window and
`model_max_output_tokens` as TOML integers — and reset removes the three,
because they are global and would otherwise describe this gateway's window to
whatever provider the user configures next. OpenCode gets
`limit: {context, output}` on the model entry, which is the only thing that
gives a model unknown to models.dev a context meter and auto-compact at all.
Crush and pi prefer the resolved values over their old hardcodes, field by
field. oh-my-pi gets a `models` entry with `contextWindow`/`maxTokens` beside
its proxy discovery. A model the catalog does not know stays absent, and every
adapter then writes exactly what it wrote before.

Two enabling fixes in the write path. The TOML patcher only ever wrote quoted
strings; a scalar `ValueJSON` (integers and booleans) is now written raw on
the append and replace paths, with everything else still refused. And a plan
whose result was an empty document was silently discarded — for TOML that is a
legitimate outcome, so a config holding nothing but this gateway's keys could
never be reset; the file just stayed. The no-op now requires the start to have
been empty too.
