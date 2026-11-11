### fix(usage): record Claude prompt-cache usage on streamed turns

- The Claude-native passthrough paths read usage with `ParseClaudeUsage`, which only understands a top-level `usage` object. Streamed Claude frames carry it at `message.usage` (`message_start`) and top level (`message_delta`), so every streamed turn logged `cached_tokens = 0` and a prompt count that fell back to the character estimate. The request detail therefore showed 0 cache for every Claude turn even when Anthropic reported a near-complete cache read.
- Added `translator.AccumulateClaudeStreamUsage`, which folds `message_start` (input, cache read, cache creation) and `message_delta` (output) into one running `OpenAIUsage`.
- Wired it into all three streamed paths: `handleClaudeMessagesStream`, the `sseStream` passthrough with a decloaker tool map, and `captureUsageFromSSEChunk` (the `SSECopy` passthrough).
- `AccumulateClaudeStreamUsage` returns a new usage on every recognised event so callers can tell a real update from a no-op by identity, and a `message_delta` no longer clobbers the prompt/cache figures set at `message_start`.
