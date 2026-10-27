### Fix Codex and Responses API streaming prompt cache token accounting

The Codex provider recorded `cached_tokens: 0` in `usage_history` for streaming
requests, causing the dashboard usage table to report 0% cache hit rate even
when upstream prompt caching was active and returning `cached_tokens`.

- Updated `captureUsageFromSSEChunk` to parse Responses API terminal events
  (`response.completed`, `response.done`) via `ParseResponsesUsage` when standard
  Chat `ParseResponseUsage` returns nil.
- Preserved usage in `CodexStreamState` across `response.completed` and `response.done`
  events in `ProcessCodexEvent`, and propagated it in `writeSSEFinish` instead of
  hardcoding a character-count fallback with zero prompt and cached tokens.
- Propagated accumulated `RawUsage` in `StreamChatToResponses` back to the request
  context so `/v1/responses` streams on Chat Completions upstreams record accurate
  prompt, cached, and reasoning token metrics.
- Extended `ParseResponsesUsage` to recognize both `input_tokens_details` and
  `prompt_tokens_details` cached token envelopes.
