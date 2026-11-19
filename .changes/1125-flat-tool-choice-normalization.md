### fix(translator): harden tool_choice normalization for flat and none shapes

- `normalizeToolChoice` now maps a flat Responses-style `{"type": "function", "name": "..."}` (or Claude-shaped `{"type": "tool", "name": ...}`) into the nested OpenAI Chat form `{"type": "function", "function": {"name": "..."}}`, and leaves a choice that already carries a `function` map untouched.
- A Claude `{"type": "none"}` choice is normalized to the OpenAI string `"none"`, so a request that forbids tool calls is not silently demoted to `"auto"`.
- Local protocol hardening only: no upstream commit, PR, or issue is cited, and the change is verified by unit tests in `internal/translator/openai_filter_test.go`.
