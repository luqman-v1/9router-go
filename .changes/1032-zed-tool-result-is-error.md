### fix(translator): always emit tool_result is_error for Zed Anthropic wire (#1032)

Ensure every `tool_result` block emitted for Claude-compatible wires (such as Zed Editor's Anthropic completions wire) explicitly carries `is_error: bool`.

- Port upstream `decolua/9router` PR #5544 / Issue #5543.
- Introduce `ToolResultIsError` in `internal/translator` to detect error signals across `is_error`, `isError`, `status: "error"`, JSON error objects, and error message prefixes.
- Always populate `is_error: bool` in `convertOpenAIMessagesToClaude` and `zedNormalizeClaudeContent`.
- Add table-driven unit tests and Zed completions replay tests.
