## fix(commandcode): tool-call and tool-result block parity

Upstream `openaiToCommandCodeRequest` (decolua/9router master) converts an
OpenAI `role:"tool"` turn into an AI-SDK `tool-result` block and an assistant
`tool_calls` turn into `tool-call` blocks with parsed `input`. The Go executor
passed both through untranslated, so CommandCode answered
`400 BAD_REQUEST` ("expected \"tool-result\" at params.messages[N].content[0].type",
"expected string, received undefined at …toolCallId") for every request following a
tool call (`z-ai/glm-5.3-flash`, `xiaomi/mimo-v2.6-flash`).

- tool-result turns are emitted as `role:"user"`, **not** upstream's
  `role:"tool"`: the provider validates `role ∈ {"user","assistant"}` and
  upstream's own schema header — "verified live with curl 2026-05-07" — documents
  `tool_result blocks (role=user)` (openai-to-commandcode.js:10). Upstream's
  JS:120-123 `role:ROLE.TOOL` contradicts its own header and is itself the bug;
  mirroring it reproduces the 400
- new `internal/proxy/executor/commandcode_request.go` ports `flattenText`,
  `toContentBlocks`, `safeParseJson` and `convertMessages`
- `buildCommandcodeBody` builds `params` from a whitelist instead of copying every
  client key, and converts OpenAI `tools` through the existing
  `convertOpenAIToolsToClaude`
- `max_tokens` default stays 1024 (upstream 64000): deliberate local divergence,
  the test ping budget depends on it

upstream decolua/9router open-sse/translator/request/openai-to-commandcode.js parity
