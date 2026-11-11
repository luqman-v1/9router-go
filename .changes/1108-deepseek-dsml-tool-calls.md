### fix(translator): parse DeepSeek DSML text into tool_calls

DeepSeek-family upstreams sometimes emit tool calls as inline DSML XML
(`<|DSML| invoke name="...">`) instead of structured `tool_calls` blocks, so
AI coding agents see no invocation and agent loops stall.

Adds `ExtractDSMLToolCalls` in `internal/translator/` that detects DSML blocks
in all marker spellings, extracts the function name and parameters, builds
valid JSON for `function.arguments`, and strips the raw markup from content.
Wired into `TranslateOpenAIToClaude` (non-streaming) and
`TranslateOpenAIToClaudeStreamSession` (streaming), so it works for DeepSeek
and any DeepSeek-family upstream reached through combo aliases.

Port of decolua/9router PR #5563. Closes #1107.

Verified with `gofmt`, `go vet`, and table-driven unit tests covering single
and multi-parameter calls, surrounding-text preservation, undeclared-tool
rejection, both pipe spellings, and truncated blocks.
