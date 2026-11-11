### Bug Fixes

- **chat:** Normalize a unary OpenAI completion's `finish_reason` from `stop` (or empty) to `tool_calls`
  when the assistant turn actually ended with a tool call. vLLM reports a plain `stop` for a named
  `tool_choice`, and agent loops key off the finish reason — they treated the tool call as the final
  answer and the turn ended. The patch is applied to the body the gateway writes, before any client
  dialect is rendered, so the OpenAI, Responses-bridge and Claude-translation (`stop_reason: tool_use`)
  paths all send the corrected reason. Bodies that do not need the patch keep their exact bytes
  (zero-copy fast path); a body that does not parse is relayed untouched.
  (Port of upstream `mydisha/keirouter` `e340784`, MIT — the unary half of that commit's fix
  "gateway: client-compatible errors, usage, created and finish reasons".)
