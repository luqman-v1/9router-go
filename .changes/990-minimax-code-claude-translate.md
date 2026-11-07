### fix(executor): translate Claude replies for OpenAI clients on minimax-code

`ForwardMinimaxCode` dispatched on `TranslateResp`, which is false for a Chat Completions caller on a Claude-native provider (`chat.go` sets it false for `claudeNative`). The OpenAI client got raw Claude JSON/SSE (no `choices`, no `finish_reason`) and `@ai-sdk/openai-compatible` (opencode) retried with `OpenAI Chat stream ended without finish_reason`.

- `minimax_code.go`: dispatch on `UpstreamClaude` (the fallback flag for `FormatClaude` upstreams), not `TranslateResp`.
- `claude_messages.go`: `handleClaudeMessagesStream`/`NonStream` key the passthrough on `ClaudeClient`, not `TranslateResp`.

Verified live against `mmg/MiniMax-M3.1-Flash` (backup credential): stream now emits `chat.completion.chunk` with `finish_reason:"stop"|"tool_calls"`, non-stream returns `choices[]`, multi-tool stream emits OpenAI `tool_calls`, `/api/models/test` passes, and `/v1/messages` passthrough unchanged. `go vet` + `go test ./internal/proxy/executor/` (1274 passed) + `go test ./internal/handlers/chat/` (1395 passed).
