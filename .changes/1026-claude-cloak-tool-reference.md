### fix(claude): rename tool_reference blocks when cloaking tools (#1025)

Rename `tool_reference` blocks (`tool_name` and `name`) in message content and
nested `tool_result.content[]` during Claude OAuth tool cloaking so deferred
tool loading in Claude Code v2.1+ matches the suffixed tool declarations
without 400 "Tool reference '<name>' not found in available tools".
Port of upstream decolua/9router PR #5537 / Issue #5536.
