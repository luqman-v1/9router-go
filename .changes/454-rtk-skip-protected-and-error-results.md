### fix(rtk): never compress an error result or a protected tool's output

RTK rewrote any `tool_result` over the size floor, including the ones the agent
came back to read a second time. A failed command's output and a `Read`/`Grep`
result are the two cases where that rewrite is actively harmful: an error trace
loses the exact text the model needs to diagnose it, and a file read loses the
content the model asked to see, so the next `Edit` is issued against a summary.

Two skips now apply to every path RTK compresses — OpenAI Chat `role: "tool"`,
Responses `function_call_output`, and Claude `tool_result` blocks:

- **Error results.** `is_error`, `isError`, and `status: "error"` (any case)
  all mark a block as an error. `is_error: false` and `status: "success"` do
  not.
- **Protected tools.** `Read`, `Grep`, `Edit`, `Write`, `patch`, and `anchor`,
  matched after trimming whitespace, so a provider that pads the name still
  hits the skip.

A `tool` message does not always carry its tool name — the OpenAI wire format
puts it on the assistant's `tool_calls`, and Responses puts it on a separate
`function_call` item. `buildToolCallMap` indexes `id`/`call_id`/`tool_use_id` →
name across `tool_calls`, `function_call`, and `tool_use` blocks so those
messages can be classified instead of being compressed by default. When no name
resolves, the message is still compressed: an unknown tool is the pre-existing
behaviour, and the skip list is a targeted exception, not an allowlist.

The same pass also fixes two result shapes that were being left untouched:
array-typed `content`/`output` on a `tool` message or `function_call_output`,
and a Claude `tool_result` whose payload is under `content` (string or text
blocks) rather than `text`.

Verified with `TestCompressMessagesWithConfig_PerBlockPreservation`: seven
subtests covering all three protocol shapes, the name-resolution paths, the
error spellings, and the array-shaped payloads. On the pre-fix code nine of its
assertions fail; the helper-validation subtest is excluded from that comparison
because it references the new symbols and the package does not compile without
them.

The test file is the bulk of this change; the production diff is 135 lines.
