### Added

- **DeepSeek & OpenAI-compatible `json_schema` to `json_object` downgrade** (upstream #5304):
  - Automatically downgrade `response_format: {"type": "json_schema", ...}` to `{"type": "json_object"}` when targeting `deepseek` or `openai-compatible-*` providers to prevent HTTP 400 rejection from providers lacking native Structured Outputs support.
  - Native Structured Output providers (such as `openai`) remain unaffected.
