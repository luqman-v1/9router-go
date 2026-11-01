### fix(executor): strip OpenAI-only `n` param off the Claude body

- Fixed a parity bug: a client sending OpenAI-format Chat Completions with the
  `n` (number-of-choices) field for a Claude provider forwarded `n` straight to
  the Anthropic Messages API, which rejects it with
  `400 "n": Extra inputs are not permitted`.
- `ensureMessagesFor` now deletes top-level `n` alongside the existing
  `stream_options` / `store` / `reasoning_effort` cleanup, matching upstream
  `decolua/9router` `openaiToClaudeRequest`, which rebuilds the Claude body so
  `n` never survives.
- Extended `TestEnsureClaudeMessages_StripsOpenAIOnlyFields` to lock the
  regression.
