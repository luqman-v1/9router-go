### fix(translator): sanitize Agent tool arguments for Claude Code subagent compatibility

- Non-Anthropic models (such as Codex, GPT, and Gemini) proxying for Claude Code frequently call the `Agent` tool without the schema-required `description` parameter, or pass alias fields like `task`, `instruction`, or `summary`.
- Missing `description` causes Claude Code client-side validation to fail with `InputValidationError: The required parameter description is missing` (`Initializing… Invalid tool parameters`), triggering repetitive retry loops and rapid quota depletion.
- Added `sanitizeAgentArgs` to `internal/translator/sanitize.go` to normalize prompt aliases and automatically synthesize a fallback `description` from the prompt prefix when absent.
