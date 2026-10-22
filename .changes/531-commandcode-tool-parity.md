## fix(commandcode): tool-call and tool-result block parity

Upstream `openaiToCommandCodeRequest` (decolua/9router master) converts an
OpenAI `role:"tool"` turn into an AI-SDK `tool-result` block and an assistant
`tool_calls` turn into `tool-call` blocks with parsed `input`. The Go executor
passed both through untranslated, so CommandCode answered
`400 BAD_REQUEST` ("expected \"tool-result\" at params.messages[N].content[0].type",
"expected string, received undefined at …toolCallId") for every request following a
tool call (`z-ai/glm-5.3-flash`, `xiaomi/mimo-v2.6-flash`).

- tool-result turns are emitted as `role:"tool"`, matching upstream
  `JS:120-123 role:ROLE.TOOL`: the provider validates `params.messages[*]`
  against a union of `{role:"user"|"assistant", content: string |
  anthropic-style blocks}` and `{role:"tool", content:[tool-result blocks]}`, so
  a tool-result block is only valid under role `"tool"` (live-verified
  2026-10-12: with `role:"user"` every tool turn answered 400
  `expected "tool" at "params.messages[N].role"` on the tool branch while its
  fields validated). An earlier revision of this fix emitted `role:"user"` on
  the strength of the header's `tool_result blocks (role=user)` note
  (curl-verified 2026-05-07); that read one union branch as the whole schema and
  kept the 400 alive — corrected by `fix(executor): commandcode tool turns emit
  role tool (provider union parity)`
- new `internal/proxy/executor/commandcode_request.go` ports `flattenText`,
  `toContentBlocks`, `safeParseJson` and `convertMessages`
- `buildCommandcodeBody` builds `params` from a whitelist instead of copying every
  client key, and converts OpenAI `tools` through the existing
  `convertOpenAIToolsToClaude`. The native `{params:{...}}` envelope goes
  through the same whitelist: the model is the resolved one, and a key outside
  the whitelist does not reach the provider
- `max_tokens` default stays 1024 (upstream 64000): deliberate local divergence,
  the test ping budget depends on it

upstream decolua/9router open-sse/translator/request/openai-to-commandcode.js parity
