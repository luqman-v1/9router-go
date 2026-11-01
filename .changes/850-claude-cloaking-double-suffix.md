### fix(claude-cloaking): prevent double-suffixing tools in multi-turn history

- In `internal/handlers/chat/claude_cloaking.go`, make `suffixClaudeToolName` idempotent so tool names ending with `_ide` are not double-suffixed to `_ide_ide` (porting upstream `decolua/9router` PR #5339, fixing #5170).
- Maps `suffixed` to `baseName` in `toolNameMap` (`baseName := strings.TrimSuffix(name, claudeToolSuffix)`).
- Registers original, suffixed, and base names in `clientToolNames` so history matching works whether the reflected tool call was already suffixed or not.
- Prevents unresolvable tool calls in downstream clients during multi-turn agent conversations.
- Added comprehensive unit test in `internal/handlers/chat/claude_cloaking_test.go`.
