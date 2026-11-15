### Chat Completions / Messages: respect the caller's `anthropic-version`

Forward the caller's `anthropic-version` request header on `claude` and `anthropic` providers instead of always overwriting it with the registry default `2023-06-01`.

- `handlerutil.WithClientAnthropicVersion` / `GetClientAnthropicVersion` carry the caller's header on the request context (same plumbing used for `anthropic-beta`).
- Captured in `HandleChatCompletions`, `HandleMessages`, and `newResponsesContext` (`/responses`), so every entry lane carries it.
- `tryForwardWithConnection` merges the caller's value into the request-local `StaticHeaders` copy for provider ids `claude` and `anthropic` (never `minimax-code`, which pins its own version), so an overwriting `2023-06-01` is replaced only when the client actually sent one.
- Regression test `TestHandleMessages_AnthropicVersionPassthrough` asserts pin-through (`2024-11-05` stays `2024-11-05`) and default retention (`2023-06-01` when the client sends nothing).

Verification: `go test ./internal/handlers/chat/... ./internal/handlerutil/...` passes.
