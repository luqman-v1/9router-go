### feat(chat): stream usage to clients so auto-compact can fire

Clients decide auto-compact from the usage each response reports against the
context window they believe the model has. On the translated paths the usage
was always zero, so the client's context meter never moved and auto-compact
never fired — sessions grew past the upstream's real limit and errored there.

Two defects fixed. `TranslateClaudeToOpenAI` never asked the upstream for
streaming usage (`stream_options.include_usage` — a field the Anthropic
dialect has no way to request), so most Chat Completions upstreams sent no
usage chunk at all; and even when an upstream reported usage, OpenAI streams
deliver it on a trailing zero-choice chunk **after** the finish chunk, while
the translator emitted the terminal `message_delta` at finish — the real
numbers landed after the client had already been told zeros.

The `/v1/messages` and `/v1/responses` handlers now inject
`stream_options.include_usage` toward Chat Completions upstreams (Claude-format
combo members strip the key again, and native Responses upstreams are
skipped), and the Claude translator defers the terminal `message_delta` until
the usage chunk arrives — flushing it at `[DONE]`, via `EnsureStreamClosed`,
or on the shutdown path when it never comes.

Two usage corrections ride along: the client-facing `input_tokens` now
excludes the cached subset (OpenAI's `prompt_tokens` is cache-inclusive,
Anthropic's `input_tokens` is not, and Claude Code sums both, so the old
mapping double-counted), and when the upstream ignores `include_usage`
entirely the terminal falls back to a seeded body-size estimate so the meter
still moves. Estimates never enter the usage analytics, and requests carrying
inline base64 media are not estimated.
