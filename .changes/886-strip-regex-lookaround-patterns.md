### fix(translator): strip regex lookarounds from tool schemas for all OpenAI-compatible targets
- **Regex Lookaround Sanitization**:
  - OpenAI, DeepSeek, and strict JSON Schema validators strictly prohibit regex lookarounds (`(?=...)`, `(?!...)`, `(?<=...)`, `(?<!...)`) in tool parameter `pattern` definitions, rejecting requests with HTTP 400 (`Invalid JSON schema: regex lookaround is not supported`).
  - Update `isValidRegex` in `internal/translator/tool_schema.go` to explicitly identify and reject lookaround assertions.
  - Expand `NormalizeToolSchemasForProvider` to sanitize tool parameter schemas across all OpenAI-compatible providers, rather than solely `openrouter.ai`.
  - Add outbound tool schema sanitization in `internal/handlers/chat/fallback.go` to ensure all dispatch routes (including custom executors like Codex and Azure) sanitize tool schemas before forwarding to upstream.
  - Add comprehensive test coverage in `internal/translator/tool_schema_test.go`.
