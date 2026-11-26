### fix(usage): preserve explicit cached_tokens: 0 on Responses usage translation

When a Responses-native upstream explicitly reports `cached_tokens: 0` inside
`input_tokens_details` or `prompt_tokens_details`, `ParseResponsesUsage`
treated it as absent and left `prompt_tokens_details` out of the shaped
usage. Downstream clients (Cursor, Codex CLI, Claude Code) then classified
cache-read as `unknown` instead of an explicit cache-miss, mismatching the
direct upstream API.

- Distinguish an absent cache counter (`nil`) from an explicit zero (`0`):
  when `InputTokensDetails` or `PromptTokensDetails` is non-nil, preserve
  `CachedTokens` including `0` and always populate `PromptTokensDetails`.
- Absent counters still omit the field entirely.
- Adds `explicit cached_tokens zero` and `no cached counter at all` cases to
  `TestParseResponsesUsage`.
