### fix(combo): treat reasoning streams as non-empty

- In `internal/proxy/stream_peeker.go`, treat reasoning/thinking streaming chunks as non-empty content in `StreamContentPeeker` (porting upstream `decolua/9router` PR #5331).
- Recognizes OpenAI `reasoning_content` and `reasoning`, Anthropic `thinking_delta`, and Responses API `response.reasoning_text.delta` events.
- Prevents premature combo failover when reasoning models (e.g. DeepSeek-R1, o1, o3, Kimi) stream reasoning tokens before regular content.
- Added comprehensive unit tests in `internal/proxy/stream_peeker_test.go`.
