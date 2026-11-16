### Fixed

- The Claude registry now sends every public beta flag the Claude Code CLI gates its features on (2.1.325: 45 gates, 14 were sent). A block behind a flag the gateway never sent was refused by the API — `tool_addition blocks require anthropic-beta: inline-tools-2026-09-15`, `role 'system' must precede an 'assistant' message` — and since the turn stays in the history, every later turn failed too. Client-side `x-cc-internal-*` latches are excluded.
- `tool_addition` and `tool_removal` count as content, so a turn whose only block is one of them is no longer read as empty and dropped before it reaches the API.
- The caller's own `anthropic-beta` flags are merged under one canonical header key; a case variant left beside `Anthropic-Beta` could win at header-application time and silently discard the merged value.
