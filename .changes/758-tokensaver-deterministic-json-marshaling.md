### fix(tokensaver): use deterministic JSON marshaling to preserve prompt prefix caching

When token savers (RTK, Caveman, Ponytail) are enabled, they rewrite request messages, system prompts, or tool outputs. Previously, `internal/tokensaver` serialized mutated JSON envelopes using `json.Marshal` without `Deterministic(true)`. In Go's `encoding/json/v2`, map member iteration order is randomized per call, causing prompt prefix cache misses across session turns on all upstream providers (OpenAI, Claude, DeepSeek, Gemini, Kimi).

This fix adds `json.Deterministic(true)` to all request serialization seams in `tokensaver`:
- `InjectSystemPrompt`, `injectChatSystemPrompt`, `injectResponsesSystemPrompt`, `InjectSystemPromptClaude`
- `CompressMessagesWithConfig` (RTK tool message compression)
- `CompressInputMessages` (Caveman input compression)
- `marshalBody` (Custom prompt injection)

Verified: Added `TestTokensaverSystemPromptsAreDeterministic` covering Chat, Responses, Claude, and RTK compression paths. All `internal/tokensaver` tests pass.
