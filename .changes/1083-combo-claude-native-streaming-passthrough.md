### fix(combo): disable TranslateResponse when upstream is already Claude-native on /messages

- When dispatching streaming combo requests via `handleMessagesComboFallback` on `/v1/messages`, `TranslateResponse` was unconditionally evaluated as `!translator.IsResponsesClient(ctx)` (evaluating to `true`).
- When the selected combo member was already Claude-native (`provider == "claude"`, `"anthropic"`, or `FormatClaude`), setting `TranslateResponse: true` caused 9router to run the Claude SSE stream through `TranslateOpenAIToClaudeStreamSession` as if the upstream were sending OpenAI chat completion chunks.
- The mismatched translator yielded zero parsed chunks, causing `StreamContentPeeker` to fail with `ErrStreamingZeroContent` (`streaming zero content, falling back to next model`) and erroneously skipping healthy Claude upstream members (falling back to secondary models like Codex) with 0% prompt cache hit rates.
- Set `TranslateResponse` to `false` when the combo member's provider is already Claude-native on the messages endpoint.
- Add unit test `TestHandleMessagesCombo_ClaudeStreamingPassthrough`.
