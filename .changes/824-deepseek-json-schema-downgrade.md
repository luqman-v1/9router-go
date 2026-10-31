### Added

- **DeepSeek `json_schema` to `json_object` downgrade** (upstream #5304):
  - Automatically downgrade `response_format: {"type": "json_schema", ...}` to `{"type": "json_object"}` when targeting `deepseek` provider to prevent HTTP 400 rejection from DeepSeek API which lacks native Structured Outputs support.
  - Native Structured Output providers (such as `openai` and `openai-compatible-*` nodes) remain unaffected.
