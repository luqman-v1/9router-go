### fix(translator): map Claude tool_choice none to OpenAI string none

- Normalizes Claude-style `{"tool_choice": {"type": "none"}}` into standard OpenAI `"tool_choice": "none"` in the message filter path (`normalizeToolChoice`).
- Local hardening: prevents unhandled object forms from falling through or demoting to "auto" on Chat Completions upstreams when callers intend to disable tool calling.
