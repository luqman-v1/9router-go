### feat: carry OpenAI structured output onto Claude-dialect upstreams (R-1)

An OpenAI client that asks for structured output via `response_format`
(`json_object` / `json_schema`) pointed at a Claude-dialect upstream
(`/v1/messages`, or an OpenAI body converted for an Anthropic upstream)
previously had the field **dropped** on the OpenAI→Claude conversion — it is
not a Claude field — so the model answered with free text and the client
silently got no JSON. This was the Anthropic half of the cross-dialect
structured-output gap (R-1).

`internal/translator.CarryClaudeStructuredOutput` now ports the ask onto the
converted request, run last inside `ensureMessagesFor` so it sees the final
tool/tool_choice/thinking state:

- **`json_schema` with no client tools, no thinking, and a free reserved name**
  becomes a forced tool — the schema as its `input_schema`, `tool_choice`
  pinned to it — so the model is constrained to the schema.
- **Otherwise** (`json_object`, client tools present, thinking on, or the
  reserved name already declared by the client) the ask rides in the system
  prompt as a JSON instruction, appended to any existing system text in every
  shape.
- The OpenAI-only `response_format` key is removed in **every** case so the
  strict Claude API does not 400 on it.

The forced tool name `9router_structured_output` is reserved. On the response
side the Claude→OpenAI converters (stream and unary) rewrite a tool_use block
with that exact name back to `message.content` and map its `tool_use`
`stop_reason` to `finish_reason: stop`, so the client sees a normal JSON answer,
not a tool call. A genuine client tool (any other name) is untouched.

`cloakClaudeTools` (Claude OAuth) skips the reserved tool: it is pinned by a
`tool_choice` whose name is not in the client-declared set, so renaming it in
`tools[]` would 400 upstream with "tool not found".

Verified with table-driven tests in `internal/translator/claude_structured_test.go`
(carry decision matrix + stream/unary unwrap) and
`internal/proxy/executor/claude_structured_output_test.go` (end-to-end through
`EnsureClaudeMessages`, including the thinking-on fallback).
