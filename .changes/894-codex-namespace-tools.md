### feat(codex): unpack namespace tools and sanitize encrypted schema constraint

- Unpack namespace tools from `tools` and `additional_tools` when `type === "namespace"`, ensuring downstream models receive real function declarations.
- Add `"encrypted"` keyword to Gemini unsupported schema constraints in `cleanGeminiSchema`.
- Tolerate `functions.` prefix in custom tool call resolution for Codex CLI.
- Verified with unit tests in `internal/translator/codex_namespace_tools_test.go` (upstream `decolua/9router#5378` parity).
